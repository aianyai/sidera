package delivery

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestNewLostRecordMakesOnlyASenderReplacement: the only record that replaces a
// delivery lost to storage corruption is the sender's own. A target that loses
// its record keeps nothing: the sender learns of it when the target answers a
// pull that it holds no record.
func TestNewLostRecordMakesOnlyASenderReplacement(t *testing.T) {
	rec, out, err := NewLostRecord(lostAt)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Side != SideSource || rec.Control || !rec.Lost || rec.State != StateDeliveryUnknown || rec.Unknown != UnknownLostAtSender || !rec.StateAt.Equal(lostAt) {
		t.Errorf("record %+v", rec)
	}
	if !rec.ReportBy.Equal(LostReportBy(lostAt)) || !rec.AcceptBefore.IsZero() || !rec.BacklogDeadline.IsZero() {
		t.Errorf("deadlines: ReportBy %v AcceptBefore %v BacklogDeadline %v", rec.ReportBy, rec.AcceptBefore, rec.BacklogDeadline)
	}
	if want := KindSet(0).With(KindLostAtSender); rec.Reported != want {
		t.Errorf("Reported %b, want %b", rec.Reported, want)
	}
	if out.Result != ResultApplied || out.From != StateInvalid || out.To != StateDeliveryUnknown || !sameKinds(kindsOf(out.Feedback), []Kind{KindLostAtSender}) {
		t.Errorf("outcome %+v", out)
	}
	if err := rec.Valid(); err != nil {
		t.Errorf("the replacement is not valid: %v", err)
	}
	if got := reflect.TypeOf(NewLostRecord).NumIn(); got != 1 {
		t.Errorf("NewLostRecord takes %d arguments: a replacement has no side and no control flag to choose", got)
	}

	rec, out, err = NewLostRecord(time.Time{})
	if !errors.Is(err, ErrInvalidInput) || !reflect.DeepEqual(rec, Record{}) || !reflect.DeepEqual(out, Outcome{}) {
		t.Errorf("a zero discovery time: %+v %+v %v", rec, out, err)
	}
}

// TestTargetNeverHoldsALossCause: a target's record is never a loss. It reaches
// delivery_unknown only for a delivery that was handed over, with that cause, and
// neither the Lost marker nor a loss cause is ever valid on it, whatever its
// state; on the source, the loss the target reported (lost_at_target) is an
// ordinary record, without the marker.
func TestTargetNeverHoldsALossCause(t *testing.T) {
	handed := handOut(t, targetRecord(t, t0, time.Hour), at(30*time.Second), 1, 1)
	handedUnknown, _ := mustApply(t, handed, at(time.Minute), unknown(0).ev)
	ctlUnknown, _ := mustApply(t, controlRecord(), at(time.Minute), unknown(0).ev)
	accepted := targetRecord(t, t0, time.Hour)
	closed, _, err := handedUnknown.Tick(handedUnknown.ReportBy.Add(time.Nanosecond))
	if err != nil || closed.State != StateClosedUnconfirmed {
		t.Fatalf("closing: state %s err %v", closed.State, err)
	}
	delivered := deliverBody(t, accepted, at(time.Minute))
	ctlDelivered, _ := mustApply(t, controlRecord(), at(time.Minute), replyCommitted())
	srcLostAtTarget, _ := mustApply(t, sourceInState(t, "accepted"), at(time.Minute), Event{Type: EventNoRecordAtTarget})
	mutate := func(r Record, f func(*Record)) Record { f(&r); return cloneRecord(r) }
	// asReplacement gives a record every field a sender's replacement has but its
	// side, so that only the side keeps it from being one.
	asReplacement := func(r Record) Record {
		return mutate(r, func(r *Record) {
			r.Lost, r.AcceptBefore, r.BacklogDeadline, r.Sub = true, time.Time{}, time.Time{}, SubNone
			r.Reported = KindSet(0).With(KindLostAtSender)
		})
	}

	rows := []struct {
		rule string
		rec  Record
		want Violation
	}{
		{"a target handed over", handedUnknown, ViolationNone},
		{"a control record handed over", ctlUnknown, ViolationNone},
		{"an accepted target", accepted, ViolationNone},
		{"a source that learned of a loss at the target", srcLostAtTarget, ViolationNone},

		{"a target with lost_at_target", mutate(handedUnknown, func(r *Record) { r.Unknown = UnknownLostAtTarget }), ViolationUnknownCause},
		{"a control record with lost_at_target", mutate(ctlUnknown, func(r *Record) { r.Unknown = UnknownLostAtTarget }), ViolationUnknownCause},
		{"a target with lost_at_sender", mutate(handedUnknown, func(r *Record) { r.Unknown = UnknownLostAtSender }), ViolationUnknownCause},
		{"a target with lost_at_sender and the marker", mutate(handedUnknown, func(r *Record) { r.Unknown, r.Lost = UnknownLostAtSender, true }), ViolationUnknownCause},
		{"a closed target shaped like a sender's replacement", asReplacement(closed), ViolationLostShape},
		{"a closed control record shaped like a sender's replacement", asReplacement(mutate(ctlDelivered, func(r *Record) { r.State = StateClosedUnconfirmed })), ViolationLostShape},
		{"a target handed over with the marker", mutate(handedUnknown, func(r *Record) { r.Lost = true }), ViolationUnknownCause},
		{"a control record handed over with the marker", mutate(ctlUnknown, func(r *Record) { r.Lost = true }), ViolationUnknownCause},
		{"an accepted target with the marker", mutate(accepted, func(r *Record) { r.Lost = true }), ViolationLostShape},
		{"a closed target with the marker", mutate(closed, func(r *Record) { r.Lost = true }), ViolationLostShape},
		{"a delivered target with the marker", mutate(delivered, func(r *Record) { r.Lost = true }), ViolationLostShape},
		{"a delivered control record with the marker", mutate(ctlDelivered, func(r *Record) { r.Lost = true }), ViolationLostShape},
		{"a source that learned of a loss at the target, with the marker", mutate(srcLostAtTarget, func(r *Record) { r.Lost = true }), ViolationUnknownCause},
		{"a target in delivery_unknown without a hand-out", mutate(accepted, func(r *Record) { r.State, r.Unknown = StateDeliveryUnknown, UnknownHandedOver }), ViolationDeliveryUnknownUnhandled},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := violationOf(t, r.rec.Valid()); got != r.want {
				t.Errorf("Valid: violation %s, want %s for %+v", got, r.want, r.rec)
			}
		})
	}

	t.Run("no fact or tick makes a target a loss", func(t *testing.T) {
		events := []Event{
			{Type: EventReceipt}, {Type: EventDelivered}, {Type: EventRejected, Reject: RejectEndpointClosed},
			unknown(0).ev, {Type: EventExpiryQueried, QueriedAt: at(afterExpiry)}, {Type: EventReportByPassed},
			{Type: EventNotified}, {Type: EventContactFailing, Cause: CauseNetwork, Since: t0},
			{Type: EventBlocked, Cause: CauseAdapterOffline}, {Type: EventUnblocked}, replyCommitted(),
			{Type: EventNoRecordAtTarget},
		}
		for name, base := range map[string]Record{"accepted": accepted, "handed out": handed, "handed over": handedUnknown, "control": controlRecord(), "control handed over": ctlUnknown} {
			for _, now := range []time.Time{at(time.Minute), base.ReportBy.Add(time.Hour)} {
				for _, ev := range events {
					after, _, err := base.Apply(now, ev)
					if err != nil {
						continue
					}
					if after.Lost || after.Unknown == UnknownLostAtTarget || after.Unknown == UnknownLostAtSender {
						t.Errorf("%s: event %d at %v left a loss on a target: %+v", name, ev.Type, now.Sub(t0), after)
					}
				}
				after, _, err := base.Tick(now)
				if err != nil || after.Lost || after.Unknown == UnknownLostAtTarget || after.Unknown == UnknownLostAtSender {
					t.Errorf("%s: Tick at %v: %+v %v", name, now.Sub(t0), after, err)
				}
			}
		}
	})
	t.Run("a target's report carries no cause, and an event names none", func(t *testing.T) {
		if _, ok := reflect.TypeOf(StatusReport{}).FieldByName("Unknown"); ok {
			t.Error("StatusReport has an Unknown field: a target never reports a loss")
		}
		if _, ok := reflect.TypeOf(Event{}).FieldByName("Unknown"); ok {
			t.Error("Event has an Unknown field: EventDeliveryUnknown is the target's hand-over, and only that")
		}
		if got := handedUnknown.StatusReport(); got != (StatusReport{State: StateDeliveryUnknown}) {
			t.Errorf("report of a delivery handed over: %+v", got)
		}
	})
	t.Run("the sender takes a reported delivery_unknown as handed over", func(t *testing.T) {
		src := sourceInState(t, "accepted")
		after, outs, err := src.ApplyStatusReport(at(time.Minute), StatusReport{State: StateDeliveryUnknown})
		if err != nil || after.Unknown != UnknownHandedOver || !sameKinds(kindsOf(outs[0].Feedback), []Kind{KindDeliveryUnknown}) {
			t.Errorf("record cause %s, outcomes %+v, err %v", after.Unknown, outs, err)
		}
	})
}
