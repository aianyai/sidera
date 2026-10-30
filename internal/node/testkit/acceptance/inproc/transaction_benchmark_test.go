package inproc

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
)

// A fixture prepares one fixed, nonempty owner operation on a real filesystem.
// Only invoke runs while observations are collected. Verification and cleanup
// are outside both the write window and the benchmark timer.
type transactionBenchmarkCase struct {
	name    string
	prepare func(*testing.B, txn.Observer) transactionBenchmarkFixture
}

type transactionBenchmarkFixture struct {
	invoke  func() error
	verify  func() error
	cleanup func()
}

type transactionWindowObserver struct {
	mu      sync.Mutex
	target  string
	active  bool
	started time.Time
	elapsed time.Duration
	commits int
	err     error
}

func (o *transactionWindowObserver) Reached(ob fsx.Observation, _ txn.Kind) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active || ob.Point.Tag.Tx != o.target {
		return
	}
	// Benchmark durations use the real monotonic clock, independently of the
	// logical clock that drives owner state.
	now := time.Now()
	if ob.Point.Step == fsx.StepCommit {
		if o.started.IsZero() {
			o.err = fmt.Errorf("%s: commit without an actual write step", o.target)
			return
		}
		o.elapsed += now.Sub(o.started)
		o.commits++
		o.started = time.Time{}
	} else if o.started.IsZero() {
		o.started = now
	}
}

func (o *transactionWindowObserver) begin() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.active = true
	o.started = time.Time{}
	o.elapsed, o.commits, o.err = 0, 0, nil
}

func (o *transactionWindowObserver) finish() (time.Duration, int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.active = false
	if o.err != nil {
		return 0, 0, o.err
	}
	if !o.started.IsZero() {
		return 0, 0, fmt.Errorf("%s: write window did not commit", o.target)
	}
	if o.commits == 0 {
		return 0, 0, fmt.Errorf("%s: no nonempty successful commit", o.target)
	}
	return o.elapsed, o.commits, nil
}

func newTransactionBenchmarkWriter(b *testing.B, observer txn.Observer) (fsx.FS, *txn.Writer) {
	b.Helper()
	root := filepath.Join(b.TempDir(), "home")
	if err := fsx.MakeRoot(root); err != nil {
		b.Fatal(err)
	}
	fs, err := fsx.OpenOS(root, harness.RandReader(200))
	if err != nil {
		b.Fatal(err)
	}
	table, err := daemon.Table()
	if err != nil {
		b.Fatal(err)
	}
	w := txn.New(fs, table, observer)
	if err := w.Recover(); err != nil {
		b.Fatal(err)
	}
	return fs, w
}

// BenchmarkTransactionWrites reports the interval from the first actual FS
// step to logical commit, separately from the time to trigger an owner operation.
func BenchmarkTransactionWrites(b *testing.B) {
	table, err := daemon.Table()
	if err != nil {
		b.Fatal(err)
	}
	declared := map[string]bool{}
	for _, key := range table.Keys() {
		declared[key.Tag.Tx] = true
	}
	groups := []struct {
		name         string
		declarations []*txn.Decl
		cases        []transactionBenchmarkCase
	}{
		{"txn", txn.Internal(), transactionBenchmarkTxnCases()},
		{"config", config.Transactions(), transactionBenchmarkConfigCases()},
		{"keys", keys.Transactions(), transactionBenchmarkKeysCases()},
		{"store", store.Transactions(), transactionBenchmarkStoreCases()},
		{"engine", engine.Transactions(), transactionBenchmarkEngineCases()},
		{"daemon", daemon.Transactions(), transactionBenchmarkDaemonCases()},
	}
	listed := map[string]transactionBenchmarkCase{}
	assigned := map[string]bool{}
	for _, group := range groups {
		owned := map[string]bool{}
		for _, d := range group.declarations {
			if !declared[d.Name] || assigned[d.Name] {
				b.Fatalf("invalid declaration group assignment %q", d.Name)
			}
			owned[d.Name], assigned[d.Name] = true, true
		}
		for _, c := range group.cases {
			if !owned[c.name] {
				b.Fatalf("unknown transaction benchmark %q in owner %s", c.name, group.name)
			}
			if _, ok := listed[c.name]; ok {
				b.Fatalf("duplicate transaction benchmark %q", c.name)
			}
			if c.prepare == nil {
				b.Fatalf("transaction benchmark %q has no fixture", c.name)
			}
			listed[c.name] = c
		}
	}
	if len(assigned) != len(declared) {
		b.Fatalf("owner groups cover %d/%d daemon declarations", len(assigned), len(declared))
	}
	// Run the declarations rather than the fixtures: an absent fixture fails its
	// real sub-benchmark. Go's selector can still run an owner subset directly.
	for _, group := range groups {
		b.Run(group.name, func(b *testing.B) {
			for _, d := range group.declarations {
				b.Run(d.Name, func(b *testing.B) {
					c, ok := listed[d.Name]
					if !ok {
						b.Fatalf("missing fixture for declared transaction %q", d.Name)
					}
					runTransactionBenchmark(b, c)
				})
			}
		})
	}
}

func runTransactionBenchmark(b *testing.B, c transactionBenchmarkCase) {
	b.StopTimer()
	observer := &transactionWindowObserver{target: c.name}
	var elapsed time.Duration
	commits := 0
	for i := 0; i < b.N; i++ {
		f := c.prepare(b, observer)
		if f.invoke == nil || f.verify == nil {
			b.Fatal("fixture requires invoke and verify")
		}
		observer.begin()
		b.StartTimer()
		err := f.invoke()
		b.StopTimer()
		window, count, observedErr := observer.finish()
		if err == nil {
			err = observedErr
		}
		if err == nil {
			err = f.verify()
		}
		if f.cleanup != nil {
			f.cleanup()
		}
		if err != nil {
			b.Fatal(err)
		}
		elapsed += window
		commits += count
	}
	b.ReportMetric(float64(elapsed.Nanoseconds())/float64(commits), "ns/write-window")
	b.ReportMetric(float64(commits)/float64(b.N), "commits/op")
	// Trigger latency includes owner scheduling and waiting; the requested cost
	// is the observed write window, so suppress the ambiguous default ns/op.
	b.ReportMetric(0, "ns/op")
}

// SeedNode prepares the existing owner-written keys/configuration and one
// installed, enabled adapter; no engine or background timer is involved.
func newTransactionBenchmarkNode(b *testing.B, observer txn.Observer) (fsx.FS, *txn.Writer, *config.Config, *keys.Keys) {
	b.Helper()
	fs, w := newTransactionBenchmarkWriter(b, observer)
	harness.MakeLayout(b, fs)
	cfg := harness.SeedNode(b, w, harness.DefaultStart)
	key, err := keys.Load(w)
	if err != nil {
		b.Fatal(err)
	}
	return fs, w, cfg, key
}
