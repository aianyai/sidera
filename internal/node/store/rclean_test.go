package store_test

import (
	"fmt"
	"path"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// cleanSteps is a run of the store without a crash, damage or injected fault,
// through the events of a life: messages in and out, prompts and blocks, leases
// and acknowledgements, sessions created and closed, endpoints closed and an
// instance disabled, feedback formed and cleared, the sweep, an acknowledged
// alert. After each step, every predicate of a recovery rule the store owns,
// evaluated on a copy of the disk, finds nothing.
func TestCleanScenariosLeaveNoRecoveryWork(t *testing.T) {
	r := newRig(t)
	s := newSim(t, r.st)
	disabled := map[string]bool{}
	enabled := func(inst string) bool { return !disabled[inst] }
	s.enabled = enabled

	step := 0
	check := func(what string) {
		t.Helper()
		step++
		view := openRigReadOnly(t, r)
		noWork(t, fmt.Sprintf("after step %d (%s)", step, what), view.scan(), enabled)
		left, err := txn.CleanupWork(view.mem)
		if err != nil || len(left) != 0 {
			t.Errorf("after step %d (%s): leftovers of interrupted writes: %v %v", step, what, left, err)
		}
		if missing, err := store.MissingStateDirs(view.mem); err != nil || len(missing) != 0 {
			t.Errorf("after step %d (%s): layout directories missing: %v %v", step, what, missing, err)
		}
	}
	check("start")
	for _, e := range []int{1, 2, 4} {
		mustDo(t, r.st.EndpointUpdate(directEndpoint(e, "base")))
	}
	check("the endpoints of the node")

	// Messages out and in, and the prompt and block events.
	for i := 1; i <= 3; i++ {
		mustDo(t, r.st.Intake(bodyBytes(i), srcOut(i, true)))
		check("intake")
	}
	mustDo(t, r.st.Deliver(bodyBytes(1), []store.InRecord{tgtIn(1)}))
	mustDo(t, r.st.Deliver(bodyBytes(2), []store.InRecord{tgtIn(2)}))
	check("deliver")
	mustDo(t, r.st.Deliver(bodyBytes(9), []store.InRecord{tgtIn(9)}))
	check("deliver with a body")

	a, b := tgtIn(1), tgtIn(2)
	mustDo(t, r.st.PromptBegin(store.AttemptsRecord{Endpoint: ep(2), InFlight: &store.InFlightPrompt{Attempt: "0b4d6e1a-2f3c-4d5e-8f60-718293a4b5c6", Kind: 1, Covers: []store.TargetKey{a.Key, b.Key}}}))
	check("prompt begin")
	head := store.AttemptsRecord{Endpoint: ep(2), Attempts: []handoff.Attempt{{At: base, Outcome: handoff.AttemptDelivered, Kind: handoff.PromptKindFirst}}}
	a.Record, b.Record = applied(t, a.Record, delivery.Event{Type: delivery.EventNotified}), applied(t, b.Record, delivery.Event{Type: delivery.EventNotified})
	mustDo(t, r.st.PromptEnd(head, []store.InRecord{a, b}))
	check("prompt end")

	head.Blocked = handoff.BlockedReport{Cause: handoff.CauseAdapterOffline, Since: base}
	a.Record = applied(t, a.Record, delivery.Event{Type: delivery.EventBlocked, Cause: delivery.CauseAdapterOffline})
	b.Record = applied(t, b.Record, delivery.Event{Type: delivery.EventBlocked, Cause: delivery.CauseAdapterOffline})
	mustDo(t, r.st.Blocked(head, []store.InRecord{a, b}))
	check("blocked")
	head.Blocked = handoff.BlockedReport{}
	a.Record = applied(t, a.Record, delivery.Event{Type: delivery.EventUnblocked})
	b.Record = applied(t, b.Record, delivery.Event{Type: delivery.EventUnblocked})
	mustDo(t, r.st.Blocked(head, []store.InRecord{a, b}))
	check("block recovered")

	// Leases and acknowledgements; a record that no longer needs its body takes
	// its own body file with it.
	a.Record = leasedRecord(a.Record, base)
	mustDo(t, r.st.Lease([]store.InRecord{a}))
	check("lease")
	a.Record, a.Body = deliveredRecord(a.Record), nil
	mustDo(t, r.st.Ack([]store.InRecord{a}))
	check("ack")
	out1 := srcOut(1, true)
	d, _, _ := out1.Outbound.Recipients[0].Delivery.Apply(base, delivery.Event{Type: delivery.EventDelivered})
	out1.Outbound.Recipients[0].Delivery, out1.Body = d, nil
	mustDo(t, r.st.SourceUpdate(out1))
	check("source update dropping the body")

	// A session is created and closed.
	c := sessionCtl(20, 5)
	c.Body = bodyRef(20)
	mustDo(t, r.st.ControlIntake(bodyBytes(20), ptr(session(5, store.EndpointCreating, ctlName(20))), c))
	check("session creation accepted")
	cr := created(t, 20, 5)
	mustDo(t, r.st.ControlResult(cr, ptr(openedBy(cr))))
	check("session created")
	derived := derivedIn(20, 5)
	mustDo(t, r.st.Deliver(bodyBytes(20), []store.InRecord{derived}))
	check("the initial message delivered to the session")
	closeCtl := closedBy(21, 5)
	mustDo(t, r.st.ControlResult(closeCtl, ptr(closedSession(5, ctlName(20)))))
	mustDo(t, r.st.EndpointDrain(s.drainBatch(r.scan())))
	check("session drained")

	// An endpoint is closed by the owner, then an instance disabled.
	mustDo(t, r.st.EndpointUpdate(directEndpoint(7, "chat")))
	mustDo(t, r.st.EndpointUpdate(directEndpoint(8, "chat")))
	// Each queued delivery has its own copy of the one body.
	second := inFor(31, 8)
	second.Body = bodyRef(30)
	mustDo(t, r.st.Deliver(bodyBytes(30), []store.InRecord{inFor(30, 7), second}))
	check("endpoints and queued deliveries")
	closed7 := directEndpoint(7, "chat")
	closed7.Status, closed7.Token = store.EndpointClosed, nil
	res := adminResult(1)
	mustDo(t, r.st.EndpointClose(closed7, &res))
	mustDo(t, r.st.EndpointDrain(s.drainBatch(r.scan())))
	check("endpoint closed and drained")
	disabled["chat"] = true
	closed8 := directEndpoint(8, "chat")
	closed8.Status, closed8.Token = store.EndpointClosed, nil
	mustDo(t, r.st.InstanceDrain([]store.EndpointRecord{closed8}, store.DrainBatch{}))
	mustDo(t, r.st.EndpointDrain(s.drainBatch(r.scan())))
	check("instance disabled and drained")

	// Feedback.
	o := srcOut(40, false)
	o.Feedback = []delivery.Item{feedbackItem(40)}
	mustDo(t, r.st.Intake(bodyBytes(40), o))
	mustDo(t, r.st.FeedbackForm([]byte("feedback wording"), noticeFor(o)))
	o.Feedback = nil
	mustDo(t, r.st.FeedbackClear([]store.OutRecord{o}))
	check("feedback cleared")

	// The rest of the single-file writes, then the sweep and an acknowledged alert.
	mustDo(t, r.st.CheckpointPut(store.CheckpointRecord{Key: store.CheckpointKey{Scope: "session", ID: ep(5).String()}, State: []byte(`{}`)}))
	mustDo(t, r.st.AlertRaise(store.AlertRecord{ID: store.AlertID{1}, Kind: "version_untested", At: base, Detail: "d"}))
	mustDo(t, r.st.ArchiveEvents(base, []store.ArchiveEvent{eventAccepted()}))
	check("checkpoint, alert, events")
	sc := r.scan()
	plan := store.PlanSweep(sc, base.Add(farAway))
	if len(plan.Records)+len(plan.Outs) == 0 {
		t.Fatal("the sweep has nothing to do after a life of finished deliveries")
	}
	mustDo(t, r.st.ArchiveSweep(plan.Chunk()))
	check("sweep")
	mustDo(t, r.st.ArchiveExpire(store.ExpiredArchiveFiles([]string{"2026-01-01.jsonl"}, base, 30*24*time.Hour)))
	acked := store.AlertRecord{ID: store.AlertID{1}, Kind: "version_untested", At: base, Detail: "d", Acked: &store.AlertAcked{At: base, Operation: adminResult(3).Key.ID}}
	mustDo(t, r.st.AlertAck(store.AlertAckInput{Alert: acked, Result: adminResult(3)}))
	check("alert acknowledged")
	if step < 20 {
		t.Errorf("only %d steps were checked", step)
	}
}

// closedSession is a session endpoint record after its close.
func closedSession(i int, createdBy string) store.EndpointRecord {
	e := session(i, store.EndpointClosed, createdBy)
	e.Session.NativeRef = fmt.Sprintf("native-%d", i)
	return e
}

// openRigReadOnly is a second view of the disk of a rig, as a copy: the predicates
// are evaluated on a clone, never on the disk of the process that wrote.
func openRigReadOnly(t *testing.T, r *rig) *rig {
	t.Helper()
	clone := r.mem.Clone()
	return openRigRaw(t, clone, nil)
}

var _ = path.Join
var _ = fsx.StepWrite
