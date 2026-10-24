package store_test

import (
	"fmt"
	"math/rand/v2"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

type mode int

const (
	// atomic: what is left after recovery is the state before or the state
	// after, as a whole.
	atomic mode = iota
	// batch: every file is, independently, as before or as after; the
	// transaction is a set of single-file transitions.
	batch
)

// scenario is one workflow of the store with the states its crashes may leave.
type scenario struct {
	name string
	// setup writes the disk before the workflow, durably.
	setup func(t *testing.T, mem *fsxtest.Mem)
	// run is the workflow: one or more calls of the store, ending at the first
	// error. The recovery scenarios run the engine's startup rules (sim).
	run  func(t *testing.T, r *rig, s *sim) error
	mode mode
	// durable is the crash point after which the head of the workflow is durable:
	// from the step after it on, recovery must complete the workflow, and the state
	// it ends in is the state after. Empty: either state is acceptable.
	durable string
	// mustComplete: the head of the workflow was made durable before the
	// scenario starts (a recovery scenario), so recovery must reach the state
	// after from every crash state.
	mustComplete bool
	enabled      func(instance string) bool
	// optional lists files whose absence is acceptable in a state that must be the
	// state after: a result the operator asks for again.
	optional func(p string) bool
	// ignore lists files that are not compared (the archive, which may repeat a line).
	ignore func(p string) bool
	// crashed is an invariant of every crash state, before anything recovers.
	crashed func(t *testing.T, where string, snap map[string]string)
	// retry is what the engine does again once recovery is done.
	retry func(t *testing.T, r *rig, s *sim)
	// bodiesResolve: in every crash state, every live record that refers to a body
	// finds its file.
	bodiesResolve bool
	// extra checks the state a crash left and recovery ended in.
	extra func(t *testing.T, where string, crashed, fin map[string]string)
	// verify states, by hand, what the workflow must have produced, on the disk
	// after the run without a crash.
	verify func(t *testing.T, r *rig)
}

func (sc *scenario) filter(m map[string]string) map[string]string {
	if sc.ignore == nil {
		return m
	}
	out := map[string]string{}
	for k, v := range m {
		if !sc.ignore(k) {
			out[k] = v
		}
	}
	return out
}

// fresh returns a disk with the layout and the setup of the scenario.
func (sc *scenario) fresh(t *testing.T) *fsxtest.Mem {
	t.Helper()
	mem := fsxtest.NewMem()
	for _, d := range append([]string{"config", "keys"}, store.StateDirs()...) {
		if err := fsx.MakeDir(mem, setupTag, d); err != nil {
			t.Fatal(err)
		}
	}
	// The endpoints every world has: two senders' own and the runtime's. A record
	// for an endpoint that has no record is work for the drain rule.
	seed(t, mem, directEndpoint(1, "base"), directEndpoint(2, "base"), directEndpoint(4, "base"))
	if sc.setup != nil {
		sc.setup(t, mem)
	}
	return mem
}

// short lists the files of a snapshot by directory and the first characters of
// the name, for a message a person reads.
func short(m map[string]string) string {
	var out []string
	for k := range m {
		d, n := path.Split(k)
		if len(n) > 8 {
			n = n[:6] + ".." + n[len(n)-8:]
		}
		out = append(out, strings.TrimPrefix(d, "state/")+n)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func equalMaps(a, b map[string]string, optional func(string) bool) string {
	var diffs []string
	for k, v := range a {
		w, ok := b[k]
		switch {
		case !ok && (optional == nil || !optional(k)):
			diffs = append(diffs, "extra "+k)
		case ok && v != w:
			diffs = append(diffs, "different "+k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok && (optional == nil || !optional(k)) {
			diffs = append(diffs, "missing "+k)
		}
	}
	sort.Strings(diffs)
	return strings.Join(diffs, ", ")
}

// accept judges the state recovery ended in.
func (sc *scenario) accept(fin, before, after map[string]string, durable bool) string {
	fin, before, after = sc.filter(fin), sc.filter(before), sc.filter(after)
	if durable || sc.mustComplete {
		return equalMaps(fin, after, sc.optional)
	}
	if sc.mode == atomic {
		if equalMaps(fin, after, sc.optional) == "" || equalMaps(fin, before, nil) == "" {
			return ""
		}
		return "neither the state before nor the state after: vs before " + equalMaps(fin, before, nil) + "; vs after " + equalMaps(fin, after, sc.optional)
	}
	var bad []string
	for k, v := range fin {
		b, inB := before[k]
		a, inA := after[k]
		if !(inB && v == b) && !(inA && v == a) {
			bad = append(bad, "unexpected "+k)
		}
	}
	for k := range before {
		if _, ok := fin[k]; !ok {
			if _, inA := after[k]; inA {
				bad = append(bad, "lost "+k)
			}
		}
	}
	sort.Strings(bad)
	return strings.Join(bad, ", ")
}

// recoverMem restarts over a disk and runs the startup rules to the end,
// returning the rig and the points the recovery reached.
func (sc *scenario) recoverMem(t *testing.T, mem *fsxtest.Mem, sink *coverageSink, hook func(p fsx.Point, k txn.Kind)) *rig {
	t.Helper()
	r := openRigRaw(t, mem, sink)
	r.rec.on = hook
	r.restart()
	s := newSim(t, r.st)
	if sc.enabled != nil {
		s.enabled = sc.enabled
	}
	s.recoverAll()
	if sc.retry != nil {
		sc.retry(t, r, s)
		s.recoverAll()
	}
	return r
}

func (sc *scenario) newSimFor(t *testing.T, r *rig) *sim {
	s := newSim(t, r.st)
	if sc.enabled != nil {
		s.enabled = sc.enabled
	}
	return s
}

// crashKinds are the disks a crash at one instant can leave.
func crashKinds(m *fsxtest.Mem, rng *rand.Rand) []crashState {
	return []crashState{
		{"process-crash", m.Clone(), false},
		{"worst-power-loss", m.CrashCloneWorst(), true},
		{"power-loss-a", m.CrashClone(rng), true},
		{"power-loss-b", m.CrashClone(rng), true},
	}
}

type crashState struct {
	name string
	mem  *fsxtest.Mem
	// power: the state is what a power loss left, everything in it is durable.
	power bool
}

// stats of a run of a scenario.
type scenarioStats struct {
	points, clones, recoveries, nested int
}

// runScenario walks every crash point of the workflow, takes the crash states
// there, and checks that restarting converges to an acceptable state, that
// recovery is idempotent, that a crash during recovery converges to the same
// state, and that the invariants of the scenario hold in the crash state.
func runScenario(t *testing.T, sc *scenario, sink *coverageSink, seed uint64) scenarioStats {
	t.Helper()
	var st scenarioStats

	// The reference: the workflow run without a crash.
	refMem := sc.fresh(t)
	before := snapshot(t, refMem, ".")
	ref := openRig(t, refMem)
	ref.rec.sink = sink
	if err := sc.run(t, ref, sc.newSimFor(t, ref)); err != nil {
		t.Fatalf("the reference run failed: %v", err)
	}
	after := snapshot(t, ref.mem, ".")
	if sc.verify != nil {
		sc.verify(t, ref)
	}
	if equalMaps(sc.filter(before), sc.filter(after), nil) == "" && !strings.HasPrefix(sc.name, "archive-events") {
		t.Fatalf("the workflow changes nothing: the scenario proves nothing")
	}
	st.points = ref.rec.count()

	// The walk.
	rng := newRand(seed)
	mem := sc.fresh(t)
	r := openRig(t, mem)
	r.rec.sink = sink
	durableSeen, durable := false, false
	stepAfter := false
	r.rec.on = func(p fsx.Point, k txn.Kind) {
		if p.Tag.Tx == "settle" && p.Hit > 1 {
			return // one directory sync of the barrier stands for all
		}
		if stepAfter {
			durable = true
			stepAfter = false
		}
		if sc.durable != "" && !durableSeen && p.String() == sc.durable {
			durableSeen, stepAfter = true, true
		}
		for _, cs := range crashKinds(r.mem, rng) {
			st.clones++
			sc.exercise(t, fmt.Sprintf("%s before %v", cs.name, p), cs, before, after, durable, sink, &st, !strings.HasPrefix(cs.name, "power-loss"))
		}
	}
	if err := sc.run(t, r, sc.newSimFor(t, r)); err != nil {
		t.Fatalf("the walked run failed: %v", err)
	}
	r.rec.on = nil
	if sc.durable != "" && !durableSeen {
		t.Errorf("the durable point %q was never reached: the scenario names a point that does not exist", sc.durable)
	}
	// After the workflow ended, every kind of crash is the state after.
	for _, cs := range crashKinds(r.mem, rng) {
		st.clones++
		sc.exercise(t, cs.name+" after the run", cs, before, after, true, sink, &st, true)
	}
	return st
}

// exercise recovers from one crash state and checks everything.
func (sc *scenario) exercise(t *testing.T, where string, cs crashState, before, after map[string]string, durable bool, sink *coverageSink, st *scenarioStats, nested bool) {
	t.Helper()
	crashedSnap := snapshot(t, cs.mem, ".")
	if sc.crashed != nil {
		sc.crashed(t, where, crashedSnap)
	}
	if sc.bodiesResolve {
		checkBodiesResolve(t, where, crashedSnap)
	}

	// Recovery, watched: every point of it gives crash states of its own.
	var inner []crashState
	var rr *rig
	hook := func(p fsx.Point, k txn.Kind) {
		if !nested || testing.Short() || cs.name == "power-loss-a" || cs.name == "power-loss-b" || (p.Tag.Tx == "settle" && p.Hit > 1) {
			return
		}
		inner = append(inner, crashState{fmt.Sprintf("%s, then a crash %s before %v", where, "process", p), rr.mem.Clone(), false})
		inner = append(inner, crashState{fmt.Sprintf("%s, then a power loss before %v", where, p), rr.mem.CrashCloneWorst(), true})
	}
	// The rig does not exist until the hook may run; recoverMem creates it, so
	// give the hook a way to reach it.
	r2 := openRigRaw(t, cs.mem, sink)
	rr = r2
	r2.rec.on = hook
	r2.restart()
	s := sc.newSimFor(t, r2)
	s.recoverAll()
	if sc.retry != nil {
		sc.retry(t, r2, s)
		s.recoverAll()
	}
	st.recoveries++
	fin := snapshot(t, r2.mem, ".")
	if why := sc.accept(fin, before, after, durable); why != "" {
		t.Errorf("%s: recovery ended in a state that is not acceptable: %s", where, why)
		return
	}
	noWork(t, where+", after recovery", r2.scan(), s.enabled)
	if sc.extra != nil {
		sc.extra(t, where, crashedSnap, fin)
	}

	// Idempotent: a second restart and a second run of the rules write nothing.
	r2.rec.on = nil
	n := r2.rec.count()
	r3 := openRigRaw(t, cs.mem, sink)
	r3.restart()
	s3 := sc.newSimFor(t, r3)
	s3.recoverAll()
	if sc.retry != nil {
		sc.retry(t, r3, s3)
	}
	_ = n
	if r3.rec.working() != 0 {
		t.Errorf("%s: a second recovery reached %d crash points: recovery is not idempotent", where, r3.rec.working())
	}
	if again := snapshot(t, r3.mem, "."); equalMaps(again, fin, nil) != "" {
		t.Errorf("%s: a second recovery changed the disk: %s", where, equalMaps(again, fin, nil))
	}

	// A crash during recovery converges to the same state.
	for _, in := range inner {
		st.nested++
		r4 := sc.recoverMem(t, in.mem, sink, nil)
		got := snapshot(t, r4.mem, ".")
		// A power loss on top of a process crash takes what the process had not
		// made durable, the head of the workflow included: it is an earlier
		// crash, and the state before is acceptable again.
		stillDurable := durable && (cs.power || !in.power)
		if why := sc.accept(got, before, after, stillDurable); why != "" {
			t.Errorf("%s: recovery after a crash during recovery ends in a state that is not acceptable: %s\n  got: %s\n  fin: %s", in.name, why, short(sc.filter(got)), short(sc.filter(fin)))
		}
		if stillDurable || sc.mustComplete {
			if why := equalMaps(sc.filter(got), sc.filter(fin), sc.optional); why != "" {
				t.Errorf("%s: recovery after a crash during recovery differs from recovery without one: %s", in.name, why)
			}
		}
	}
}

// checkBodiesResolve fails when a live record refers to a body that is not on
// the disk. A body is written before the record that refers to it and removed
// after the last one that did.
func checkBodiesResolve(t *testing.T, where string, snap map[string]string) {
	t.Helper()
	for p, content := range snap {
		dir, name := path.Split(strings.TrimSuffix(p, "/"))
		dir = strings.TrimSuffix(dir, "/")
		if !strings.HasSuffix(name, ".rec") {
			continue
		}
		var refs []*store.BodyRef
		switch rec, dmg := store.DecodeRecord(dir, name, []byte(content)); {
		case dmg != nil:
			continue
		default:
			switch r := rec.(type) {
			case store.OutRecord:
				refs = append(refs, r.Body)
			case store.InRecord:
				refs = append(refs, r.Body)
			case store.CtlRecord:
				refs = append(refs, r.Body)
			}
		}
		for _, ref := range refs {
			if ref == nil {
				continue
			}
			if _, ok := snap[path.Join(store.BodiesDir, ref.Name())]; !ok {
				t.Errorf("%s: %s refers to the body %s, which is not there", where, p, ref.Name())
			}
		}
	}
}
