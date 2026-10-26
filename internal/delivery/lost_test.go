package delivery

import (
	"errors"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/timing"
)

// lostAt is the instant at which the tests discover that a delivery record was
// lost to storage corruption.
var lostAt = at(90 * time.Minute)

// lostReportBy is the instant a lost record is kept until, written out by hand:
// the longest delivery deadline an accepted request can have (one hour plus the
// two minute clock tolerance after its acceptance), then the three hours and two
// minutes between a delivery deadline and its report_by.
func lostReportBy(discovered time.Time) time.Time {
	return discovered.Add(time.Hour + 2*time.Minute + 3*time.Hour + 2*time.Minute)
}

func newLost(t *testing.T) (Record, Outcome) {
	t.Helper()
	r, out, err := NewLostRecord(lostAt)
	if err != nil {
		t.Fatalf("NewLostRecord: %v", err)
	}
	return r, out
}

// TestLostMarkerIsSetByNewLostRecordAndNeverCleared: the marker is what tells a
// replacement from a delivery accepted normally once its cause is gone, so every
// state the replacement can reach keeps it and no other record has it.
func TestLostMarkerIsSetByNewLostRecordAndNeverCleared(t *testing.T) {
	for _, r := range []Record{
		NewSourceRecord(t0, at(10*time.Minute)),
		targetRecord(t, t0, 30*time.Minute),
		controlRecord(),
	} {
		if r.Lost {
			t.Errorf("a record made by another constructor is marked lost: %+v", r)
		}
	}
	rec, _ := newLost(t)
	if !rec.Lost {
		t.Fatalf("NewLostRecord did not mark the record: %+v", rec)
	}
	closed := closeAt(t, rec)
	if !closed.Lost || closed.Unknown != UnknownCauseNone {
		t.Errorf("after closing: lost %v, cause %s", closed.Lost, closed.Unknown)
	}
}

func TestLostReportByBoundsEveryAcceptedDeadline(t *testing.T) {
	const (
		acceptedWindow = time.Hour + 2*time.Minute
		reportOffset   = 3*time.Hour + 2*time.Minute
	)
	rng := rand.New(rand.NewSource(7))
	hits := map[string]int{}
	for i := 0; i < 5000; i++ {
		discovered := t0.Add(time.Duration(rng.Int63n(int64(48 * time.Hour))))
		var acceptedAt time.Time
		switch rng.Intn(4) {
		case 0:
			acceptedAt = discovered
			hits["accepted at discovery"]++
		default:
			acceptedAt = discovered.Add(-time.Duration(rng.Int63n(int64(6 * time.Hour))))
		}
		var acceptBefore time.Time
		switch rng.Intn(4) {
		case 0:
			acceptBefore = acceptedAt
			hits["deadline at acceptance"]++
		case 1:
			acceptBefore = acceptedAt.Add(acceptedWindow)
			hits["deadline at the cap"]++
		default:
			acceptBefore = acceptedAt.Add(time.Duration(rng.Int63n(int64(acceptedWindow) + 1)))
		}
		if v, err := Intake(acceptedAt, acceptBefore, authz.TrafficMessage, allow, roomy); err != nil || v.Outcome != IntakeAccept {
			t.Fatalf("the generator left the range Intake accepts: deadline %v after acceptance: %+v %v", acceptBefore.Sub(acceptedAt), v, err)
		}
		reportBy := acceptBefore.Add(reportOffset)
		if got := LostReportBy(discovered); reportBy.After(got) {
			t.Fatalf("report_by %v of a request accepted at %v lies after LostReportBy(%v) = %v", reportBy, acceptedAt, discovered, got)
		}
		if acceptedAt.Equal(discovered) && acceptBefore.Equal(acceptedAt.Add(acceptedWindow)) {
			hits["bound reached"]++
			if got := LostReportBy(discovered); !got.Equal(reportBy) {
				t.Fatalf("the bound is not tight: %v, the largest accepted report_by is %v", got, reportBy)
			}
		}
	}
	for _, p := range []string{"accepted at discovery", "deadline at acceptance", "deadline at the cap", "bound reached"} {
		if hits[p] < 20 {
			t.Errorf("path %q reached %d times, want at least 20", p, hits[p])
		}
	}
	if got := LostReportBy(t0); !got.Equal(t0.Add(4*time.Hour + 4*time.Minute)) {
		t.Errorf("LostReportBy(t0) = %v", got)
	}
	if timing.ClockTolerance != 2*time.Minute {
		t.Errorf("the hand-written oracle assumes a two minute clock tolerance, got %v", timing.ClockTolerance)
	}
}

func TestSourceLostRecordReportsOnceWithSenderWording(t *testing.T) {
	rec, out := newLost(t)
	if want := []Kind{KindLostAtSender}; !sameKinds(kindsOf(out.Feedback), want) {
		t.Fatalf("feedback %v, want %v", kindsOf(out.Feedback), want)
	}
	if fb := out.Feedback[0]; fb.State != StateDeliveryUnknown || fb.Cause != CauseNone || fb.Reject != RejectNone {
		t.Errorf("feedback %+v", fb)
	}
	if !rec.Reported.Has(KindLostAtSender) || rec.Reported != KindSet(0).With(KindLostAtSender) {
		t.Errorf("Reported %b", rec.Reported)
	}
	if !strings.Contains(out.Feedback[0].Kind.Summary(), "this node lost") {
		t.Errorf("wording %q", out.Feedback[0].Kind.Summary())
	}
	t.Run("later facts and time give no second notice", func(t *testing.T) {
		events := []Event{
			{Type: EventReceipt}, {Type: EventDelivered},
			{Type: EventRejected, Reject: RejectUnauthorized},
			{Type: EventDeliveryUnknown}, {Type: EventNoRecordAtTarget},
			{Type: EventExpiryQueried, QueriedAt: lostAt.Add(24 * time.Hour)},
			{Type: EventContactFailing, Cause: CauseNetwork, Since: t0},
			{Type: EventBlocked, Cause: CauseAdapterOffline}, {Type: EventUnblocked},
		}
		r := rec
		for _, ev := range events {
			n, o, err := r.Apply(lostAt.Add(time.Minute), ev)
			if err != nil || len(o.Feedback) != 0 || !reflect.DeepEqual(n, rec) {
				t.Errorf("event %+v: err %v feedback %+v changed %v", ev, err, o.Feedback, !reflect.DeepEqual(n, rec))
			}
		}
		n, outs, err := r.Tick(rec.ReportBy)
		if err != nil || len(outs) != 0 || !reflect.DeepEqual(n, rec) {
			t.Errorf("Tick at ReportBy: %+v %v", outs, err)
		}
	})
}

func TestApplyRefusesEveryOtherWayIntoStorageLoss(t *testing.T) {
	rows := []struct {
		rule string
		rec  func(*testing.T) Record
		ev   Event
		want error
	}{
		{"a target message record without a hand-out cannot become handed over", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) }, Event{Type: EventDeliveryUnknown}, ErrIllegalTransition},
		{"a target record has no way to learn of a loss: the answer of a pull is the sender's", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) }, Event{Type: EventNoRecordAtTarget}, ErrWrongSide},
		{"a target control record has no way to learn of a loss", func(*testing.T) Record { return controlRecord() }, Event{Type: EventNoRecordAtTarget}, ErrWrongSide},
		{"an event of no type", func(*testing.T) Record { return NewSourceRecord(t0, at(10*time.Minute)) }, Event{Type: 0}, ErrInvalidEvent},
		{"an event past the last type", func(*testing.T) Record { return NewSourceRecord(t0, at(10*time.Minute)) }, Event{Type: EventNoRecordAtTarget + 1}, ErrInvalidEvent},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			rec := r.rec(t)
			after, out, err := rec.Apply(at(time.Minute), r.ev)
			if !errors.Is(err, r.want) {
				t.Fatalf("err = %v, want %v", err, r.want)
			}
			if !reflect.DeepEqual(after, rec) || !reflect.DeepEqual(out, Outcome{}) {
				t.Errorf("a refused event changed the record or returned an outcome: %+v %+v", after, out)
			}
		})
	}
}

func TestSourceLostRecordIgnoresLaterFacts(t *testing.T) {
	rec, _ := newLost(t)
	facts := []struct {
		rule string
		ev   Event
	}{
		{"a receipt", Event{Type: EventReceipt}},
		{"a delivery", Event{Type: EventDelivered}},
		{"a rejection", Event{Type: EventRejected, Reject: RejectBacklogExpired}},
		{"an unknown result handed over", Event{Type: EventDeliveryUnknown}},
		{"the target's answer that it holds no record", Event{Type: EventNoRecordAtTarget}},
	}
	for _, f := range facts {
		t.Run(f.rule+" is ignored as record lost", func(t *testing.T) {
			after, out, err := rec.Apply(lostAt.Add(time.Minute), f.ev)
			if err != nil || out.Result != ResultIgnored || out.Ignored != IgnoreRecordLost || len(out.Feedback) != 0 || !reflect.DeepEqual(after, rec) {
				t.Errorf("out %+v err %v", out, err)
			}
		})
	}
	t.Run("status reports are ignored the same way", func(t *testing.T) {
		for _, rep := range []StatusReport{
			{State: StateAccepted}, {State: StateDelivered}, {State: StateRejected, Reject: RejectUnauthorized},
			{State: StateDeliveryUnknown},
		} {
			after, outs, err := rec.ApplyStatusReport(lostAt.Add(time.Minute), rep)
			if err != nil || len(outs) == 0 || outs[0].Ignored != IgnoreRecordLost || !reflect.DeepEqual(after, rec) {
				t.Errorf("report %+v: %+v %v", rep, outs, err)
			}
		}
	})
	t.Run("the record closes silently after its report_by and then facts are final", func(t *testing.T) {
		if _, _, err := rec.Tick(rec.ReportBy); err != nil {
			t.Fatal(err)
		}
		closed, outs, err := rec.Tick(rec.ReportBy.Add(time.Nanosecond))
		if err != nil || len(outs) != 1 || outs[0].To != StateClosedUnconfirmed || len(outs[0].Feedback) != 0 {
			t.Fatalf("outs %+v err %v", outs, err)
		}
		if closed.State != StateClosedUnconfirmed || closed.Unknown != UnknownCauseNone {
			t.Errorf("state %s cause %s", closed.State, closed.Unknown)
		}
		_, out, err := closed.Apply(rec.ReportBy.Add(time.Minute), Event{Type: EventDelivered})
		if err != nil || out.Ignored != IgnoreAlreadyFinal {
			t.Errorf("after closing: %+v %v", out, err)
		}
	})
	t.Run("a malformed fact is still an error, not an ignored one", func(t *testing.T) {
		if _, _, err := rec.Apply(lostAt, Event{Type: EventRejected}); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestTransitionFeedbackChoosesKindByUnknownCause(t *testing.T) {
	causeKind := map[UnknownCause]Kind{
		UnknownHandedOver:   KindDeliveryUnknown,
		UnknownLostAtTarget: KindLostAtTarget,
		UnknownLostAtSender: KindLostAtSender,
	}
	for cause, kind := range causeKind {
		for _, from := range []State{StateQueued, StateAccepted, StateExpiredUnconfirmed} {
			t.Run(cause.String()+" entered from "+from.String(), func(t *testing.T) {
				if got := TransitionFeedback(from, StateDeliveryUnknown, cause, 0); !sameKinds(got, []Kind{kind}) {
					t.Errorf("got %v, want %v", got, kind)
				}
				if got := TransitionFeedback(from, StateDeliveryUnknown, cause, KindSet(0).With(kind)); len(got) != 0 {
					t.Errorf("a kind already reported was reported again: %v", got)
				}
			})
		}
		t.Run(cause.String()+" does not report another cause's kind as already given", func(t *testing.T) {
			for other, otherKind := range causeKind {
				if other == cause {
					continue
				}
				if got := TransitionFeedback(StateAccepted, StateDeliveryUnknown, cause, KindSet(0).With(otherKind)); !sameKinds(got, []Kind{kind}) {
					t.Errorf("after %v, got %v, want %v", otherKind, got, kind)
				}
			}
		})
		t.Run("leaving "+cause.String()+" for delivered is still a correction", func(t *testing.T) {
			if got := TransitionFeedback(StateDeliveryUnknown, StateDelivered, cause, 0); !sameKinds(got, []Kind{KindUnknownCorrected}) {
				t.Errorf("got %v", got)
			}
		})
	}
}

func TestStatusReportOfAnUnknownResultIsAHandOver(t *testing.T) {
	t.Run("a target's report of delivery_unknown is a hand-over", func(t *testing.T) {
		handed := handOut(t, targetRecord(t, t0, time.Hour), at(30*time.Second), 1, 1)
		handed, _ = mustApply(t, handed, at(time.Minute), Event{Type: EventDeliveryUnknown})
		if got := handed.StatusReport(); got != (StatusReport{State: StateDeliveryUnknown}) {
			t.Errorf("handed over: %+v", got)
		}
		ctl, _ := mustApply(t, controlRecord(), at(time.Minute), Event{Type: EventDeliveryUnknown})
		if got := ctl.StatusReport(); got != (StatusReport{State: StateDeliveryUnknown}) {
			t.Errorf("control: %+v", got)
		}
	})
	t.Run("the sender applying it enters delivery_unknown as handed over, with its one notice", func(t *testing.T) {
		rec, outs, err := NewSourceRecord(t0, at(10*time.Minute)).ApplyStatusReport(at(time.Minute), StatusReport{State: StateDeliveryUnknown})
		if err != nil || rec.Unknown != UnknownHandedOver || !sameKinds(kindsOf(outs[0].Feedback), []Kind{KindDeliveryUnknown}) {
			t.Errorf("record cause %s, feedback %+v, err %v", rec.Unknown, outs, err)
		}
	})
}

func TestNeedsBodyByStateAndSide(t *testing.T) {
	src := func(steps ...step) Record {
		r := NewSourceRecord(t0, at(10*time.Minute))
		for _, s := range steps {
			r, _ = mustApply(t, r, t0.Add(s.at), s.ev)
		}
		return r
	}
	srcLost, _ := newLost(t)
	queued := targetRecord(t, t0, time.Hour)
	notified, _ := mustApply(t, queued, at(time.Second), Event{Type: EventNotified})
	blocked, _ := mustApply(t, queued, at(time.Second), Event{Type: EventBlocked, Cause: CauseAdapterOffline})
	leased := handOut(t, queued, at(time.Minute), 2, 1)
	unknownT, _ := mustApply(t, leased, at(2*time.Minute), Event{Type: EventDeliveryUnknown})
	rejectedT, _ := mustApply(t, queued, at(time.Second), Event{Type: EventRejected, Reject: RejectBacklogExpired})
	closedT, _, _ := queued.Tick(queued.ReportBy.Add(time.Second))
	ctlUnknown, _ := mustApply(t, controlRecord(), at(time.Minute), Event{Type: EventDeliveryUnknown})
	ctlDelivered, _ := mustApply(t, controlRecord(), at(time.Minute), replyCommitted())
	rows := []struct {
		rule string
		rec  Record
		want bool
	}{
		{"a source record that is queued", src(), true},
		{"a source record that is accepted", src(receipt(time.Second)), false},
		{"a source record that is delivered", src(delivered(time.Second)), false},
		{"a source record that is rejected", src(rejectedBy(time.Second, RejectUnauthorized)), false},
		{"a source record that is delivery_unknown", src(unknown(time.Second)), false},
		{"a source record that is expired_unconfirmed", src(queried(afterExpiry)), false},
		{"a source record that is closed", src(reportByPassed(reportAt + time.Second)), false},
		{"a lost source record", srcLost, false},
		{"a target message waiting in the queue", queued, true},
		{"a target message that was notified", notified, true},
		{"a target message that is blocked", blocked, true},
		{"a target message under a lease", leased, true},
		{"a target message that is delivery_unknown", unknownT, false},
		{"a target message that is delivered", deliverBody(t, queued, at(time.Minute)), false},
		{"a target message that is rejected", rejectedT, false},
		{"a target message that is closed", closedT, false},
		{"an accepted target control record", controlRecord(), false},
		{"a delivery_unknown target control record", ctlUnknown, false},
		{"a delivered target control record", ctlDelivered, false},
		{"the zero record", Record{}, false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := r.rec.NeedsBody(); got != r.want {
				t.Errorf("NeedsBody = %v, want %v for %+v", got, r.want, r.rec)
			}
		})
	}
}

func TestStorageLossKindsMergeByMessage(t *testing.T) {
	for _, kind := range []Kind{KindLostAtSender, KindLostAtTarget} {
		t.Run(kind.String(), func(t *testing.T) {
			it := item("s1", "n11", "m1", kind, 0)
			key := MergeKeyOf(it)
			if key.Class != MergeByMessage || key.Message != "m1" || key.Sender != it.Ref.Sender || !key.Node.IsZero() || !key.Target.IsZero() {
				t.Errorf("key %+v", key)
			}
			var q FeedbackQueue
			if err := q.Add(it); err != nil {
				t.Fatalf("a storage loss notice cannot be queued: %v", err)
			}
			if err := q.Add(item("s1", "n12", "m1", KindDeliveryUnknown, time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := q.Add(item("s1", "n11", "m2", kind, 0)); err != nil {
				t.Fatal(err)
			}
			got, err := q.Flush(at(time.Minute), testParams)
			if err != nil || len(got) != 2 {
				t.Fatalf("notices %+v err %v", got, err)
			}
			merged := 0
			for _, n := range got {
				if n.Key.Message == "m1" {
					merged = len(n.Items)
				}
			}
			if merged != 2 {
				t.Errorf("anomalies of one message were not merged: %d items", merged)
			}
		})
	}
}

// TestUnknownCauseAndLostShapeInvariants is the table of the records that carry
// an unknown cause or the Lost marker, valid and not, each judged by Valid and
// named after the rule it checks. Every row starts from a record the package
// produces. The rows of a target's record are in TestTargetNeverHoldsALossCause.
func TestUnknownCauseAndLostShapeInvariants(t *testing.T) {
	srcLost, _ := newLost(t)
	handed := handOut(t, targetRecord(t, t0, time.Hour), at(30*time.Second), 1, 1)
	handed, _ = mustApply(t, handed, at(time.Minute), Event{Type: EventDeliveryUnknown})
	srcHanded, _ := mustApply(t, NewSourceRecord(t0, at(10*time.Minute)), at(time.Minute), Event{Type: EventDeliveryUnknown})
	srcTargetLoss, _ := mustApply(t, sourceInState(t, "accepted"), at(time.Minute), Event{Type: EventNoRecordAtTarget})
	srcTargetLossDelivered, _ := mustApply(t, srcTargetLoss, at(2*time.Minute), Event{Type: EventDelivered})
	srcTargetLossClosed, _ := mustApply(t, srcTargetLoss, at(reportAt+time.Second), Event{Type: EventReportByPassed})
	srcLostClosed := closeAt(t, srcLost)
	mutate := func(r Record, f func(*Record)) Record { f(&r); return cloneRecord(r) }
	rows := []struct {
		rule string
		rec  Record
		want Violation // ViolationNone: the record is valid
	}{
		{"a record outside delivery_unknown has no cause", NewSourceRecord(t0, at(10*time.Minute)), ViolationNone},
		{"a source record that is handed over", srcHanded, ViolationNone},
		{"a source record that learned of a loss at the target", srcTargetLoss, ViolationNone},
		{"a source record that learned of a loss at the target, then was delivered", srcTargetLossDelivered, ViolationNone},
		{"a source record that learned of a loss at the target, then closed", srcTargetLossClosed, ViolationNone},
		{"a target message handed over after a hand-out", handed, ViolationNone},
		{"a lost source record", srcLost, ViolationNone},
		{"a lost source record after it closed", srcLostClosed, ViolationNone},

		{"a cause outside delivery_unknown", mutate(NewSourceRecord(t0, at(10*time.Minute)), func(r *Record) { r.Unknown = UnknownHandedOver }), ViolationUnknownCause},
		{"delivery_unknown without a cause", mutate(srcHanded, func(r *Record) { r.Unknown = UnknownCauseNone }), ViolationUnknownCause},
		{"an unknown cause value", mutate(srcHanded, func(r *Record) { r.Unknown = 200 }), ViolationUnknownCause},
		{"a source replacement is never handed over", mutate(srcLost, func(r *Record) { r.Unknown = UnknownHandedOver }), ViolationUnknownCause},
		{"lost_at_sender on a target", mutate(handed, func(r *Record) { r.Unknown = UnknownLostAtSender }), ViolationUnknownCause},
		{"lost_at_sender without the marker", mutate(srcLost, func(r *Record) { r.Lost = false }), ViolationUnknownCause},
		{"lost_at_target on a source replacement", mutate(srcLost, func(r *Record) { r.Unknown = UnknownLostAtTarget }), ViolationUnknownCause},
		{"the marker on a record handed over at the source", mutate(srcHanded, func(r *Record) { r.Lost = true }), ViolationUnknownCause},
		{"the marker on a record that learned of a loss at the target", mutate(srcTargetLoss, func(r *Record) { r.Lost = true }), ViolationUnknownCause},

		{"the marker on a queued record", mutate(NewSourceRecord(t0, at(10*time.Minute)), func(r *Record) { r.Lost = true }), ViolationLostShape},
		{"the marker on a delivered source record", mutate(srcTargetLossDelivered, func(r *Record) { r.Lost = true }), ViolationLostShape},
		{"a lost record is never delivered", mutate(srcLost, func(r *Record) { r.State, r.Unknown = StateDelivered, UnknownCauseNone }), ViolationLostShape},
		{"a lost record is never accepted again", mutate(srcLost, func(r *Record) { r.State, r.Unknown = StateAccepted, UnknownCauseNone }), ViolationLostShape},
		{"a lost record is never rejected", mutate(srcLost, func(r *Record) { r.State, r.Unknown, r.Reject = StateRejected, UnknownCauseNone, RejectExpired }), ViolationLostShape},
		{"a lost record that is a control record", mutate(srcLost, func(r *Record) { r.Control = true }), ViolationControlSide},
		{"a lost record without ReportBy", mutate(srcLost, func(r *Record) { r.ReportBy = time.Time{} }), ViolationLostShape},
		{"a lost record with AcceptBefore", mutate(srcLost, func(r *Record) { r.AcceptBefore = at(time.Minute) }), ViolationLostShape},
		{"a lost record with BacklogDeadline", mutate(srcLost, func(r *Record) { r.BacklogDeadline = at(time.Minute) }), ViolationLostShape},
		{"a lost record with a prompt", mutate(srcLost, func(r *Record) { r.Sub = SubNotified }), ViolationSubState},
		{"a lost record with a rejection reason", mutate(srcLost, func(r *Record) { r.Reject = RejectExpired }), ViolationRejectReason},
		{"a lost record that is blocked", mutate(srcLost, func(r *Record) { r.Blocked = true }), ViolationBlockedState},
		{"a lost record with a block cause", mutate(srcLost, func(r *Record) { r.BlockedCause = CauseAdapterOffline }), ViolationLostShape},
		{"a lost record with a hand-out", mutate(srcLost, func(r *Record) { r.Lease.Handouts = 1 }), ViolationLostShape},
		{"a lost record with an open lease", mutate(srcLost, func(r *Record) { r.Lease.Active = true }), ViolationLostShape},
		{"a lost record with chunks", mutate(srcLost, func(r *Record) { r.Lease.Chunks = 1 }), ViolationLostShape},
		{"a lost record with handed-out flags", mutate(srcLost, func(r *Record) { r.Lease.HandedOut = []bool{true} }), ViolationLostShape},
		{"a lost record with acknowledgement flags", mutate(srcLost, func(r *Record) { r.Lease.Acked = []bool{true} }), ViolationLostShape},
		{"a lost record with page identifiers", mutate(srcLost, func(r *Record) { r.Lease.IDs = []LeaseID{freshLease()} }), ViolationLostShape},
		{"a lost record with a lease issue time", mutate(srcLost, func(r *Record) { r.Lease.IssuedAt = at(time.Minute) }), ViolationLostShape},
		{"a lost record with a lease expiry", mutate(srcLost, func(r *Record) { r.Lease.ExpiresAt = at(time.Minute) }), ViolationLostShape},
		{"a lost record that has not reported its notice", mutate(srcLost, func(r *Record) { r.Reported = 0 }), ViolationLostShape},
		{"a lost record that reported something else", mutate(srcLost, func(r *Record) { r.Reported = r.Reported.With(KindUnreachable) }), ViolationLostShape},
		{"a lost record that reported the other loss", mutate(srcLost, func(r *Record) { r.Reported = KindSet(0).With(KindLostAtTarget) }), ViolationLostShape},
		{"a source record that reported its own loss without the marker", mutate(srcHanded, func(r *Record) { r.Reported = r.Reported.With(KindLostAtSender) }), ViolationReportedDeliveryUnknown},
		{"a source record that learned of a loss at the target reported it", mutate(srcTargetLoss, func(r *Record) { r.Reported = 0 }), ViolationReportedDeliveryUnknown},
		{"a source record's cause and its notice agree", mutate(srcTargetLoss, func(r *Record) { r.Reported = KindSet(0).With(KindDeliveryUnknown) }), ViolationReportedDeliveryUnknown},
		{"a source record has at most one unknown notice", mutate(srcTargetLoss, func(r *Record) { r.Reported = r.Reported.With(KindDeliveryUnknown) }), ViolationReportedDeliveryUnknown},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := violationOf(t, r.rec.Valid()); got != r.want {
				t.Errorf("Valid: violation %s, want %s for %+v", got, r.want, r.rec)
			}
		})
	}
}

// closeAt returns the record closed by Tick at the instant after its ReportBy.
func closeAt(t *testing.T, r Record) Record {
	t.Helper()
	closed, _, err := r.Tick(r.ReportBy.Add(time.Nanosecond))
	if err != nil || closed.State != StateClosedUnconfirmed {
		t.Fatalf("closing: state %s err %v", closed.State, err)
	}
	return closed
}

// TestCheckUnknownCauseJudgesTheSideOfALossCause: a loss cause is held per side,
// and an unknown side holds none, so a record whose side is not one of the two is
// refused by the check itself and not only by the check of the side.
func TestCheckUnknownCauseJudgesTheSideOfALossCause(t *testing.T) {
	srcLost, _ := newLost(t)
	for _, side := range []Side{0, 3, SideTarget} {
		r := srcLost
		r.Side = side
		if got := r.checkUnknownCause(); got != ViolationUnknownCause {
			t.Errorf("lost_at_sender on side %d: %s, want %s", side, got, ViolationUnknownCause)
		}
	}
}

// TestChangeRefusesACauseThatDoesNotFitTheState: entering delivery_unknown
// takes one of the three causes, and entering any other state takes none. The
// callers always keep to it; the guard turns a slip into an error instead of a
// record whose cause and state disagree.
func TestChangeRefusesACauseThatDoesNotFitTheState(t *testing.T) {
	rows := []struct {
		rule    string
		to      State
		unknown UnknownCause
	}{
		{"delivery_unknown without a cause", StateDeliveryUnknown, UnknownCauseNone},
		{"delivered with a cause", StateDelivered, UnknownHandedOver},
		{"closed_unconfirmed with a cause", StateClosedUnconfirmed, UnknownLostAtTarget},
		{"accepted with a cause", StateAccepted, UnknownLostAtSender},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			for _, rec := range []Record{NewSourceRecord(t0, at(10*time.Minute)), controlRecord()} {
				after, out, err := rec.change(at(time.Minute), r.to, RejectNone, r.unknown)
				if !errors.Is(err, ErrInvalidInput) || out.Result != 0 || !reflect.DeepEqual(after, rec) {
					t.Errorf("%v: result %v, record changed %v, err %v, want ErrInvalidInput", rec.Side, out.Result, !reflect.DeepEqual(after, rec), err)
				}
			}
		})
	}
}

// lostModel is the oracle of the lost-record lifecycle: it knows only the
// instants and the kinds of event, not how the record decides.
type lostModel struct {
	discover time.Time
	closed   bool // the record was closed by a Tick or EventReportByPassed after the end
	feedback int
}

func (m *lostModel) reportBy() time.Time { return lostReportBy(m.discover) }

const lostLifecycleRuns = 3000

func TestPropertyLostRecordLifecycle(t *testing.T) {
	runLostLifecycles(t, lostLifecycleRuns, func(*testing.T, Record) {})
}

// runLostLifecycles drives the sender's replacement records with random,
// time-ordered facts and checks them against lostModel, an oracle that knows
// only the instants and the kinds of event. observe is called with the record
// after every step, and Valid is required of it there too: a replacement is
// Valid in every state it reaches, which is what lets a restart keep it instead
// of replacing and reporting it again. Calling checkUnknownCause as well is an
// extra assertion of the unknown-cause invariant, not the oracle.
func runLostLifecycles(t *testing.T, runs int, observe func(*testing.T, Record)) {
	hits := map[string]int{}
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < runs; i++ {
		var m lostModel
		m.discover = t0.Add(time.Duration(rng.Intn(600)) * time.Minute)
		rec, out, err := NewLostRecord(m.discover)
		if err != nil {
			t.Fatal(err)
		}
		m.feedback = len(out.Feedback)
		if m.feedback != 1 {
			t.Fatalf("run %d: a lost record gave %d notices at birth", i, m.feedback)
		}
		observe(t, rec)
		now := m.discover
		for step := 0; step < 40; step++ {
			// Steps on the scale of minutes, sometimes of hours, so the end of
			// the record's life is crossed in about half the runs.
			if rng.Intn(10) == 0 {
				now = now.Add(time.Duration(rng.Intn(180)) * time.Minute)
			} else {
				now = now.Add(time.Duration(rng.Intn(20)) * time.Minute)
			}
			pastEnd := now.After(m.reportBy())
			var o Outcome
			var outs []Outcome
			var e error
			switch k := rng.Intn(5); k {
			case 0:
				rec, outs, e = rec.Tick(now)
				if pastEnd {
					m.closed = true
				}
				hits["tick"]++
			case 1:
				rec, o, e = rec.Apply(now, Event{Type: EventReportByPassed})
				if pastEnd {
					m.closed = true
				}
				hits["report_by passed"]++
			case 2:
				facts := []Event{{Type: EventReceipt}, {Type: EventDelivered}, {Type: EventRejected, Reject: RejectUnauthorized}, {Type: EventDeliveryUnknown}, {Type: EventNoRecordAtTarget}}
				rec, o, e = rec.Apply(now, facts[rng.Intn(len(facts))])
				switch {
				case m.closed:
					if o.Ignored != IgnoreAlreadyFinal && o.Ignored != IgnoreRecordLost {
						t.Fatalf("run %d: a fact after closing: %+v", i, o)
					}
				case o.Ignored != IgnoreRecordLost:
					t.Fatalf("run %d: a fact for a lost source record: %+v", i, o)
				}
				hits["source fact ignored"]++
			case 3:
				reps := []StatusReport{{State: StateAccepted}, {State: StateDelivered}, {State: StateDeliveryUnknown}}
				var os []Outcome
				rec, os, e = rec.ApplyStatusReport(now, reps[rng.Intn(len(reps))])
				for _, x := range os {
					m.feedback += len(x.Feedback)
				}
				hits["source report applied"]++
			default:
				hits["idle"]++
			}
			if e != nil {
				t.Fatalf("run %d: %v", i, e)
			}
			m.feedback += len(o.Feedback)
			for _, x := range outs {
				m.feedback += len(x.Feedback)
			}

			// The oracle's verdicts.
			if m.closed {
				if rec.State != StateClosedUnconfirmed {
					t.Fatalf("run %d: state %s after the end of the record's life", i, rec.State)
				}
			} else if rec.State != StateDeliveryUnknown {
				t.Fatalf("run %d: state %s, a lost record stays unknown until it closes", i, rec.State)
			}
			if err := rec.Valid(); err != nil {
				t.Fatalf("run %d step %d: %v\n%+v", i, step, err, rec)
			}
			if v := rec.checkUnknownCause(); v != ViolationNone {
				t.Fatalf("run %d: %s", i, v)
			}
			observe(t, rec)
			if m.feedback != 1 {
				t.Fatalf("run %d: %d notices in total, want 1", i, m.feedback)
			}
		}
		// Run the clock out: everything closes.
		end, outs, err := rec.Tick(m.reportBy().Add(time.Nanosecond))
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range outs {
			if len(x.Feedback) != 0 {
				t.Fatalf("run %d: closing gave feedback", i)
			}
		}
		if err := end.Valid(); err != nil {
			t.Fatalf("run %d: the record after the clock ran out: %v\n%+v", i, err, end)
		}
		observe(t, end)
		hits["ended closed"]++
		if end.State != StateClosedUnconfirmed || !end.NextTick().IsZero() {
			t.Fatalf("run %d: ended %s", i, end.State)
		}
	}
	for _, p := range []string{"tick", "report_by passed", "source fact ignored", "source report applied", "ended closed"} {
		if hits[p] < 100 {
			t.Errorf("path %q reached %d times, want at least 100", p, hits[p])
		}
	}
}
