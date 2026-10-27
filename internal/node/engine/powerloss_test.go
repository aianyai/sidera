package engine_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/reply"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The power is cut under a node that does what the property of closing has it do
// (sends, reads, acknowledgements through the agent and through the model's own turn,
// control signals, a target that cannot be reached, time that expires and feeds back,
// the sweep of the archive), and the node is started again on the disk the model of
// the file system leaves: some of what was written but not synced is there and some is
// not, by the draw of a seed. The cuts come at two kinds of moment: between two steps,
// when the node is quiet, and inside a transaction, before a step the draw picks, while
// a call to the node waits. The run goes on after each cut on the node that was started,
// so the next cut falls on a disk that earlier cuts have left, and at the end every
// record is closed and no sender was told a message was delivered that its target did
// not show whole, as in a run without cuts.
//
// After each cut, what the node answered before it is still true on the disk: a message
// it took from a sender, a page it handed out, a part it acknowledged. No file is half
// a record, no record has lost its body, no alert says a record was quarantined, the
// view the engine rebuilt equals what a scan of the disk builds, the recovery the start
// ran left nothing to recover, and a sender's record that says delivered has a target
// record that says it too.

// powerLossSeeds are the seeds of the runs: the draw of the steps and of the moments
// of the cuts follows from the seed, and so does the disk each cut leaves.
var powerLossSeeds = []int64{20261027, 20261028}

const (
	// powerLossSteps is how many random steps one run takes.
	powerLossSteps = 280
	// sweepEverySteps is how often the run lets the retention of records pass and the
	// sweep run.
	sweepEverySteps = 80
)

func TestPowerLossCloneEngineKeepsAcknowledgedWrites(t *testing.T) {
	totals := newPowerLossTotals()
	for _, seed := range powerLossSeeds {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			p := runPowerLoss(t, seed)
			totals.add(p)
		})
	}
	if t.Failed() {
		return
	}
	totals.judge(t)
}

func runPowerLoss(t *testing.T, seed int64) *powerLoss {
	p := newPowerLoss(t, seed)
	m := newModelRunWith(t, seed, harness.Options{Observer: p, ArchiveSweepEvery: time.Hour})
	p.attach(m)
	m.pathFloor = 1
	for i := range powerLossSteps {
		p.beginStep()
		p.runStep(i)
		p.endStep()
		switch {
		case i%sweepEverySteps == sweepEverySteps-1:
			p.sweepRetention()
		case p.rng.IntN(100) < 8:
			p.cutBetweenSteps()
		}
	}
	p.disarm()
	m.finish()
	m.judge()
	p.judgeOnDisk("at the end of the run", nil)
	p.report()
	return p
}

// ackedLog is what the node answered as done, in the order it was answered, as the
// agents saw it: a message it took from a sender (the answer to a send or a control
// signal), a page it handed out, a part it acknowledged. An entry is made only after the
// call returned, so everything in the log was durable before the cut that follows it.
type ackedLog struct {
	mu    sync.Mutex
	facts []fact
	// known is every message the log ever held, kept when the log is cleared.
	known map[string]bool
}

type factKind int

const (
	factSent factKind = iota + 1
	factHandedOut
	factAcked
)

type fact struct {
	kind  factKind
	who   ident.EndpointID // the sender of a sent message, the endpoint of a page or an acknowledgement
	msg   delivery.MessageID
	lease string
	part  int
}

func (l *ackedLog) add(fs ...fact) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.facts = append(l.facts, fs...)
}

func (l *ackedLog) sent(from ident.EndpointID, id adapterproto.MessageID) {
	l.mu.Lock()
	if l.known == nil {
		l.known = map[string]bool{}
	}
	l.known[string(id)] = true
	l.mu.Unlock()
	l.add(fact{kind: factSent, who: from, msg: delivery.MessageID(id)})
}

func (l *ackedLog) handedOut(who ident.EndpointID, page adapterproto.RecvPage) {
	var fs []fact
	for _, pm := range page.Messages {
		fs = append(fs, fact{kind: factHandedOut, who: who, msg: delivery.MessageID(pm.ID), lease: string(page.Lease)})
	}
	l.add(fs...)
}

func (l *ackedLog) acknowledged(who ident.EndpointID, items []adapterproto.PartAck) {
	var fs []fact
	for _, it := range items {
		switch it.Ack {
		case adapterproto.ItemRecorded, adapterproto.ItemCompleted, adapterproto.ItemDuplicate:
			fs = append(fs, fact{kind: factAcked, who: who, msg: delivery.MessageID(it.MessageID), part: it.Part})
		}
	}
	l.add(fs...)
}

// snapshot is the log as it is now. Later entries do not change it.
func (l *ackedLog) snapshot() []fact {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.facts[:len(l.facts):len(l.facts)]
}

// clear forgets what was answered, for the records that the retention then takes out of
// the live files by right.
func (l *ackedLog) clear() {
	l.mu.Lock()
	l.facts = nil
	l.mu.Unlock()
}

func (l *ackedLog) isKnown(id delivery.MessageID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.known[string(id)]
}

func (l *ackedLog) markKnown(id delivery.MessageID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.known == nil {
		l.known = map[string]bool{}
	}
	l.known[string(id)] = true
}

// The classes of transaction a cut can be aimed at, by the transactions of each, and
// the classes of step. A cut is taken before a step, so a class of step is the
// instant the disk has when that step is about to run.
var powerLossClasses = map[string][]string{
	"send":     {"intake"},
	"intake":   {"deliver"},
	"control":  {"control-intake", "control-result", "control-reply"},
	"recv":     {"lease"},
	"ack":      {"ack"},
	"feedback": {"feedback-form", "feedback-clear"},
	"time":     {"tick"},
	"prompt":   {"prompt-begin", "prompt-end"},
	"sweep":    {"archive-sweep", "archive-summaries", "archive-expire"},
}

// anyClass aims a step at whichever class of transaction or class of step has been cut
// at least, and sweepClass at the sweep alone, which only the sweep step reaches.
const (
	anyClass   = "*"
	sweepClass = "sweep"
	// cutQuota is how many cuts a class of transaction and a class of step are given
	// before the steps stop aiming at them: the run cuts where it has cut least, so the
	// rare moments are reached as often as the common ones.
	cutQuota = 8
)

// stepClassOf names the class of each step of a write.
var stepClassOf = map[fsx.Step]string{
	fsx.StepCreate: "write", fsx.StepOpen: "write", fsx.StepWrite: "write",
	fsx.StepSync:   "sync",
	fsx.StepRename: "rename", fsx.StepRenameDir: "rename",
	fsx.StepSyncDir: "sync-dir",
	fsx.StepRemove:  "remove", fsx.StepRemoveAll: "remove",
	fsx.StepMkdir:  "write",
	fsx.StepCommit: "commit",
}

func classOfTx(tx string) string {
	for class, txs := range powerLossClasses {
		if slices.Contains(txs, tx) {
			return class
		}
	}
	return ""
}

// powerLoss is the observer of every write step of the node, which cuts the power at
// the step it has been aimed at, and what the run needs to judge the disks the cuts
// leave.
type powerLoss struct {
	t    *testing.T
	seed int64
	rng  *rand.Rand
	h    *harness.H
	m    *modelRun

	// waiting is set while the test waits for a call, the node's own or the wait for it
	// to settle, and request is the cut the observer asks the waiting test to make. The
	// power is cut only there, by the test's own goroutine: the test touches nothing of
	// the fixture meanwhile, and the fixture is not used by two goroutines at once.
	waiting  bool
	request  *cutRequest
	wake     chan struct{}
	crashing atomic.Bool
	cuts     atomic.Int32
	aimed    atomic.Pointer[string] // the class a step is aimed at, nil for none
	skip     atomic.Int32           // how many matching steps pass before the cut

	mu       sync.Mutex
	reached  map[txn.Key]int // every step the processes of the run reached, by key
	pending  []cut
	flight   ident.EndpointID // the sender of a send the test waits on
	fired    map[string]int   // cuts inside a transaction so far, by class of transaction and by class of step ("step " prefix)
	stepCuts int32            // the number of cuts when the step began

	// what the run reached, for the assertions at the end
	moments   map[string]int // cuts inside a transaction, by class of transaction
	stepsAt   map[string]int // cuts inside a transaction, by class of step
	between   int            // cuts between two steps
	aimMissed map[string]int // steps aimed at a class that never came
	inFlight  map[string]int // the sends a cut interrupted: "whole" when the record is on the disk, "absent" when not
	judged    int            // disks judged
	checked   map[string]int // facts checked, by kind
}

// cutRequest is a cut the observer asks for; the observer waits until it was made.
type cutRequest struct {
	c        cut
	released chan struct{}
}

// cut is a power cut that has not been judged yet.
type cut struct {
	facts []fact
	class string
	step  string
	where string
	from  ident.EndpointID // the sender of the send the cut interrupted
}

func newPowerLoss(t *testing.T, seed int64) *powerLoss {
	return &powerLoss{
		t: t, seed: seed, rng: rand.New(rand.NewPCG(uint64(seed), 0x5ce)),
		wake: make(chan struct{}, 1), fired: map[string]int{}, reached: map[txn.Key]int{}, moments: map[string]int{}, stepsAt: map[string]int{}, aimMissed: map[string]int{},
		inFlight: map[string]int{}, checked: map[string]int{},
	}
}

func (p *powerLoss) attach(m *modelRun) {
	p.m, p.h = m, m.h
	m.pl = p
}

// Reached is told every step of every process before it runs. The step that the run
// aimed at cuts the power: the disk is taken before the step, the old process is
// abandoned and a new one starts on it, and the old loop goes on, cut off from its disk,
// as a process that was killed at that instant would not.
func (p *powerLoss) Reached(pt fsx.Point, kind txn.Kind) {
	p.mu.Lock()
	p.reached[txn.Key{Key: pt.Key, Kind: kind}]++
	p.mu.Unlock()
	if kind != txn.Normal || p.crashing.Load() {
		return
	}
	aim := p.aimed.Load()
	class, step := classOfTx(pt.Tag.Tx), "step "+stepClassOf[pt.Step]
	if aim == nil || class == "" || (*aim != anyClass && *aim != class) {
		return
	}
	p.mu.Lock()
	if !p.waiting || (*aim == anyClass && !p.neededLocked(class, step)) {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	if p.skip.Add(-1) >= 0 {
		return
	}
	if !p.aimed.CompareAndSwap(aim, nil) {
		return
	}
	p.mu.Lock()
	if !p.waiting { // the call returned meanwhile: nobody is there to cut the power
		p.mu.Unlock()
		p.aimed.CompareAndSwap(nil, aim)
		return
	}
	p.fired[class]++
	p.fired[step]++
	req := &cutRequest{
		c:        cut{facts: p.m.acked.snapshot(), class: class, step: stepClassOf[pt.Step], where: pt.Key.String(), from: p.flight},
		released: make(chan struct{}),
	}
	p.request = req
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
	<-req.released
}

// neededLocked says whether a cut inside a transaction of the class, before a step of
// the class of step, is wanted: the classes of transaction and of step that have been cut
// at least are served first, so the rare ones are reached as often as the common ones.
// The caller holds mu.
func (p *powerLoss) neededLocked(class, step string) bool {
	least := func(keys []string) int {
		m := cutQuota
		for _, k := range keys {
			m = min(m, p.fired[k])
		}
		return m
	}
	var classes, steps []string
	for c := range powerLossClasses {
		if c != sweepClass {
			classes = append(classes, c)
		}
	}
	for _, s := range stepClassOf {
		steps = append(steps, "step "+s)
	}
	return (p.fired[class] < cutQuota && p.fired[class] <= least(classes)+1) || (p.fired[step] < cutQuota && p.fired[step] <= least(steps)+1)
}

// call waits for f, which calls the node. The cut that the observer asks for while it
// waits is made here: the old loop is held in the observer until the new process runs.
// A cut asked for as the call returns comes after it, when the call did not fail by it.
func (p *powerLoss) call(f func()) {
	done := make(chan struct{})
	p.mu.Lock()
	p.waiting = true
	p.mu.Unlock()
	go func() {
		defer close(done)
		f()
	}()
	for {
		select {
		case <-done:
			p.mu.Lock()
			p.waiting = false
			req := p.request
			p.request = nil
			p.mu.Unlock()
			p.serve(req, true)
			return
		case <-p.wake:
			p.mu.Lock()
			req := p.request
			p.request = nil
			p.mu.Unlock()
			p.serve(req, false)
		}
	}
}

// serve makes the cut a request asks for and lets the observer go on. A call that had
// returned before the cut interrupted nothing.
func (p *powerLoss) serve(req *cutRequest, callDone bool) {
	if req == nil {
		return
	}
	if callDone {
		req.c.from = ident.EndpointID{}
	}
	p.cut(req.c)
	close(req.released)
}

// cut cuts the power: a power loss whose seed is the number of the cut, and a start on
// the disk it leaves. The cut is judged later, when the node has settled.
func (p *powerLoss) cut(c cut) {
	p.crashing.Store(true)
	defer p.crashing.Store(false)
	n := p.cuts.Add(1)
	p.h.Crash(harness.PowerLoss(uint64(p.seed) + uint64(n)).At(c.where))
	p.mu.Lock()
	p.pending = append(p.pending, c)
	p.mu.Unlock()
}

// cutBetweenSteps cuts the power while the node is quiet.
func (p *powerLoss) cutBetweenSteps() {
	p.cut(cut{facts: p.m.acked.snapshot(), class: "between steps", where: "between two steps"})
	p.mu.Lock()
	p.between++
	p.mu.Unlock()
	p.quiet()
	p.judgePending()
}

func (p *powerLoss) beginStep() {
	p.stepCuts = p.cuts.Load()
	if p.rng.IntN(100) < 70 {
		p.aim(anyClass)
	}
}

func (p *powerLoss) aim(class string) {
	p.skip.Store(int32(p.rng.IntN(8)))
	p.aimed.Store(&class)
}

func (p *powerLoss) disarm() { p.aimed.Store(nil) }

func (p *powerLoss) endStep() {
	if aim := p.aimed.Swap(nil); aim != nil {
		p.mu.Lock()
		p.aimMissed[*aim]++
		p.mu.Unlock()
	}
	p.mu.Lock()
	n := len(p.pending)
	p.mu.Unlock()
	if n > 0 {
		p.quiet()
		p.judgePending()
	}
}

// cutDuringStep says whether the power was cut since the step began.
func (p *powerLoss) cutDuringStep() bool { return p.cuts.Load() != p.stepCuts }

// runStep runs a step, which ends early when the cut interrupted a call it was
// making.
func (p *powerLoss) runStep(i int) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(stepInterrupted); !ok {
				panic(r)
			}
		}
	}()
	p.m.step(i)
}

// flightStart notes the sender of a send the test waits on, flightEnd that it returned.
func (p *powerLoss) flightStart(from ident.EndpointID) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.flight = from
	p.mu.Unlock()
}

func (p *powerLoss) flightEnd() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.flight = ident.EndpointID{}
	p.mu.Unlock()
}

// quiet waits for the node to settle, in rounds short enough for the cut the observer
// asks for to be made between them: while the old loop is held in the observer the node
// cannot settle, and a wait that did not end would never let the test cut. A cut ends the
// round, and the wait goes on with the new process.
func (p *powerLoss) quiet() {
	t := p.t
	t.Helper()
	p.mu.Lock()
	p.waiting = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.waiting = false
		req := p.request
		p.request = nil
		p.mu.Unlock()
		p.serve(req, true)
	}()
	deadline := time.Now().Add(90 * time.Second)
	for {
		before := p.cuts.Load()
		err := p.h.Quiesce(scenario.WithTimeout(120 * time.Millisecond))
		p.mu.Lock()
		req := p.request
		p.request = nil
		p.mu.Unlock()
		p.serve(req, false)
		if err == nil && p.cuts.Load() == before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the node did not settle after the cuts of the power: %v", err)
		}
	}
}

// sweepRetention lets the retention of closed records pass, a pass of the sweep at a
// time, with the power cut in the middle of the sweep. The records it takes out of the
// live files go by right, so what was answered before is no longer asked for.
func (p *powerLoss) sweepRetention() {
	p.quiet()
	p.judgePending()
	// Every report_by passes first: the property of closing holds here too, and it is
	// judged before the messages it covers leave the live files.
	p.m.finish()
	p.m.down = false
	p.judgePending()
	p.m.judgeClosed()
	p.m.acked.clear()
	p.m.newControlGen(p.seed)
	p.stepCuts = p.cuts.Load()
	p.aim(sweepClass)
	for range 2 {
		p.h.Advance(3*24*time.Hour + time.Minute)
		p.quiet()
		for range 3 {
			p.h.Advance(time.Hour)
			p.quiet()
		}
	}
	p.disarm()
	p.quiet()
	p.judgePending()
}

// judgePending judges every cut made since the last time.
func (p *powerLoss) judgePending() {
	p.mu.Lock()
	cuts := p.pending
	p.pending = nil
	p.mu.Unlock()
	for _, c := range cuts {
		p.mu.Lock()
		if c.class != "between steps" {
			p.moments[c.class]++
			p.stepsAt[c.step]++
		}
		p.mu.Unlock()
		p.judgeOnDisk(c.where, &c)
	}
}

// judgeOnDisk holds the node, settled after a cut, to what the cut must not have taken.
func (p *powerLoss) judgeOnDisk(where string, c *cut) {
	t, h := p.t, p.h
	t.Helper()
	sc := scanOf(t, h)
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf("after the power was cut %s (cut %d): "+format, append([]any{where, p.cuts.Load()}, args...)...)
	}

	// Nothing half written: every file decodes, every record has the body it names, and
	// nothing was quarantined, which only a file that was written in part can need.
	if len(sc.Damaged) != 0 {
		fail("damaged files: %v", sc.Damaged)
	}
	if len(sc.Unknown) != 0 {
		fail("files that are no record: %v", sc.Unknown)
	}
	bodies := map[string]bool{}
	for _, b := range sc.Bodies {
		bodies[b] = true
	}
	for _, in := range sc.In {
		if in.Body != nil && !bodies[store.BodyFileName(in.Name())] {
			fail("target record %s has no body", in.Name())
		}
	}
	for _, o := range sc.Out {
		if o.Body != nil && !bodies[store.BodyFileName(o.Name())] {
			fail("source record %s has no body", o.Name())
		}
	}
	for _, a := range sc.Alerts {
		if a.Kind == "corrupt_record" || a.Kind == "corrupt_body" {
			fail("the node quarantined a file: %s %s", a.Kind, a.Detail)
		}
	}

	// What the recovery of the start found to do it did, and nothing is left to do.
	if plan := engine.PlanRecovery(sc, h.Clock().Now(), func(string) bool { return true }); !plan.Empty() {
		fail("recovery work is left after the node settled: %v", plan.Items())
	}
	if left, err := txn.CleanupWork(h.Disk()); err != nil || len(left) != 0 {
		fail("leftovers of interrupted writes remain: %v %v", left, err)
	}

	// The view the engine rebuilt from the disk is the view a scan builds.
	if diff, err := engine.ViewDiffersFromDisk(h.Ctx(), h.Engine(), sc); err != nil || len(diff) != 0 {
		fail("the view of the engine differs from the disk: %v %v", diff, err)
	}

	p.judgeFloor(sc, fail)
	if c != nil {
		p.judgeFacts(sc, c, fail)
	}
	p.mu.Lock()
	p.judged++
	p.mu.Unlock()
}

// judgeFloor: a source record that says delivered has a target record that says it. A
// sender is told what its record says, so a target that lost the delivery while the
// source kept it would be a message that was reported delivered and was not.
func (p *powerLoss) judgeFloor(sc *store.Scan, fail func(string, ...any)) {
	node := harness.NodeID()
	ins := map[store.TargetKey]delivery.Record{}
	for _, in := range sc.In {
		ins[in.Key] = in.Record
	}
	ctls := map[store.TargetKey]delivery.Record{}
	for _, c := range sc.Ctl {
		ctls[c.Key] = c.Record
	}
	now := p.h.Clock().Now()
	for _, o := range sc.Out {
		if o.Lost != nil {
			continue
		}
		for _, tr := range o.Outbound.Recipients {
			if tr.Delivery.State != delivery.StateDelivered || tr.Address.Node != node {
				continue
			}
			k := store.TargetKey{SenderNode: node, Sender: o.Key.Sender, Message: o.Key.Message, Recipient: tr.Address.Endpoint}
			target, ok := ins[k]
			if o.Key.Control {
				target, ok = ctls[k]
			}
			switch {
			case !ok && !now.Before(reply.RetainFactsUntil(tr.Delivery.ReportBy)):
				// The target's record may have served its retention first: the sweep keeps
				// a record until the latest of several terms, of which this is the least.
			case !ok:
				fail("the source says message %s is delivered to %s and the target holds no record of it", o.Key.Message, tr.Address)
			case target.State != delivery.StateDelivered:
				fail("the source says message %s is delivered to %s and the target's record is %s", o.Key.Message, tr.Address, target.State)
			}
			p.mu.Lock()
			p.checked["source delivered"]++
			p.mu.Unlock()
		}
	}
}

// judgeFacts: everything the node answered before the cut is on the disk.
func (p *powerLoss) judgeFacts(sc *store.Scan, c *cut, fail func(string, ...any)) {
	outs := map[delivery.MessageID][]store.OutRecord{}
	for _, o := range sc.Out {
		outs[o.Key.Message] = append(outs[o.Key.Message], o)
	}
	type target struct {
		who ident.EndpointID
		msg delivery.MessageID
	}
	ins := map[target]delivery.Record{}
	for _, in := range sc.In {
		ins[target{in.Key.Recipient, in.Key.Message}] = in.Record
	}
	count := func(kind string) {
		p.mu.Lock()
		p.checked[kind]++
		p.mu.Unlock()
	}
	for _, f := range c.facts {
		switch f.kind {
		case factSent:
			count("sent")
			if !slices.ContainsFunc(outs[f.msg], func(o store.OutRecord) bool { return o.Key.Sender == f.who }) {
				fail("message %s from %s was taken from the sender and the node holds no source record of it", f.msg, f.who)
			}
		case factHandedOut:
			count("handed out")
			r, ok := ins[target{f.who, f.msg}]
			switch {
			case !ok:
				fail("message %s was handed out to %s and the node holds no record of it", f.msg, f.who)
			case !slices.ContainsFunc(r.Lease.IDs, func(id delivery.LeaseID) bool { return id.String() == f.lease }):
				fail("message %s was handed out to %s on page %s and the record does not remember it (%d leases)", f.msg, f.who, f.lease, r.Lease.Handouts)
			}
		case factAcked:
			count("acknowledged")
			r, ok := ins[target{f.who, f.msg}]
			switch {
			case !ok:
				fail("part %d of message %s was acknowledged by %s and the node holds no record of it", f.part, f.msg, f.who)
			case r.State == delivery.StateDelivered:
			case f.part < 1 || f.part > len(r.Lease.Acked) || !r.Lease.Acked[f.part-1]:
				fail("part %d of message %s was acknowledged by %s and the record is %s with parts acknowledged %v", f.part, f.msg, f.who, r.State, r.Lease.Acked)
			}
		}
	}

	// A send the cut interrupted is whole or absent: its source record is there with its
	// body (judged above) or there is none.
	if c.from.IsZero() {
		return
	}
	extra := 0
	for _, o := range sc.Out {
		if o.Key.Sender == c.from && !p.m.acked.isKnown(o.Key.Message) {
			extra++
			p.m.acked.markKnown(o.Key.Message)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if extra > 0 {
		p.inFlight["whole"]++
	} else {
		p.inFlight["absent"]++
	}
}

func (p *powerLoss) report() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.t.Logf("seed %d: %d power cuts (%d between steps), %d disks judged; cuts inside a transaction by class %v and by step %v; steps aimed at a class that did not come %v; sends the cut interrupted %v; facts checked %v",
		p.seed, p.cuts.Load(), p.between, p.judged, p.moments, p.stepsAt, p.aimMissed, p.inFlight, p.checked)
}

// powerLossTotals add up what the runs reached.
type powerLossTotals struct {
	cuts, between, judged int
	moments, stepsAt      map[string]int
	inFlight, checked     map[string]int
	reached               map[txn.Key]int
	control               map[string]int
}

func newPowerLossTotals() *powerLossTotals {
	return &powerLossTotals{moments: map[string]int{}, stepsAt: map[string]int{}, inFlight: map[string]int{}, checked: map[string]int{}, reached: map[txn.Key]int{}, control: map[string]int{}}
}

func (a *powerLossTotals) add(p *powerLoss) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a.cuts += int(p.cuts.Load())
	a.between += p.between
	a.judged += p.judged
	for k, n := range p.moments {
		a.moments[k] += n
	}
	for k, n := range p.stepsAt {
		a.stepsAt[k] += n
	}
	for k, n := range p.inFlight {
		a.inFlight[k] += n
	}
	for k, n := range p.checked {
		a.checked[k] += n
	}
	for k, n := range p.reached {
		a.reached[k] += n
	}
	for k, n := range p.m.controlPaths() {
		a.control[k] += n
	}
}

// judge holds the runs to the coverage they claim: every class of transaction and every
// class of step was cut at, in the middle of a transaction and between two steps, a cut
// left a send whole and a cut left one absent, and every kind of what the node answered
// was found on a disk. A run that stopped reaching one of them would still pass and prove
// less.
func (a *powerLossTotals) judge(t *testing.T) {
	t.Helper()
	t.Logf("%d power cuts (%d between steps), %d disks judged; by class %v; by step %v; sends interrupted %v; facts checked %v",
		a.cuts, a.between, a.judged, a.moments, a.stepsAt, a.inFlight, a.checked)
	const perClass = 3
	for class := range powerLossClasses {
		if a.moments[class] < perClass {
			t.Errorf("the power was cut %d times inside a transaction of class %q, want at least %d", a.moments[class], class, perClass)
		}
	}
	for _, step := range []string{"write", "sync", "rename", "sync-dir", "remove", "commit"} {
		if a.stepsAt[step] < perClass {
			t.Errorf("the power was cut %d times before a step of class %q, want at least %d", a.stepsAt[step], step, perClass)
		}
	}
	if a.between < 10 {
		t.Errorf("the power was cut %d times between two steps, want at least 10", a.between)
	}
	for _, outcome := range []string{"whole", "absent"} {
		if a.inFlight[outcome] == 0 {
			t.Errorf("no send that a cut interrupted was found %s", outcome)
		}
	}
	for _, kind := range []string{"sent", "handed out", "acknowledged", "source delivered"} {
		if a.checked[kind] < 50 {
			t.Errorf("%d facts of kind %q were found on a disk, want at least 50", a.checked[kind], kind)
		}
	}
	for _, path := range harness.ControlPaths {
		if a.control[path] == 0 {
			t.Errorf("the control path %s was never taken", path)
		}
	}
	a.judgeTable(t)
}

// engineWalks are the transactions the engine itself declares, and the walk that crashes
// at every point of each. The run cuts the power inside the transactions of the store
// that the traffic of a node reaches; the others are the walks' to cover.
var engineWalks = map[string]string{
	"direct-register":         "TestDirectRegisterRecoversFromACrashAtEveryPoint",
	"runtime-admit":           "TestRuntimeAdmitRecoversFromACrashAtEveryPoint",
	"adapter-install":         "TestAnInstallationRecoversFromACrashAtEveryPoint",
	"adapter-install-current": "TestAnInstallationRecoversFromACrashAtEveryPoint",
	"backup-confirm":          "TestTheOwnersOperationsRecoverFromACrashAtEveryPoint",
	"clock-clear":             "TestTheOwnersOperationsRecoverFromACrashAtEveryPoint",
	"identity-retire":         "TestTheOwnersOperationsRecoverFromACrashAtEveryPoint",
}

// judgeTable reads the transactions of the program from its table and says who crashes
// at each: this run, for the transactions of the store that it names; the walks of the
// owner of the others, which each assert that they reached every point of their own
// table. A transaction that is declared and has none of these is one nobody has decided
// how to test, and a transaction this run names that the program no longer declares is a
// run that stopped reaching what it claims.
func (a *powerLossTotals) judgeTable(t *testing.T) {
	t.Helper()
	tab := harness.ProgramTable(t)
	declared := map[string]bool{}
	for _, k := range tab.Keys() {
		declared[k.Tag.Tx] = true
	}
	owned := map[string]string{}
	own := func(owner string, decls []*txn.Decl) {
		for _, d := range decls {
			owned[d.Name] = owner
		}
	}
	own("txn", txn.Internal())
	own("store", store.Transactions())
	own("config", config.Transactions())
	own("keys", keys.Transactions())
	own("engine", engine.Transactions())
	own("daemon", daemon.Transactions())

	for tx := range declared {
		if _, ok := owned[tx]; !ok {
			t.Errorf("transaction %q of the program has no owner among the packages that declare transactions", tx)
		}
	}
	for tx, walk := range engineWalks {
		if owned[tx] != "engine" {
			t.Errorf("transaction %q is listed with the walk %s and the engine does not declare it", tx, walk)
		}
	}
	for tx, owner := range owned {
		if owner == "engine" && declared[tx] {
			if _, ok := engineWalks[tx]; !ok {
				t.Errorf("transaction %q of the engine has no walk listed", tx)
			}
		}
	}

	commits := map[string]int{}
	for k, n := range a.reached {
		if k.Kind == txn.Normal && k.Step == fsx.StepCommit {
			commits[k.Tag.Tx] += n
		}
	}
	var cutAt []string
	for class, txs := range powerLossClasses {
		reached := 0
		for _, tx := range txs {
			cutAt = append(cutAt, tx)
			if owned[tx] != "store" {
				t.Errorf("class %q of the run names %q, which the store does not declare", class, tx)
			}
			if commits[tx] > 0 {
				reached++
			}
		}
		if reached == 0 {
			t.Errorf("no transaction of class %q was run to its commit", class)
		}
	}
	var unreached []string
	for _, tx := range cutAt {
		if commits[tx] == 0 {
			unreached = append(unreached, tx)
		}
	}
	sort.Strings(unreached)
	t.Logf("transactions of the program: %d; cut at by the run: %d, of which none ran to a commit: %v", len(declared), len(cutAt), unreached)
}
