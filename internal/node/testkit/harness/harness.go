package harness

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent/engineagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Options configure a fixture. The zero value is a node that starts at
// DefaultStart with an adapter that may register as many direct endpoints as the
// protocol allows.
type Options struct {
	// Start is the origin of the test clock; zero means DefaultStart.
	Start time.Time
	// Seed seeds the engine's random source and, with the process generation,
	// makes tokens and identifiers reproducible; zero means 1.
	Seed uint64
	// MaxDirect is the most direct endpoints the adapter declares; zero means the
	// protocol's own limit.
	MaxDirect int
	// Params are the engine's tunables; a zero field takes the engine's default.
	Params engine.Params
	// Observer, when set, is told about every write step of every process the
	// fixture runs, from the goroutine that is about to run it. A test uses it to
	// stop the engine in the middle of a command.
	Observer txn.Observer
	// RollbackPaused starts the engine as if the persisted state said the wall
	// clock was set back and not cleared.
	RollbackPaused bool
}

// H is a node in one process of the test: the engine and what it runs on, the
// shared clock, the ledger and the agents of the endpoints registered through it.
// It is safe for concurrent use, as far as the engine is: a test drives it from
// one goroutine.
type H struct {
	t    testing.TB
	opts Options

	ctx    context.Context
	cancel context.CancelFunc

	clk     *clocktest.Fake
	led     *ledger.Memory
	fixture *ledger.Writer
	tab     *txn.Table
	admit   adapterreg.Admission

	// dying is the processes that Crash left behind, still being stopped.
	dying sync.WaitGroup

	mu    sync.Mutex
	proc  *process
	gen   int
	names map[string]endpoint
	agent map[string]fakeagent.Agent
	steps map[string]bool
	quiet []quietCondition
}

// process is one run of the node over a disk.
type process struct {
	mounted *Mounted
	cfg     *config.Config
	log     *LogCapture
	eng     *engine.Engine
}

// endpoint is a direct endpoint registered through the fixture.
type endpoint struct {
	ref  adapterproto.EndpointRef
	addr ident.Address
}

var _ scenario.Env = (*H)(nil)

// New makes the node (initialised through the program's own code, the adapter
// Instance installed and enabled), starts the engine and returns when it is
// running. Everything it starts is stopped when the test ends, and the test
// fails if a goroutine of the fixture's packages is left.
func New(t testing.TB, o Options) *H {
	t.Helper()
	NoLeaks(t)
	if o.Start.IsZero() {
		o.Start = DefaultStart
	}
	if o.Seed == 0 {
		o.Seed = 1
	}
	if o.MaxDirect == 0 {
		o.MaxDirect = adapterproto.MaxDirectEndpoints
	}
	h := &H{
		t: t, opts: o,
		clk:   clocktest.NewFake(o.Start),
		led:   ledger.NewMemory(),
		tab:   ProgramTable(t),
		admit: AdapterAdmission(t, o.MaxDirect),
		names: map[string]endpoint{}, agent: map[string]fakeagent.Agent{}, steps: map[string]bool{},
	}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	var err error
	if h.fixture, err = h.led.Open("fixture", 0, h.clk.Now); err != nil {
		t.Fatalf("harness: ledger: %v", err)
	}
	t.Cleanup(h.close)
	h.proc = h.boot(seededDisk(t, o.Start))
	return h
}

// boot runs a process over the disk through the startup steps the engine has: the
// scan, the start of the loop, the admission of the adapter, the recovery of
// deliveries and the switch to serving.
func (h *H) boot(disk *fsxtest.Mem) *process {
	h.t.Helper()
	m := Mount(h.t, h.tab, disk, h.opts.Observer)
	cfg, err := config.Load(m.Writer)
	if err != nil {
		h.t.Fatalf("harness: Load: %v", err)
	}
	h.gen++
	p := &process{mounted: m, cfg: cfg, log: &LogCapture{}}
	deps := engine.Deps{
		Node: NodeID(), Clock: h.clk, HighWater: clock.NewHighWater(time.Time{}), RollbackPaused: h.opts.RollbackPaused,
		Writer: m.Writer, Store: m.Store, Config: cfg, Rand: RandReader(h.opts.Seed + uint64(h.gen)),
		Log: slog.New(p.log),
	}
	p.eng, _, err = engine.Open(h.ctx, deps, h.opts.Params)
	if err != nil {
		h.t.Fatalf("harness: engine.Open: %v", err)
	}
	p.eng.Start()
	if err := p.eng.AdapterAdmitted(h.ctx, Instance, h.admit); err != nil {
		h.t.Fatalf("harness: AdapterAdmitted: %v", err)
	}
	if err := p.eng.RecoverDelivery(h.ctx); err != nil {
		h.t.Fatalf("harness: RecoverDelivery: %v", err)
	}
	p.eng.Serve()
	return p
}

// stopLimit is how long a process is given to stop, on the real clock.
const stopLimit = 20 * time.Second

// stop stops the engine of a process the way the daemon does on a normal exit.
func stop(p *process) error {
	ctx, cancel := context.WithTimeout(context.Background(), stopLimit)
	defer cancel()
	p.eng.BeginShutdown()
	return p.eng.Stop(ctx)
}

func (h *H) close() {
	h.mu.Lock()
	p := h.proc
	h.mu.Unlock()
	if err := stop(p); err != nil {
		h.t.Errorf("harness: stopping the engine: %v", err)
	}
	h.dying.Wait()
	h.cancel()
}

func (h *H) current() *process {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.proc
}

// Engine is the engine of the running process.
func (h *H) Engine() *engine.Engine { return h.current().eng }

// Clock is the one test clock: the engine reads it, the ledger stamps with it,
// and a fake takes its Now method value as the func() time.Time it cannot do
// without.
func (h *H) Clock() *clocktest.Fake { return h.clk }

// Faults is the fault wrapper the running process writes through. A rule added to
// it applies to this process only, and the test fails if a rule never fires.
func (h *H) Faults() *fsxtest.Faulty { return h.current().mounted.Faulty }

// Disk is the in-memory file system of the running process, for reading.
func (h *H) Disk() *fsxtest.Mem { return h.current().mounted.Mem }

// Config is the configuration the running process loaded.
func (h *H) Config() *config.Config { return h.current().cfg }

// Log is what the running process's engine logged.
func (h *H) Log() *LogCapture { return h.current().log }

// Ctx is the context of the test; it ends when the test does.
func (h *H) Ctx() context.Context { return h.ctx }

// Direct registers a direct endpoint of the adapter Instance under the key name,
// through the entry the adapter host will call, and returns its address. The same
// name is the same endpoint, before and after a restart. A refusal is returned as
// the engine gave it.
func (h *H) Direct(name string) (string, error) {
	res, err := h.Engine().RegisterDirect(h.ctx, Instance, adapterproto.RegisterParams{Key: name, DisplayName: "Name of " + name})
	if err != nil {
		return "", err
	}
	id, err := ident.ParseEndpointID(string(res.Endpoint))
	if err != nil {
		return "", fmt.Errorf("harness: the engine returned the endpoint %q: %w", res.Endpoint, err)
	}
	ep := endpoint{ref: res.Endpoint, addr: ident.Address{Node: NodeID(), Endpoint: id}}
	h.mu.Lock()
	h.names[name] = ep
	h.mu.Unlock()
	return ep.addr.String(), nil
}

// MustDirect is Direct for a test that has no use for a refusal.
func (h *H) MustDirect(name string) string {
	h.t.Helper()
	addr, err := h.Direct(name)
	if err != nil {
		h.t.Fatalf("harness: Direct(%q): %v", name, err)
	}
	return addr
}

func (h *H) endpoint(name string) endpoint {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	ep, ok := h.names[name]
	if !ok {
		h.t.Fatalf("harness: no endpoint was registered as %q", name)
	}
	return ep
}

// Addr is the address of the endpoint registered as name.
func (h *H) Addr(name string) string { h.t.Helper(); return h.endpoint(name).addr.String() }

// Agent returns the agent of the endpoint registered as name: the engine-layer
// backend, wrapped so that every call writes its two entries to the ledger. The
// same agent is returned for the same name for the life of the fixture, across
// restarts, and it always calls the engine that is running.
func (h *H) Agent(name string) fakeagent.Agent {
	h.t.Helper()
	ep := h.endpoint(name)
	h.mu.Lock()
	defer h.mu.Unlock()
	if a, ok := h.agent[name]; ok {
		return a
	}
	id := ep.addr.Endpoint.String()
	w, err := h.led.Open("agent/"+id, 0, h.clk.Now)
	if err != nil {
		h.t.Fatalf("harness: ledger writer of %s: %v", name, err)
	}
	inner := engineagent.New(currentEngine{h}, engine.ByAdapter(Instance, ep.ref))
	a := fakeagent.Recording(inner, w, id, fakeagent.RecordingOptions{})
	h.agent[name] = a
	return a
}

// currentEngine is the agent service of whichever engine is running, so that an
// agent made before a restart acts on the one after it.
type currentEngine struct{ h *H }

func (c currentEngine) Declare(ctx context.Context, caller engine.Caller, p engine.DeclarationPatch) (authz.Declaration, error) {
	return c.h.Engine().Declare(ctx, caller, p)
}

// Advance moves the clock forward by d. The engine reacts asynchronously: call
// Quiesce before looking at the result.
func (h *H) Advance(d time.Duration) { h.clk.Advance(d) }

// Set sets the clock, backwards too; going back fires no timer.
func (h *H) Set(t time.Time) { h.clk.Set(t) }

// BeginStep announces the script step that is about to run: it sets the step the
// ledger stamps on every entry written from now on and writes the entry that says
// so. A name is announced once, because an entry names its step and nothing else.
func (h *H) BeginStep(name string) error {
	h.mu.Lock()
	if h.steps[name] {
		h.mu.Unlock()
		return fmt.Errorf("harness: step %q was announced before", name)
	}
	h.steps[name] = true
	h.mu.Unlock()
	h.led.SetStep(name)
	return h.fixture.Append(ledger.KindFixtureStep, ledger.Attrs{Name: name})
}

// Ledger reads what the agents and the fakes wrote, validated.
func (h *H) Ledger() (*ledger.Log, error) { return h.led.Log() }

// CheckLedger judges the ledger by every judgement that needs nothing but the
// ledger.
func (h *H) CheckLedger() error {
	log, err := h.led.Log()
	if err != nil {
		return err
	}
	return scenario.Check(log, scenario.Invariants{})
}

// Records is a read of the records on the disk of the running process, through the
// store's scan. It is the one place a test may look at the node's own records, and
// only where the scenario it belongs to is judged by them.
func (h *H) Records() (*store.Scan, error) { return h.current().mounted.Store.Scan() }

// Adapter has nothing to return yet: the engine layer has no adapter host, and a
// stand-in would make a script pass for what it did not exercise.
func (h *H) Adapter(string) (fakeadapter.Controller, error) {
	return nil, scenario.NotAvailable("the engine layer has no adapter host")
}

// Runtime has nothing to return yet: native sessions exist only through a session
// creation, which the engine does not offer.
func (h *H) Runtime() (fakeruntime.Controller, error) {
	return nil, scenario.NotAvailable("the engine layer cannot create a session, so there is no runtime to drive")
}

// CrashMode says what a crash takes with it.
type CrashMode struct {
	power bool
	seed  uint64
}

// ProcessCrash kills the process: every write that completed is on the disk, the
// page cache outlives the process, and nothing else does.
var ProcessCrash = CrashMode{}

// PowerLoss cuts the power: only what was synced is certain, and the model of the
// disk decides the rest from the seed, so that a seed is a reproducible disk.
func PowerLoss(seed uint64) CrashMode { return CrashMode{power: true, seed: seed} }

// Crash takes the disk a crash of the mode leaves at this instant, abandons the
// running process (it is stopped in the background and never writes to the disk
// the next process uses) and starts a new process on the disk. The test clock,
// the ledger and the endpoints registered through the fixture stay.
// The second word of the source is fixed, as the crash model's own tests fix it, so
// that a seed is all a test has to remember.
func (h *H) Crash(mode CrashMode) {
	h.t.Helper()
	h.mu.Lock()
	old := h.proc
	h.mu.Unlock()
	var disk *fsxtest.Mem
	if mode.power {
		disk = old.mounted.Mem.CrashClone(rand.New(rand.NewPCG(mode.seed, 0x5ce)))
	} else {
		disk = old.mounted.Mem.Clone()
	}
	h.dying.Add(1)
	go func() {
		defer h.dying.Done()
		_ = stop(old) // a dying process: how it ends is no one's business
	}()
	p := h.boot(disk)
	h.mu.Lock()
	h.proc = p
	h.mu.Unlock()
}

// Restart stops the process the way the daemon does on a normal exit and starts a
// new one on the same disk.
func (h *H) Restart() {
	h.t.Helper()
	old := h.current()
	if err := stop(old); err != nil {
		h.t.Fatalf("harness: stopping the engine: %v", err)
	}
	p := h.boot(old.mounted.Mem)
	h.mu.Lock()
	h.proc = p
	h.mu.Unlock()
}

// The conditions of quiet that the fixture's own parts answer, besides the
// engine's. A part that is added to the fixture (the adapter host and the fake
// adapter, the fake runtime) registers its conditions here.
type quietCondition struct {
	name string
	ok   func() bool
}

func (h *H) addQuiet(name string, ok func() bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.quiet = append(h.quiet, quietCondition{name, ok})
}

// unmet names the conditions of the fixture that do not hold.
func (h *H) unmet() []string {
	h.mu.Lock()
	conds := append([]quietCondition(nil), h.quiet...)
	h.mu.Unlock()
	var out []string
	for _, c := range conds {
		if !c.ok() {
			out = append(out, c.name)
		}
	}
	return out
}

// QuiesceError says why the system did not go quiet in time.
type QuiesceError struct {
	// Waited is how long the wait lasted, on the real clock.
	Waited time.Duration
	// Engine is the last report of the engine; Answered is false when the loop
	// never answered the probe, which means it was inside a command.
	Engine   engine.Report
	Answered bool
	// Unmet names the conditions of the fixture that did not hold.
	Unmet []string
	// Err is why the wait ended: the context, or the engine's refusal.
	Err error
}

func (e *QuiesceError) Error() string {
	s := fmt.Sprintf("harness: not quiet after %v", e.Waited.Round(time.Millisecond))
	if e.Answered {
		s += fmt.Sprintf(": the engine reports %+v", e.Engine)
	} else {
		s += ": the engine's loop did not answer the probe, so a command is still running"
	}
	if len(e.Unmet) > 0 {
		s += fmt.Sprintf(": the fixture waits for %v", e.Unmet)
	}
	return s + ": " + e.Err.Error()
}

func (e *QuiesceError) Unwrap() error { return e.Err }

// quietPoll is how often the conditions of the fixture are asked again, on the
// real clock: the fakes are objects with no channel to wait on.
const quietPoll = time.Millisecond

// Quiesce returns when the system has stopped reacting at the time of the clock
// now: the conditions of the fixture hold, and then the engine's do, and the
// fixture's still hold, since what the engine did may have made a fake busy. It
// fails with a *QuiesceError when the limit, on the real clock, ends first.
func (h *H) Quiesce(opts ...scenario.QuiesceOption) error {
	o := scenario.ResolveQuiesce(opts)
	ctx, cancel := context.WithTimeout(h.ctx, o.Timeout)
	defer cancel()
	return h.quiesce(ctx, o)
}

func (h *H) quiesce(ctx context.Context, _ scenario.QuiesceOptions) error {
	sys := clock.System()
	begin := sys.Now()
	fail := func(rep engine.Report, unmet []string, err error) error {
		if ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
			err = fmt.Errorf("%v: %w", err, ctx.Err())
		}
		return &QuiesceError{Waited: sys.Now().Sub(begin), Engine: rep, Answered: rep != engine.Report{}, Unmet: unmet, Err: err}
	}
	if err := ctx.Err(); err != nil {
		return fail(engine.Report{}, nil, err)
	}
	for {
		if unmet := h.unmet(); len(unmet) > 0 {
			timer := sys.NewTimer(quietPoll)
			select {
			case <-timer.C():
			case <-ctx.Done():
				timer.Stop()
				return fail(engine.Report{}, unmet, ctx.Err())
			}
			continue
		}
		rep, err := h.Engine().Quiesce(ctx, engine.Expect{})
		if err != nil {
			return fail(rep, nil, err)
		}
		if unmet := h.unmet(); len(unmet) == 0 {
			return nil
		}
	}
}
