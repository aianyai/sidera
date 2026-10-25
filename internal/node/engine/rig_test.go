package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	t      testing.TB
	mem    *fsxtest.Mem
	faulty *fsxtest.Faulty
	clk    *clocktest.Fake
	tab    *txn.Table
	w      *txn.Writer
	st     *store.Store
	cfg    *config.Config
	log    *harness.LogCapture
	node   ident.NodeID
	hw     clock.HighWater
	seed   uint64
	eng    *engine.Engine
	params engine.Params
	admit  adapterreg.Admission
	paused bool
	obs    txn.Observer
}

type rigOption func(*rig)

func withParams(p engine.Params) rigOption { return func(r *rig) { r.params = p } }

func withObserver(o txn.Observer) rigOption { return func(r *rig) { r.obs = o } }

// newRig builds a node that has been initialised, has the adapter "chat"
// installed and enabled as the instance "chat", and has not been started. The
// adapter declares direct endpoints, at most maxDirect of them.
func newRig(t testing.TB, maxDirect int, opts ...rigOption) *rig {
	t.Helper()
	r := &rig{t: t, mem: fsxtest.NewMem(), clk: clocktest.NewFake(t0), log: &harness.LogCapture{}, seed: 1}
	r.node = harness.NodeID()
	for _, o := range opts {
		o(r)
	}
	harness.MakeLayout(t, r.mem)
	r.tab = harness.ProgramTable(t)
	r.mount(r.mem)
	r.cfg = harness.SeedNode(t, r.w, t0)
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
	return engine.Deps{
		Node: r.node, Clock: r.clk, HighWater: r.hw, RollbackPaused: r.paused,
		Writer: r.w, Store: r.st, Config: r.cfg, Rand: harness.RandReader(r.seed),
		Log: slog.New(r.log),
	}
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
	if err := e.AdapterAdmitted(context.Background(), "chat", r.admit); err != nil {
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
	f := &rig{t: r.t, clk: clocktest.NewFake(r.clk.Now()), log: &harness.LogCapture{}, seed: r.seed + 100, node: r.node, tab: r.tab, admit: r.admit, params: r.params}
	f.mount(disk)
	var err error
	if f.cfg, err = config.Load(f.w); err != nil {
		r.t.Fatalf("Load on the copy of the disk: %v", err)
	}
	return f
}
