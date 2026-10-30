package harness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
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
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/daemon"
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
	// OpenPackage opens an adapter package as a reader rooted at its source.
	// Every generation passes it unchanged to the engine; nil leaves installation
	// without a package reader.
	OpenPackage func(string) (fsx.Reader, error)
	// ArchiveSweepEvery, when positive, has the node run the archive sweep every
	// period on the fixture's clock, as the daemon does. Zero runs none.
	ArchiveSweepEvery time.Duration
	// RollbackPaused forces a durable rollback pause at every start, restarts
	// included. An existing pause keeps its original start time.
	RollbackPaused bool
	// Presets are presets the node has besides the one `init` makes, by name, each
	// the JSON object `presets add` takes, added through the configuration's own
	// change before the first start.
	Presets map[string]string
	// Adapter, when set, gives the node an adapter host that starts a fake adapter
	// for the instance Instance (and for each of Adapter.ExtraInstances) in the test
	// process, on the fixture's test clock, and admits the instances through it as the
	// daemon does. Nil keeps the node
	// without a host: the fixture admits the instance itself through the engine's
	// entry, and Adapter has nothing to return.
	Adapter *AdapterOptions
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

	// launcher starts the fake adapters of the host, nil without Options.Adapter.
	// It is the fixture's and not a process's: the launches are kept across
	// restarts, and the ledger writer of the adapter is one for all of them.
	launcher      *AdapterLauncher
	adapterInputs *fakeadapter.InputRegistry
	// instances are the adapter instances of the node, Instance first; without
	// Options.Adapter Instance alone, which the fixture then admits itself.
	instances []string
	// runtimes are the fake native runtimes of the adapters that declare one, by
	// instance; none otherwise.
	runtimes map[string]*fakeruntime.Runtime
	// adapterHome is the data directory the host and the engine are told: the real
	// directory the instance lock files are in. A lock path is not a socket path,
	// so a temporary directory of the test will do.
	adapterHome string

	mu sync.Mutex
	// proc is the running process, nil while the node is down. disk is the disk
	// the running process writes to, or while the node is down the one the next
	// Start runs on.
	proc *process
	// unreaped prevents another start after cleanup exceeded its limit.
	unreaped *process
	disk     *fsxtest.Mem
	gen      int
	names    map[string]endpoint
	agent    map[string]fakeagent.Agent
	steps    map[string]bool
	quiet    []quietCondition
}

// process is one run of the node over a disk.
type process struct {
	cancel  context.CancelFunc
	mounted *Mounted
	cfg     *config.Config
	log     *LogCapture
	eng     *engine.Engine
	lb      *transport.Loopback
	// host is the adapter host of the process, nil when the fixture has none.
	host *adapterhost.Host

	// abandoned is set when a crash cuts the process off: a call that was made to
	// it and returns afterwards has no answer anyone could have read.
	abandoned atomic.Bool
}

// endpoint is a direct endpoint registered through the fixture.
type endpoint struct {
	// instance is the adapter instance the endpoint belongs to.
	instance string
	ref      adapterproto.EndpointRef
	addr     ident.Address
}

var _ scenario.Env = (*H)(nil)

// New makes the node (initialised through the program's own code, the adapter
// Instance installed and enabled), starts the engine and returns when it is
// running. Everything it starts is stopped when the test ends, and the test
// fails if a goroutine of the fixture's packages is left.
func New(t testing.TB, o Options) *H {
	t.Helper()
	h := NewStopped(t, o)
	h.Start()
	return h
}

// NewStopped prepares the same node as New, including its initialized disk and
// fakes, but leaves it down so its first start can be controlled with TryStart.
func NewStopped(t testing.TB, o Options) *H {
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
		t: t, opts: o, instances: []string{Instance},
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
	packages := adapterPackages(t, o.Adapter, o.MaxDirect)
	if o.Adapter != nil {
		h.adapterHome = t.TempDir() // before the cleanup of the fixture: it is removed after the fixture has stopped
		for _, p := range packages[1:] {
			h.instances = append(h.instances, p.instance)
		}
		h.adapterInputs, err = fakeadapter.NewInputRegistry(h.instances, o.Adapter.Inputs)
		if err != nil {
			t.Fatalf("harness: adapter inputs: %v", err)
		}
		h.runtimes = map[string]*fakeruntime.Runtime{}
		var launchOrder []string
		for _, p := range packages {
			if !p.disabled {
				launchOrder = append(launchOrder, p.instance)
			}
		}
		lc := LauncherConfig{
			T: t, Ctx: h.ctx, Now: h.clk.Now, Ledgers: h.led, Manifest: AdapterManifest(o.MaxDirect),
			Instances: map[string]LauncherInstance{}, Order: launchOrder, Inputs: h.adapterInputs,
		}
		for i, p := range packages {
			name := p.instance
			own := LauncherInstance{Manifest: p.options}
			if own.Manifest.Runtime {
				// The seed names the native sessions, so each runtime has one of its own.
				rt, err := fakeruntime.New(fakeruntime.Config{Now: h.clk.Now, Ledgers: h.led, Dir: t.TempDir(), Seed: o.Seed + uint64(i), Runner: engineRunner{h}})
				if err != nil {
					t.Fatalf("harness: the fake runtime of %s: %v", name, err)
				}
				h.runtimes[name] = rt
				own.Runtime = rt
			}
			if i == 0 {
				lc.Manifest, lc.Runtime = own.Manifest, own.Runtime
			} else {
				lc.Instances[name] = own
			}
			h.addAdapterQuiet(name)
			if rt := h.runtimes[name]; rt != nil {
				h.addRuntimeQuiet(name, rt)
			}
		}
		h.launcher = NewAdapterLauncher(lc)
	}
	t.Cleanup(h.close)
	var base fsx.FS
	if o.FS == RealFS {
		h.real = realDisk(t, o, packages)
		base = h.real
	} else if o.Disk != nil {
		h.disk = o.Disk
		base = h.disk
	} else {
		h.disk = seededDisk(t, o.Start, packages)
		base = h.disk
	}
	if o.Disk == nil {
		h.prepare(base)
	}
	return h
}

// checkedExtraInstances fails the test when a name of AdapterOptions.ExtraInstances
// is not a valid adapter identifier, is Instance, or is given twice: the node
// would refuse it much later and less clearly.
func checkedExtraInstances(t testing.TB, names []string) []string {
	t.Helper()
	seen := map[string]bool{Instance: true}
	for _, n := range names {
		if err := adapterproto.ValidateAdapterID(n); err != nil {
			t.Fatalf("harness: ExtraInstances: %v", err)
		}
		if seen[n] {
			t.Fatalf("harness: ExtraInstances: the instance %q is Instance or given twice", n)
		}
		seen[n] = true
	}
	return names
}

// realDisk makes an initialised node in a directory of the real file system. It
// is made for the test, since nothing of it is shared the way the in-memory
// snapshot is.
func realDisk(t testing.TB, o Options, packages []adapterPackage) fsx.FS {
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
	seedNode(t, m.Writer, o.Start, packages)
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
	if len(h.opts.Presets) > 0 {
		m := MountOn(h.t, h.tab, base, nil, nil)
		cfg, err := config.Load(m.Writer)
		if err != nil {
			h.t.Fatalf("harness: Load: %v", err)
		}
		for i, name := range slices.Sorted(maps.Keys(h.opts.Presets)) {
			var op admin.OperationID
			op[0], op[1] = 4, byte(i)
			req := config.Request{Key: admin.KeyFor(admin.LocalOwner(), op), Expected: cfg.Presets().Version}
			if _, err := cfg.AddPreset(req, name, []byte(h.opts.Presets[name]), func(config.Presets) (json.RawMessage, error) {
				return json.RawMessage(`{"added":true}`), nil
			}); err != nil {
				h.t.Fatalf("harness: the preset %s: %v", name, err)
			}
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
// scan, the start of the loop, the admission of the adapter (through the host when
// the fixture has one, otherwise by the fixture itself), the recovery of deliveries
// and the switch to serving.
//
// Every start restores the clock through the daemon's lifecycle. An explicit
// RollbackPaused option forces a durable pause when none was restored.
func (h *H) boot(ctx context.Context, p *process, check func() error) error {
	m := p.mounted
	if err := check(); err != nil {
		return err
	}
	if err := m.Writer.Recover(); err != nil {
		return fmt.Errorf("Recover: %w", err)
	}
	if err := check(); err != nil {
		return err
	}
	cfg, err := config.Load(m.Writer)
	if err != nil {
		return fmt.Errorf("Load: %w", err)
	}
	k, err := keys.Load(m.Writer)
	if err != nil {
		return fmt.Errorf("keys.Load: %w", err)
	}
	p.cfg = cfg
	if err := check(); err != nil {
		return err
	}
	now, log := h.clk.Now(), slog.New(p.log)
	clockState, err := daemon.RestoreClock(m.Writer.Reader(), k, now, log)
	if err != nil {
		return fmt.Errorf("RestoreClock: %w", err)
	}
	if h.opts.RollbackPaused && !clockState.RollbackPaused {
		if err := k.SaveClock(keys.ClockState{HighWater: clockState.HighWater.Value(), RollbackPausedSince: now}); err != nil {
			return fmt.Errorf("persisting the forced clock pause: %w", err)
		}
		clockState.RollbackPaused, clockState.RollbackPausedSince = true, now
	}
	if err := check(); err != nil {
		return err
	}
	var st engine.Store = m.Store
	if h.opts.DiskFree != nil {
		st = freeSpace{Store: m.Store, free: h.opts.DiskFree}
	}
	deps := engine.Deps{
		Node: NodeID(), Clock: h.clk, HighWater: clockState.HighWater,
		RollbackPaused: clockState.RollbackPaused, RollbackPausedSince: clockState.RollbackPausedSince,
		Writer: m.Writer, Store: st, Config: cfg, Keys: k, Rand: RandReader(h.opts.Seed + uint64(h.gen)),
		OpenPackage: h.opts.OpenPackage, Log: log,
	}
	if h.launcher != nil {
		// The host runs on the test clock: its pings, its backoff and its waits for a
		// process to end are moved by Advance, and nothing in it waits for real time.
		p.host, err = adapterhost.New(adapterhost.Config{
			Launcher: h.launcher, Params: h.opts.Adapter.Host, Clock: h.clk, Log: slog.New(p.log),
		})
		if err != nil {
			return fmt.Errorf("adapterhost.New: %w", err)
		}
		deps.Adapters = abandonable(p.host, &p.abandoned)
		deps.Home = h.adapterHome
	}
	p.eng, _, err = engine.Open(ctx, deps, h.opts.Params)
	if err != nil {
		return fmt.Errorf("engine.Open: %w", err)
	}
	if err := check(); err != nil {
		return err
	}
	p.lb = transport.NewLoopback(NodeID(), p.eng, h.knobs)
	if err := p.eng.SetTransport(p.lb); err != nil {
		return fmt.Errorf("SetTransport: %w", err)
	}
	if err := daemon.AddClockState(p.eng, k); err != nil {
		return fmt.Errorf("AddClockState: %w", err)
	}
	if h.opts.ArchiveSweepEvery > 0 {
		if err := p.eng.AddArchiveSweep(h.opts.ArchiveSweepEvery); err != nil {
			return fmt.Errorf("AddArchiveSweep: %w", err)
		}
	}
	p.eng.Start()
	if p.host != nil {
		env := adapterhost.Env{Home: h.adapterHome, Source: cfg, Logs: newLogs(h.t), Engine: apiwire.AdapterEngine(p.eng)}
		if err := p.host.Start(ctx, env); err != nil {
			return fmt.Errorf("starting the adapter host: %w", err)
		}
		// Host.Start ends at adapter.start, before the asynchronous probe draws
		// tokens. Finish those draws before recovery or a script uses the same
		// seeded stream, including after a restart with older launches retained.
		waitCtx, cancel := context.WithTimeout(ctx, adapterProbeWait)
		defer cancel()
		for instance, in := range cfg.Instances().Items {
			if in.Enabled {
				if err := h.launcher.awaitProbe(waitCtx, instance); err != nil {
					return fmt.Errorf("waiting for the adapter probe of %s: %w", instance, err)
				}
			}
		}
	} else {
		info := adapterproto.InitializeResult{
			ProtocolVersion: 1,
			Adapter:         adapterproto.AdapterInfo{ID: Instance, Version: "1.0.0"}, // the manifest the fixture installs
			Capabilities:    h.admit.Capabilities(),
		}
		if _, err := p.eng.AdapterAdmitted(ctx, Instance, h.admit, info); err != nil {
			return fmt.Errorf("AdapterAdmitted: %w", err)
		}
	}
	if err := check(); err != nil {
		return err
	}
	if err := p.eng.RecoverDelivery(ctx); err != nil {
		return fmt.Errorf("RecoverDelivery: %w", err)
	}
	return check()
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

// stop stops a process the way the daemon does on a normal exit: the engine begins
// to refuse, the adapter host shuts the adapters down, and the engine ends. The
// host is stopped on the real clock's limit, as the engine is: a process that will
// not end (a surviving one) is the test's to end first.
func stop(p *process) error {
	defer p.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), stopLimit)
	defer cancel()
	p.eng.BeginShutdown()
	var hostErr error
	if p.host != nil {
		if hostErr = p.host.Shutdown(ctx); hostErr != nil {
			hostErr = fmt.Errorf("stopping the adapter host: %w", hostErr)
		}
	}
	return errors.Join(hostErr, p.eng.Stop(ctx))
}

func (h *H) close() {
	if h.launcher != nil {
		h.launcher.endSurvivors()
	}
	if p := h.current(); p != nil {
		if err := stop(p); err != nil {
			h.t.Errorf("harness: stopping the node: %v", err)
		}
	}
	h.dying.Wait()
	if h.unreaped != nil {
		if err := h.reapStart(h.unreaped); err != nil {
			h.t.Errorf("harness: reaping a failed start: %v", err)
		}
	}
	if h.launcher != nil {
		h.launcher.Close()
	}
	h.cancel()
	for _, name := range h.instances {
		if rt := h.runtimes[name]; rt != nil {
			if err := rt.Close(); err != nil {
				h.t.Errorf("harness: closing the fake runtime of %s: %v", name, err)
			}
		}
	}
}

// current is the running process, nil while the node is down.
func (h *H) current() *process {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.proc
}

// currentProcess selects the process once for an operation that can fail.
func (h *H) currentProcess() (*process, error) {
	p := h.current()
	if p == nil {
		return nil, engineagent.ErrNodeNotRunning
	}
	return p, nil
}

// running is the running process; it fails the test when the node is down, for the
// accessors that have nothing to return then.
func (h *H) running() *process {
	h.t.Helper()
	p, err := h.currentProcess()
	if err != nil {
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
func (h *H) Direct(name string) (string, error) { return h.DirectOn(Instance, name) }

// DirectOn is Direct for the endpoint of another instance of the fixture
// (AdapterOptions.ExtraInstances). A name stands for one endpoint of the whole
// fixture, so the same name on two instances fails the test.
func (h *H) DirectOn(instance, name string) (string, error) {
	h.t.Helper()
	if !slices.Contains(h.instances, instance) {
		h.t.Fatalf("harness: DirectOn(%q, %q): the fixture has no such instance", instance, name)
	}
	h.mu.Lock()
	known, ok := h.names[name]
	h.mu.Unlock()
	if ok && known.instance != instance {
		h.t.Fatalf("harness: DirectOn(%q, %q): the name is already an endpoint of %q", instance, name, known.instance)
	}
	res, err := onProcess(h, func(e *engine.Engine) (adapterproto.RegisterResult, error) {
		return e.RegisterDirect(h.ctx, instance, adapterproto.RegisterParams{Key: name, DisplayName: "Name of " + name})
	})
	if err != nil {
		return "", err
	}
	id, err := ident.ParseEndpointID(string(res.Endpoint))
	if err != nil {
		return "", fmt.Errorf("harness: the engine returned the endpoint %q: %w", res.Endpoint, err)
	}
	ep := endpoint{instance: instance, ref: res.Endpoint, addr: ident.Address{Node: NodeID(), Endpoint: id}}
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
	inner := engineagent.New(currentEngine{h}, engine.ByAdapter(ep.instance, ep.ref))
	a := fakeagent.Recording(inner, w, id, h.recordingOptions())
	h.agent[name] = a
	return a
}

// recordingOptions are what every agent of the fixture writes to the ledger besides
// its calls: what each page showed, one model.saw entry for each message or part, with
// the notices of the node's own system endpoint told from the messages of others.
func (h *H) recordingOptions() fakeagent.RecordingOptions {
	system := ident.Address{Node: NodeID(), Endpoint: ident.SystemEndpoint()}.String()
	return fakeagent.RecordingOptions{
		ModelSaw: true,
		Feedback: func(from, _ string) (string, bool) { return "notice", from == system },
	}
}

// Session returns the agent of a session endpoint, by the endpoint's identifier as the
// reply to its creation names it: the engine-layer backend that holds the token of the
// session, as the tools of a session do. The token is read from the endpoint's record
// on the disk, so a test reaches a session only through what the node has made, never
// by registering one. The same agent is returned for the same endpoint for the life of
// the fixture, across restarts.
func (h *H) Session(endpointID string) fakeagent.Agent {
	h.t.Helper()
	id, err := ident.ParseEndpointID(endpointID)
	if err != nil {
		h.t.Fatalf("harness: Session(%q): %v", endpointID, err)
	}
	h.mu.Lock()
	a, ok := h.agent["session/"+endpointID]
	h.mu.Unlock()
	if ok {
		return a
	}
	sc, err := h.Records()
	if err != nil {
		h.t.Fatalf("harness: Session(%q): %v", endpointID, err)
	}
	var token adapterproto.Token
	for _, ep := range sc.Endpoints {
		if ep.ID == id && ep.Kind == adapterreg.KindSession && ep.Status == store.EndpointOpen {
			token = adapterproto.Token(base64.RawURLEncoding.EncodeToString(ep.Token))
		}
	}
	if token == "" {
		h.t.Fatalf("harness: Session(%q): the node holds no open session endpoint of that identifier", endpointID)
	}
	w, err := h.led.Open("agent/"+id.String(), 0, h.clk.Now)
	if err != nil {
		h.t.Fatalf("harness: ledger writer of the session %s: %v", id, err)
	}
	a = fakeagent.Recording(engineagent.New(currentEngine{h}, engine.ByToken(token)), w, id.String(), h.recordingOptions())
	h.mu.Lock()
	h.agent["session/"+endpointID] = a
	h.mu.Unlock()
	return a
}

// currentEngine is the agent service of whichever engine is running, so that an
// agent made before a restart acts on the one after it.
type currentEngine struct{ h *H }

func (c currentEngine) Me(ctx context.Context, caller engine.Caller) (engine.MeView, error) {
	return onProcess(c.h, func(e *engine.Engine) (engine.MeView, error) { return e.Me(ctx, caller) })
}

func (c currentEngine) Peers(ctx context.Context, caller engine.Caller) ([]engine.PeerView, error) {
	return onProcess(c.h, func(e *engine.Engine) ([]engine.PeerView, error) { return e.Peers(ctx, caller) })
}

func (c currentEngine) Directory(ctx context.Context, caller engine.Caller, q engine.DirectoryQuery) (engine.DirectoryPage, error) {
	return onProcess(c.h, func(e *engine.Engine) (engine.DirectoryPage, error) { return e.Directory(ctx, caller, q) })
}

func (c currentEngine) PresentProbeToken(ctx context.Context, token adapterproto.Token, frontend adapterproto.Frontend) (engine.ProbeTarget, error) {
	return onProcess(c.h, func(e *engine.Engine) (engine.ProbeTarget, error) {
		return e.PresentProbeToken(ctx, token, frontend)
	})
}

func (c currentEngine) Declare(ctx context.Context, caller engine.Caller, p engine.DeclarationPatch) (authz.Declaration, error) {
	return onProcess(c.h, func(e *engine.Engine) (authz.Declaration, error) { return e.Declare(ctx, caller, p) })
}

func (c currentEngine) Send(ctx context.Context, caller engine.Caller, r engine.SendRequest) (engine.SendResult, error) {
	return onProcess(c.h, func(e *engine.Engine) (engine.SendResult, error) { return e.Send(ctx, caller, r) })
}

func (c currentEngine) Reply(ctx context.Context, caller engine.Caller, r engine.ReplyRequest) (engine.SendResult, error) {
	return onProcess(c.h, func(e *engine.Engine) (engine.SendResult, error) { return e.Reply(ctx, caller, r) })
}

func (c currentEngine) Control(ctx context.Context, caller engine.Caller, r engine.ControlRequest) (engine.SendResult, error) {
	return onProcess(c.h, func(e *engine.Engine) (engine.SendResult, error) { return e.Control(ctx, caller, r) })
}

func (c currentEngine) Recv(ctx context.Context, caller engine.Caller, r engine.RecvRequest) ([]byte, error) {
	return onProcess(c.h, func(e *engine.Engine) ([]byte, error) { return e.Recv(ctx, caller, r) })
}

func (c currentEngine) Ack(ctx context.Context, caller engine.Caller, r engine.AckRequest) ([]adapterproto.PartAck, error) {
	return onProcess(c.h, func(e *engine.Engine) ([]adapterproto.PartAck, error) { return e.Ack(ctx, caller, r) })
}

func (c currentEngine) MessageStatus(ctx context.Context, caller engine.Caller, id adapterproto.MessageID) (adapterproto.MessageStatusResult, error) {
	return onProcess(c.h, func(e *engine.Engine) (adapterproto.MessageStatusResult, error) {
		return e.MessageStatus(ctx, caller, id)
	})
}

// onProcess is a call made to the process that is running. When that process is
// abandoned by a crash before the call is made or returns, nobody could have read
// what it answers, so the call's result is that its outcome is unknown, whatever
// the old engine made of it: a client of a dead process sees a connection that
// ended, not a success.
func onProcess[T any](h *H, call func(*engine.Engine) (T, error)) (T, error) {
	p, err := h.currentProcess()
	if err != nil {
		var zero T
		return zero, err
	}
	return unlessAbandoned(&p.abandoned, func() (T, error) { return call(p.eng) })
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
// disk. A down node returns engineagent.ErrNodeNotRunning.
func (h *H) VerifyReport(rec record.Record) (envelope.StatusReport, error) {
	p, err := h.currentProcess()
	if err != nil {
		return envelope.StatusReport{}, err
	}
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

// VerifyControlReply is VerifyReport for the control reply of a signal the node
// itself sent. A down node returns engineagent.ErrNodeNotRunning.
func (h *H) VerifyControlReply(rec record.Record) (envelope.ControlReply, error) {
	p, err := h.currentProcess()
	if err != nil {
		return envelope.ControlReply{}, err
	}
	k, err := keys.Load(p.mounted.Writer)
	if err != nil {
		return envelope.ControlReply{}, err
	}
	accepted := p.cfg.RevocationSet().NetworkKey(k.Certificate(), k.SerialSeen().Serial)
	sr, err := envelope.VerifyControlReply(rec, NodeID(), NodeID(), accepted, h.clk.Now())
	if err != nil {
		return envelope.ControlReply{}, err
	}
	return sr.Reply(), nil
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
// engine has not finished is missing. A down node returns engineagent.ErrNodeNotRunning.
func (h *H) Records() (*store.Scan, error) {
	p, err := h.currentProcess()
	if err != nil {
		return nil, err
	}
	return p.mounted.Store.Scan()
}

// Adapter is the controller of the fake adapter process that was started last for
// the instance, the one the host is talking to now or the last it talked to. A
// fixture without an adapter host has none, and neither has one in which no process
// was started yet: a stand-in would make a script pass for what it did not exercise.
func (h *H) Adapter(instance string) (fakeadapter.Controller, error) {
	if h.launcher == nil {
		return nil, scenario.NotAvailable("the fixture has no adapter host (Options.Adapter)")
	}
	ls := h.launcher.Launches(instance)
	if len(ls) == 0 {
		return nil, scenario.NotAvailable("no adapter process was started for " + instance)
	}
	return ls[len(ls)-1].Controller(), nil
}

// AdapterLaunches are the adapter processes the host started for the instance, in
// the order it started them, across the restarts of the node. Nil when the fixture
// has no adapter host.
func (h *H) AdapterLaunches(instance string) []*AdapterLaunch {
	if h.launcher == nil {
		return nil
	}
	return h.launcher.Launches(instance)
}

// addAdapterQuiet registers what the fixture asks of the latest process of an
// adapter instance for the system to be quiet: it has received its probe or ended,
// and has no request of its own that the daemon has not answered. Its progress
// counts launches and the notifications sent by the latest one. A
// process that is hung or that was abandoned is not the latest once the host has
// started another, so it can make the fixture wait for ever only until then.
func (h *H) addAdapterQuiet(instance string) {
	h.addQuiet("the adapter of "+instance+" has not received its probe or ended", func() (bool, uint64) {
		settled, launches := h.launcher.probeState(instance)
		// A disabled instance may never have launched. Startup separately waits
		// for enabled instances, including their first launch.
		return launches == 0 || settled, launches
	})
	h.addQuiet("the adapter of "+instance+" has requests of its own that are not answered", func() (bool, uint64) {
		st, ok := h.latestAdapterStats(instance)
		if !ok {
			return true, 0
		}
		return st.Outstanding == 0, uint64(st.NotificationsSent)
	})
}

// addRuntimeQuiet registers what the fixture asks of the fake runtime of an instance for
// the system to be quiet: no turn of it has actions that are running or waiting to run.
// What such a turn does, a receive through the engine, the report of what the model was
// shown, the end of the turn, happens in a goroutine of the runtime that neither the
// adapter's requests nor the engine's own counts show while it is on its way, so a quiet
// that did not wait for it would return in the gap before the turn's next call and
// the run would depend on how fast that goroutine is. Its progress is how often a
// turn's actions began or finished.
func (h *H) addRuntimeQuiet(instance string, rt *fakeruntime.Runtime) {
	h.addQuiet("the runtime of "+instance+" has a turn whose actions are not finished", rt.Activity)
}

// latestAdapterStats is what the latest process of the instance reports of itself.
func (h *H) latestAdapterStats(instance string) (fakeadapter.StatsReply, bool) {
	ls := h.launcher.Launches(instance)
	if len(ls) == 0 {
		return fakeadapter.StatsReply{}, false
	}
	st, err := ls[len(ls)-1].Controller().Stats(h.ctx)
	if err != nil {
		return fakeadapter.StatsReply{}, false
	}
	return st, true
}

// Runtime is the controller of the fake native runtime of the adapter Instance,
// which exists when that instance's declaration includes a runtime. Native
// sessions exist only through a session creation.
func (h *H) Runtime() (fakeruntime.Controller, error) { return h.RuntimeOf(Instance) }

// RuntimeOf is Runtime for another instance of the fixture
// (AdapterOptions.ExtraInstances): each has a runtime of its own, and the sessions
// of an instance are those of its runtime.
func (h *H) RuntimeOf(instance string) (fakeruntime.Controller, error) {
	rt := h.runtimes[instance]
	if rt == nil {
		return nil, scenario.NotAvailable("the fixture has no runtime of " + instance + " (the instance must be listed and declare a runtime)")
	}
	return rt, nil
}

// RuntimeAddr is the address of the runtime endpoint of the adapter instance, the
// target of a session creation. It is the id the configuration keeps for the
// instance, so it is known from the first start on and the same after a restart.
func (h *H) RuntimeAddr() string { h.t.Helper(); return h.RuntimeAddrOf(Instance) }

// RuntimeAddrOf is RuntimeAddr for another instance of the fixture.
func (h *H) RuntimeAddrOf(instance string) string {
	h.t.Helper()
	id := h.Config().EndpointIDs(instance).Runtime
	if id.IsZero() {
		h.t.Fatalf("harness: the instance %s has no runtime endpoint: the instance must declare a runtime and its adapter must have been taken on", instance)
	}
	return ident.Address{Node: NodeID(), Endpoint: id}.String()
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
// not an event of the node. When an adapter host is attached, a crash aborts it:
// the pipes are cut and no shutdown is sent, as a process that died would. A test must still
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
	if err := h.cutCrash(old, mode); err != nil {
		h.t.Fatalf("harness: crash: %v", err)
	}
	if old.host != nil {
		// The host is aborted, not shut down, and the pipes are cut: a process that
		// died sends its adapters nothing. This is after the disk was taken, so
		// that what the host does while it ends reaches no disk.
		old.host.Abort()
		h.launcher.cutAll()
	}
	h.dying.Add(1)
	go func() {
		defer h.dying.Done()
		_ = stop(old) // a dying process: how it ends is no one's business
	}()
	h.mu.Lock()
	h.proc = nil
	h.mu.Unlock()
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
// fails the test unless a real crash ends that generation.
func (h *H) StartWith(arm func(*fsxtest.Faulty)) {
	h.t.Helper()
	if _, err := h.TryStart(h.ctx, StartOptions{Arm: arm}); err != nil {
		h.t.Fatalf("harness: starting the node: %v", err)
	}
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
// With an adapter host it names the instance's connection and says how many
// notifications the latest process of the instance has sent and which requests it
// holds on purpose: the engine is quiet only when it has handled that many.
func (h *H) expect() engine.Expect {
	if h.launcher == nil {
		return engine.Expect{}
	}
	adapters := make(map[string]engine.AdapterExpect, len(h.instances))
	for _, instance := range h.instances {
		var x engine.AdapterExpect
		if st, ok := h.latestAdapterStats(instance); ok {
			x = engine.AdapterExpect{Notifications: st.NotificationsSent, Held: st.Held}
		}
		adapters[instance] = x
	}
	return engine.Expect{Adapters: adapters}
}

// QuiesceError says why the wait for quiet failed.
type QuiesceError struct {
	// Waited is how long the wait lasted, on the real clock.
	Waited time.Duration
	// Asked is whether the engine was asked at all: it is not while a condition of
	// the fixture does not hold.
	Asked bool
	// Engine is the last report of the engine, and Answered whether there was one.
	// A live engine that was asked and neither answered nor refused has a loop that
	// is inside a command.
	Engine   engine.Report
	Answered bool
	// Unmet names the conditions of the fixture that did not hold.
	Unmet []string
	// Err is why the wait ended: the context, the engine's refusal, or abandonment.
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
	case errors.Is(e.Err, engineagent.ErrProcessEnded):
		s += ": the selected process ended"
	default:
		s += ": the engine's loop did not answer the probe, so a command is still running"
	}
	if len(e.Unmet) > 0 {
		s += fmt.Sprintf(": the fixture waits for %v", e.Unmet)
	}
	return s + ": " + e.Err.Error()
}

func (e *QuiesceError) Unwrap() error { return e.Err }

// adapterRound is how long, on the real clock, the engine is asked about a quiet
// system in one round when the fixture has an adapter host, before the numbers it
// is asked against are made again.
const adapterRound = 100 * time.Millisecond

// quietPoll is how often the conditions of the fixture are asked again, on the
// real clock: the fakes are objects with no channel to wait on.
const quietPoll = time.Millisecond

// Quiesce returns when the system has stopped reacting at the time of the clock
// now: one whole round has changed no count. A round is a look at the conditions of
// the fixture, which must all hold, then the engine's own conditions, then a look
// at the fixture's again: what the engine did may have made a fake busy, and a fake
// that took work during the round, finished it and went idle again holds on both
// looks, so the counts of the two looks must be equal as well. It fails with a
// *QuiesceError when the real-clock limit ends first or the selected process is
// abandoned before the wait returns. A replacement process cannot satisfy it.
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
	p, err := h.currentProcess()
	if err != nil {
		return fail(false, engine.Report{}, nil, err)
	}
	var asked bool
	var rep engine.Report
	var unmet []string
	// The whole quiet round belongs to this process, including the final fixture
	// look. Keep diagnostics outside the call whose result abandonment discards.
	_, err = unlessAbandoned(&p.abandoned, func() (struct{}, error) {
		for {
			asked, rep = false, engine.Report{}
			before := h.look()
			if unmet = before.unmet; len(unmet) > 0 {
				timer := sys.NewTimer(quietPoll)
				select {
				case <-timer.C():
				case <-ctx.Done():
					timer.Stop()
					return struct{}{}, ctx.Err()
				}
				continue
			}
			round, endRound := ctx, context.CancelFunc(func() {})
			if h.launcher != nil {
				// What the adapter has sent grows while the engine is asked, and the
				// engine holds the numbers it was given: a round that takes long is made
				// again with the numbers of now.
				round, endRound = context.WithTimeout(ctx, adapterRound)
			}
			asked = true
			var err error
			rep, err = p.eng.Quiesce(round, h.expect())
			roundOver := round.Err() != nil // before endRound, which ends it too
			endRound()
			if err != nil {
				if roundOver && ctx.Err() == nil {
					continue
				}
				unmet = nil
				return struct{}{}, err
			}
			after := h.look()
			if unmet = after.unmet; len(unmet) == 0 && slices.Equal(before.progress, after.progress) {
				return struct{}{}, nil
			}
		}
	})
	if err != nil {
		return fail(asked, rep, unmet, err)
	}
	return nil
}

// SetAdapterInputs replaces the run-owned inputs of a listed adapter instance.
// Inputs survive node and adapter replacement until explicitly changed.
func (h *H) SetAdapterInputs(instance string, inputs fakeadapter.ExternalInputs) error {
	if h.adapterInputs == nil {
		return scenario.NotAvailable("an adapter host")
	}
	return h.adapterInputs.Set(h.Ctx(), instance, inputs)
}
