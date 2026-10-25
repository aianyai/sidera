package store_test

import (
	"fmt"
	"math/rand/v2"
	"path"
	"sort"
	"strings"
	"sync"
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

// seededPowerLosses are the kinds of crash that draw their fate from a random
// source: two draws of the distribution of the crash model.
var seededPowerLosses = []string{"power-loss-a", "power-loss-b"}

// crashKinds are the disks a crash at one instant can leave that the walks judge.
// The reliability floor needs two: the process crash, where every completed call
// survives, and the worst power loss, where only what was synced survives. The
// worst one shows that an acknowledged write was synced; the process one shows
// that recovery copes with everything that was written. Under the transaction
// discipline a random power loss only keeps a subset of the one group of writes
// that is not synced yet, between those two, so an atomic transaction takes no
// seeded one: the property tests of the package draw them. A batch is a set of
// independent single-file transitions, where any subset of its written files is
// a state of its own, so a batch also takes the two seeded power losses, except
// under the race detector: a walk is single-threaded on a file system of its own,
// so the detector finds nothing there and only multiplies the cost of the seeded
// states, which the runs without it take.
func crashKinds(m *fsxtest.Mem, rng *rand.Rand, md mode) []crashState {
	kinds := []crashState{
		{name: "process-crash", mem: m.Clone()},
		{name: "worst-power-loss", mem: m.CrashCloneWorst(), power: true},
	}
	if md == batch && !raceEnabled {
		for _, name := range seededPowerLosses {
			kinds = append(kinds, crashState{name: name, mem: m.CrashClone(rng), power: true, seeded: true})
		}
	}
	return kinds
}

// floorKindNames are the kinds every walk takes at every point, whatever the mode
// of the transaction.
func floorKindNames() []string {
	var names []string
	for _, cs := range crashKinds(fsxtest.NewMem(), nil, atomic) {
		names = append(names, cs.name)
	}
	return names
}

type crashState struct {
	name string
	mem  *fsxtest.Mem
	// power: the state is what a power loss left, everything in it is durable.
	power bool
	// seeded: the state is a random power loss, not one of the two every walk takes.
	seeded bool
}

// stats of a run of a scenario.
type scenarioStats struct {
	points, clones, recoveries, nested int
}

// runScenario walks every crash point of the workflow, takes the crash states
// there, and checks that restarting converges to an acceptable state, that
// recovery is idempotent and that the invariants of the scenario hold in the
// crash state. The crash states whose recovery is the first to reach a point of
// a recovery rule are offered to the sink, which crashes those recoveries again
// (runNestedWalks); seed orders the scenarios, so that the choice does not
// depend on which walk finishes first.
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
		if p.Tag.Tx == "txn-settle" && p.Hit > 1 {
			return // one directory sync of the barrier stands for all
		}
		if stepAfter {
			durable = true
			stepAfter = false
		}
		if sc.durable != "" && !durableSeen && p.String() == sc.durable {
			durableSeen, stepAfter = true, true
		}
		for _, cs := range crashKinds(r.mem, rng, sc.mode) {
			st.clones++
			sc.exercise(t, fmt.Sprintf("%s before %v", cs.name, p), cs, before, after, durable, sink, &st, nestedOrder{seed, st.clones}, !cs.seeded)
			sink.took(p, cs.name)
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
	for _, cs := range crashKinds(r.mem, rng, sc.mode) {
		st.clones++
		sc.exercise(t, cs.name+" after the run", cs, before, after, true, sink, &st, nestedOrder{seed, st.clones}, !cs.seeded)
	}
	return st
}

// exercise recovers from one crash state and checks everything but the crash
// during that recovery: when offerNested is set, the state goes to the sink as a
// candidate for it.
func (sc *scenario) exercise(t *testing.T, where string, cs crashState, before, after map[string]string, durable bool, sink *coverageSink, st *scenarioStats, order nestedOrder, offerNested bool) {
	t.Helper()
	crashedSnap := snapshot(t, cs.mem, ".")
	if sc.crashed != nil {
		sc.crashed(t, where, crashedSnap)
	}
	if sc.bodiesResolve {
		checkBodiesResolve(t, where, crashedSnap)
	}

	// Recovery, watched: the crash during it is the walk of nestedCase.
	domain := recoveryDomain()
	reached := map[fsx.Key]bool{}
	hook := func(p fsx.Point, k txn.Kind) {
		if domain[p.Key] {
			reached[p.Key] = true
		}
	}
	var pristine *fsxtest.Mem
	if offerNested {
		pristine = cs.mem.Clone()
	}
	r2 := openRigRaw(t, cs.mem, sink)
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
	r3 := openRigRaw(t, cs.mem, sink)
	r3.restart()
	s3 := sc.newSimFor(t, r3)
	s3.recoverAll()
	if sc.retry != nil {
		sc.retry(t, r3, s3)
	}
	if r3.rec.working() != 0 {
		t.Errorf("%s: a second recovery reached %d crash points: recovery is not idempotent", where, r3.rec.working())
	}
	if again := snapshot(t, r3.mem, "."); equalMaps(again, fin, nil) != "" {
		t.Errorf("%s: a second recovery changed the disk: %s", where, equalMaps(again, fin, nil))
	}

	if offerNested {
		sink.offerNested(&nestedCase{sc: sc, where: where, order: order, disk: pristine, reached: reached, power: cs.power, durable: durable, before: before, after: after, fin: fin})
	}
}

// nestedOrder places a crash state in the walks: the seed of its scenario and its
// number in the walk of it.
type nestedOrder struct {
	seed uint64
	n    int
}

func (a nestedOrder) before(b nestedOrder) bool {
	return a.seed < b.seed || (a.seed == b.seed && a.n < b.n)
}

// nestedCase is a crash state whose recovery is crashed again, with what the
// walk that took it knows about the state: the disk as the crash left it, and the
// states before, after and recovery ended in.
type nestedCase struct {
	sc            *scenario
	where         string
	order         nestedOrder
	disk          *fsxtest.Mem
	reached       map[fsx.Key]bool // the points of the recovery rules its recovery reaches
	power         bool
	durable       bool
	before, after map[string]string
	fin           map[string]string
}

// recoveryDomain is every crash point of the recovery rules the nested walk has to
// crash at: the points of the recovery transactions of the store, and of the
// cleanup and the settle that txn.Recover runs at every start. Two kinds of point
// are left out because no recovery reaches them: the removal of a discarded
// directory (the store never discards one, so no disk of its workflows holds one)
// and the renaming of a damaged body into quarantine, which the read that finds
// the body damaged does, so that a recovery only continues the chunks after it.
var recoveryDomain = sync.OnceValue(func() map[fsx.Key]bool {
	prog, err := programTable()
	if err != nil {
		panic(err)
	}
	internal, mine := map[string]bool{}, map[string]bool{}
	for _, d := range txn.Internal() {
		internal[d.Name] = true
	}
	for _, d := range store.Transactions() {
		mine[d.Name] = true
	}
	domain := map[fsx.Key]bool{}
	for _, k := range prog.Keys() {
		switch {
		case internal[k.Tag.Tx] && k.Tag.Role != "trash":
			domain[k.Key] = true
		case mine[k.Tag.Tx] && k.Kind == txn.Recovery && !(k.Tag.Tx == "quarantine-body" && k.Tag.Role == "body"):
			domain[k.Key] = true
		}
	}
	return domain
})

// walk crashes the recovery of the case at every point of it, in the two
// kinds of crash that draw nothing, and checks that recovering again ends in the
// state the recovery without the crash ended in. It returns the number of
// recoveries it crashed.
func (nc *nestedCase) walk(t *testing.T, sink *coverageSink) int {
	t.Helper()
	sc := nc.sc
	type inner struct {
		name  string
		at    fsx.Point
		model string
		mem   *fsxtest.Mem
		power bool
	}
	var states []inner
	var rr *rig
	hook := func(p fsx.Point, k txn.Kind) {
		if p.Tag.Tx == "txn-settle" && p.Hit > 1 {
			return // one directory sync of the barrier stands for all
		}
		states = append(states,
			inner{nc.where + ", then a crash before " + p.String(), p, "process-crash", rr.mem.Clone(), false},
			inner{nc.where + ", then a power loss before " + p.String(), p, "worst-power-loss", rr.mem.CrashCloneWorst(), true})
	}
	rr = openRigRaw(t, nc.disk, sink)
	rr.rec.on = hook
	rr.restart()
	s := sc.newSimFor(t, rr)
	s.recoverAll()
	if sc.retry != nil {
		sc.retry(t, rr, s)
		s.recoverAll()
	}

	for _, in := range states {
		sink.tookNested(in.at, in.model)
		r4 := sc.recoverMem(t, in.mem, sink, nil)
		got := snapshot(t, r4.mem, ".")
		// A power loss on top of a process crash takes what the process had not
		// made durable, the head of the workflow included: it is an earlier
		// crash, and the state before is acceptable again.
		stillDurable := nc.durable && (nc.power || !in.power)
		if why := sc.accept(got, nc.before, nc.after, stillDurable); why != "" {
			t.Errorf("%s: recovery after a crash during recovery ends in a state that is not acceptable: %s\n  got: %s\n  fin: %s", in.name, why, short(sc.filter(got)), short(sc.filter(nc.fin)))
		}
		if stillDurable || sc.mustComplete {
			if why := equalMaps(sc.filter(got), sc.filter(nc.fin), sc.optional); why != "" {
				t.Errorf("%s: recovery after a crash during recovery differs from recovery without one: %s", in.name, why)
			}
		}
	}
	return len(states)
}

// runNestedWalks crashes again, at every point, the recovery of each crash state
// the walks offered, and returns the number of recoveries after a crash during
// recovery. It runs after the walks it follows, in the test that ran them.
func runNestedWalks(t *testing.T, sink *coverageSink) int {
	t.Helper()
	var mu sync.Mutex
	total := 0
	t.Run("nested", func(t *testing.T) {
		for _, nc := range sink.nestedCases() {
			t.Run(nc.where, func(t *testing.T) {
				t.Parallel()
				n := nc.walk(t, sink)
				mu.Lock()
				defer mu.Unlock()
				total += n
			})
		}
	})
	return total
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
