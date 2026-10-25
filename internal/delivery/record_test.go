package delivery

import (
	"errors"
	"testing"
	"time"
)

// step is one event applied at an offset from t0.
type step struct {
	at time.Duration
	ev Event
}

func receipt(at time.Duration) step   { return step{at, Event{Type: EventReceipt}} }
func delivered(at time.Duration) step { return step{at, Event{Type: EventDelivered}} }
func unknown(at time.Duration) step {
	return step{at, Event{Type: EventDeliveryUnknown, Unknown: UnknownHandedOver}}
}
func rejectedBy(at time.Duration, why RejectReason) step {
	return step{at, Event{Type: EventRejected, Reject: why}}
}
func queried(at time.Duration) step {
	return step{at, Event{Type: EventExpiryQueried, QueriedAt: t0.Add(at)}}
}
func reportByPassed(at time.Duration) step { return step{at, Event{Type: EventReportByPassed}} }
func blocked(at time.Duration, c Cause) step {
	return step{at, Event{Type: EventBlocked, Cause: c}}
}
func unblocked(at time.Duration) step { return step{at, Event{Type: EventUnblocked}} }
func failing(at, since time.Duration, c Cause) step {
	return step{at, Event{Type: EventContactFailing, Cause: c, Since: t0.Add(since)}}
}

// Source records in these tests have their delivery deadline ten minutes after
// t0, so the expiry may be judged after t0+12m and report_by is t0+3h12m.
const (
	afterExpiry = 12*time.Minute + time.Second
	reportAt    = 10*time.Minute + ReportByOffset
)

func TestSourceTransitions(t *testing.T) {
	rows := []struct {
		rule         string
		steps        []step
		wantState    State
		wantFeedback []Kind // all feedback over the steps, in order
		wantLast     IgnoreReason
		wantErr      error
	}{
		{"normal progress emits nothing", []step{receipt(time.Second), delivered(time.Minute)}, StateDelivered, nil, IgnoreNone, nil},
		{"a delivered report straight from queued emits nothing", []step{delivered(time.Minute)}, StateDelivered, nil, IgnoreNone, nil},
		{"a rejection emits one rejected notice", []step{rejectedBy(time.Minute, RejectUnauthorized)}, StateRejected, []Kind{KindRejected}, IgnoreNone, nil},
		{"a repeated rejection report is ignored as duplicate", []step{rejectedBy(time.Minute, RejectUnauthorized), rejectedBy(2*time.Minute, RejectUnauthorized)}, StateRejected, []Kind{KindRejected}, IgnoreDuplicate, nil},
		{"a rejection without a reason is malformed", []step{rejectedBy(time.Minute, RejectNone)}, StateQueued, nil, IgnoreNone, ErrInvalidEvent},
		{"a rejection with an unknown reason is malformed", []step{rejectedBy(time.Minute, RejectReason(200))}, StateQueued, nil, IgnoreNone, ErrInvalidEvent},
		{"delivery_unknown emits one notice", []step{receipt(time.Second), unknown(time.Minute)}, StateDeliveryUnknown, []Kind{KindDeliveryUnknown}, IgnoreNone, nil},
		{"delivery_unknown corrected by delivery emits one correction", []step{receipt(time.Second), unknown(time.Minute), delivered(2 * time.Minute)}, StateDelivered, []Kind{KindDeliveryUnknown, KindUnknownCorrected}, IgnoreNone, nil},
		{"a repeated delivered report after correction is silent", []step{unknown(time.Minute), delivered(2 * time.Minute), delivered(3 * time.Minute)}, StateDelivered, []Kind{KindDeliveryUnknown, KindUnknownCorrected}, IgnoreDuplicate, nil},
		{"expiry is not judged at the deadline plus tolerance exactly", []step{queried(12 * time.Minute)}, StateQueued, nil, IgnoreNotDueYet, nil},
		{"expiry is not judged by a query made before the deadline plus tolerance", []step{{20 * time.Minute, Event{Type: EventExpiryQueried, QueriedAt: at(11 * time.Minute)}}}, StateQueued, nil, IgnoreNotDueYet, nil},
		{"expiry is judged by a query after the deadline plus tolerance", []step{queried(afterExpiry)}, StateExpiredUnconfirmed, []Kind{KindExpiredUnconfirmed}, IgnoreNone, nil},
		{"a repeated expiry judgment is ignored", []step{queried(afterExpiry), queried(afterExpiry + time.Minute)}, StateExpiredUnconfirmed, []Kind{KindExpiredUnconfirmed}, IgnoreNotApplicable, nil},
		{"expiry is not judged once a receipt is known", []step{receipt(time.Minute), queried(afterExpiry)}, StateAccepted, nil, IgnoreNotApplicable, nil},
		{"expired then delivered is corrected once", []step{queried(afterExpiry), delivered(20 * time.Minute), delivered(21 * time.Minute)}, StateDelivered, []Kind{KindExpiredUnconfirmed, KindExpiredCorrected}, IgnoreDuplicate, nil},
		{"expired then accepted then delivered is corrected once", []step{queried(afterExpiry), receipt(20 * time.Minute), delivered(30 * time.Minute)}, StateDelivered, []Kind{KindExpiredUnconfirmed, KindExpiredCorrected}, IgnoreNone, nil},
		{"expired then rejected gives the rejection, not a correction", []step{queried(afterExpiry), rejectedBy(20*time.Minute, RejectBacklogExpired)}, StateRejected, []Kind{KindExpiredUnconfirmed, KindRejected}, IgnoreNone, nil},
		{"expired then unknown then delivered gives each news once", []step{queried(afterExpiry), unknown(20 * time.Minute), delivered(30 * time.Minute)}, StateDelivered, []Kind{KindExpiredUnconfirmed, KindDeliveryUnknown, KindUnknownCorrected}, IgnoreNone, nil},
		{"a stale receipt after delivery_unknown is ignored", []step{unknown(time.Minute), receipt(2 * time.Minute)}, StateDeliveryUnknown, []Kind{KindDeliveryUnknown}, IgnoreStale, nil},
		{"a stale unknown report after delivery is ignored", []step{delivered(time.Minute), unknown(2 * time.Minute)}, StateDelivered, nil, IgnoreStale, nil},
		{"a rejection after delivery contradicts it", []step{delivered(time.Minute), rejectedBy(2*time.Minute, RejectUnauthorized)}, StateDelivered, nil, IgnoreNone, ErrIllegalTransition},
		{"a delivery after rejection contradicts it", []step{rejectedBy(time.Minute, RejectUnauthorized), delivered(2 * time.Minute)}, StateRejected, []Kind{KindRejected}, IgnoreNone, ErrIllegalTransition},
		{"a rejection after delivery_unknown contradicts it", []step{unknown(time.Minute), rejectedBy(2*time.Minute, RejectUnauthorized)}, StateDeliveryUnknown, []Kind{KindDeliveryUnknown}, IgnoreNone, ErrIllegalTransition},
		{"nothing closes a delivery before report_by", []step{reportByPassed(reportAt)}, StateQueued, nil, IgnoreNotDueYet, nil},
		{"a queued delivery closes silently after report_by", []step{reportByPassed(reportAt + time.Second)}, StateClosedUnconfirmed, nil, IgnoreNone, nil},
		{"an expired delivery closes silently after report_by", []step{queried(afterExpiry), reportByPassed(reportAt + time.Second)}, StateClosedUnconfirmed, []Kind{KindExpiredUnconfirmed}, IgnoreNone, nil},
		{"an accepted delivery closes silently after report_by", []step{receipt(time.Minute), reportByPassed(reportAt + time.Second)}, StateClosedUnconfirmed, nil, IgnoreNone, nil},
		{"a delivery_unknown delivery closes silently after report_by", []step{unknown(time.Minute), reportByPassed(reportAt + time.Second)}, StateClosedUnconfirmed, []Kind{KindDeliveryUnknown}, IgnoreNone, nil},
		{"a final delivery does not close", []step{delivered(time.Minute), reportByPassed(reportAt + time.Second)}, StateDelivered, nil, IgnoreAlreadyFinal, nil},
		{"facts after closing are ignored", []step{reportByPassed(reportAt + time.Second), delivered(reportAt + time.Minute)}, StateClosedUnconfirmed, nil, IgnoreAlreadyFinal, nil},
		{"an unknown event type is malformed", []step{{time.Minute, Event{Type: 99}}}, StateQueued, nil, IgnoreNone, ErrInvalidEvent},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			rec := NewSourceRecord(t0, at(10*time.Minute))
			var all []Kind
			var last Outcome
			var err error
			for _, s := range r.steps {
				rec, last, err = rec.Apply(t0.Add(s.at), s.ev)
				if err != nil {
					break
				}
				all = append(all, kindsOf(last.Feedback)...)
			}
			if !errors.Is(err, r.wantErr) {
				t.Fatalf("err = %v, want %v", err, r.wantErr)
			}
			if rec.State != r.wantState {
				t.Errorf("state = %s, want %s", rec.State, r.wantState)
			}
			if !sameKinds(all, r.wantFeedback) {
				t.Errorf("feedback = %v, want %v", all, r.wantFeedback)
			}
			if err == nil && last.Ignored != r.wantLast {
				t.Errorf("last ignored = %v, want %v", last.Ignored, r.wantLast)
			}
		})
	}
}

func TestRejectionReasonIsCarried(t *testing.T) {
	reasons := []RejectReason{
		RejectUnauthorized, RejectEndpointMissing, RejectEndpointClosed, RejectEndpointDeclined,
		RejectTooLarge, RejectExpired, RejectBacklogExpired, RejectSecondCheckFailed,
		RejectMalformed, RejectDeadlineOutOfRange, RejectBacklogFull,
	}
	for _, why := range reasons {
		t.Run(why.String(), func(t *testing.T) {
			rec := NewSourceRecord(t0, at(10*time.Minute))
			rec, out := mustApply(t, rec, at(time.Minute), Event{Type: EventRejected, Reject: why})
			if rec.Reject != why || len(out.Feedback) != 1 || out.Feedback[0].Reject != why {
				t.Errorf("record reason %v, feedback %+v", rec.Reject, out.Feedback)
			}
		})
	}
}

func TestUnreachableHint(t *testing.T) {
	rows := []struct {
		rule         string
		steps        []step
		wantFeedback []Kind
		wantCause    Cause
		wantLast     IgnoreReason
		wantErr      error
	}{
		{"contact failing less than two minutes gives no hint", []step{failing(2*time.Minute-time.Second, 0, CauseNetwork)}, nil, 0, IgnoreNotDueYet, nil},
		{"contact failing for two minutes gives the hint with the network cause", []step{failing(2*time.Minute, 0, CauseNetwork)}, []Kind{KindUnreachable}, CauseNetwork, IgnoreNone, nil},
		{"a suspended trust chain gives the hint with that cause", []step{failing(3*time.Minute, 0, CauseTrustSuspended)}, []Kind{KindUnreachable}, CauseTrustSuspended, IgnoreNone, nil},
		{"the hint is given once per delivery", []step{failing(3*time.Minute, 0, CauseNetwork), failing(4*time.Minute, 0, CauseTrustSuspended)}, []Kind{KindUnreachable}, CauseNetwork, IgnoreAlreadyReported, nil},
		{"no hint once the sender stopped retrying", []step{failing(10*time.Minute, 0, CauseNetwork)}, nil, 0, IgnoreRetryEnded, nil},
		{"no hint for a delivery the target already accepted", []step{receipt(time.Second), failing(3*time.Minute, 0, CauseNetwork)}, nil, 0, IgnoreNotApplicable, nil},
		{"a blocked-delivery cause is not an unreachable cause", []step{failing(3*time.Minute, 0, CauseAdapterOffline)}, nil, 0, IgnoreNone, ErrInvalidEvent},
		{"a missing start time is malformed", []step{{3 * time.Minute, Event{Type: EventContactFailing, Cause: CauseNetwork}}}, nil, 0, IgnoreNone, ErrInvalidEvent},
		{"the hint does not change the state", []step{failing(3*time.Minute, 0, CauseNetwork), delivered(5 * time.Minute)}, []Kind{KindUnreachable}, CauseNetwork, IgnoreNone, nil},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			rec := NewSourceRecord(t0, at(10*time.Minute))
			var all []Feedback
			var last Outcome
			var err error
			for _, s := range r.steps {
				rec, last, err = rec.Apply(t0.Add(s.at), s.ev)
				if err != nil {
					break
				}
				all = append(all, last.Feedback...)
			}
			if !errors.Is(err, r.wantErr) {
				t.Fatalf("err = %v, want %v", err, r.wantErr)
			}
			if !sameKinds(kindsOf(all), r.wantFeedback) {
				t.Fatalf("feedback = %v, want %v", kindsOf(all), r.wantFeedback)
			}
			if len(all) > 0 && all[0].Cause != r.wantCause {
				t.Errorf("cause = %v, want %v", all[0].Cause, r.wantCause)
			}
			if err == nil && last.Ignored != r.wantLast {
				t.Errorf("last ignored = %v, want %v", last.Ignored, r.wantLast)
			}
		})
	}
}

func TestDeliveryBlocked(t *testing.T) {
	rows := []struct {
		rule         string
		steps        []step
		wantState    State
		wantFeedback []Kind
		wantCause    []Cause // cause of each feedback
		wantLast     IgnoreReason
		wantErr      error
	}{
		{"a block is reported once with its cause and the delivery stays accepted", []step{receipt(time.Second), blocked(time.Hour, CauseAdapterOffline)}, StateAccepted, []Kind{KindDeliveryBlocked}, []Cause{CauseAdapterOffline}, IgnoreNone, nil},
		{"each block cause is accepted", []step{receipt(time.Second), blocked(time.Hour, CauseRuntimeAuth)}, StateAccepted, []Kind{KindDeliveryBlocked}, []Cause{CauseRuntimeAuth}, IgnoreNone, nil},
		{"the native-unavailable cause is accepted", []step{receipt(time.Second), blocked(time.Hour, CauseNativeUnavailable)}, StateAccepted, []Kind{KindDeliveryBlocked}, []Cause{CauseNativeUnavailable}, IgnoreNone, nil},
		{"a repeated block report is ignored", []step{receipt(time.Second), blocked(time.Hour, CauseAdapterOffline), blocked(2*time.Hour, CauseAdapterOffline)}, StateAccepted, []Kind{KindDeliveryBlocked}, []Cause{CauseAdapterOffline}, IgnoreDuplicate, nil},
		{"recovery after a reported block is corrected once", []step{receipt(time.Second), blocked(time.Hour, CauseAdapterOffline), unblocked(2 * time.Hour), unblocked(3 * time.Hour)}, StateAccepted, []Kind{KindDeliveryBlocked, KindBlockedRecovered}, []Cause{CauseAdapterOffline, CauseAdapterOffline}, IgnoreNotApplicable, nil},
		{"a second block of the same delivery is not reported again", []step{receipt(time.Second), blocked(time.Hour, CauseAdapterOffline), unblocked(2 * time.Hour), blocked(3*time.Hour, CauseRuntimeAuth), unblocked(4 * time.Hour)}, StateAccepted, []Kind{KindDeliveryBlocked, KindBlockedRecovered}, []Cause{CauseAdapterOffline, CauseAdapterOffline}, IgnoreNone, nil},
		{"delivery while still blocked reports the recovery", []step{receipt(time.Second), blocked(time.Hour, CauseRuntimeAuth), delivered(2 * time.Hour)}, StateDelivered, []Kind{KindDeliveryBlocked, KindBlockedRecovered}, []Cause{CauseRuntimeAuth, CauseRuntimeAuth}, IgnoreNone, nil},
		{"rejection while blocked reports the rejection only", []step{receipt(time.Second), blocked(time.Hour, CauseRuntimeAuth), rejectedBy(2*time.Hour, RejectBacklogExpired)}, StateRejected, []Kind{KindDeliveryBlocked, KindRejected}, []Cause{CauseRuntimeAuth, CauseNone}, IgnoreNone, nil},
		{"unblocking something never blocked is ignored", []step{receipt(time.Second), unblocked(time.Hour)}, StateAccepted, nil, nil, IgnoreNotApplicable, nil},
		{"a block on a delivery not yet accepted is ignored", []step{blocked(time.Hour, CauseAdapterOffline)}, StateQueued, nil, nil, IgnoreNotApplicable, nil},
		{"a network cause is not a block cause", []step{receipt(time.Second), blocked(time.Hour, CauseNetwork)}, StateAccepted, nil, nil, IgnoreNone, ErrInvalidEvent},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			rec := NewSourceRecord(t0, at(10*time.Minute))
			var all []Feedback
			var last Outcome
			var err error
			for _, s := range r.steps {
				rec, last, err = rec.Apply(t0.Add(s.at), s.ev)
				if err != nil {
					break
				}
				all = append(all, last.Feedback...)
			}
			if !errors.Is(err, r.wantErr) {
				t.Fatalf("err = %v, want %v", err, r.wantErr)
			}
			if rec.State != r.wantState {
				t.Errorf("state = %s, want %s", rec.State, r.wantState)
			}
			if !sameKinds(kindsOf(all), r.wantFeedback) {
				t.Fatalf("feedback = %v, want %v", kindsOf(all), r.wantFeedback)
			}
			for i, fb := range all {
				if fb.Cause != r.wantCause[i] {
					t.Errorf("feedback %d cause = %v, want %v", i, fb.Cause, r.wantCause[i])
				}
			}
			if err == nil && last.Ignored != r.wantLast {
				t.Errorf("last ignored = %v, want %v", last.Ignored, r.wantLast)
			}
		})
	}
}

func TestTargetRecordProducesNoFeedback(t *testing.T) {
	rows := []struct {
		rule      string
		handedOut bool
		ev        Event
		want      State
	}{
		{"a rejection on the target emits no feedback", false, Event{Type: EventRejected, Reject: RejectBacklogExpired}, StateRejected},
		{"a closure after a hand-out on the target emits no feedback", true, Event{Type: EventRejected, Reject: RejectEndpointClosed}, StateDeliveryUnknown},
		{"delivery_unknown on the target emits no feedback", true, Event{Type: EventDeliveryUnknown, Unknown: UnknownHandedOver}, StateDeliveryUnknown},
		{"a block on the target emits no feedback", false, Event{Type: EventBlocked, Cause: CauseAdapterOffline}, StateAccepted},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			rec := targetRecord(t, t0, DefaultEndpointMaxQueue)
			if r.handedOut {
				rec = handOut(t, rec, at(30*time.Second), 1, 1)
			}
			rec, out := mustApply(t, rec, at(time.Minute), r.ev)
			if len(out.Feedback) != 0 || rec.State != r.want || rec.Reported != 0 {
				t.Errorf("state %s feedback %+v reported %b", rec.State, out.Feedback, rec.Reported)
			}
		})
	}
	t.Run("delivery on the target through acknowledgement emits no feedback", func(t *testing.T) {
		rec := handOut(t, targetRecord(t, t0, DefaultEndpointMaxQueue), at(30*time.Second), 1, 1)
		rec, v, out := ack(t, rec, at(time.Minute), 1)
		if v != AckCompleted || len(out.Feedback) != 0 || rec.State != StateDelivered || rec.Reported != 0 {
			t.Errorf("verdict %v state %s feedback %+v reported %b", v, rec.State, out.Feedback, rec.Reported)
		}
	})
}

func TestTargetDeliveryUnknownNeedsHandout(t *testing.T) {
	t.Run("delivery_unknown without a body that was handed out is illegal", func(t *testing.T) {
		_, _, err := targetRecord(t, t0, time.Hour).Apply(at(time.Minute), Event{Type: EventDeliveryUnknown, Unknown: UnknownHandedOver})
		if !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("err = %v, want ErrIllegalTransition", err)
		}
	})
	t.Run("a refused delivery_unknown leaves the record as it was", func(t *testing.T) {
		rec := targetRecord(t, t0, time.Hour)
		after, _, _ := rec.Apply(at(time.Minute), Event{Type: EventDeliveryUnknown, Unknown: UnknownHandedOver})
		if after.State != StateAccepted {
			t.Errorf("state %s", after.State)
		}
	})
	t.Run("after a hand-out delivery_unknown is legal", func(t *testing.T) {
		rec := handOut(t, targetRecord(t, t0, time.Hour), at(30*time.Second), 1, 1)
		rec, _ = mustApply(t, rec, at(time.Minute), Event{Type: EventDeliveryUnknown, Unknown: UnknownHandedOver})
		if rec.State != StateDeliveryUnknown {
			t.Errorf("state %s", rec.State)
		}
	})
}

func TestSideRules(t *testing.T) {
	rows := []struct {
		rule string
		rec  func(*testing.T) Record
		ev   Event
	}{
		{"expiry is judged by the sender only", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) }, Event{Type: EventExpiryQueried, QueriedAt: at(time.Hour)}},
		{"unreachability is seen by the sender only", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) }, Event{Type: EventContactFailing, Cause: CauseNetwork, Since: t0}},
		{"prompts are handed out by the target only", func(t *testing.T) Record { return NewSourceRecord(t0, at(10*time.Minute)) }, Event{Type: EventNotified}},
		{"a target record cannot be marked delivered except by acknowledging every chunk", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) }, Event{Type: EventDelivered}},
		{"a handed-out target record cannot be marked delivered either", func(t *testing.T) Record {
			return handOut(t, targetRecord(t, t0, time.Hour), at(time.Minute), 1, 1)
		}, Event{Type: EventDelivered}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			_, _, err := r.rec(t).Apply(at(time.Hour), r.ev)
			if !errors.Is(err, ErrWrongSide) {
				t.Errorf("err = %v, want ErrWrongSide", err)
			}
		})
	}
	t.Run("an accepted source record has no queue: time alone does not reject it before report_by minus the margin", func(t *testing.T) {
		acceptBefore := at(10 * time.Minute)
		rec, _ := mustApply(t, NewSourceRecord(t0, acceptBefore), at(time.Second), Event{Type: EventReceipt})
		// Far past where a target's backlog deadline (at most MaxQueueCap after
		// acceptance) would fall, yet before the settle deadline.
		now := at(10*time.Minute + MaxQueueCap + 30*time.Minute)
		if !now.Before(SettleBy(rec.ReportBy)) {
			t.Fatalf("test instant %v is not before the settle deadline %v", now, SettleBy(rec.ReportBy))
		}
		after, outs, err := rec.Tick(now)
		if err != nil || after.State != StateAccepted || len(outs) != 0 {
			t.Errorf("state %s outcomes %+v err %v", after.State, outs, err)
		}
	})
}

func TestNotifiedSubState(t *testing.T) {
	t.Run("a prompt marks the accepted delivery notified without feedback or a state change", func(t *testing.T) {
		rec := targetRecord(t, t0, time.Hour)
		rec, out := mustApply(t, rec, at(time.Minute), Event{Type: EventNotified})
		if rec.Sub != SubNotified || rec.State != StateAccepted || len(out.Feedback) != 0 || out.Result != ResultApplied {
			t.Errorf("sub %v state %s outcome %+v", rec.Sub, rec.State, out)
		}
	})
	t.Run("a second prompt is a duplicate", func(t *testing.T) {
		rec := targetRecord(t, t0, time.Hour)
		rec, _ = mustApply(t, rec, at(time.Minute), Event{Type: EventNotified})
		_, out := mustApply(t, rec, at(2*time.Minute), Event{Type: EventNotified})
		if out.Ignored != IgnoreDuplicate {
			t.Errorf("ignored = %v", out.Ignored)
		}
	})
	t.Run("a prompt for a delivered message is ignored", func(t *testing.T) {
		rec := deliverBody(t, targetRecord(t, t0, time.Hour), at(time.Minute))
		_, out := mustApply(t, rec, at(2*time.Minute), Event{Type: EventNotified})
		if out.Ignored != IgnoreNotApplicable {
			t.Errorf("ignored = %v", out.Ignored)
		}
	})
	t.Run("leaving accepted clears the notified sub-state", func(t *testing.T) {
		rec := targetRecord(t, t0, time.Hour)
		rec, _ = mustApply(t, rec, at(time.Minute), Event{Type: EventNotified})
		rec = deliverBody(t, rec, at(2*time.Minute))
		if rec.Sub != SubNone {
			t.Errorf("sub = %v", rec.Sub)
		}
	})
}
