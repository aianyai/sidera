package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent/engineagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/record"
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
	// Disk starts the node on this in-memory disk instead of a fresh one: a copy
	// a crash walk took, or a disk a test prepared. It is used as it is, so
	// Policy and NodeSettings are not applied to it.
	Disk *fsxtest.Mem
	// FS says which file system the node runs on: the one in memory, which can be
	// cloned for a crash, or a directory on the real one, for what only a real file
	// system answers (a full disk, a failed fsync) and whose faults go through the
	// same wrapper. A node on the real file system cannot Crash or be cloned.
	FS FSMode
	// Policy replaces the node's rules (policy.json) before the first start.
	Policy *config.PolicyFile
	// NodeSettings is a patch of the node's settings, the JSON object `node set`
	// takes, applied through the configuration's own change before the first start.
	NodeSettings string
	// DiskFree stands in for the query of the free space of the volume; nil asks
	// the file system. The engine's check of the disk reserve reads it.
	DiskFree func() uint64
	// RollbackPaused starts the engine as if the persisted state said the wall
	// clock was set back and not cleared. It applies to every start, restarts
	// included, because a process has no memory of the last one.
	RollbackPaused bool
}

// FSMode is the file system a node runs on.
type FSMode uint8

// The file systems a node can run on.
const (
	// MemFS is the in-memory file system with its crash model (the default).
	MemFS FSMode = iota
	// RealFS is a directory of the machine's own file system.
	RealFS
)

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
	knobs   *transport.Knobs
	// real is the file system of a node that runs on the real one, nil otherwise.
	real fsx.FS

	// dying is the processes that Crash left behind, still being stopped.
	dying sync.WaitGroup

	mu sync.Mutex
	// proc is the running process, nil while the node is down. disk is the disk
	// the running process writes to, or while the node is down the one the next
	// Start runs on.
	proc  *process
	disk  *fsxtest.Mem
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
	lb      *transport.Loopback

	// abandoned is set when a crash cuts the process off: a call that was made to
	// it and returns afterwards has no answer anyone could have read.
	abandoned atomic.Bool
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
	h.knobs = transport.NewKnobs(h.clk)
	h.ctx, h.cancel = context.WithCancel(context.Background())
	var err error
	if h.fixture, err = h.led.Open("fixture", 0, h.clk.Now); err != nil {
		t.Fatalf("harness: ledger: %v", err)
	}
	h.knobs.Observe(h.knobHit)
	h.addQuiet("loopback", h.loopbackQuiet)
	t.Cleanup(h.close)
	var base fsx.FS
	if o.FS == RealFS {
		h.real = realDisk(t, o)
		base = h.real
	} else if o.Disk != nil {
		h.disk = o.Disk
		base = h.disk
	} else {
		h.disk = seededDisk(t, o.Start)
		base = h.disk
	}
	if o.Disk == nil {
		h.prepare(base)
	}
	h.proc = h.boot(base, nil)
	return h
}

// realDisk makes an initialised node in a directory of the real file system. It
// is made for the test, since nothing of it is shared the way the in-memory
// snapshot is.
func realDisk(t testing.TB, o Options) fsx.FS {
	t.Helper()
	root := filepath.Join(t.TempDir(), "home")
	if err := fsx.MakeRoot(root); err != nil {
		t.Fatalf("harness: the data directory: %v", err)
	}
	base, err := fsx.OpenOS(root, RandReader(o.Seed+100))
	if err != nil {
		t.Fatalf("harness: OpenOS: %v", err)
	}
	MakeLayout(t, base)
	m := MountOn(t, ProgramTable(t), base, nil, nil)
	SeedNode(t, m.Writer, o.Start)
	return base
}

// prepare applies the node-level options to the disk before the first start:
// the rules the test replaced and the patch of the settings, the latter through
// the configuration's own change, so that what the node reads is what `node set`
// would have left.
func (h *H) prepare(base fsx.FS) {
	h.t.Helper()
	if h.opts.Policy != nil {
		data, err := h.opts.Policy.Encode()
		if err != nil {
			h.t.Fatalf("harness: the policy: %v", err)
		}
		if err := fsx.Replace(base, setupTag, config.PolicyPath, data, fsx.ModePrivate); err != nil {
			h.t.Fatalf("harness: writing the policy: %v", err)
		}
	}
	if h.opts.NodeSettings != "" {
		m := MountOn(h.t, h.tab, base, nil, nil)
		cfg, err := config.Load(m.Writer)
		if err != nil {
			h.t.Fatalf("harness: Load: %v", err)
		}
		var op admin.OperationID
		op[0] = 3
		req := config.Request{Key: admin.KeyFor(admin.LocalOwner(), op), Expected: cfg.Node().Version}
		if _, err := cfg.SetNodeSettings(req, []byte(h.opts.NodeSettings), func(config.Node) (json.RawMessage, error) {
			return json.RawMessage(`{"set":true}`), nil
		}); err != nil {
			h.t.Fatalf("harness: the node settings %s: %v", h.opts.NodeSettings, err)
		}
	}
}

// knobHit writes a hit of a knob of the loopback to the ledger, from the goroutine
// of the call that was hit.
func (h *H) knobHit(hit transport.Hit) {
	detail := fmt.Sprintf("call=%s node=%s", hit.Call, hit.Node)
	if hit.Message != "" {
		detail += " message=" + string(hit.Message)
	}
	if err := h.fixture.Append(ledger.KindKnobHit, ledger.Attrs{Knob: hit.Knob.String(), Detail: detail}); err != nil {
		h.t.Errorf("harness: ledger: %v", err)
	}
}

// loopbackQuiet is the loopback's condition of quiet. It always holds: a call
// that waits out a delay is not work, and one whose delay is due is a worker of
// the engine, which the engine's own condition counts. Its count is how many
// hits the knobs have had, so that a hit between two looks is not missed.
func (h *H) loopbackQuiet() (bool, uint64) {
	var n int
	for k := transport.KnobDropReceipt; k <= transport.KnobMutate; k++ {
		n += h.knobs.Hits(k)
	}
	return true, uint64(n)
}

// boot runs a process over the disk through the startup steps the engine has: the
// scan, the start of the loop, the admission of the adapter, the recovery of
// deliveries and the switch to serving.
//
// RollbackPaused and the clock's high-water mark are applied from zero at every start,
// as a daemon that was just launched has no memory of the last run: a restart does not
// carry the high-water mark over, and a node that Options made start paused starts
// paused every time.
func (h *H) boot(base fsx.FS, arm func(*fsxtest.Faulty)) *process {
	h.t.Helper()
	m := MountOn(h.t, h.tab, base, h.opts.Observer, arm)
	if mem, ok := base.(*fsxtest.Mem); ok {
		m.Mem = mem
	}
	cfg, err := config.Load(m.Writer)
	if err != nil {
		h.t.Fatalf("harness: Load: %v", err)
	}
	k, err := keys.Load(m.Writer)
	if err != nil {
		h.t.Fatalf("harness: keys.Load: %v", err)
	}
	h.gen++
	p := &process{mounted: m, cfg: cfg, log: &LogCapture{}}
	var st engine.Store = m.Store
	if h.opts.DiskFree != nil {
		st = freeSpace{Store: m.Store, free: h.opts.DiskFree}
	}
	deps := engine.Deps{
		Node: NodeID(), Clock: h.clk, HighWater: clock.NewHighWater(time.Time{}), RollbackPaused: h.opts.RollbackPaused,
		Writer: m.Writer, Store: st, Config: cfg, Keys: k, Rand: RandReader(h.opts.Seed + uint64(h.gen)),
		Log: slog.New(p.log),
	}
	p.eng, _, err = engine.Open(h.ctx, deps, h.opts.Params)
	if err != nil {
		h.t.Fatalf("harness: engine.Open: %v", err)
	}
	p.lb = transport.NewLoopback(NodeID(), p.eng, h.knobs)
	if err := p.eng.SetTransport(p.lb); err != nil {
		h.t.Fatalf("harness: SetTransport: %v", err)
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

// freeSpace is the store with the free space the test says: the stand-in for the
// query of the volume.
type freeSpace struct {
	engine.Store
	free func() uint64
}

func (f freeSpace) Available() (uint64, error) { return f.free(), nil }

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
	if p := h.current(); p != nil {
		if err := stop(p); err != nil {
			h.t.Errorf("harness: stopping the engine: %v", err)
		}
	}
	h.dying.Wait()
	h.cancel()
}

// current is the running process, nil while the node is down.
func (h *H) current() *process {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.proc
}

// running is the running process; it fails the test when the node is down, for the
// accessors that have nothing to return then.
func (h *H) running() *process {
	h.t.Helper()
	p := h.current()
	if p == nil {
		h.t.Fatal("harness: the node is not running: Start it first")
	}
	return p
}

// Engine is the engine of the running process.
func (h *H) Engine() *engine.Engine { return h.running().eng }

// Clock is the one test clock: the engine reads it, the ledger stamps with it,
// and a fake takes its Now method value as the func() time.Time it cannot do
// without.
func (h *H) Clock() *clocktest.Fake { return h.clk }

// Faults is the fault wrapper the running process writes through. A rule added to
// it applies to this process only, and the test fails if a rule never fires.
func (h *H) Faults() *fsxtest.Faulty { return h.running().mounted.Faulty }

// Disk is the in-memory file system the running process writes to; while the node
// is down, the one the next Start runs on, which is where a test changes files
// offline (see Stop and Kill). A test that writes to it while a process runs
// writes behind the process's back.
func (h *H) Disk() *fsxtest.Mem {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.disk == nil {
		h.t.Fatal("harness: the node runs on the real file system, which has no in-memory disk")
	}
	return h.disk
}

// Config is the configuration the running process loaded.
func (h *H) Config() *config.Config { return h.running().cfg }

// Log is what the running process's engine logged.
func (h *H) Log() *LogCapture { return h.running().log }

// Ctx is the context of the test; it ends when the test does.
func (h *H) Ctx() context.Context { return h.ctx }

// Direct registers a direct endpoint of the adapter Instance under the key name,
// through the entry the adapter host will call, and returns its address. The same
// name is the same endpoint, before and after a restart. A refusal is returned as
// the engine gave it.
func (h *H) Direct(name string) (string, error) {
	res, err := onProcess(h, func(e *engine.Engine) (adapterproto.RegisterResult, error) {
		return e.RegisterDirect(h.ctx, Instance, adapterproto.RegisterParams{Key: name, DisplayName: "Name of " + name})
	})
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
	return onProcess(c.h, func(e *engine.Engine) (authz.Declaration, error) { return e.Declare(ctx, caller, p) })
}

func (c currentEngine) Send(ctx context.Context, caller engine.Caller, r engine.SendRequest) (engine.SendResult, error) {
	return onProcess(c.h, func(e *engine.Engine) (engine.SendResult, error) { return e.Send(ctx, caller, r) })
}

func (c currentEngine) Reply(ctx context.Context, caller engine.Caller, r engine.ReplyRequest) (engine.SendResult, error) {
	return onProcess(c.h, func(e *engine.Engine) (engine.SendResult, error) { return e.Reply(ctx, caller, r) })
}

// onProcess is a call made to the process that is running. When that process is
// abandoned by a crash before the call returns, nobody could have read what it
// answers, so the call's result is that its outcome is unknown, whatever the old
// engine made of it: a client of a dead process sees a connection that ended, not
// a success.
func onProcess[T any](h *H, call func(*engine.Engine) (T, error)) (T, error) {
	p := h.current()
	if p == nil {
		var zero T
		return zero, engineagent.ErrNodeNotRunning
	}
	v, err := call(p.eng)
	if p.abandoned.Load() {
		var zero T
		return zero, fmt.Errorf("%w (the answer was %v)", engineagent.ErrProcessEnded, err)
	}
	return v, err
}

// Loopback is the loopback of the running process, for a test that makes a
// request no agent can: an envelope another node's endpoint would send.
func (h *H) Loopback() *transport.Loopback { return h.running().lb }

// The knobs of the loopback. They are the faults of the transport, are off until a
// test sets one, and each hit is written to the ledger as a knob.hit entry. A knob
// keyed by a message is armed before the message first goes on the wire: hold the
// sender's calls with Unreachable first, learn the message's identifier, arm, then
// let the calls go.

// DropReceipt makes the next n sends the target processed come back as a timeout.
func (h *H) DropReceipt(n int) { h.knobs.DropReceipt(n) }

// Unreachable makes every call to the node itself fail as unreachable, or stops
// doing so.
func (h *H) Unreachable(on bool) { h.knobs.Unreachable(NodeID(), on) }

// UnreachableNode is Unreachable for a node named by the test: a call to it fails as
// unreachable and is written to the ledger as a hit. The loopback reaches no node but
// its own, so a call to any other fails either way; without this knob nothing shows
// that the call was made.
func (h *H) UnreachableNode(node ident.NodeID, on bool) { h.knobs.Unreachable(node, on) }

// HoldReports keeps the reports of a message from its sender: its receipt comes
// back empty and a pull for it is answered empty.
func (h *H) HoldReports(message string) { h.knobs.HoldReports(delivery.MessageID(message)) }

// Release lets the reports of a held message through again.
func (h *H) Release(message string) { h.knobs.Release(delivery.MessageID(message)) }

// Delay makes every call that starts from now on wait d on the test clock first.
func (h *H) Delay(d time.Duration) { h.knobs.Delay(d) }

// Mutate makes the target take the next send of the message once more, with
// another body, as if another request had used its identifier.
func (h *H) Mutate(message string) { h.knobs.Mutate(delivery.MessageID(message)) }

// Mutated is what the target answered to the replay Mutate made, and whether one
// was made.
func (h *H) Mutated(message string) (transport.Replay, bool) {
	return h.knobs.Mutated(delivery.MessageID(message))
}

// VerifyReport verifies a status report the node signed about a message the node
// itself sent, as a sender verifies what a receipt carries, with the keys on the
// disk.
func (h *H) VerifyReport(rec record.Record) (envelope.StatusReport, error) {
	p := h.running()
	k, err := keys.Load(p.mounted.Writer)
	if err != nil {
		return envelope.StatusReport{}, err
	}
	accepted := p.cfg.RevocationSet().NetworkKey(k.Certificate(), k.SerialSeen().Serial)
	sr, err := envelope.VerifyStatusReport(rec, NodeID(), NodeID(), accepted, h.clk.Now())
	if err != nil {
		return envelope.StatusReport{}, err
	}
	return sr.Report(), nil
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
// only where the scenario it belongs to is judged by them. The records are what
// the engine has written by now, so read them after Quiesce: before it, work the
// engine has not finished is missing.
func (h *H) Records() (*store.Scan, error) { return h.running().mounted.Store.Scan() }

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
	point string
}

// At names the point of the run the crash is made at, for the entry the crash
// writes to the ledger: a crash walk gives the identifier of the write it killed
// at. The fixture does not interpret it.
func (m CrashMode) At(point string) CrashMode {
	m.point = point
	return m
}

// describe is the text of the fixture.crash entry: the point the test named, if
// any, and the mode with the seed of a power loss.
func (m CrashMode) describe() string {
	mode := "process crash"
	if m.power {
		mode = fmt.Sprintf("power loss, seed %d", m.seed)
	}
	if m.point == "" {
		return mode
	}
	return m.point + " (" + mode + ")"
}

// ProcessCrash kills the process: every write that completed is on the disk, the
// page cache outlives the process, and nothing else does.
var ProcessCrash = CrashMode{}

// PowerLoss cuts the power: only what was synced is certain, and the model of the
// disk decides the rest from the seed, so that a seed is a reproducible disk.
func PowerLoss(seed uint64) CrashMode { return CrashMode{power: true, seed: seed} }

// Crash is Kill followed by Start: the process is cut off, the disk a crash of the
// mode leaves is taken, and a new process starts on it. The test clock, the ledger
// and the endpoints registered through the fixture stay.
func (h *H) Crash(mode CrashMode) {
	h.t.Helper()
	h.Kill(mode)
	h.Start()
}

// Kill cuts the running process off from its disk, takes the disk a crash of the
// mode leaves at that instant and leaves the node down on it; Start runs a new
// process on that disk, and until then Disk is where a test makes files wrong
// offline.
//
// The old process is abandoned, not stopped: every call it makes to its disk after
// the cut fails, so what it was in the middle of reaches no disk; a call that was
// made to it and returns later is reported as an outcome that is unknown; and its
// engine is then stopped in the background, which is the cleanup of the test and
// not an event of the node. When an adapter host is attached, a crash cuts the
// pipe and sends no shutdown, as a process that died would. A test must still
// release anything it held the old loop with (an observer) before it ends.
//
// The crash is written to the ledger as a fixture.crash entry that says the mode, the
// seed of a power loss and the point the test named with CrashMode.At.
//
// The second word of the source is fixed, as the crash model's own tests fix it, so
// that a seed is all a test has to remember.
func (h *H) Kill(mode CrashMode) {
	h.t.Helper()
	if h.real != nil {
		h.t.Fatal("harness: a node on the real file system cannot be crashed: its disk cannot be cloned")
	}
	old := h.running()
	old.abandoned.Store(true)
	old.mounted.cutOff() // the disk below is now what the crash leaves
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
	h.mu.Lock()
	h.proc, h.disk = nil, disk
	h.mu.Unlock()
	if err := h.fixture.Append(ledger.KindFixtureCrash, ledger.Attrs{CrashPoint: mode.describe()}); err != nil {
		h.t.Fatalf("harness: ledger: %v", err)
	}
}

// Restart is Stop followed by Start.
func (h *H) Restart() {
	h.t.Helper()
	h.Stop()
	h.Start()
}

// Stop stops the running process the way the daemon does on a normal exit and
// leaves the node down on the same disk, which a test may change before Start.
// Calls to a node that is down fail as a refused connection does: unavailable.
func (h *H) Stop() {
	h.t.Helper()
	old := h.running()
	if err := stop(old); err != nil {
		h.t.Fatalf("harness: stopping the engine: %v", err)
	}
	h.mu.Lock()
	h.proc = nil
	h.mu.Unlock()
}

// Start starts a process on the disk of the node that is down, through the startup
// steps the engine has, and returns when it is serving.
func (h *H) Start() {
	h.t.Helper()
	h.StartWith(nil)
}

// StartWith is Start with a chance to arm the fault wrapper the new process will
// write through: arm is called with it before the process's recovery runs, so a
// rule can target a write that only a recovery makes. A rule that never fires
// fails the test, as always.
func (h *H) StartWith(arm func(*fsxtest.Faulty)) {
	h.t.Helper()
	if h.current() != nil {
		h.t.Fatal("harness: the node is already running")
	}
	var base fsx.FS = h.real
	if base == nil {
		base = h.Disk()
	}
	p := h.boot(base, arm)
	h.mu.Lock()
	h.proc = p
	h.mu.Unlock()
}

// The conditions of quiet that the fixture's own parts answer, besides the
// engine's. A part that is added to the fixture (the adapter host and the fake
// adapter, the fake runtime) registers its conditions here.
//
// A condition answers whether it holds and how much it has done so far (a count
// that never goes down: the notifications a fake sent, the turns it ended). Holding
// is a state; the count is what shows that the state was not left and reached
// again in between.
type quietCondition struct {
	name  string
	state func() (holds bool, progress uint64)
}

func (h *H) addQuiet(name string, state func() (holds bool, progress uint64)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.quiet = append(h.quiet, quietCondition{name, state})
}

// reading is one look at every condition of the fixture.
type reading struct {
	// unmet names the conditions that do not hold.
	unmet []string
	// progress is the count of each condition, in the order they were added.
	progress []uint64
}

// look reads every condition of the fixture once.
func (h *H) look() reading {
	h.mu.Lock()
	conds := append([]quietCondition(nil), h.quiet...)
	h.mu.Unlock()
	r := reading{progress: make([]uint64, len(conds))}
	for i, c := range conds {
		holds, progress := c.state()
		r.progress[i] = progress
		if !holds {
			r.unmet = append(r.unmet, c.name)
		}
	}
	return r
}

// expect is what the engine is asked to be quiet against: the numbers the fixture's
// parts know from outside the engine. It is made again for every round, because
// those numbers grow with what the fakes did while the engine was being asked.
// The engine's Expect has no field yet; the adapter host adds the notification
// and hold counts, which this takes from the conditions' counters.
func (h *H) expect() engine.Expect { return engine.Expect{} }

// QuiesceError says why the system did not go quiet in time.
type QuiesceError struct {
	// Waited is how long the wait lasted, on the real clock.
	Waited time.Duration
	// Asked is whether the engine was asked at all: it is not while a condition of
	// the fixture does not hold.
	Asked bool
	// Engine is the last report of the engine, and Answered whether there was one.
	// An engine that was asked and neither answered nor refused has a loop that is
	// inside a command.
	Engine   engine.Report
	Answered bool
	// Unmet names the conditions of the fixture that did not hold.
	Unmet []string
	// Err is why the wait ended: the context, or the engine's refusal.
	Err error
}

func (e *QuiesceError) Error() string {
	s := fmt.Sprintf("harness: not quiet after %v", e.Waited.Round(time.Millisecond))
	_, refused := engine.ReasonOf(e.Err)
	switch {
	case !e.Asked:
	case e.Answered:
		s += fmt.Sprintf(": the engine reports %+v", e.Engine)
	case refused:
		s += ": the engine refused the probe"
	default:
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
// now: one whole round has changed no count. A round is a look at the conditions of
// the fixture, which must all hold, then the engine's own conditions, then a look
// at the fixture's again: what the engine did may have made a fake busy, and a fake
// that took work during the round, finished it and went idle again holds on both
// looks, so the counts of the two looks must be equal as well. It fails with a
// *QuiesceError when the limit, on the real clock, ends first.
func (h *H) Quiesce(opts ...scenario.QuiesceOption) error {
	o := scenario.ResolveQuiesce(opts)
	ctx, cancel := context.WithTimeout(h.ctx, o.Timeout)
	defer cancel()
	return h.quiesce(ctx, o)
}

func (h *H) quiesce(ctx context.Context, _ scenario.QuiesceOptions) error {
	sys := clock.System()
	begin := sys.Now()
	// fail ends the wait. Whether the loop answered a probe is read from the report:
	// a satisfied report ends the wait, and an unsatisfied one has a count that is not
	// zero, so an all-zero report on this path is the absence of one. What an
	// engine's refusal means is decided from the error and never from the report.
	fail := func(asked bool, rep engine.Report, unmet []string, err error) error {
		if ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
			err = fmt.Errorf("%v: %w", err, ctx.Err())
		}
		return &QuiesceError{Waited: sys.Now().Sub(begin), Asked: asked, Engine: rep, Answered: rep != engine.Report{}, Unmet: unmet, Err: err}
	}
	if err := ctx.Err(); err != nil {
		return fail(false, engine.Report{}, nil, err)
	}
	if h.current() == nil {
		return fail(false, engine.Report{}, nil, engineagent.ErrNodeNotRunning)
	}
	var unmet []string
	for {
		before := h.look()
		if unmet = before.unmet; len(unmet) > 0 {
			timer := sys.NewTimer(quietPoll)
			select {
			case <-timer.C():
			case <-ctx.Done():
				timer.Stop()
				return fail(false, engine.Report{}, unmet, ctx.Err())
			}
			continue
		}
		rep, err := h.Engine().Quiesce(ctx, h.expect())
		if err != nil {
			return fail(true, rep, nil, err)
		}
		after := h.look()
		if unmet = after.unmet; len(unmet) == 0 && slices.Equal(before.progress, after.progress) {
			return nil
		}
	}
}
