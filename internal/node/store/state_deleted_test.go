package store_test

import (
	"fmt"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// runLayout is the layout rule as the composition root declares it: make the
// directories the store lists, parent first, those that are missing.
func runLayout(t testing.TB, r *rig) {
	t.Helper()
	missing, err := store.MissingStateDirs(r.mem)
	if err != nil {
		t.Fatalf("MissingStateDirs: %v", err)
	}
	if len(missing) == 0 {
		return
	}
	tx := r.w.Begin(&declXLayout)
	defer tx.Close()
	for _, d := range missing {
		if err := tx.MakeDir("dir", d); err != nil {
			t.Fatalf("MakeDir(%s): %v", d, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestMissingStateDirsListsWhatIsMissingParentFirst(t *testing.T) {
	r := newRig(t)
	if got, err := store.MissingStateDirs(r.mem); err != nil || len(got) != 0 {
		t.Fatalf("a complete layout has nothing missing, got %v %v", got, err)
	}
	mustDo(t, r.mem.RemoveAll(setupTag, "state/out"))
	mustDo(t, r.mem.RemoveAll(setupTag, "state/seq"))
	got, _ := store.MissingStateDirs(r.mem)
	if fmt.Sprint(got) != "[state/out state/seq]" {
		t.Errorf("missing = %v", got)
	}
	mustDo(t, r.mem.RemoveAll(setupTag, "state"))
	got, _ = store.MissingStateDirs(r.mem)
	want := store.StateDirs()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("with state/ gone, missing = %v, want every directory in the order of StateDirs %v", got, want)
	}
	if want[0] != "state" {
		t.Errorf("a parent comes before its children: %v", want)
	}
}

// Deleting state/ is a crash that loses what was in flight (the design says so):
// what is left is the layout rule's work and nothing else. Afterwards the store
// works, the sequence numbers start from 1 again, and nothing of config/ or
// keys/ was touched.
func TestDeletingStateIsALossOfInFlightDataAndNothingMore(t *testing.T) {
	r := newRig(t)
	mustDo(t, r.st.Intake(bodyBytes(1), withSeq(1, 41, true)))
	mustDo(t, r.st.ArchiveEvents(base, []store.ArchiveEvent{eventAccepted()}))
	pe, err := store.PrepareEndpoint(directEndpoint(7, "chat"))
	mustDo(t, err)
	tx := r.w.Begin(&declXAdmit)
	mustDo(t, tx.Replace("ids", "config/ids", []byte("stable ids"), fsx.ModePrivate))
	mustDo(t, store.PutEndpoint(tx, store.RoleEndpoint, pe))
	mustDo(t, tx.Commit())
	mustDo(t, r.mem.RemoveAll(setupTag, "state"))

	r = openRig(t, r.mem)
	sc := r.scan()
	if len(sc.Out)+len(sc.Endpoints)+len(sc.Bodies) != 0 {
		t.Fatalf("a scan after the deletion found %+v", sc)
	}
	if w := work(sc, func(string) bool { return true }); len(w) != 0 {
		t.Errorf("deleting state/ leaves recovery work of the store's rules: %v", w)
	}
	runLayout(t, r)
	if missing, _ := store.MissingStateDirs(r.mem); len(missing) != 0 {
		t.Errorf("layout left %v missing", missing)
	}
	if b, err := r.mem.ReadFile("config/ids"); err != nil || string(b) != "stable ids" {
		t.Errorf("config/ was touched: %q %v", b, err)
	}
	if got := store.NewSeqBook(r.scan()).Next(ep(1), addr(local, 2)); got != 1 {
		t.Errorf("Next after deleting state/ = %d, want 1: the numbers start again", got)
	}
	mustDo(t, r.st.Intake(bodyBytes(1), srcOut(1, true)))
	if len(r.scan().Out) != 1 {
		t.Errorf("the store does not work after the layout is rebuilt")
	}
	// The cross-owner transaction runs again: the record the deleted directory held is
	// written afresh, the identifiers in config/ are the same.
	tx = r.w.Begin(&declXAdmit)
	mustDo(t, store.PutEndpoint(tx, store.RoleEndpoint, pe))
	mustDo(t, tx.Commit())
	if len(r.scan().Endpoints) != 1 {
		t.Errorf("the endpoint record was not written again")
	}
}

// A crash during the layout rule, in any of its kinds, ends in a complete layout
// after the rule runs again.
func TestLayoutRuleConvergesFromEveryCrashState(t *testing.T) {
	mem := fsxtest.NewMem()
	mustDo(t, fsx.MakeDir(mem, setupTag, "config"))
	r := openRig(t, mem)
	rng := newRand(5)
	var states []crashState
	r.rec.on = func(p fsx.Point, k txn.Kind) {
		states = append(states, crashKinds(r.mem, rng)...)
		if k != txn.Recovery {
			t.Errorf("the layout rule is a recovery transaction, the point %v says it is not", p)
		}
	}
	runLayout(t, r)
	if len(states) < 4*len(store.StateDirs()) {
		t.Fatalf("only %d crash states for %d directories", len(states), len(store.StateDirs()))
	}
	for _, cs := range states {
		again := openRig(t, cs.mem)
		runLayout(t, again)
		if missing, _ := store.MissingStateDirs(again.mem); len(missing) != 0 {
			t.Errorf("%s: still missing %v", cs.name, missing)
		}
		// Idempotent: nothing more to do.
		before := again.rec.count()
		runLayout(t, again)
		if again.rec.count() != before {
			t.Errorf("%s: the layout rule did work on a complete layout", cs.name)
		}
	}
}
