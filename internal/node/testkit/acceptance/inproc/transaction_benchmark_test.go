package inproc

import (
	"crypto/rand"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/fsx"
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
	fs, err := fsx.OpenOS(b.TempDir(), rand.Reader)
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
		name  string
		cases []transactionBenchmarkCase
	}{
		{"txn", transactionBenchmarkTxnCases()},
		{"config", transactionBenchmarkConfigCases()},
		{"keys", transactionBenchmarkKeysCases()},
		{"store", transactionBenchmarkStoreCases()},
		{"engine", transactionBenchmarkEngineCases()},
		{"daemon", transactionBenchmarkDaemonCases()},
	}
	listed := map[string]bool{}
	for _, group := range groups {
		for _, c := range group.cases {
			if !declared[c.name] {
				b.Fatalf("unknown transaction benchmark %q", c.name)
			}
			if listed[c.name] {
				b.Fatalf("duplicate transaction benchmark %q", c.name)
			}
			if c.prepare == nil {
				b.Fatalf("transaction benchmark %q has no fixture", c.name)
			}
			listed[c.name] = true
		}
	}
	for _, group := range groups {
		b.Run(group.name, func(b *testing.B) {
			for _, c := range group.cases {
				b.Run(c.name, func(b *testing.B) { runTransactionBenchmark(b, c) })
			}
		})
	}
	// Go's ordinary sub-benchmark selector can run an owner subset while the
	// unfiltered suite always checks the complete, dynamically derived table.
	b.Run("_coverage", func(b *testing.B) {
		b.StopTimer()
		var missing []string
		for name := range declared {
			if !listed[name] {
				missing = append(missing, name)
			}
		}
		slices.Sort(missing)
		if len(missing) != 0 {
			b.Fatalf("transaction benchmark fixtures missing %d/%d declarations: %v", len(missing), len(declared), missing)
		}
		b.ReportMetric(float64(len(listed)), "declarations")
		b.ReportMetric(0, "ns/op")
	})
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
