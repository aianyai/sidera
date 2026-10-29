package acceptance

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/txn"
)

// Binaries are built from the caller's checkout, outside the fixture.
type Binaries struct{ Tagged, Plain, Adapter, Runtime string }

type Options struct {
	Seed       string
	ClockStart time.Time
	Log        *slog.Logger
}

// GenerationSpec gives each process its own generation and record file. Target
// is empty or a canonical transaction point, never a CLI process point.
type GenerationSpec struct {
	Number         int
	Target, Record string
}

type generation struct {
	spec           GenerationSpec
	p              *process
	setup          int
	frozen         []byte
	cancelled      bool
	stdout, stderr string
}

// Fixture owns a foreground daemon, input packages and a control listener across
// daemon generations. Calls are serialized by the caller; Now is safe for writers.
type Fixture struct {
	root, home     string
	binaries       Binaries
	opts           Options
	profile        fixtureProfile
	agentWriters   []*ledger.Writer
	clockMu        sync.Mutex
	now            time.Time
	control        *fakectl.Server
	client         *manageapi.Client
	table          *txn.Table
	files          *ledger.Dir
	writer         *ledger.Writer
	gens           map[int]*generation
	current        *generation
	steps          map[string]bool
	step           string
	op             uint64
	inputs         []adapterInput
	bootstrapSteps []bootstrapStep
	bootstrapNext  int
}

func NewFixture(ctx context.Context, root string, binaries Binaries, opts Options) (*Fixture, error) {
	return newFixture(ctx, root, binaries, opts, fixtureProfile{})
}

func newFixture(ctx context.Context, root string, binaries Binaries, opts Options, profile fixtureProfile) (*Fixture, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("acceptance: root must be absolute")
	}
	if opts.Seed == "" {
		opts.Seed = "acceptance"
	}
	if opts.ClockStart.IsZero() {
		opts.ClockStart = time.Unix(1800000000, 0).UTC()
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	f := &Fixture{root: root, home: filepath.Join(root, "n"), binaries: binaries, opts: opts, profile: profile, now: opts.ClockStart, gens: map[int]*generation{}, steps: map[string]bool{}}
	var err error
	f.table, err = daemon.Table()
	if err != nil {
		return nil, err
	}
	f.control, err = fakectl.Listen(ctx)
	if err != nil {
		return nil, err
	}
	f.client, err = manageapi.NewClient(manageapi.ClientConfig{Home: f.home})
	if err != nil {
		_ = f.control.Close()
		return nil, err
	}
	f.files = ledger.NewDir(filepath.Join(root, "l"))
	f.writer, err = f.files.Open("fixture", os.Getpid(), f.Now)
	if err != nil {
		f.client.Close()
		_ = f.control.Close()
		return nil, err
	}
	if err = f.prepareInputs(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func (f *Fixture) Now() time.Time { f.clockMu.Lock(); defer f.clockMu.Unlock(); return f.now }
func (f *Fixture) Runtime() fakeruntime.RemoteController {
	return fakeruntime.RemoteController{Server: f.control}
}
func (f *Fixture) Ledger() (*ledger.Log, error) { return ledger.ReadDir(filepath.Join(f.root, "l")) }

func (f *Fixture) begin(spec GenerationSpec, command string) (*generation, error) {
	if spec.Number <= 0 {
		return nil, fmt.Errorf("acceptance: generation must be positive")
	}
	if f.current != nil && !f.current.p.exited() {
		return nil, fmt.Errorf("acceptance: daemon is still running")
	}
	if _, ok := f.gens[spec.Number]; ok {
		return nil, fmt.Errorf("acceptance: generation %d already exists", spec.Number)
	}
	if spec.Target != "" {
		p, err := fsx.ParsePoint(spec.Target)
		if err != nil {
			return nil, err
		}
		found := false
		for _, k := range f.table.Keys() {
			if k.Key == p.Key {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("acceptance: undeclared target %s", spec.Target)
		}
	}
	if spec.Record == "" {
		spec.Record = filepath.Join(f.root, fmt.Sprintf("g%d.jsonl", spec.Number))
	}
	if !filepath.IsAbs(spec.Record) {
		return nil, fmt.Errorf("acceptance: record must be absolute")
	}
	record, err := os.OpenFile(spec.Record, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if err = record.Close(); err != nil {
		return nil, err
	}
	g := &generation{spec: spec, stdout: filepath.Join(f.root, fmt.Sprintf("g%d.out", spec.Number)), stderr: filepath.Join(f.root, fmt.Sprintf("g%d.err", spec.Number))}
	values := map[string]string{"SIDERA_TEST_SEED": f.processSeed(command, spec.Number), "SIDERA_TEST_CLOCK_START_MS": strconv.FormatInt(f.Now().UnixMilli(), 10), "SIDERA_CRASH_RECORD": spec.Record, "SIDERA_CRASH_AT": spec.Target, "SIDERA_CRASH_GEN": strconv.Itoa(spec.Number)}
	g.p, err = f.launch(command, g.stdout, g.stderr, values)
	if err != nil {
		return nil, err
	}
	if f.opts.Log != nil {
		f.opts.Log.Debug("acceptance process started", "command", command, "generation", spec.Number, "pid", g.p.cmd.Process.Pid)
	}
	f.gens[spec.Number] = g
	return g, nil
}

func processEnv(values map[string]string) []string {
	out := []string{}
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if _, ok := values[key]; !ok {
			out = append(out, v)
		}
	}
	for key, value := range values {
		if value != "" {
			out = append(out, key+"="+value)
		}
	}
	return out
}

func (f *Fixture) Init(ctx context.Context, spec GenerationSpec) error {
	g, err := f.begin(spec, "init")
	if err != nil {
		return err
	}
	if err = g.p.wait(ctx); ctx.Err() != nil {
		g.cancelled = true
		_ = f.kill(g)
		return ctx.Err()
	}
	return f.exitResult(g)
}

func (f *Fixture) exitResult(g *generation) error {
	t, err := f.ReadTrace(g.spec.Number)
	if err != nil {
		return err
	}
	if g.spec.Target != "" && t.Reached {
		return nil
	}
	if g.p.err != nil {
		out, _ := os.ReadFile(g.stdout)
		stderr, _ := os.ReadFile(g.stderr)
		return fmt.Errorf("acceptance: generation %d exited: %w\nstdout:\n%s\nstderr:\n%s", g.spec.Number, g.p.err, out, stderr)
	}
	if g.spec.Target != "" {
		return fmt.Errorf("acceptance: target %s was not reached", g.spec.Target)
	}
	return nil
}

func (f *Fixture) Start(ctx context.Context, spec GenerationSpec) (err error) {
	if err := f.writeInputs(); err != nil {
		return err
	}
	g, err := f.begin(spec, "daemon")
	if err != nil {
		return err
	}
	f.current = g
	defer func() {
		if err != nil {
			err = f.startFailure(ctx, g, err)
		}
	}()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if g.p.exited() {
			return f.exitResult(g)
		}
		// A tagged ping can answer during recovery. Public status is the
		// composition root's evidence that ordinary changes may now run.
		if _, err = f.Manage(ctx, "test.ping", manageapi.Request{}); err == nil {
			raw, statusErr := f.Manage(ctx, "status", manageapi.Request{})
			if statusErr == nil {
				var status struct {
					Daemon struct {
						State string `json:"state"`
					} `json:"daemon"`
				}
				if err = resultBody(raw, &status); err != nil {
					return err
				}
				if status.Daemon.State == "running" {
					break
				}
			}
		}
		select {
		case <-g.p.done:
			return f.exitResult(g)
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
	if err = f.checkTable(ctx); err != nil {
		return err
	}
	if err = f.bootstrap(ctx); err != nil {
		return err
	}
	if err = f.waitProbes(ctx); err != nil {
		return err
	}
	return f.Quiesce(ctx)
}

// A dropped management connection is only a transport failure. A recorded
// target marker permits waiting for its actual exit and checking both facts.
func (f *Fixture) startFailure(ctx context.Context, g *generation, cause error) error {
	if ctx.Err() != nil {
		g.cancelled = true
		return errors.Join(ctx.Err(), f.kill(g))
	}
	if g.p.exited() {
		return f.exitResult(g)
	}
	trace, _, err := readTrace(g.spec.Record, g.spec.Number, g.setup, f.table, true)
	if err == nil && g.spec.Target != "" && trace.Crash == g.spec.Target {
		if err := g.p.wait(ctx); ctx.Err() != nil {
			g.cancelled = true
			return errors.Join(err, f.kill(g))
		}
		return f.exitResult(g)
	}
	return cause
}

func (f *Fixture) Manage(ctx context.Context, method string, in manageapi.Request) (json.RawMessage, error) {
	return f.client.Call(ctx, method, in)
}

func (f *Fixture) change(ctx context.Context, method string, version *admin.Version, params any) error {
	_, err := f.Manage(ctx, method, f.changeRequest(version, params))
	return err
}

func (f *Fixture) changeRequest(version *admin.Version, params any) manageapi.Request {
	f.op++
	var id admin.OperationID
	binary.BigEndian.PutUint64(id[8:], f.op)
	return manageapi.Request{OperationID: id, ExpectedVersion: version, Params: params}
}

func (f *Fixture) Stop(ctx context.Context) error {
	g := f.current
	if g == nil {
		return nil
	}
	if g.p.exited() {
		return f.exitResult(g)
	}
	trace, _, err := readTrace(g.spec.Record, g.spec.Number, g.setup, f.table, true)
	if err != nil {
		return err
	}
	if g.spec.Target != "" && trace.Crash == g.spec.Target {
		if err := g.p.wait(ctx); ctx.Err() != nil {
			return err
		}
		return f.exitResult(g)
	}
	// Freeze the scenario at a complete-line boundary before daemon.stop writes.
	raw, err := os.ReadFile(g.spec.Record)
	if err != nil {
		return err
	}
	if i := strings.LastIndexByte(string(raw), '\n'); i >= 0 {
		g.frozen = append([]byte{}, raw[:i+1]...)
	} else {
		g.frozen = []byte{}
	}
	if err = f.change(ctx, "daemon.stop", nil, nil); err != nil {
		return err
	}
	if err = g.p.wait(ctx); ctx.Err() != nil {
		return ctx.Err()
	}
	return f.exitResult(g)
}

func (f *Fixture) Kill(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g := f.current
	if g == nil {
		return nil
	}
	return f.kill(g)
}

func (f *Fixture) kill(g *generation) error {
	killed, err := g.p.killAndWait()
	if killed {
		err = errors.Join(err, f.writer.Append(ledger.KindFixtureKill, ledger.Attrs{PIDs: []int64{int64(g.p.cmd.Process.Pid)}}))
	}
	return err
}

func (f *Fixture) ReadTrace(gen int) (Trace, error) {
	g, ok := f.gens[gen]
	if !ok {
		return Trace{}, fmt.Errorf("acceptance: unknown generation %d", gen)
	}
	if !g.p.exited() {
		return Trace{}, fmt.Errorf("acceptance: generation %d has not been waited for", gen)
	}
	var t Trace
	var err error
	if g.frozen != nil {
		t, _, err = parseTrace(g.frozen, gen, g.setup, f.table, false)
	} else {
		t, _, err = readTrace(g.spec.Record, gen, g.setup, f.table, false)
	}
	if err != nil {
		return t, err
	}
	var abnormal *exec.ExitError
	t.Reached = !g.cancelled && g.spec.Target != "" && t.Crash == g.spec.Target && errors.As(g.p.err, &abnormal)
	return t, nil
}

func (f *Fixture) BeginStep(ctx context.Context, name string) error {
	if name == "" || f.steps[name] {
		return fmt.Errorf("acceptance: step must be nonempty and unique: %q", name)
	}
	if f.step == "" && f.current != nil {
		g := f.current
		_, n, err := readTrace(g.spec.Record, g.spec.Number, 0, f.table, true)
		if err != nil {
			return err
		}
		g.setup = n
	}
	f.steps[name] = true
	f.step = name
	f.files.SetStep(name)
	if err := f.writer.Append(ledger.KindFixtureStep, ledger.Attrs{Name: name}); err != nil {
		return err
	}
	return f.syncConnections(ctx)
}

func (f *Fixture) Close() error {
	var errs []error
	if f.current != nil && !f.current.p.exited() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := f.Stop(ctx)
		cancel()
		if err != nil {
			errs = append(errs, err)
			errs = append(errs, f.Kill(context.Background()))
		}
	}
	f.client.Close()
	errs = append(errs, f.control.Close())
	for _, w := range f.agentWriters {
		errs = append(errs, w.Close())
	}
	errs = append(errs, f.writer.Close())
	return errors.Join(errs...)
}
