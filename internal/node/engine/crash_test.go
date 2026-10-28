package engine_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// crashDisks are the disks a crash at one instant can leave that the walks judge.
// The reliability floor needs two: the process crash, where every completed call
// survives, and the worst power loss, where only what was synced survives. The
// worst one shows that what the engine acknowledged was synced; the process one
// shows that a restart copes with everything that was written. Under the
// transaction discipline a random power loss only keeps a subset of the one group
// of writes that is not synced yet, between those two, so the walks take no
// seeded one: the property test that follows the walks draws them.
func crashDisks(mem *fsxtest.Mem) []crashDisk {
	return []crashDisk{{processCrash, mem.Clone()}, {worstPowerLoss, mem.CrashCloneWorst()}}
}

// The kinds of crash crashDisks takes at every point.
const (
	processCrash   = "process crash"
	worstPowerLoss = "worst power loss"
)

var crashKindNames = []string{processCrash, worstPowerLoss}

type crashDisk struct {
	kind string
	disk *fsxtest.Mem
}

// crashWalker is an Observer that, before every step of the transaction it
// watches, takes the disks a crash at that instant can leave, and hands each to
// check.
type crashWalker struct {
	rig   *rig
	txs   map[string]bool
	check func(where string, disk *fsxtest.Mem)
	mu    sync.Mutex
	taken map[fsx.Key]map[string]int
}

func newCrashWalker(check func(where string, disk *fsxtest.Mem), txs ...string) *crashWalker {
	set := map[string]bool{}
	for _, tx := range txs {
		set[tx] = true
	}
	return &crashWalker{txs: set, check: check, taken: map[fsx.Key]map[string]int{}}
}

func (w *crashWalker) note(p fsx.Point, model string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.taken[p.Key] == nil {
		w.taken[p.Key] = map[string]int{}
	}
	w.taken[p.Key][model]++
}

func (w *crashWalker) Reached(observation fsx.Observation, _ txn.Kind) {
	p := observation.Point

	if w.rig == nil || !w.txs[p.Tag.Tx] {
		return
	}
	where := "before " + p.String()
	for _, cd := range crashDisks(w.rig.mem) {
		w.check(fmt.Sprintf("%s (%s)", where, cd.kind), cd.disk)
		w.note(p, cd.kind)
	}
}

// The registration of a direct endpoint writes the id in config/ and the record
// in state/. Wherever the node dies, the next start opens, finds at most the one
// endpoint and never one without its id, and the adapter's next registration of
// the key gives the id that was kept, if one was, and a working endpoint in any
// case.
func TestDirectRegisterRecoversFromACrashAtEveryPoint(t *testing.T) {
	noLeaks(t)
	var r *rig
	checked := 0
	walker := newCrashWalker(func(where string, disk *fsxtest.Mem) {
		checked++
		f := r.fork(disk)
		e, rep, err := f.open()
		if err != nil {
			t.Errorf("%s: Open: %v", where, err)
			return
		}
		startForked(t, f, e)
		defer f.stop2(e)
		if len(rep.Rules) != 0 {
			t.Errorf("%s: the scan entry had recovery work %v: a crash of a registration leaves none", where, rep.Rules)
		}
		eps, err := e.Endpoints(context.Background())
		if err != nil {
			t.Errorf("%s: Endpoints: %v", where, err)
			return
		}
		kept, hasID := f.cfg.EndpointIDs("chat").Direct["notes"]
		switch len(eps) {
		case 0:
		case 1:
			ep := eps[0]
			if !hasID || kept != ep.ID {
				t.Errorf("%s: endpoint %s on disk without its id in config/ (id kept: %v %s)", where, ep.ID, hasID, kept)
			}
			if ep.Status != store.EndpointOpen || ep.DisplayName != "Name of notes" {
				t.Errorf("%s: the record is %+v", where, ep)
			}
		default:
			t.Errorf("%s: %d endpoints", where, len(eps))
		}
		res, err := e.RegisterDirect(context.Background(), "chat", registerParams("notes"))
		if err != nil {
			t.Errorf("%s: registering again: %v", where, err)
			return
		}
		if hasID && string(res.Endpoint) != kept.String() {
			t.Errorf("%s: the key gave %s, want the id kept, %s", where, res.Endpoint, kept)
		}
		if got := openEndpoints(t, e); got != 1 {
			t.Errorf("%s: %d endpoints open after registering again, want 1", where, got)
		}
	}, "direct-register")
	r = newRig(t, 10, withObserver(walker))
	walker.rig = r
	e := r.start()
	if _, err := e.RegisterDirect(context.Background(), "chat", registerParams("notes")); err != nil {
		t.Fatalf("RegisterDirect: %v", err)
	}
	if checked == 0 {
		t.Fatal("no crash state was judged")
	}

	// Coverage: every step of the transaction was crashed at, under each kind of
	// crash the walks take, and the keys are the table's, not a list the run produced.
	var want []fsx.Key
	for _, k := range r.tab.Keys() {
		if k.Tag.Tx == "direct-register" {
			want = append(want, k.Key)
		}
	}
	if len(want) == 0 {
		t.Fatal("the table holds no key of direct-register")
	}
	var missing []string
	for _, k := range want {
		for _, model := range crashKindNames {
			if walker.taken[k][model] == 0 {
				missing = append(missing, fmt.Sprintf("%v under %s", k, model))
			}
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf("crash points never crashed at: %v", missing)
	}
}

// startForked runs a forked engine to the running phase with the instance
// admitted and returns it, for the caller to stop.
func startForked(t testing.TB, f *rig, e *engine.Engine) *engine.Engine {
	t.Helper()
	e.Start()
	if err := e.RecoverDelivery(context.Background()); err != nil {
		t.Errorf("RecoverDelivery: %v", err)
	}
	e.Serve()
	if err := admitOn(context.Background(), e, "chat", f.admit); err != nil {
		t.Errorf("AdapterAdmitted: %v", err)
	}
	return e
}

// Closing an endpoint is a head record and chunks of closed deliveries. Wherever
// the node dies, the next start finds the endpoint open with every delivery still
// queued, or closed with every delivery closed: the rule that finishes a closing
// is the one that closes a chunk, and nothing is left half done.
func TestClosingAnEndpointRecoversFromACrashAtEveryPoint(t *testing.T) {
	noLeaks(t)
	var r *rig
	checked, closedStates, openStates := 0, 0, 0
	var a ident.EndpointID
	walker := newCrashWalker(func(where string, disk *fsxtest.Mem) {
		checked++
		f := r.fork(disk)
		e, _, err := f.open()
		if err != nil {
			t.Errorf("%s: Open: %v", where, err)
			return
		}
		defer f.stop2(e)
		sc := f.scan()
		var status store.EndpointStatus
		for _, ep := range sc.Endpoints {
			if ep.ID == a {
				status = ep.Status
			}
		}
		rejected, accepted := 0, 0
		for _, in := range sc.In {
			switch in.Record.State {
			case delivery.StateRejected:
				rejected++
			case delivery.StateAccepted:
				accepted++
			}
		}
		switch status {
		case store.EndpointClosed:
			closedStates++
			if accepted != 0 || rejected != 4 || len(sc.Bodies) != 0 {
				t.Errorf("%s: the endpoint is closed with %d queued, %d rejected, %d bodies; want all 4 rejected and no body", where, accepted, rejected, len(sc.Bodies))
			}
		case store.EndpointOpen:
			openStates++
			if accepted != 4 || rejected != 0 || len(sc.Bodies) != 4 {
				t.Errorf("%s: the endpoint is open with %d queued, %d rejected, %d bodies; want 4, 0, 4", where, accepted, rejected, len(sc.Bodies))
			}
		default:
			t.Errorf("%s: the endpoint is %v", where, status)
		}
		if plan := engine.PlanRecovery(sc, f.clk.Now(), func(string) bool { return true }); !plan.Empty() {
			t.Errorf("%s: work left after Open: %v", where, plan.Items())
		}
	}, "endpoint-close", "endpoint-drain")
	r = newRig(t, 10, withParams(engine.Params{ChunkReplaces: 2}), withObserver(walker))
	e := r.start()
	a = register(t, e, "a")
	r.restart()
	r.queue(1, 4, a)
	e = r.start()
	walker.rig = r
	if _, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: a}); err != nil {
		t.Fatal(err)
	}
	settle(t, e)
	if checked == 0 || closedStates == 0 || openStates == 0 {
		t.Fatalf("%d crash states judged, %d closed and %d open: the walk must reach both sides of the head record", checked, closedStates, openStates)
	}
	for _, name := range []string{"endpoint-close", "endpoint-drain"} {
		if r.commits(name) < 1 {
			t.Errorf("transaction %s never reached its commit", name)
		}
	}
	for _, k := range r.tab.Keys() {
		if k.Tag.Tx != "endpoint-close" && k.Tag.Tx != "endpoint-drain" {
			continue
		}
		hit := false
		for _, model := range crashKindNames {
			if walker.taken[k.Key][model] > 0 {
				hit = true
			}
		}
		// A step that this scenario does not take (the optional result of the
		// head record, the removal of bodies of a chunk without one) is not
		// reached; every step that was reached was crashed at, under every model.
		for _, c := range r.w.Coverage() {
			if c.Key.Key == k.Key && c.Hits > 0 && !hit {
				t.Errorf("crash point %v was reached and never crashed at", k.Key)
			}
		}
	}
}

// What the engine acknowledged is on the disk when a power loss takes everything
// that was not synced: the clones of the disk right after the acknowledgements,
// the worst one and some seeded ones, all show it.
func TestPowerLossKeepsWhatTheEngineAcknowledged(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10, withParams(engine.Params{ChunkReplaces: 2}))
	e := r.start()
	a, b := register(t, e, "a"), register(t, e, "b")
	paused := engineCaller(a)
	r.restart()
	r.queue(1, 3, a)
	r.queue(10, 2, b)
	e = r.start()
	if _, err := e.Declare(context.Background(), paused, engine.DeclarationPatch{Paused: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	id, err := e.RaiseAlert(context.Background(), "disk_low", "state", "below the reserve")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: b}); err != nil {
		t.Fatal(err)
	}
	settle(t, e)

	rng := rand.New(rand.NewPCG(5, 6))
	clones := []*fsxtest.Mem{r.mem.CrashCloneWorst()}
	for i := 0; i < 8; i++ {
		clones = append(clones, r.mem.CrashClone(rng))
	}
	for i, disk := range clones {
		f := r.fork(disk)
		e2, rep, err := f.open()
		if err != nil {
			t.Fatalf("clone %d: Open: %v", i, err)
		}
		if len(rep.Rules) != 0 {
			t.Errorf("clone %d: recovery work %v: everything acknowledged was synced", i, rep.Rules)
		}
		e2.Start()
		eps, _ := e2.Endpoints(context.Background())
		got := map[ident.EndpointID]engine.EndpointView{}
		for _, ep := range eps {
			got[ep.ID] = ep
		}
		if got[a].Status != store.EndpointOpen || !got[a].Declaration.Paused || got[a].DisplayName != "Name of a" {
			t.Errorf("clone %d: endpoint a is %+v", i, got[a])
		}
		if got[b].Status != store.EndpointClosed {
			t.Errorf("clone %d: endpoint b is %v, want closed", i, got[b].Status)
		}
		alerts, _ := e2.Alerts(context.Background())
		if len(alerts) != 1 || alerts[0].ID != id || alerts[0].Kind != "disk_low" || alerts[0].Detail != "below the reserve" {
			t.Errorf("clone %d: alerts %+v", i, alerts)
		}
		for _, in := range f.scan().In {
			wantState := delivery.StateAccepted
			if in.Key.Recipient == b {
				wantState = delivery.StateRejected
			}
			if in.Record.State != wantState {
				t.Errorf("clone %d: delivery %s is %v, want %v", i, in.Key.Message, in.Record.State, wantState)
			}
		}
		f.stop2(e2)
	}
}

// assertEveryStepCrashedAt fails for every key of the declared transaction that
// the walk did not crash at under every kind of crash: the keys are the table's,
// not a list the run produced.
func assertEveryStepCrashedAt(t *testing.T, r *rig, walker *crashWalker, tx string) {
	t.Helper()
	var want []fsx.Key
	for _, k := range r.tab.Keys() {
		if k.Tag.Tx == tx {
			want = append(want, k.Key)
		}
	}
	if len(want) == 0 {
		t.Fatalf("the table holds no key of %s", tx)
	}
	var missing []string
	for _, k := range want {
		for _, model := range crashKindNames {
			if walker.taken[k][model] == 0 {
				missing = append(missing, fmt.Sprintf("%v under %s", k, model))
			}
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf("crash points of %s never crashed at: %v", tx, missing)
	}
}

// The admission of a runtime adapter writes the id of its runtime endpoint in
// config/ and the record in state/. Wherever the node dies, the next start opens
// with no recovery work, finds at most the one runtime endpoint and never one
// without its id, and the admission done again gives the id that was kept, if
// one was, and an open runtime endpoint in any case.
func TestRuntimeAdmitRecoversFromACrashAtEveryPoint(t *testing.T) {
	noLeaks(t)
	rt := runtimeAdmission(t, 0, "cli")
	var r *rig
	checked, withRecord, withoutRecord := 0, 0, 0
	walker := newCrashWalker(func(where string, disk *fsxtest.Mem) {
		checked++
		f := r.fork(disk)
		e, rep, err := f.open()
		if err != nil {
			t.Errorf("%s: Open: %v", where, err)
			return
		}
		startForked(t, f, e)
		defer f.stop2(e)
		if len(rep.Rules) != 0 {
			t.Errorf("%s: the scan entry had recovery work %v: a crash of an admission leaves none", where, rep.Rules)
		}
		kept := f.cfg.EndpointIDs("chat").Runtime
		recs := f.runtimeRecords()
		switch len(recs) {
		case 0:
			withoutRecord++
		case 1:
			withRecord++
			rec := recs[0]
			if kept.IsZero() || kept != rec.ID {
				t.Errorf("%s: runtime endpoint %s on disk without its id in config/ (kept: %s)", where, rec.ID, kept)
			}
			if rec.Status != store.EndpointOpen || len(rec.Token) != store.TokenLen {
				t.Errorf("%s: the record is %+v", where, rec)
			}
		default:
			t.Errorf("%s: %d runtime records", where, len(recs))
		}
		got, err := admitFull(context.Background(), e, "chat", rt)
		if err != nil {
			t.Errorf("%s: admitting again: %v", where, err)
			return
		}
		if !kept.IsZero() && string(got.RuntimeEndpoint) != kept.String() {
			t.Errorf("%s: the admission gave %s, want the id kept, %s", where, got.RuntimeEndpoint, kept)
		}
		if n := len(f.runtimeRecords()); n != 1 {
			t.Errorf("%s: %d runtime records after admitting again, want 1", where, n)
		}
		if got := openEndpoints(t, e); got != 1 {
			t.Errorf("%s: %d endpoints open after admitting again, want 1", where, got)
		}
	}, "runtime-admit")
	r = newRig(t, 10, withObserver(walker))
	walker.rig = r
	e := r.start()
	if err := admitOn(context.Background(), e, "chat", rt); err != nil {
		t.Fatal(err)
	}
	if checked == 0 || withRecord == 0 || withoutRecord == 0 {
		t.Fatalf("%d crash states judged, %d with the record and %d without: the walk must reach both sides of the commit", checked, withRecord, withoutRecord)
	}
	assertEveryStepCrashedAt(t, r, walker, "runtime-admit")
}

// A checkpoint is one replaced file. Wherever the node dies, the next start opens
// with no recovery work and the checkpoint is the old state or the new one, whole,
// never a partial one and never absent once an earlier put was answered.
func TestCheckpointPutRecoversFromACrashAtEveryPoint(t *testing.T) {
	noLeaks(t)
	const old, next = `{"state":"old"}`, `{"state":"new","more":[1,2,3]}`
	key := store.CheckpointKey{Scope: "attempt", ID: attemptA}
	var r *rig
	checked, sawOld, sawNew := 0, 0, 0
	walker := newCrashWalker(func(where string, disk *fsxtest.Mem) {
		checked++
		f := r.fork(disk)
		e, rep, err := f.open()
		if err != nil {
			t.Errorf("%s: Open: %v", where, err)
			return
		}
		defer f.stop2(e)
		if len(rep.Rules) != 0 {
			t.Errorf("%s: the scan entry had recovery work %v: a crash of a checkpoint leaves none", where, rep.Rules)
		}
		got := checkpoints(t, f)
		switch {
		case len(got) != 1:
			t.Errorf("%s: checkpoints %v, want exactly the one", where, got)
		case got[key] == old:
			sawOld++
		case got[key] == next:
			sawNew++
		default:
			t.Errorf("%s: the checkpoint is %q, want the old state or the new one whole", where, got[key])
		}
	}, "checkpoint-put")
	r = newRig(t, 10, withObserver(walker))
	r.seedCreations("chat", attemptA)
	e := r.start()
	if err := e.CheckpointPut(context.Background(), "chat", attemptPut(attemptA, old)); err != nil {
		t.Fatal(err)
	}
	walker.rig = r
	if err := e.CheckpointPut(context.Background(), "chat", attemptPut(attemptA, next)); err != nil {
		t.Fatal(err)
	}
	if checked == 0 || sawOld == 0 || sawNew == 0 {
		t.Fatalf("%d crash states judged, %d old and %d new: the walk must reach both sides of the commit", checked, sawOld, sawNew)
	}
	assertEveryStepCrashedAt(t, r, walker, "checkpoint-put")
}
