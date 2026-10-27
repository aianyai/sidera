package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	nodekeys "github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// The rig is the engine on the real store and the real configuration, on the
// in-memory file system with the crash model: the disk is what a restart sees,
// and nothing of the store's semantics is written a second time here.

var setupTag = fsx.Tag{Tx: "setup", Role: "f"}

// t0 is the origin of the test clock.
var t0 = harness.DefaultStart

func programTable(t testing.TB) *txn.Table { return harness.ProgramTable(t) }

// rig is one process of the node over a disk.
type rig struct {
	t testing.TB
	// creations counts the sessions seedCreations has put on the disk.
	creations int
	mem       *fsxtest.Mem
	faulty    *fsxtest.Faulty
	clk       *clocktest.Fake
	// engineClock, when set, is the clock the engine is given instead of clk: a
	// wrapper of it that a test uses to move time at a chosen moment.
	engineClock clock.Clock
	tab         *txn.Table
	w           *txn.Writer
	st          *store.Store
	cfg         *config.Config
	log         *harness.LogCapture
	node        ident.NodeID
	hw          clock.HighWater
	seed        uint64
	eng         *engine.Engine
	params      engine.Params
	admit       adapterreg.Admission
	paused      bool
	// pausedSince is when the persisted pause began, for a rig that starts paused.
	pausedSince time.Time
	obs         txn.Observer
	// keys are the node's keys, loaded from its disk, for a rig made withKeys; nil
	// otherwise, as for an engine that is not told to sign.
	keys     *nodekeys.Keys
	wantKeys bool
	// home is the data directory the engine is told; empty when a test says so.
	home string
	// adapters is the adapter host the engine is given, nil when a test has none.
	adapters engine.Adapters
	// installed, when set, changes the installed adapter packages the engine
	// reads from the configuration (a test that needs tested runtime ranges).
	installed func([]config.Adapter) []config.Adapter
	// packages are the package directories an owner can name to install, by the
	// path the owner names; each is a disk of its own, rooted at the package.
	packages map[string]fsx.Reader
}

// testHome is the data directory of a rig, unless a test says it has none.
const testHome = "/var/lib/sidera-test"

type rigOption func(*rig)

func withParams(p engine.Params) rigOption { return func(r *rig) { r.params = p } }

func withObserver(o txn.Observer) rigOption { return func(r *rig) { r.obs = o } }

// withKeys gives the engine the keys of the node, as the composition root does.
func withKeys() rigOption { return func(r *rig) { r.wantKeys = true } }

// loadKeys loads the keys from the disk the rig is mounted on, when the rig is
// made with keys.
func (r *rig) loadKeys() {
	r.t.Helper()
	if !r.wantKeys {
		return
	}
	k, err := nodekeys.Load(r.w)
	if err != nil {
		r.t.Fatalf("keys.Load: %v", err)
	}
	r.keys = k
}

func withoutHome() rigOption { return func(r *rig) { r.home = "" } }

func withAdapters(a engine.Adapters) rigOption { return func(r *rig) { r.adapters = a } }

func withInstalled(f func([]config.Adapter) []config.Adapter) rigOption {
	return func(r *rig) { r.installed = f }
}

// withPackage makes the directory dir the package src, for the owner to install.
func withPackage(dir string, src fsx.Reader) rigOption {
	return func(r *rig) { r.addPackage(dir, src) }
}

func (r *rig) addPackage(dir string, src fsx.Reader) {
	if r.packages == nil {
		r.packages = map[string]fsx.Reader{}
	}
	r.packages[dir] = src
}

// openPackage is how the engine reads a package directory the owner named: the
// disk of the package, or the error of a directory that is not there.
func (r *rig) openPackage(dir string) (fsx.Reader, error) {
	if src, ok := r.packages[dir]; ok {
		return src, nil
	}
	return nil, &fs.PathError{Op: "open", Path: dir, Err: fs.ErrNotExist}
}

// newRig builds a node that has been initialised, has the adapter "chat"
// installed and enabled as the instance "chat", and has not been started. The
// adapter declares direct endpoints, at most maxDirect of them.
func newRig(t testing.TB, maxDirect int, opts ...rigOption) *rig {
	t.Helper()
	r := &rig{t: t, mem: fsxtest.NewMem(), clk: clocktest.NewFake(t0), log: &harness.LogCapture{}, seed: 1, home: testHome}
	r.node = harness.NodeID()
	for _, o := range opts {
		o(r)
	}
	harness.MakeLayout(t, r.mem)
	r.tab = harness.ProgramTable(t)
	r.mount(r.mem)
	r.cfg = harness.SeedNode(t, r.w, t0)
	r.loadKeys()
	r.admit = harness.AdapterAdmission(t, maxDirect)
	return r
}

func mustNode(t testing.TB, b byte) ident.NodeID {
	t.Helper()
	n, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{b}, ident.NodeIDLen))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// mount starts a process over the disk: a writer, a store, the recovery of the
// transaction layer and the configuration, as a start does before the engine.
func (r *rig) mount(disk *fsxtest.Mem) {
	r.t.Helper()
	m := harness.Mount(r.t, r.tab, disk, r.obs)
	r.mem, r.faulty, r.w, r.st = m.Mem, m.Faulty, m.Writer, m.Store
	r.hw = clock.NewHighWater(time.Time{})
}

// deps is what the composition root hands the engine on this rig.
func (r *rig) deps() engine.Deps {
	r.seed++
	var clk clock.Clock = r.clk
	if r.engineClock != nil {
		clk = r.engineClock
	}
	d := engine.Deps{
		Node: r.node, Clock: clk, HighWater: r.hw, RollbackPaused: r.paused, RollbackPausedSince: r.pausedSince,
		Writer: r.w, Store: r.st, Config: r.engineConfig(), Rand: harness.RandReader(r.seed),
		Adapters: r.adapters, Home: r.home, Log: slog.New(r.log), OpenPackage: r.openPackage,
	}
	if r.keys != nil {
		d.Keys = r.keys
	}
	return d
}

// installedConfig is the configuration with the installed packages a test chose.
type installedConfig struct {
	engine.Config
	pick func([]config.Adapter) []config.Adapter
}

func (c installedConfig) Adapters() []config.Adapter { return c.pick(c.Config.Adapters()) }

func (r *rig) engineConfig() engine.Config {
	if r.installed == nil {
		return r.cfg
	}
	return installedConfig{Config: r.cfg, pick: r.installed}
}

// initInfo is what an adapter with the admission's capabilities answers to
// initialize: the adapter of the manifest every rig installs.
func initInfo(adm adapterreg.Admission) adapterproto.InitializeResult {
	return adapterproto.InitializeResult{
		ProtocolVersion: 1,
		Adapter:         adapterproto.AdapterInfo{ID: "chat", Version: "1.0.0"},
		Capabilities:    adm.Capabilities(),
	}
}

// admitFull admits the instance as the adapter host does and returns what
// adapter.start carries.
func admitFull(ctx context.Context, e *engine.Engine, instance string, adm adapterreg.Admission) (adapterproto.StartParams, error) {
	return e.AdapterAdmitted(ctx, instance, adm, initInfo(adm))
}

// admitOn is admitFull for a caller that only wants to know whether it worked.
func admitOn(ctx context.Context, e *engine.Engine, instance string, adm adapterreg.Admission) error {
	_, err := admitFull(ctx, e, instance, adm)
	return err
}

// open runs the scan entry.
func (r *rig) open() (*engine.Engine, engine.ScanReport, error) {
	r.t.Helper()
	return engine.Open(context.Background(), r.deps(), r.params)
}

// start opens the engine and runs it to the running phase, with the adapter
// instance admitted. The engine is stopped when the test ends.
func (r *rig) start() *engine.Engine {
	r.t.Helper()
	e, _, err := r.open()
	if err != nil {
		r.t.Fatalf("Open: %v", err)
	}
	e.Start()
	if err := e.RecoverDelivery(context.Background()); err != nil {
		r.t.Fatalf("RecoverDelivery: %v", err)
	}
	e.Serve()
	if err := admitOn(context.Background(), e, "chat", r.admit); err != nil {
		r.t.Fatalf("AdapterAdmitted: %v", err)
	}
	r.eng = e
	r.t.Cleanup(func() { r.stop(e) })
	return e
}

func (r *rig) stop(e *engine.Engine) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	e.BeginShutdown()
	if err := e.Stop(ctx); err != nil {
		r.t.Errorf("Stop: %v", err)
	}
}

// restart is a process crash: the disk keeps every completed write, the
// process and its memory are gone, and a new process mounts the disk.
func (r *rig) restart() {
	r.t.Helper()
	if r.eng != nil {
		r.stop(r.eng)
		r.eng = nil
	}
	r.mount(r.mem.Clone())
	var err error
	if r.cfg, err = config.Load(r.w); err != nil {
		r.t.Fatalf("Load: %v", err)
	}
	r.loadKeys()
}

// register registers the direct endpoint key of the instance chat.
func register(t testing.TB, e *engine.Engine, key string) ident.EndpointID {
	t.Helper()
	res, err := e.RegisterDirect(context.Background(), "chat", adapterproto.RegisterParams{Key: key, DisplayName: "Name of " + key})
	if err != nil {
		t.Fatalf("RegisterDirect(%q): %v", key, err)
	}
	id, err := ident.ParseEndpointID(string(res.Endpoint))
	if err != nil {
		t.Fatalf("endpoint ref %q: %v", res.Endpoint, err)
	}
	return id
}

func wantReason(t testing.TB, err error, want engine.Reason) *engine.Refusal {
	t.Helper()
	var ref *engine.Refusal
	if err == nil {
		t.Fatalf("no error, want refusal %s", want)
	}
	r, ok := engine.ReasonOf(err)
	if !ok || r != want {
		t.Fatalf("error %v, want refusal %s", err, want)
	}
	if !errorsAs(err, &ref) {
		t.Fatalf("error %v is not a *Refusal", err)
	}
	return ref
}

func errorsAs(err error, ref **engine.Refusal) bool {
	r, ok := err.(*engine.Refusal)
	if ok {
		*ref = r
		return true
	}
	if u, ok := err.(interface{ Unwrap() error }); ok {
		return errorsAs(u.Unwrap(), ref)
	}
	return false
}

// settle returns once the engine has nothing left to do at the current time of
// the test clock.
func settle(t testing.TB, e *engine.Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep, err := e.Quiesce(ctx, engine.Expect{})
	if err != nil {
		t.Fatalf("Quiesce: %v (report %+v)", err, rep)
	}
}

// openEndpoints counts the endpoints of the owner's list that are not closed.
func openEndpoints(t testing.TB, e *engine.Engine) int {
	t.Helper()
	eps, err := e.Endpoints(context.Background())
	if err != nil {
		t.Fatalf("Endpoints: %v", err)
	}
	n := 0
	for _, ep := range eps {
		if ep.Status != store.EndpointClosed {
			n++
		}
	}
	return n
}

// noLeaks fails the test when goroutines that the test started are still
// running at its end: the engine owns every goroutine it starts and stops it on
// shutdown.
func noLeaks(t testing.TB) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			n := runtime.NumGoroutine()
			if n <= before {
				return
			}
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<16)
				buf = buf[:runtime.Stack(buf, true)]
				t.Errorf("%d goroutines running, %d before the test:\n%s", n, before, buf)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func keyName(i int) string { return fmt.Sprintf("key-%04d", i) }

func mustManifest(t testing.TB) manifest.Manifest {
	t.Helper()
	m, err := manifest.Parse([]byte(harness.ManifestText()))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func registerParams(key string) adapterproto.RegisterParams {
	return adapterproto.RegisterParams{Key: key, DisplayName: "Name of " + key}
}

const sessionKind = adapterreg.KindSession

// stop2 stops an engine the test opened itself.
func (r *rig) stop2(e *engine.Engine) { r.stop(e) }

// seedRecord writes a record onto the disk durably, outside any transaction.
func (r *rig) seedRecord(rec store.Record) {
	r.t.Helper()
	data, err := store.EncodeRecord(rec)
	if err != nil {
		r.t.Fatalf("seed %T: %v", rec, err)
	}
	var dir string
	switch rec.(type) {
	case store.EndpointRecord:
		dir = store.EndpointsDir
	case store.InRecord:
		dir = store.InDir
	case store.CtlRecord:
		dir = store.CtlDir
	case store.OutRecord:
		dir = store.OutDir
	case store.AlertRecord:
		dir = store.AlertsDir
	case store.AttemptsRecord:
		dir = store.AttemptsDir
	default:
		r.t.Fatalf("seedRecord: no directory for %T", rec)
	}
	r.seedFile(dir, rec.Name(), data)
}

// snapshot is every file under state/, config/ with its content.
func (r *rig) snapshot() map[string]string {
	r.t.Helper()
	got := map[string]string{}
	var walk func(d string)
	walk = func(d string) {
		entries, err := r.mem.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := path.Join(d, e.Name)
			if e.IsDir {
				walk(p)
				continue
			}
			b, err := r.mem.ReadFile(p)
			if err != nil {
				r.t.Fatalf("ReadFile(%q): %v", p, err)
			}
			got[p] = string(b)
		}
	}
	walk("state")
	walk("config")
	return got
}

func configRequest(op admin.OperationID, expected admin.Version) config.Request {
	return config.Request{Key: admin.KeyFor(admin.LocalOwner(), op), Expected: expected}
}

func jsonResult(config.Instances) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }

var served struct {
	mu sync.Mutex
}

func isServed(p *bool) bool { served.mu.Lock(); defer served.mu.Unlock(); return *p }
func setServed(p *bool)     { served.mu.Lock(); defer served.mu.Unlock(); *p = true }

// waitUntil polls on the real clock until cond holds.
func waitUntil(t testing.TB, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for a condition")
		}
		time.Sleep(time.Millisecond)
	}
}

type matchT = fsxtest.Match

func engineCaller(id ident.EndpointID) engine.Caller {
	return engine.ByAdapter("chat", adapterproto.EndpointRef(id.String()))
}

// fork is a new process over a copy of the disk, as a restart after a crash
// gives, with its configuration loaded and nothing started.
func (r *rig) fork(disk *fsxtest.Mem) *rig {
	r.t.Helper()
	f := &rig{t: r.t, clk: clocktest.NewFake(r.clk.Now()), log: &harness.LogCapture{}, seed: r.seed + 100, node: r.node, tab: r.tab, admit: r.admit, params: r.params, wantKeys: r.wantKeys, home: r.home, adapters: r.adapters, installed: r.installed, packages: r.packages}
	f.mount(disk)
	var err error
	if f.cfg, err = config.Load(f.w); err != nil {
		r.t.Fatalf("Load on the copy of the disk: %v", err)
	}
	f.loadKeys()
	return f
}
