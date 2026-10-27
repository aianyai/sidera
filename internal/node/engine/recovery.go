package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/store"
)

// RecoveryPlan is the work of the recovery rules of the scan entry, as a pure
// function of a scan: what each rule has to do. The scan entry executes it, and
// a test that a disk no crash and no damage touched has no work reads the same
// plan, so the check cannot drift from the rules.
type RecoveryPlan struct {
	// Quarantine is the damaged files.
	Quarantine []store.DamagedFile
	// CtlEffectReplay are the endpoints whose control record says more than they
	// do.
	CtlEffectReplay []store.EndpointRecord
	// DerivedReplay are the control records whose delivery of an initial message a
	// crash left unwritten.
	DerivedReplay []store.CtlRecord
	// InstanceDrain are the endpoints of instances the owner disabled, still not
	// closed.
	InstanceDrain []store.EndpointRecord
	// OrphanEndpoints are session endpoints that were being created and that no
	// control record creates.
	OrphanEndpoints []store.EndpointRecord
	// EndpointDrain is the closing of the records queued for closed endpoints.
	EndpointDrain store.DrainBatch
	// CheckpointDrop is the state kept for sessions that are closed.
	CheckpointDrop []store.CheckpointKey
	// FeedbackReplay is the source records still holding feedback that a notice
	// covers.
	FeedbackReplay []store.OutRecord
	// OutLostReplay are the out-lost records whose item a notice already covers.
	OutLostReplay []store.OutLostRecord
	// OrphanBodies are the bodies no record refers to.
	OrphanBodies []string
}

// PlanRecovery works out the work of the recovery rules on a scan. enabled says
// whether the owner has an adapter instance enabled; now is the time at which the
// delivery rules judge the records.
func PlanRecovery(sc *store.Scan, now time.Time, enabled func(instance string) bool) RecoveryPlan {
	p := RecoveryPlan{
		Quarantine:      sc.Damaged,
		CtlEffectReplay: store.EffectReplays(sc),
		DerivedReplay:   store.DerivedReplays(sc),
		CheckpointDrop:  store.ClosedSessionCheckpoints(sc),
		InstanceDrain:   store.InstanceDrainWork(sc, enabled),
		OrphanEndpoints: store.OrphanEndpoints(sc),
		FeedbackReplay:  store.FeedbackReplays(sc),
		OutLostReplay:   store.OutLostReplays(sc),
		OrphanBodies:    store.OrphanBodies(sc),
	}
	ins, ctls := store.DrainCandidates(sc)
	p.EndpointDrain, _ = closeBatch(now, ins, ctls)
	return p
}

// Items names the work, one line for each thing a rule has to do, in the form
// "rule: what". An empty list is a plan with nothing to do.
func (p RecoveryPlan) Items() []string {
	var out []string
	add := func(rule, what string) { out = append(out, rule+": "+what) }
	for _, d := range p.Quarantine {
		add("quarantine", d.Dir+"/"+d.Name)
	}
	for _, ep := range p.CtlEffectReplay {
		add("ctl-effect-replay", ep.ID.String())
	}
	for _, c := range p.DerivedReplay {
		add("derived-replay", c.Key.String())
	}
	for _, ep := range p.InstanceDrain {
		add("instance-drain-replay", ep.ID.String())
	}
	for _, ep := range p.OrphanEndpoints {
		add("orphan-gc-endpoint", ep.ID.String())
	}
	for _, in := range p.EndpointDrain.In {
		add("endpoint-drain-replay", in.Key.String())
	}
	for _, c := range p.EndpointDrain.Ctl {
		add("endpoint-drain-replay", c.Key.String())
	}
	for _, k := range p.CheckpointDrop {
		add("checkpoint-drop", k.Scope+"/"+k.ID)
	}
	for _, o := range p.FeedbackReplay {
		add("feedback-replay", o.Name())
	}
	for _, o := range p.OutLostReplay {
		add("feedback-replay", o.Name())
	}
	for _, b := range p.OrphanBodies {
		add("orphan-gc", b)
	}
	return out
}

// Empty reports whether no rule has anything to do.
func (p RecoveryPlan) Empty() bool { return len(p.Items()) == 0 }

// ScanReport says what the scan entry did.
type ScanReport struct {
	// Passes is how many times the rules were run over a fresh scan; one when
	// there was nothing to do.
	Passes int
	// Rules is how many times each rule that had work was run.
	Rules map[string]int
	// Order is the rules in the order they ran, one entry for each run.
	Order []string
	// Endpoints is how many endpoints the node has that are open or being created.
	Endpoints int
	// Unfinished is how many deliveries the node holds that still have work behind
	// them: messages waiting to be handed out, accepted control signals and sent
	// messages that no receipt answered.
	Unfinished int
}

// maxRecoveryPasses is how many times the rules are run over a fresh scan before
// the entry gives up: a recovery that does not settle is a defect.
const maxRecoveryPasses = 12

// Open is the scan entry, the sixth step of the start. It runs on the caller's
// goroutine before the loop exists: it scans state/, runs the recovery rules in
// the order the store fixes until none has work, and builds the view. Nothing is
// asked of any adapter and no socket is open yet, so it is not cut in chunks.
//
// The caller has made the transaction layer recover and the directories of
// state/ exist: the rules read what is durable only after that.
func Open(ctx context.Context, d Deps, p Params) (*Engine, ScanReport, error) {
	rep := ScanReport{Rules: map[string]int{}}
	e, err := newEngine(d, p.withDefaults())
	if err != nil {
		return nil, rep, err
	}
	enabled := func(instance string) bool {
		in, ok := d.Config.Instances().Items[instance]
		return ok && in.Enabled
	}
	for rep.Passes = 1; ; rep.Passes++ {
		if rep.Passes > maxRecoveryPasses {
			return nil, rep, fmt.Errorf("engine: the recovery rules left work after %d passes", maxRecoveryPasses)
		}
		if err := ctx.Err(); err != nil {
			return nil, rep, err
		}
		sc, err := d.Store.Scan()
		if err != nil {
			return nil, rep, err
		}
		plan := PlanRecovery(sc, d.Clock.Now(), enabled)
		if plan.Empty() {
			if e.view, err = buildView(d.Node, sc); err != nil {
				return nil, rep, err
			}
			e.view.addConfigResults(d.Config.LastOperations())
			if err := e.loadFeedback(sc, d.Clock.Now()); err != nil {
				return nil, rep, err
			}
			e.watchTicks()
			rep.Endpoints, rep.Unfinished = e.view.OpenEndpoints(), e.view.Unfinished()
			return e, rep, nil
		}
		if err := e.runRules(ctx, sc, plan, enabled, &rep); err != nil {
			return nil, rep, err
		}
	}
}

// runRules runs the rules that have work, in the order of the store's
// guarantee. After a rule has written, the plan is worked out again from a fresh
// scan, because each rule reads what the one before it left. There is no view
// yet to keep in step (it is built from the last scan), so the records a write
// returns are not used here.
func (e *Engine) runRules(ctx context.Context, sc *store.Scan, plan RecoveryPlan, enabled func(string) bool, rep *ScanReport) error {
	steps := []struct {
		rule string
		work func(RecoveryPlan) int
		do   func(sc *store.Scan, plan RecoveryPlan, now time.Time) error
	}{
		{"quarantine", func(p RecoveryPlan) int { return len(p.Quarantine) },
			func(_ *store.Scan, p RecoveryPlan, now time.Time) error { return e.quarantineAll(now, p.Quarantine) }},
		{"ctl-effect-replay", func(p RecoveryPlan) int { return len(p.CtlEffectReplay) },
			func(_ *store.Scan, p RecoveryPlan, _ time.Time) error {
				return e.d.Store.CtlEffectReplay(p.CtlEffectReplay)
			}},
		{"derived-replay", func(p RecoveryPlan) int { return len(p.DerivedReplay) },
			func(_ *store.Scan, p RecoveryPlan, _ time.Time) error {
				ins := make([]store.InRecord, len(p.DerivedReplay))
				for i, c := range p.DerivedReplay {
					d, err := derivedDelivery(c)
					if err != nil {
						return err
					}
					ins[i] = d
				}
				return e.d.Store.DerivedReplay(ins)
			}},
		{"instance-drain-replay", func(p RecoveryPlan) int { return len(p.InstanceDrain) },
			func(sc *store.Scan, p RecoveryPlan, now time.Time) error {
				return e.d.Store.InstanceDrainReplay(closedRecords(p.InstanceDrain), instanceDrainBatch(sc, p.InstanceDrain, now))
			}},
		{"orphan-gc-endpoint", func(p RecoveryPlan) int { return len(p.OrphanEndpoints) },
			func(_ *store.Scan, p RecoveryPlan, _ time.Time) error {
				return e.d.Store.OrphanGCEndpoint(p.OrphanEndpoints)
			}},
		{"endpoint-drain-replay", func(p RecoveryPlan) int { return len(p.EndpointDrain.In) + len(p.EndpointDrain.Ctl) },
			func(_ *store.Scan, p RecoveryPlan, _ time.Time) error {
				return e.d.Store.EndpointDrainReplay(p.EndpointDrain)
			}},
		{"checkpoint-drop", func(p RecoveryPlan) int { return len(p.CheckpointDrop) },
			func(_ *store.Scan, p RecoveryPlan, _ time.Time) error {
				return e.d.Store.CheckpointDrop(p.CheckpointDrop)
			}},
		{"feedback-replay", func(p RecoveryPlan) int { return len(p.FeedbackReplay) + len(p.OutLostReplay) },
			func(_ *store.Scan, p RecoveryPlan, _ time.Time) error {
				if len(p.FeedbackReplay) > 0 {
					if err := e.d.Store.FeedbackReplay(p.FeedbackReplay); err != nil {
						return err
					}
				}
				if len(p.OutLostReplay) > 0 {
					return e.d.Store.OutLostClear(p.OutLostReplay)
				}
				return nil
			}},
		{"orphan-gc", func(p RecoveryPlan) int { return len(p.OrphanBodies) },
			func(_ *store.Scan, p RecoveryPlan, _ time.Time) error { return e.d.Store.OrphanGC(p.OrphanBodies) }},
	}
	for _, s := range steps {
		if s.work(plan) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		now := e.d.Clock.Now()
		if err := s.do(sc, plan, now); err != nil {
			return fmt.Errorf("engine: recovery rule %s: %w", s.rule, err)
		}
		rep.Rules[s.rule]++
		rep.Order = append(rep.Order, s.rule)
		var err error
		if sc, err = e.d.Store.Scan(); err != nil {
			return err
		}
		plan = PlanRecovery(sc, e.d.Clock.Now(), enabled)
	}
	return nil
}

// closedRecords are the endpoints of a disabled instance as closed records, the
// token gone.
func closedRecords(eps []store.EndpointRecord) []store.EndpointRecord {
	out := make([]store.EndpointRecord, len(eps))
	for i, ep := range eps {
		ep.Status, ep.Token = store.EndpointClosed, nil
		out[i] = ep
	}
	return out
}

// instanceDrainBatch is the closing of the records queued for the endpoints
// about to be closed, together with those of endpoints that are closed already:
// the scan as it will be once the endpoints are closed.
func instanceDrainBatch(sc *store.Scan, eps []store.EndpointRecord, now time.Time) store.DrainBatch {
	shadow := *sc
	closing := map[string]bool{}
	for _, ep := range eps {
		closing[ep.ID.String()] = true
	}
	shadow.Endpoints = slices.Clone(sc.Endpoints)
	for i, ep := range shadow.Endpoints {
		if closing[ep.ID.String()] {
			shadow.Endpoints[i].Status = store.EndpointClosed
		}
	}
	ins, ctls := store.DrainCandidates(&shadow)
	b, _ := closeBatch(now, ins, ctls)
	return b
}

// ---------------------------------------------------------------------------
// The recovery-delivery entry

// recoveryPhase is one step of the recovery of deliveries.
type recoveryPhase struct {
	step int
	name string
	run  func(e *Engine, now time.Time) error
}

// The phases of RecoverDelivery, in the order of the startup steps. The feature
// that owns a phase fills in its function; until then it is empty. The order is a
// fact of the start: step 11 asks the connected adapters about creations, closes
// and interrupts, releases and prompts, so it comes after they have connected (the
// busy state they reported is used for renewal after serving), and the feedback
// goes out after the leases were judged.
var recoveryPhases = [...]recoveryPhase{
	{11, "reconcile", (*Engine).recoverReconcile},
	{12, "tick", (*Engine).recoverTick},
	{13, "feedback", (*Engine).recoverFeedback},
	{14, "outbound", (*Engine).recoverOutbound},
}

// recoverReconcile is step 11 of the start: what was not settled when the node went
// down is settled or asked of its adapter again, in this order: the creations, the
// closes and interrupts, the release of the native sessions of closed endpoints, and the
// prompts. A close settled here leaves its native session owed, so the releases are
// asked for after the closes.
func (e *Engine) recoverReconcile(now time.Time) error {
	e.reconciling = true
	if err := e.reconcileCreations(now); err != nil {
		return err
	}
	if err := e.recoverExecutions(now); err != nil {
		return err
	}
	e.recoverReleases(now)
	e.reconcilePrompts(now)
	return nil
}

// recoverFeedback sends the feedback that fell due while the node was down, the
// feedback of the records replaced after a storage loss included (step 13). The
// queue was loaded from the disk by the scan entry, after the replay of feedback
// took off it what a notice already covers.
func (e *Engine) recoverFeedback(now time.Time) error { return e.runFeedback(now) }

type cmdRecoveryPhase struct {
	call[struct{}]
	phase recoveryPhase
}

func (c *cmdRecoveryPhase) gate() gate { return gateAlways }
func (c *cmdRecoveryPhase) run(e *Engine, now time.Time) {
	e.log.Info(fmt.Sprintf("startup step %d begin", c.phase.step), "name", c.phase.name)
	err := c.phase.run(e, now)
	if err == nil {
		e.log.Info(fmt.Sprintf("startup step %d end", c.phase.step), "name", c.phase.name)
	} else {
		e.log.Error(fmt.Sprintf("startup step %d failed", c.phase.step), "name", c.phase.name, "error", err.Error())
	}
	c.done(struct{}{}, e.classify(err))
}

// RecoverDelivery is the recovery-delivery entry: the steps 11 to 14 of the
// start, in that order, each as a command of the loop, after the adapters were
// started and have said which turns run. It returns when the last is done. It is
// called between Start and Serve.
func (e *Engine) RecoverDelivery(ctx context.Context) error {
	if e.Phase() != PhaseRecovering {
		return errors.New("engine: RecoverDelivery runs between Start and Serve")
	}
	for _, ph := range recoveryPhases {
		c := &cmdRecoveryPhase{call: newCall[struct{}](), phase: ph}
		if _, err := roundTrip(ctx, e, submitInternal, c, c.call); err != nil {
			return fmt.Errorf("engine: startup step %d (%s): %w", ph.step, ph.name, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// The capacity check of the engine

// admitNewWork is the one place the engine asks whether the node takes in new
// work: the intake of what a local endpoint sends, replies or controls. It feeds
// checkCapacity with what the engine owns and answers with a refusal or nil. It
// records nothing: backpressure leaves no trace, and what the node already holds
// goes on to its end whatever the answer.
func (e *Engine) admitNewWork(recipients int) error {
	avail, availErr := e.d.Store.Available()
	if availErr != nil {
		avail = ^uint64(0) // judged last: a pause or a full queue says more
	}
	s := e.d.Config.Node().Settings
	v := checkCapacity(capacityInput{
		Retired: e.retired, RollbackPaused: e.paused, Unfinished: e.view.Unfinished(), Recipients: recipients,
		NodeQueueLimit: s.NodeQueueLimit, Available: avail, DiskReserve: s.DiskReserveBytes,
	})
	if !v.Admit {
		return refuse(v.Reason.Reason())
	}
	if availErr != nil {
		return &Refusal{Reason: ReasonStorageUnavailable, Err: availErr}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Quiet

// Expect is what the caller of Quiesce knows from outside the engine: what the
// engine side of being quiet is measured against.
type Expect struct {
	// Adapters names the adapter instances whose connections must be quiet, by
	// instance. An instance that is not named is not judged. Naming one needs an
	// adapter host in Deps.
	Adapters map[string]AdapterExpect
}

// AdapterExpect is what the caller knows of one adapter's connection.
type AdapterExpect struct {
	// Notifications is how many notifications the adapter says it sent: the
	// connection is quiet only when the engine has finished with that many.
	Notifications int
	// Held lists the methods of the requests that the host has sent the adapter and
	// that the adapter holds on purpose, one entry per request. The order does not
	// matter and a method held twice is listed twice.
	Held []string
}

// holds says whether the counts of a connection are those of a quiet one, and
// when they are not, what differs.
func (x AdapterExpect) holds(c adapterhost.ConnCounts) (bool, string) {
	var why []string
	if c.Handled != x.Notifications {
		why = append(why, fmt.Sprintf("%d notifications handled, the adapter sent %d", c.Handled, x.Notifications))
	}
	if c.Buffered != 0 {
		why = append(why, fmt.Sprintf("%d notifications buffered", c.Buffered))
	}
	if c.Requests != 0 {
		why = append(why, fmt.Sprintf("%d requests of the adapter being answered", c.Requests))
	}
	if c.Working {
		why = append(why, "the host is working")
	}
	calls, held := slices.Clone(c.Calls), slices.Clone(x.Held)
	slices.Sort(calls)
	slices.Sort(held)
	if !slices.Equal(calls, held) {
		why = append(why, fmt.Sprintf("requests held by the adapter %v, expected %v", calls, held))
	}
	return len(why) == 0, strings.Join(why, "; ")
}

// Report says what Quiesce found.
type Report struct {
	// InboxLen is how many commands wait for the loop.
	InboxLen int
	// Continuations is how many commands the loop queued for itself.
	Continuations int
	// DueEntries is how many entries of the schedule are due at the time of the
	// clock now.
	DueEntries int
	// Parked is how many commands wait for an endpoint to be drained.
	Parked int
	// Workers is how many goroutines the engine owns that have not returned.
	Workers int
	// AdaptersPending is how many of the instances named in Expect.Adapters are not
	// quiet. The error Quiesce returns when it gives up says which and why.
	AdaptersPending int
	// Satisfied is true when none of them holds anything.
	Satisfied bool
}

// quiesceAnswer is a Report and, for the error of a Quiesce that gives up, what
// each pending adapter lacks, and whether the next look must not wait for the
// engine's activity alone.
type quiesceAnswer struct {
	report  Report
	pending []string
	// poll is set while the outbound worker runs and is not yet seen to wait out a
	// delay: the transport counts a call that starts to wait without telling the
	// engine, so a probe that came a moment before it would otherwise never be
	// followed by another.
	poll bool
}

type cmdQuiesce struct {
	call[quiesceAnswer]
	expect Expect
}

func (c *cmdQuiesce) gate() gate { return gateAlways }
func (c *cmdQuiesce) run(e *Engine, now time.Time) {
	e.probed = true // a probe is not activity: it must not wake the next probe
	r := Report{
		InboxLen: len(e.inbox), Continuations: len(e.cont), DueEntries: e.sched.dueCount(now),
		Parked: len(e.parked), Workers: int(e.workers.Load()),
	}
	parked := e.outboundParked()
	if parked && r.Workers > 0 {
		r.Workers-- // the worker waits out a delay; that is not work
	}
	// The counts of the host are read in this command, after the others, so that
	// what the loop was doing and what the host counts are of one moment. A worker
	// that waits for a request the adapter holds on purpose is not at work: the
	// host's own check below makes sure that the requests it has in flight are
	// exactly those the caller expects held.
	var pending []string
	held := 0
	for _, inst := range sortedKeys(c.expect.Adapters) {
		x := c.expect.Adapters[inst]
		held += len(x.Held)
		if ok, why := x.holds(e.d.Adapters.Counts(inst)); !ok {
			pending = append(pending, fmt.Sprintf("adapter %q: %s", inst, why))
		}
	}
	r.Workers -= min(int(e.adapterCalls.Load()), held, r.Workers)
	r.AdaptersPending = len(pending)
	r.Satisfied = r.InboxLen == 0 && r.Continuations == 0 && r.DueEntries == 0 && r.Parked == 0 && r.Workers == 0 && r.AdaptersPending == 0
	c.done(quiesceAnswer{report: r, pending: pending, poll: e.outboundActive() && !parked}, nil)
}

// quiescePoll is how often, on the real clock, Quiesce looks again while an adapter
// does not hold or the outbound worker may be about to wait out a delay: the host's
// counts and the transport's count move without the engine being told, and nothing
// else would wake the loop to look at them.
const quiescePoll = 2 * time.Millisecond

// Quiesce returns when the engine has nothing left to do at the time of its
// clock: no command waits, the loop has none queued for itself or held for the
// drain of an endpoint, nothing in the schedule is due, no goroutine of the
// engine is running, and the connection of every adapter named in x is quiet by
// the host's own counts (see AdapterExpect). It fails when ctx ends first, with
// the report of what did not hold and, in the error, what each adapter lacks. A
// test moves the clock, calls Quiesce and then looks at the records; nothing in
// the engine is slept on.
func (e *Engine) Quiesce(ctx context.Context, x Expect) (Report, error) {
	if len(x.Adapters) > 0 && e.d.Adapters == nil {
		return Report{}, errors.New("engine: Quiesce is asked about adapters, and the engine has no adapter host")
	}
	var last quiesceAnswer
	for {
		c := &cmdQuiesce{call: newCall[quiesceAnswer](), expect: x}
		a, err := roundTrip(ctx, e, submitInternal, c, c.call)
		if err == nil {
			if last = a; a.report.Satisfied {
				return a.report, nil
			}
			err = e.awaitChange(ctx, a.report.AdaptersPending > 0 || a.poll)
		}
		if err != nil {
			if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
				err = fmt.Errorf("engine: not quiet: %+v %s: %w", last.report, strings.Join(last.pending, "; "), err)
			}
			return last.report, err
		}
	}
}

// awaitChange waits for something that may have changed what Quiesce found: the
// engine's own activity, and with poll also a short time on the real clock (the
// adapter host's counts and the transport's count of waiting calls move without
// the engine being told).
func (e *Engine) awaitChange(ctx context.Context, poll bool) error {
	var tick <-chan time.Time
	if poll {
		t := clock.System().NewTimer(quiescePoll)
		defer t.Stop()
		tick = t.C()
	}
	select {
	case <-e.activity:
		return nil
	case <-tick:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-e.done:
		return refuse(ReasonShuttingDown)
	}
}
