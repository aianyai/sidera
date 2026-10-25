package engine_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// crashWalker is an Observer that, before every step of the transaction it
// watches, takes the disks a crash at that instant can leave, and hands each to
// check: the process dying, the worst power loss, and some seeded ones.
type crashWalker struct {
	rig   *rig
	tx    string
	rng   *rand.Rand
	check func(where string, disk *fsxtest.Mem)
	mu    sync.Mutex
	taken map[fsx.Key]map[string]int
}

func newCrashWalker(tx string, seed uint64, check func(where string, disk *fsxtest.Mem)) *crashWalker {
	return &crashWalker{tx: tx, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), check: check, taken: map[fsx.Key]map[string]int{}}
}

func (w *crashWalker) note(p fsx.Point, model string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.taken[p.Key] == nil {
		w.taken[p.Key] = map[string]int{}
	}
	w.taken[p.Key][model]++
}

func (w *crashWalker) Reached(p fsx.Point, _ txn.Kind) {
	if w.rig == nil || p.Tag.Tx != w.tx {
		return
	}
	mem := w.rig.mem
	where := "before " + p.String()
	w.check(where+" (process crash)", mem.Clone())
	w.note(p, "process crash")
	w.check(where+" (worst power loss)", mem.CrashCloneWorst())
	w.note(p, "worst power loss")
	for i := 0; i < 3; i++ {
		w.check(fmt.Sprintf("%s (power loss %d)", where, i), mem.CrashClone(w.rng))
		w.note(p, "power loss")
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
	walker := newCrashWalker("direct-register", 11, func(where string, disk *fsxtest.Mem) {
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
	})
	r = newRig(t, 10, withObserver(walker))
	walker.rig = r
	e := r.start()
	if _, err := e.RegisterDirect(context.Background(), "chat", registerParams("notes")); err != nil {
		t.Fatalf("RegisterDirect: %v", err)
	}
	if checked == 0 {
		t.Fatal("no crash state was judged")
	}

	// Coverage: every step of the transaction was crashed at, under every kind of
	// crash, and the keys are the table's, not a list the run produced.
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
		for _, model := range []string{"process crash", "worst power loss", "power loss"} {
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
	if err := e.AdapterAdmitted(context.Background(), "chat", f.admit); err != nil {
		t.Errorf("AdapterAdmitted: %v", err)
	}
	return e
}

var _ = adapterproto.MaxRecipients
