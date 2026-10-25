package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/node/store"
)

// RecoveryPlan is the work of the recovery rules of the scan entry, as a pure
// function of a scan: what each rule has to do. The scan entry executes it, and
// a test that a disk no crash and no damage touched has no work reads the same
// plan, so the check cannot drift from the rules.
type RecoveryPlan struct {
	// Quarantine, QuarantineResume and QuarantineBody are the damaged files, the
	// quarantines that were not finished, and the quarantined bodies whose alert
	// is missing.
	Quarantine       []store.DamagedFile
	QuarantineResume []store.CorruptFile
	QuarantineBody   []store.CorruptFile
	// CtlEffectReplay are the endpoints whose control record says more than they
	// do.
	CtlEffectReplay []store.EndpointRecord
	// InstanceDrain are the endpoints of instances the owner disabled, still not
	// closed.
	InstanceDrain []store.EndpointRecord
	// OrphanEndpoints are session endpoints that were being created and that no
	// control record creates.
	OrphanEndpoints []store.EndpointRecord
	// EndpointDrain is the closing of the records queued for closed endpoints.
	EndpointDrain store.DrainBatch
	// PromptReplay, BlockedReplay and FeedbackReplay are the followers of a head
	// record that a crash left unwritten.
	PromptReplay   []store.PendingWork
	BlockedReplay  []store.PendingWork
	FeedbackReplay []store.OutRecord
	// OrphanBodies are the bodies no record refers to.
	OrphanBodies []string
}

// PlanRecovery works out the work of the recovery rules on a scan. enabled says
// whether the owner has an adapter instance enabled; now is the time at which the
// delivery rules judge the records.
func PlanRecovery(sc *store.Scan, now time.Time, enabled func(instance string) bool) RecoveryPlan {
	p := RecoveryPlan{
		Quarantine:       sc.Damaged,
		QuarantineResume: store.UnfinishedQuarantines(sc),
		QuarantineBody:   store.UnfinishedBodyQuarantines(sc),
		CtlEffectReplay:  store.EffectReplays(sc),
		InstanceDrain:    store.InstanceDrainWork(sc, enabled),
		OrphanEndpoints:  store.OrphanEndpoints(sc),
		PromptReplay:     store.PromptReplays(sc),
		BlockedReplay:    store.BlockedReplays(sc),
		FeedbackReplay:   store.FeedbackReplays(sc),
		OrphanBodies:     store.OrphanBodies(sc),
	}
	ins, ctls := store.DrainCandidates(sc)
	p.EndpointDrain, _ = closeBatch(countBodies(sc), now, ins, ctls)
	return p
}

// countBodies is how many records of the scan refer to each body.
func countBodies(sc *store.Scan) bodyCounts {
	c := bodyCounts{}
	for _, o := range sc.Out {
		c.add(o.Body, 1)
	}
	for _, in := range sc.In {
		c.add(in.Body, 1)
	}
	for _, ctl := range sc.Ctl {
		c.add(ctl.Body, 1)
	}
	return c
}

// Items names the work, one line for each thing a rule has to do, in the form
// "rule: what". An empty list is a plan with nothing to do.
func (p RecoveryPlan) Items() []string {
	var out []string
	add := func(rule, what string) { out = append(out, rule+": "+what) }
	for _, d := range p.Quarantine {
		add("quarantine", d.Dir+"/"+d.Name)
	}
	for _, c := range p.QuarantineResume {
		add("quarantine-resume", c.Dir+"/"+c.Name)
	}
	for _, c := range p.QuarantineBody {
		add("quarantine-body", c.Dir+"/"+c.Name)
	}
	for _, ep := range p.CtlEffectReplay {
		add("ctl-effect-replay", ep.ID.String())
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
	for _, w := range p.PromptReplay {
		add("prompt-replay", w.Head.Endpoint.String())
	}
	for _, w := range p.BlockedReplay {
		add("blocked-replay", w.Head.Endpoint.String())
	}
	for _, o := range p.FeedbackReplay {
		add("feedback-replay", o.Name())
	}
	for _, b := range p.OrphanBodies {
		add("orphan-gc", b)
	}
	return out
}

// Empty reports whether no rule has anything to do.
func (p RecoveryPlan) Empty() bool { return len(p.Items()) == 0 }

// unwired is the first rule of the plan that has work and that is not
// wired yet, with how many things it has to do; ok is false when there is none.
// The quarantine of a damaged file forms the replacement of a lost delivery and
// the feedback it causes, the replays of a prompt and of a block belong to the
// prompts, the replay of feedback to the feedback: each is filled in with the
// feature that writes what it replays.
func (p RecoveryPlan) unwired() (rule string, items int, ok bool) {
	for _, r := range []struct {
		name string
		n    int
	}{
		{"quarantine", len(p.Quarantine)},
		{"quarantine-resume", len(p.QuarantineResume)},
		{"quarantine-body", len(p.QuarantineBody)},
		{"prompt-replay", len(p.PromptReplay)},
		{"blocked-replay", len(p.BlockedReplay)},
		{"feedback-replay", len(p.FeedbackReplay)},
	} {
		if r.n > 0 {
			return r.name, r.n, true
		}
	}
	return "", 0, false
}

// PendingRecoveryError is the refusal of the scan entry to open over work of a
// rule that is not wired yet. It is returned before the entry writes
// anything in the pass that found the work: a damaged file is not left out of
// the view as if it were not there.
type PendingRecoveryError struct {
	Rule  string
	Items int
}

func (e *PendingRecoveryError) Error() string {
	return fmt.Sprintf("engine: the recovery rule %s has work (%d) and is not wired", e.Rule, e.Items)
}

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
			rep.Endpoints, rep.Unfinished = e.view.OpenEndpoints(), e.view.Unfinished()
			return e, rep, nil
		}
		if rule, n, ok := plan.unwired(); ok {
			return nil, rep, &PendingRecoveryError{Rule: rule, Items: n}
		}
		if err := e.runRules(ctx, sc, plan, enabled, &rep); err != nil {
			return nil, rep, err
		}
	}
}

// runRules runs the rules that have work, in the order of the store's
// guarantee. After a rule has written, the plan is worked out again from a fresh
// scan, because each rule reads what the one before it left.
func (e *Engine) runRules(ctx context.Context, sc *store.Scan, plan RecoveryPlan, enabled func(string) bool, rep *ScanReport) error {
	steps := []struct {
		rule string
		work func(RecoveryPlan) int
		do   func(sc *store.Scan, plan RecoveryPlan, now time.Time) error
	}{
		{"ctl-effect-replay", func(p RecoveryPlan) int { return len(p.CtlEffectReplay) },
			func(_ *store.Scan, p RecoveryPlan, _ time.Time) error {
				return e.d.Store.CtlEffectReplay(p.CtlEffectReplay)
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
		if rule, n, ok := plan.unwired(); ok {
			return &PendingRecoveryError{Rule: rule, Items: n}
		}
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
	b, _ := closeBatch(countBodies(sc), now, ins, ctls)
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
// fact of the start: the leases are only released after the adapters have said
// which turns are running, and the feedback goes out after the leases were
// judged.
var recoveryPhases = [...]recoveryPhase{
	{11, "reconcile", (*Engine).recoverReconcile},
	{12, "tick", (*Engine).recoverTick},
	{13, "feedback", (*Engine).recoverFeedback},
	{14, "outbound", (*Engine).recoverOutbound},
}

// recoverReconcile checks the creation intents and the prompt attempts that were
// not settled (step 11). Filled in with the prompts and the session creation.
func (e *Engine) recoverReconcile(time.Time) error { return nil }

// recoverTick runs Tick on every record, in chunks, and settles what became due
// (step 12). Filled in with the pull of messages.
func (e *Engine) recoverTick(time.Time) error { return nil }

// recoverFeedback sends the feedback that fell due, the feedback of the records
// replaced after a storage loss included (step 13). Filled in with the feedback.
func (e *Engine) recoverFeedback(time.Time) error { return nil }

// recoverOutbound goes on with the loopback of outgoing messages and the
// applying of status reports (step 14). Filled in with sending and intake.
func (e *Engine) recoverOutbound(time.Time) error { return nil }

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
// CheckCapacity with what the engine owns and answers with a refusal or nil. It
// records nothing: backpressure leaves no trace, and what the node already holds
// goes on to its end whatever the answer.
func (e *Engine) admitNewWork(recipients int) error {
	avail, availErr := e.d.Store.Available()
	if availErr != nil {
		avail = ^uint64(0) // judged last: a pause or a full queue says more
	}
	s := e.d.Config.Node().Settings
	v := CheckCapacity(CapacityInput{
		RollbackPaused: e.paused, Unfinished: e.view.Unfinished(), Recipients: recipients,
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
// engine side of being quiet is measured against. It grows with the adapter
// host, whose connections are counted by the notifications the adapters say they
// sent.
type Expect struct{}

// Report says what Quiesce found.
type Report struct {
	// InboxLen is how many commands wait for the loop.
	InboxLen int
	// Continuations is how many commands the loop queued for itself.
	Continuations int
	// DueEntries is how many entries of the schedule are due at the time of the
	// clock now.
	DueEntries int
	// Workers is how many goroutines the engine owns that have not returned.
	Workers int
	// Satisfied is true when none of them holds anything.
	Satisfied bool
}

type cmdQuiesce struct{ call[Report] }

func (c *cmdQuiesce) gate() gate { return gateAlways }
func (c *cmdQuiesce) run(e *Engine, now time.Time) {
	e.probed = true // a probe is not activity: it must not wake the next probe
	r := Report{
		InboxLen: len(e.inbox), Continuations: len(e.cont), DueEntries: e.sched.dueCount(now),
		Workers: int(e.workers.Load()),
	}
	r.Satisfied = r.InboxLen == 0 && r.Continuations == 0 && r.DueEntries == 0 && r.Workers == 0
	c.done(r, nil)
}

// Quiesce returns when the engine has nothing left to do at the time of its
// clock: no command waits, the loop has none queued for itself, nothing in the
// schedule is due and no goroutine of the engine is running. It fails when ctx
// ends first, with the report of what did not hold. A test moves the clock, calls
// Quiesce and then looks at the records; nothing in the engine is slept on.
func (e *Engine) Quiesce(ctx context.Context, _ Expect) (Report, error) {
	var last Report
	for {
		c := &cmdQuiesce{call: newCall[Report]()}
		r, err := roundTrip(ctx, e, submitInternal, c, c.call)
		if err != nil {
			return last, err
		}
		if last = r; r.Satisfied {
			return r, nil
		}
		select {
		case <-e.activity:
		case <-ctx.Done():
			return last, fmt.Errorf("engine: not quiet: %+v: %w", last, ctx.Err())
		case <-e.done:
			return last, refuse(ReasonShuttingDown)
		}
	}
}

var _ = sort.Strings
