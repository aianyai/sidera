package txn_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// A property test over random transactions. Each scenario declares a random
// transaction, runs a random legal sequence of calls against it, optionally
// with one injected failure, and checks, against a model written here and not
// taken from the package under test:
//
//   - the crash points reached are exactly the steps the operations run, in
//     order, numbered per key, with the commit last;
//   - the class of the failure is what the commit point rule says;
//   - at every point before the commit point, in every kind of crash, nothing
//     the transaction owns that is not a preparatory file or an append has
//     changed; a failure before the commit point leaves the same state;
//   - everything is durable at the commit point and after Commit returned nil;
//   - a restart removes only leftovers;
//   - the counts of Coverage are the counts of the points reached.
//
// The seed is fixed; the coverage assertions at the end say how much of each
// case the seed reached.

// call is one call of a scenario to the Tx.
type call struct {
	write   int // index of the declared write it belongs to
	op      txn.Op
	role    string
	path    string
	to      string
	data    string
	prepare bool
}

type scenario struct {
	decl    *txn.Decl
	initial map[string]string // snapshot of the disk before the run
	calls   []call
}

// event is one step the run is expected to take.
type event struct {
	call  int
	op    txn.Op
	step  fsx.Step
	point fsx.Point
}

// The oracle: the steps of each operation, written out here separately from
// the package under test.
func oracleSteps(op txn.Op, firstAppendOfPath bool) []fsx.Step {
	switch op {
	case txn.OpReplace:
		return []fsx.Step{fsx.StepCreate, fsx.StepWrite, fsx.StepSync, fsx.StepRename, fsx.StepSyncDir}
	case txn.OpReplaceDefer:
		return []fsx.Step{fsx.StepCreate, fsx.StepWrite, fsx.StepSync, fsx.StepRename}
	case txn.OpAppend:
		if firstAppendOfPath {
			return []fsx.Step{fsx.StepOpen, fsx.StepWrite}
		}
		return []fsx.Step{fsx.StepWrite}
	case txn.OpSyncFile:
		return []fsx.Step{fsx.StepSync, fsx.StepSyncDir}
	case txn.OpRenameFile:
		return []fsx.Step{fsx.StepRename, fsx.StepSyncDir}
	case txn.OpRemove:
		return []fsx.Step{fsx.StepRemove}
	case txn.OpSyncDir:
		return []fsx.Step{fsx.StepSyncDir}
	case txn.OpMakeDir:
		return []fsx.Step{fsx.StepMkdir, fsx.StepSyncDir}
	case txn.OpRenameDir:
		return []fsx.Step{fsx.StepRenameDir, fsx.StepSyncDir}
	case txn.OpDiscard:
		return []fsx.Step{fsx.StepRenameDir, fsx.StepSyncDir, fsx.StepRemoveAll}
	case txn.OpRemoveAll:
		return []fsx.Step{fsx.StepRemoveAll}
	}
	panic("oracle: unknown operation")
}

// isCommitStep is the oracle of the commit point: the completion of a
// namespace step, or the sync of an appended file, or the directory sync of a
// lone SyncDir, of a write that is not preparatory.
func isCommitStep(op txn.Op, step fsx.Step) bool {
	switch op {
	case txn.OpReplace, txn.OpReplaceDefer, txn.OpRenameFile:
		return step == fsx.StepRename
	case txn.OpRemove:
		return step == fsx.StepRemove
	case txn.OpMakeDir:
		return step == fsx.StepMkdir
	case txn.OpRenameDir, txn.OpDiscard:
		return step == fsx.StepRenameDir
	case txn.OpRemoveAll:
		return step == fsx.StepRemoveAll
	case txn.OpSyncFile:
		return step == fsx.StepSync
	case txn.OpSyncDir:
		return step == fsx.StepSyncDir
	}
	return false
}

// isFSFatal is the oracle of the file system layer's own fatal steps.
func isFSFatal(op txn.Op, step fsx.Step) bool {
	return step == fsx.StepSyncDir || (op == txn.OpSyncFile && step == fsx.StepSync)
}

// expectedEvents expands the calls into the steps the run takes, numbering
// each key from 1.
func expectedEvents(name string, calls []call) []event {
	var events []event
	hits := map[fsx.Key]int{}
	appended := map[string]bool{}
	for i, c := range calls {
		first := false
		if c.op == txn.OpAppend && !appended[c.path] {
			appended[c.path], first = true, true
		}
		for _, step := range oracleSteps(c.op, first) {
			k := fsx.Key{Tag: fsx.Tag{Tx: name, Role: c.role}, Step: step}
			hits[k]++
			events = append(events, event{call: i, op: c.op, step: step, point: fsx.Point{Key: k, Hit: hits[k]}})
		}
	}
	return events
}

func commitPointStep(name string) fsx.Point {
	return fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: name}, Step: fsx.StepCommit}, Hit: 1}
}

// --- the generator ---

type generator struct {
	rng      *rand.Rand
	name     string
	files    map[string]string // files to create before the run
	dirs     []string          // directories to create before the run, parents first
	writes   []txn.Write
	calls    []call
	next     int
	appended map[string]bool
}

func (g *generator) coin() bool     { return g.rng.IntN(2) == 0 }
func (g *generator) optional() bool { return g.rng.IntN(5) == 0 }
func (g *generator) dir() string    { return fmt.Sprintf("d%d", g.rng.IntN(3)) }
func (g *generator) id() int        { g.next++; return g.next }
func (g *generator) role() string   { return fmt.Sprintf("w%d", len(g.writes)) }
func (g *generator) data() string   { return fmt.Sprintf("v%d", g.id()) }
func (g *generator) count(many bool) int {
	if many {
		return 1 + g.rng.IntN(3)
	}
	return 1
}

// declare adds a write and says whether the run executes it: a write that is
// not optional always is, an optional one half of the time.
func (g *generator) declare(w txn.Write) (idx int, run bool) {
	g.writes = append(g.writes, w)
	return len(g.writes) - 1, !w.Optional || g.coin()
}

func (g *generator) add(c call) { g.calls = append(g.calls, c) }

func (g *generator) newFile(dir string) string { return fmt.Sprintf("%s/f%d", dir, g.id()) }

func (g *generator) existingFile(dir string) string {
	p := g.newFile(dir)
	g.files[p] = g.data()
	return p
}

func (g *generator) existingDir(dir string) string {
	p := fmt.Sprintf("%s/e%d", dir, g.id())
	g.dirs = append(g.dirs, p)
	g.files[p+"/inner"] = g.data()
	return p
}

func (g *generator) replace(prepare bool) {
	w := txn.Write{Role: g.role(), Op: txn.OpReplace, Many: g.coin(), Optional: g.optional(), Prepare: prepare}
	idx, run := g.declare(w)
	if !run {
		return
	}
	dir := g.dir()
	for i := 0; i < g.count(w.Many); i++ {
		g.add(call{write: idx, op: w.Op, role: w.Role, path: g.newFile(dir), data: g.data(), prepare: prepare})
	}
}

func (g *generator) deferred() {
	w := txn.Write{Role: g.role(), Op: txn.OpReplaceDefer, Many: g.coin(), Optional: g.optional()}
	idx, run := g.declare(w)
	sync := txn.Write{Role: g.role(), Op: txn.OpSyncDir, Optional: w.Optional}
	sidx, _ := g.declare(sync)
	if !run {
		return
	}
	dir := g.dir()
	for i := 0; i < g.count(w.Many); i++ {
		g.add(call{write: idx, op: w.Op, role: w.Role, path: g.newFile(dir), data: g.data()})
	}
	g.add(call{write: sidx, op: sync.Op, role: sync.Role, path: dir})
}

func (g *generator) removals() {
	w := txn.Write{Role: g.role(), Op: txn.OpRemove, Many: g.coin(), Optional: g.optional()}
	idx, run := g.declare(w)
	sync := txn.Write{Role: g.role(), Op: txn.OpSyncDir, Optional: w.Optional}
	sidx, _ := g.declare(sync)
	if !run {
		return
	}
	dir := g.dir()
	for i := 0; i < g.count(w.Many); i++ {
		g.add(call{write: idx, op: w.Op, role: w.Role, path: g.existingFile(dir)})
	}
	g.add(call{write: sidx, op: sync.Op, role: sync.Role, path: dir})
}

func (g *generator) removeAlls() {
	w := txn.Write{Role: g.role(), Op: txn.OpRemoveAll, Many: g.coin(), Optional: g.optional()}
	idx, run := g.declare(w)
	sync := txn.Write{Role: g.role(), Op: txn.OpSyncDir, Optional: w.Optional}
	sidx, _ := g.declare(sync)
	if !run {
		return
	}
	dir := g.dir()
	for i := 0; i < g.count(w.Many); i++ {
		g.add(call{write: idx, op: w.Op, role: w.Role, path: g.existingDir(dir)})
	}
	g.add(call{write: sidx, op: sync.Op, role: sync.Role, path: dir})
}

func (g *generator) renameFile() {
	w := txn.Write{Role: g.role(), Op: txn.OpRenameFile, Many: g.coin(), Optional: g.optional()}
	idx, run := g.declare(w)
	if !run {
		return
	}
	dir := g.dir()
	for i := 0; i < g.count(w.Many); i++ {
		g.add(call{write: idx, op: w.Op, role: w.Role, path: g.existingFile(dir), to: g.newFile(dir)})
	}
}

func (g *generator) makeDir(prepare bool) {
	w := txn.Write{Role: g.role(), Op: txn.OpMakeDir, Many: g.coin(), Optional: g.optional(), Prepare: prepare}
	idx, run := g.declare(w)
	if !run {
		return
	}
	dir := g.dir()
	for i := 0; i < g.count(w.Many); i++ {
		g.add(call{write: idx, op: w.Op, role: w.Role, path: fmt.Sprintf("%s/m%d", dir, g.id()), prepare: prepare})
	}
}

func (g *generator) renameDir(prepare bool) {
	w := txn.Write{Role: g.role(), Op: txn.OpRenameDir, Many: g.coin(), Optional: g.optional(), Prepare: prepare}
	idx, run := g.declare(w)
	if !run {
		return
	}
	dir := g.dir()
	for i := 0; i < g.count(w.Many); i++ {
		g.add(call{write: idx, op: w.Op, role: w.Role, path: g.existingDir(dir), to: fmt.Sprintf("%s/t%d", dir, g.id()), prepare: prepare})
	}
}

func (g *generator) discard(prepare bool) {
	w := txn.Write{Role: g.role(), Op: txn.OpDiscard, Many: g.coin(), Optional: g.optional(), Prepare: prepare}
	idx, run := g.declare(w)
	if !run {
		return
	}
	dir := g.dir()
	for i := 0; i < g.count(w.Many); i++ {
		g.add(call{write: idx, op: w.Op, role: w.Role, path: g.existingDir(dir), prepare: prepare})
	}
}

func (g *generator) appends(synced bool) {
	role := g.role()
	w := txn.Write{Role: role, Op: txn.OpAppend, Many: g.coin(), Optional: g.optional()}
	idx, run := g.declare(w)
	sidx := -1
	if synced {
		sidx, _ = g.declare(txn.Write{Role: role, Op: txn.OpSyncFile, Optional: w.Optional})
	}
	if !run {
		return
	}
	dir := g.dir()
	p := g.newFile(dir)
	if g.coin() {
		g.files[p] = "pre\n"
	}
	for i := 0; i < g.count(w.Many); i++ {
		g.add(call{write: idx, op: w.Op, role: role, path: p, data: g.data() + "\n"})
	}
	if synced {
		g.add(call{write: sidx, op: txn.OpSyncFile, role: role, path: p})
	}
}

func (g *generator) syncDir() {
	w := txn.Write{Role: g.role(), Op: txn.OpSyncDir, Optional: g.optional()}
	idx, run := g.declare(w)
	if run {
		g.add(call{write: idx, op: w.Op, role: w.Role, path: g.dir()})
	}
}

// scenarioFor builds the random transaction named name and the disk it runs on.
func scenarioFor(t testing.TB, rng *rand.Rand, name string) scenario {
	g := &generator{rng: rng, name: name, files: map[string]string{}}
	for i := g.rng.IntN(3); i > 0; i-- {
		switch g.rng.IntN(4) {
		case 0:
			g.replace(true)
		case 1:
			g.makeDir(true)
		case 2:
			g.renameDir(true)
		default:
			g.discard(true)
		}
	}
	for i := 1 + g.rng.IntN(4); i > 0; i-- {
		switch g.rng.IntN(12) {
		case 0:
			g.replace(false)
		case 1, 9: // a deferred group is the rule with the most to prove, so it has two slots
			g.deferred()
		case 2:
			g.removals()
		case 3:
			g.renameFile()
		case 4:
			g.makeDir(false)
		case 5:
			g.renameDir(false)
		case 6:
			g.discard(false)
		case 7:
			g.appends(true)
		case 8:
			g.appends(false)
		case 10:
			g.removeAlls()
		default:
			g.syncDir()
		}
	}

	mem := fsxtest.NewMem()
	mkdirs(t, mem, "d0", "d1", "d2")
	mkdirs(t, mem, g.dirs...)
	put(t, mem, g.files)
	return scenario{
		decl:    &txn.Decl{Name: name, Kind: txn.Normal, Writes: g.writes},
		initial: snapshot(t, mem),
		calls:   g.calls,
	}
}

// build recreates the scenario's initial disk.
func (s scenario) disk(t testing.TB) *fsxtest.Mem {
	mem := fsxtest.NewMem()
	mkdirs(t, mem, "d0", "d1", "d2")
	var dirs []string
	files := map[string]string{}
	for k, v := range s.initial {
		if strings.HasSuffix(k, "/") {
			if d := strings.TrimSuffix(k, "/"); d != "d0" && d != "d1" && d != "d2" {
				dirs = append(dirs, d)
			}
		} else {
			files[k] = v
		}
	}
	sort.Strings(dirs) // a parent sorts before its children
	mkdirs(t, mem, dirs...)
	put(t, mem, files)
	return mem
}

func (c call) do(tx *txn.Tx) error {
	switch c.op {
	case txn.OpReplace:
		return tx.Replace(c.role, c.path, []byte(c.data), mode)
	case txn.OpReplaceDefer:
		return tx.ReplaceDefer(c.role, c.path, []byte(c.data), mode)
	case txn.OpAppend:
		return tx.Append(c.role, c.path, []byte(c.data))
	case txn.OpSyncFile:
		return tx.SyncFile(c.role, c.path)
	case txn.OpRenameFile:
		return tx.RenameFile(c.role, c.path, c.to)
	case txn.OpRemove:
		return tx.Remove(c.role, c.path)
	case txn.OpSyncDir:
		return tx.SyncDir(c.role, c.path)
	case txn.OpMakeDir:
		return tx.MakeDir(c.role, c.path)
	case txn.OpRenameDir:
		return tx.RenameDir(c.role, c.path, c.to)
	case txn.OpDiscard:
		return tx.Discard(c.role, c.path)
	case txn.OpRemoveAll:
		return tx.RemoveAll(c.role, c.path)
	}
	panic("unknown operation")
}

// --- the model of what the calls do to the disk ---

func keysUnder(m map[string]string, dir string) []string {
	var out []string
	for k := range m {
		if k == dir+"/" || strings.HasPrefix(k, dir+"/") {
			out = append(out, k)
		}
	}
	return out
}

// apply is the effect of a completed call on the model of the disk.
func apply(m map[string]string, c call) {
	switch c.op {
	case txn.OpReplace, txn.OpReplaceDefer:
		m[c.path] = c.data
	case txn.OpAppend:
		m[c.path] += c.data
	case txn.OpRenameFile:
		m[c.to] = m[c.path]
		delete(m, c.path)
	case txn.OpRemove:
		delete(m, c.path)
	case txn.OpMakeDir:
		m[c.path+"/"] = ""
	case txn.OpRenameDir:
		for _, k := range keysUnder(m, c.path) {
			m[c.to+strings.TrimPrefix(k, c.path)] = m[k]
			delete(m, k)
		}
	case txn.OpDiscard, txn.OpRemoveAll:
		for _, k := range keysUnder(m, c.path) {
			delete(m, k)
		}
	}
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// touched is every path of the disk a call can change, in the initial state
// and in the final one.
func touched(c call, initial, final map[string]string) []string {
	switch c.op {
	case txn.OpReplace, txn.OpReplaceDefer, txn.OpRemove:
		return []string{c.path}
	case txn.OpRenameFile:
		return []string{c.path, c.to}
	case txn.OpMakeDir:
		return []string{c.path + "/"}
	case txn.OpRenameDir:
		return append(keysUnder(initial, c.path), keysUnder(final, c.to)...)
	case txn.OpDiscard, txn.OpRemoveAll:
		return keysUnder(initial, c.path)
	}
	return nil
}

// sameEntry compares one path of two snapshots, absence included.
func sameEntry(a, b map[string]string, p string) bool {
	av, aok := a[p]
	bv, bok := b[p]
	return aok == bok && av == bv
}

// runScenario's checks, kept with the scenario they belong to.
type checker struct {
	t        *testing.T
	s        scenario
	final    map[string]string
	appends  map[string]bool // paths appended to
	synced   map[string]bool // appended paths that a SyncFile of the run covers
	untilNow []string        // paths a non-preparatory call can change, appends excluded
	everyOne []string        // paths any call can change, unsynced appends excluded
}

func newChecker(t *testing.T, s scenario) *checker {
	c := &checker{t: t, s: s, final: cloneMap(s.initial), appends: map[string]bool{}, synced: map[string]bool{}}
	for _, call := range s.calls {
		switch call.op {
		case txn.OpAppend:
			c.appends[call.path] = true
		case txn.OpSyncFile:
			c.synced[call.path] = true
		}
		apply(c.final, call)
	}
	seenOwned, seenAll := map[string]bool{}, map[string]bool{}
	for _, call := range s.calls {
		for _, p := range touched(call, s.initial, c.final) {
			if c.appends[p] {
				continue
			}
			if !call.prepare && !seenOwned[p] {
				seenOwned[p] = true
				c.untilNow = append(c.untilNow, p)
			}
			if !seenAll[p] {
				seenAll[p] = true
				c.everyOne = append(c.everyOne, p)
			}
		}
	}
	for p := range c.synced {
		c.everyOne = append(c.everyOne, p)
	}
	return c
}

// beforeCommit: nothing a non-preparatory write owns has changed.
func (c *checker) beforeCommit(where string, got map[string]string) {
	c.t.Helper()
	for _, p := range c.untilNow {
		if !sameEntry(got, c.s.initial, p) {
			c.t.Errorf("%s: %q is %q (present %v), it was %q (present %v) before the transaction, and no commit step has completed",
				where, p, got[p], has(got, p), c.s.initial[p], has(c.s.initial, p))
		}
	}
}

// durable: every effect of the run is on the disk.
func (c *checker) durable(where string, got map[string]string) {
	c.t.Helper()
	for _, p := range c.everyOne {
		if !sameEntry(got, c.final, p) {
			c.t.Errorf("%s: %q is %q (present %v), want %q (present %v)", where, p, got[p], has(got, p), c.final[p], has(c.final, p))
		}
	}
}

func has(m map[string]string, k string) bool { _, ok := m[k]; return ok }

// tally is how much of each case the run of the property reached.
type tally struct {
	scenarios, injected, failedClass, fatalAfterCommit, fatalInFS, succeeded int
	withPrepare, withDefer, withoutWrites, calls                             int
	opCalls                                                                  map[txn.Op]int
	preCommitPoints, postCommitPoints, commitPoints                          int
	failedWithNothingChanged                                                 int
}

func TestRandomTransactionsHonourTheCommitPointOrderingAndDurability(t *testing.T) {
	const scenarios = 400
	rng := rand.New(rand.NewPCG(20241024, 8))
	tl := tally{opCalls: map[txn.Op]int{}}

	for n := 0; n < scenarios; n++ {
		name := fmt.Sprintf("r%d", n)
		s := scenarioFor(t, rng, name)
		t.Run(name, func(t *testing.T) { runScenario(t, rng, s, &tl) })
	}

	t.Logf("scenarios %d (%d with an injected failure: %d failed, %d fatal after commit, %d fatal in the file system layer; %d succeeded)",
		tl.scenarios, tl.injected, tl.failedClass, tl.fatalAfterCommit, tl.fatalInFS, tl.succeeded)
	t.Logf("points checked: %d before the commit point, %d after, %d commit points; %d calls", tl.preCommitPoints, tl.postCommitPoints, tl.commitPoints, tl.calls)

	t.Logf("scenarios with a preparatory write %d, with a deferred group %d, with no write %d; calls per operation %v", tl.withPrepare, tl.withDefer, tl.withoutWrites, tl.opCalls)

	atLeast := func(what string, got, min int) {
		t.Helper()
		if got < min {
			t.Errorf("the seed reached %s only %d times, want at least %d", what, got, min)
		}
	}
	atLeast("scenarios", tl.scenarios, scenarios)
	atLeast("injected failures", tl.injected, 150)
	atLeast("failures of class Failed", tl.failedClass, 40)
	atLeast("fatal failures decided by the commit point", tl.fatalAfterCommit, 40)
	atLeast("fatal failures decided by the file system layer", tl.fatalInFS, 20)
	atLeast("successful runs", tl.succeeded, 100)
	atLeast("failed runs whose disk was checked for no change", tl.failedWithNothingChanged, 40)
	atLeast("scenarios with a preparatory write", tl.withPrepare, 100)
	atLeast("scenarios with a deferred group", tl.withDefer, 100)
	atLeast("scenarios that made no write", tl.withoutWrites, 2)
	atLeast("points before the commit point", tl.preCommitPoints, 1000)
	atLeast("points after the commit point", tl.postCommitPoints, 1000)
	for _, op := range []txn.Op{txn.OpReplace, txn.OpReplaceDefer, txn.OpAppend, txn.OpSyncFile, txn.OpRenameFile, txn.OpRemove,
		txn.OpSyncDir, txn.OpMakeDir, txn.OpRenameDir, txn.OpDiscard, txn.OpRemoveAll} {
		atLeast(fmt.Sprintf("operation %d", op), tl.opCalls[op], 60)
	}
}

func runScenario(t *testing.T, rng *rand.Rand, s scenario, tl *tally) {
	events := expectedEvents(s.decl.Name, s.calls)
	chk := newChecker(t, s)

	// Where to inject a failure: half of the runs fail one step, chosen among
	// those the run takes.
	failAt := -1
	if len(events) > 0 && rng.IntN(2) == 0 {
		failAt = rng.IntN(len(events))
	}
	// committedBefore[i]: a commit step completed before step i.
	committedBefore := make([]bool, len(events)+1)
	for i, e := range events {
		committedBefore[i+1] = committedBefore[i] || (isCommitStep(e.op, e.step) && !s.calls[e.call].prepare)
	}

	mem := s.disk(t)
	var faulty *fsxtest.Faulty
	var base fsx.FS = mem
	if failAt >= 0 {
		faulty = fsxtest.NewFaulty(t, mem)
		base = faulty
		p := events[failAt].point
		faulty.Fail(fsxtest.Match{Tx: p.Tag.Tx, Role: p.Tag.Role, Step: p.Step, Hit: p.Hit}, syscall.EIO)
	}
	tab := table(t, s.decl)
	rec := &recorder{}
	crashRNG := rand.New(rand.NewPCG(uint64(len(events)), 99))
	// The Writer recovers before the scenario begins, as a program's does. The
	// observer is not set yet, so the points of that Recover are neither counted
	// nor made crash states of: the scenario is the declaration under test.
	w := txn.New(base, tab, rec)
	if err := w.Recover(); err != nil {
		t.Fatalf("setup Recover: %v", err)
	}
	recoverPoints := rec.names()
	rec.forget()
	rec.on = func(p fsx.Point, _ txn.Kind) {
		i := rec.count() - 1
		isCommit := p.Step == fsx.StepCommit
		var pre bool
		if isCommit {
			tl.commitPoints++
		} else {
			pre = !committedBefore[i]
			if pre {
				tl.preCommitPoints++
			} else {
				tl.postCommitPoints++
			}
		}
		for _, cs := range crashStates(mem, crashRNG, 1) {
			got := snapshot(t, cs.mem)
			where := fmt.Sprintf("%s before %v", cs.name, p)
			if pre {
				chk.beforeCommit(where, got)
			}
			if isCommit && cs.name != "process-crash" {
				chk.durable(where, got)
			}
			if cs.name != "process-crash" {
				if err := txn.New(cs.mem, tab, nil).Recover(); err != nil {
					t.Fatalf("%s: Recover: %v", where, err)
				}
				if after := snapshot(t, cs.mem); !reflect.DeepEqual(after, got) {
					t.Errorf("%s: Recover changed more than leftovers", where)
				}
				if left := leftovers(t, cs.mem); len(left) != 0 {
					t.Errorf("%s: leftovers after Recover: %v", where, left)
				}
			}
		}
	}

	tx := w.Begin(s.decl)
	errs := make([]error, 0, len(s.calls)+1)
	for _, c := range s.calls {
		errs = append(errs, c.do(tx))
	}
	errs = append(errs, tx.Commit())
	tx.Close()

	tl.scenarios++
	tl.calls += len(s.calls)
	hasDefer, hasPrepare := false, false
	for _, c := range s.calls {
		tl.opCalls[c.op]++
		hasDefer = hasDefer || c.op == txn.OpReplaceDefer
		hasPrepare = hasPrepare || c.prepare
	}
	if hasDefer {
		tl.withDefer++
	}
	if len(s.calls) == 0 {
		tl.withoutWrites++
	}
	if hasPrepare {
		tl.withPrepare++
	}

	// The points reached.
	want := []string{}
	last := len(events) - 1
	if failAt >= 0 {
		last = failAt
	}
	for _, e := range events[:last+1] {
		want = append(want, e.point.String())
	}
	// A transaction that made no write has nothing to commit.
	if failAt < 0 && len(s.calls) > 0 {
		want = append(want, commitPointStep(s.decl.Name).String())
	}
	if got := rec.names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("points reached:\n got %v\nwant %v", got, want)
	}

	// Coverage counts what was reached, the points of the setup Recover included.
	counted := map[string]int{}
	for _, name := range append(recoverPoints, want...) {
		p, err := fsx.ParsePoint(name)
		if err != nil {
			t.Fatal(err)
		}
		counted[p.Key.String()]++
	}
	gotCov := map[string]int{}
	for _, c := range w.Coverage() {
		if c.Hits > 0 {
			gotCov[c.Key.Key.String()] = c.Hits
		}
	}
	if !reflect.DeepEqual(gotCov, counted) {
		t.Errorf("Coverage:\n got %v\nwant %v", gotCov, counted)
	}

	if failAt < 0 {
		tl.succeeded++
		for i, err := range errs {
			if err != nil {
				t.Fatalf("call %d returned %v", i, err)
			}
		}
		chk.durable("after Commit", snapshot(t, mem))
		for _, cs := range crashStates(mem, crashRNG, 3) {
			chk.durable("after Commit, "+cs.name, snapshot(t, cs.mem))
		}
		return
	}

	// A failed run: the class, the sticky error, and the state.
	tl.injected++
	fi, first := firstError(errs)
	if fi < 0 {
		t.Fatalf("the failure injected at %v did not surface", events[failAt].point)
	}
	if fi != events[failAt].call {
		t.Errorf("the error came from call %d, the failure was injected in call %d", fi, events[failAt].call)
	}
	for j := fi + 1; j < len(errs); j++ {
		if errs[j] != first {
			t.Errorf("call %d returned %v after the first error %v", j, errs[j], first)
		}
	}
	te := txnError(t, first)
	e := events[failAt]
	wantClass := fsx.Failed
	switch {
	case isFSFatal(e.op, e.step):
		wantClass = fsx.Fatal
		tl.fatalInFS++
	case committedBefore[failAt]:
		wantClass = fsx.Fatal
		tl.fatalAfterCommit++
	default:
		tl.failedClass++
	}
	if te.Class != wantClass {
		t.Errorf("failure at %v has class %v, want %v (commit step completed before it: %v)", e.point, te.Class, wantClass, committedBefore[failAt])
	}
	if txn.IsFatal(first) != (wantClass == fsx.Fatal) {
		t.Errorf("IsFatal = %v for class %v", txn.IsFatal(first), wantClass)
	}
	if te.Point != e.point || te.Tx != s.decl.Name {
		t.Errorf("error at %v of %q, want %v of %q", te.Point, te.Tx, e.point, s.decl.Name)
	}
	if !errors.Is(first, syscall.EIO) {
		t.Errorf("error %v does not wrap the system error", first)
	}
	if wantClass == fsx.Failed {
		// Nothing durable changed: what a Failed class promises.
		tl.failedWithNothingChanged++
		chk.beforeCommit("after the failure", snapshot(t, mem))
		for _, cs := range crashStates(mem, crashRNG, 2) {
			chk.beforeCommit("after the failure, "+cs.name, snapshot(t, cs.mem))
		}
	}
}
