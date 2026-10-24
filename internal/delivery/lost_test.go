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

func unknownWith(cause UnknownCause) Event {
	return Event{Type: EventDeliveryUnknown, Unknown: cause}
}

func newLost(t *testing.T, side Side, control bool) (Record, Outcome) {
	t.Helper()
	r, out, err := NewLostRecord(side, control, lostAt)
	if err != nil {
		t.Fatalf("NewLostRecord(%v, %v): %v", side, control, err)
	}
	return r, out
}

func TestNewLostRecordStartsDeliveryUnknownWithTheSidesLossCause(t *testing.T) {
	rows := []struct {
		rule    string
		side    Side
		control bool
		cause   UnknownCause
	}{
		{"a source message record is lost at the sender", SideSource, false, UnknownLostAtSender},
		{"a target message record is lost at the target", SideTarget, false, UnknownLostAtTarget},
		{"a target control record is lost at the target and stays a control record", SideTarget, true, UnknownLostAtTarget},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			rec, out := newLost(t, r.side, r.control)
			if rec.Side != r.side || rec.State != StateDeliveryUnknown || rec.Unknown != r.cause || rec.Control != r.control {
				t.Errorf("record %+v", rec)
			}
			if want := lostReportBy(lostAt); !rec.ReportBy.Equal(want) || !rec.ReportBy.Equal(LostReportBy(lostAt)) {
				t.Errorf("ReportBy %v, want %v", rec.ReportBy, want)
			}
			if !rec.AcceptBefore.IsZero() || !rec.BacklogDeadline.IsZero() {
				t.Errorf("AcceptBefore %v and BacklogDeadline %v are unknown and stay zero", rec.AcceptBefore, rec.BacklogDeadline)
			}
			if rec.Reject != RejectNone || rec.Blocked || rec.Lease.Handouts != 0 || rec.Lease.Active || rec.Sub != SubNone {
				t.Errorf("a lost record carries history it does not have: %+v", rec)
			}
			if out.Result != ResultApplied || out.From != StateInvalid || out.To != StateDeliveryUnknown {
				t.Errorf("outcome %+v", out)
			}
		})
	}
	invalid := []struct {
		rule    string
		side    Side
		control bool
		at      time.Time
	}{
		{"a control record exists only on the target", SideSource, true, lostAt},
		{"the zero side is invalid", 0, false, lostAt},
		{"an unknown side is invalid", 9, false, lostAt},
		{"a zero discovery time is invalid", SideTarget, false, time.Time{}},
	}
	for _, r := range invalid {
		t.Run(r.rule, func(t *testing.T) {
			rec, out, err := NewLostRecord(r.side, r.control, r.at)
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput", err)
			}
			if !reflect.DeepEqual(rec, Record{}) || !reflect.DeepEqual(out, Outcome{}) {
				t.Errorf("a refused input returned %+v %+v", rec, out)
			}
		})
	}
}

// TestLostReportByBoundsEveryAcceptedDeadline checks the bound against an
// oracle of what Intake accepts, written here: a request accepted at a is
// accepted with any deadline from a up to a plus one hour plus two minutes, its
// report_by is that deadline plus three hours and two minutes, and the loss is
// discovered no earlier than the acceptance.
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
	rec, out := newLost(t, SideSource, false)
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
			unknownWith(UnknownHandedOver), unknownWith(UnknownLostAtTarget),
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

func TestTargetLostRecordProducesNoFeedbackAndReportsLostAtTarget(t *testing.T) {
	for _, control := range []bool{false, true} {
		name := "message"
		if control {
			name = "control"
		}
		t.Run(name, func(t *testing.T) {
			rec, out := newLost(t, SideTarget, control)
			if len(out.Feedback) != 0 || rec.Reported != 0 {
				t.Errorf("feedback %+v reported %b", out.Feedback, rec.Reported)
			}
			want := StatusReport{State: StateDeliveryUnknown, Unknown: UnknownLostAtTarget}
			if got := rec.StatusReport(); got != want {
				t.Errorf("StatusReport() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestApplyRefusesEveryOtherWayIntoStorageLoss(t *testing.T) {
	handedOutTarget := func(t *testing.T) Record {
		return handOut(t, targetRecord(t, t0, time.Hour), at(30*time.Second), 1, 1)
	}
	rows := []struct {
		rule string
		rec  func(*testing.T) Record
		ev   Event
		want error
	}{
		{"a source record: delivery_unknown without a cause", func(*testing.T) Record { return NewSourceRecord(at(10 * time.Minute)) }, unknownWith(UnknownCauseNone), ErrInvalidEvent},
		{"a target record: delivery_unknown without a cause", handedOutTarget, unknownWith(UnknownCauseNone), ErrInvalidEvent},
		{"a control record: delivery_unknown without a cause", func(*testing.T) Record { return controlRecord() }, unknownWith(UnknownCauseNone), ErrInvalidEvent},
		{"a source record: lost_at_sender is never a reported fact", func(*testing.T) Record { return NewSourceRecord(at(10 * time.Minute)) }, unknownWith(UnknownLostAtSender), ErrInvalidEvent},
		{"a target record: lost_at_sender is never a reported fact", handedOutTarget, unknownWith(UnknownLostAtSender), ErrInvalidEvent},
		{"an out-of-range cause is no cause", func(*testing.T) Record { return NewSourceRecord(at(10 * time.Minute)) }, unknownWith(UnknownCause(200)), ErrInvalidEvent},
		{"a target message record without a hand-out cannot become lost_at_target", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) }, unknownWith(UnknownLostAtTarget), ErrIllegalTransition},
		{"a handed-out target record cannot become lost_at_target", handedOutTarget, unknownWith(UnknownLostAtTarget), ErrIllegalTransition},
		{"a target control record cannot become lost_at_target", func(*testing.T) Record { return controlRecord() }, unknownWith(UnknownLostAtTarget), ErrIllegalTransition},
		{"a target message record without a hand-out cannot become handed over", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) }, unknownWith(UnknownHandedOver), ErrIllegalTransition},
		{"a lost target record cannot be put into a loss again", func(t *testing.T) Record { r, _ := newLost(t, SideTarget, false); return r }, unknownWith(UnknownLostAtTarget), ErrIllegalTransition},
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
	t.Run("a refused event is also refused through a status report", func(t *testing.T) {
		src := NewSourceRecord(at(10 * time.Minute))
		if _, _, err := src.ApplyStatusReport(at(time.Minute), StatusReport{State: StateDeliveryUnknown, Unknown: UnknownLostAtSender}); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("err = %v, want ErrInvalidEvent", err)
		}
	})
}

func TestSourceAppliesTargetLossOnceWithTargetWording(t *testing.T) {
	lost := StatusReport{State: StateDeliveryUnknown, Unknown: UnknownLostAtTarget}
	prepare := map[string]func(*testing.T) Record{
		"queued": func(*testing.T) Record { return NewSourceRecord(at(10 * time.Minute)) },
		"accepted": func(t *testing.T) Record {
			r, _ := mustApply(t, NewSourceRecord(at(10*time.Minute)), at(time.Second), Event{Type: EventReceipt})
			return r
		},
		"expired_unconfirmed": func(t *testing.T) Record {
			r, _ := mustApply(t, NewSourceRecord(at(10*time.Minute)), at(afterExpiry), Event{Type: EventExpiryQueried, QueriedAt: at(afterExpiry)})
			return r
		},
	}
	for name, mk := range prepare {
		t.Run("from "+name+" the sender hears of the loss once", func(t *testing.T) {
			rec := mk(t)
			rec, outs, err := rec.ApplyStatusReport(at(20*time.Minute), lost)
			if err != nil {
				t.Fatal(err)
			}
			var got []Kind
			for _, o := range outs {
				got = append(got, kindsOf(o.Feedback)...)
			}
			if !sameKinds(got, []Kind{KindLostAtTarget}) || rec.State != StateDeliveryUnknown || rec.Unknown != UnknownLostAtTarget {
				t.Fatalf("kinds %v state %s cause %s", got, rec.State, rec.Unknown)
			}
			if !rec.Reported.Has(KindLostAtTarget) {
				t.Errorf("Reported %b", rec.Reported)
			}
			again, outs, err := rec.ApplyStatusReport(at(21*time.Minute), lost)
			if err != nil || len(outs) != 1 || outs[0].Ignored != IgnoreDuplicate || len(outs[0].Feedback) != 0 || !reflect.DeepEqual(again, rec) {
				t.Errorf("the same report again: %+v %v", outs, err)
			}
		})
	}
	t.Run("an expired delivery's earlier notice is replaced, not corrected", func(t *testing.T) {
		rec := prepare["expired_unconfirmed"](t)
		rec, outs, _ := rec.ApplyStatusReport(at(20*time.Minute), lost)
		if got := kindsOf(outs[0].Feedback); !sameKinds(got, []Kind{KindLostAtTarget}) {
			t.Errorf("feedback %v", got)
		}
		_ = rec
	})
	t.Run("from delivery_unknown (handed over) the report is a duplicate", func(t *testing.T) {
		rec, _ := mustApply(t, NewSourceRecord(at(10*time.Minute)), at(time.Minute), unknownWith(UnknownHandedOver))
		after, outs, err := rec.ApplyStatusReport(at(20*time.Minute), lost)
		if err != nil || outs[0].Ignored != IgnoreDuplicate || len(outs[0].Feedback) != 0 || !reflect.DeepEqual(after, rec) {
			t.Errorf("%+v %v", outs, err)
		}
	})
	t.Run("from delivered the report is stale", func(t *testing.T) {
		rec, _ := mustApply(t, NewSourceRecord(at(10*time.Minute)), at(time.Minute), Event{Type: EventDelivered})
		after, outs, err := rec.ApplyStatusReport(at(20*time.Minute), lost)
		if err != nil || outs[0].Ignored != IgnoreStale || len(outs[0].Feedback) != 0 || !reflect.DeepEqual(after, rec) {
			t.Errorf("%+v %v", outs, err)
		}
	})
	t.Run("from closed_unconfirmed the report is already final", func(t *testing.T) {
		rec := NewSourceRecord(at(10 * time.Minute))
		rec, _ = mustApply(t, rec, at(reportAt+time.Second), Event{Type: EventReportByPassed})
		_, outs, err := rec.ApplyStatusReport(at(reportAt+time.Minute), lost)
		if err != nil || outs[0].Ignored != IgnoreAlreadyFinal {
			t.Errorf("%+v %v", outs, err)
		}
	})
	t.Run("a later delivery corrects the loss", func(t *testing.T) {
		rec := NewSourceRecord(at(10 * time.Minute))
		rec, _, _ = rec.ApplyStatusReport(at(time.Minute), lost)
		rec, o := mustApply(t, rec, at(2*time.Minute), Event{Type: EventDelivered})
		if got := kindsOf(o.Feedback); !sameKinds(got, []Kind{KindUnknownCorrected}) || rec.Unknown != UnknownCauseNone {
			t.Errorf("feedback %v cause %s", got, rec.Unknown)
		}
	})
}

func TestSourceLostRecordIgnoresLaterFacts(t *testing.T) {
	rec, _ := newLost(t, SideSource, false)
	facts := []struct {
		rule string
		ev   Event
	}{
		{"a receipt", Event{Type: EventReceipt}},
		{"a delivery", Event{Type: EventDelivered}},
		{"a rejection", Event{Type: EventRejected, Reject: RejectBacklogExpired}},
		{"an unknown result handed over", unknownWith(UnknownHandedOver)},
		{"an unknown result lost at the target", unknownWith(UnknownLostAtTarget)},
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
			{State: StateDeliveryUnknown, Unknown: UnknownHandedOver}, {State: StateDeliveryUnknown, Unknown: UnknownLostAtTarget},
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

func TestTargetLostRecordStaysUnknownUnderForcedClosure(t *testing.T) {
	rec, _ := newLost(t, SideTarget, false)
	for _, why := range []RejectReason{RejectEndpointClosed, RejectBacklogExpired, RejectSecondCheckFailed} {
		t.Run("forced closure for "+why.String()+" keeps it unknown", func(t *testing.T) {
			after, out, err := rec.Apply(lostAt.Add(time.Minute), Event{Type: EventRejected, Reject: why})
			if err != nil || out.Result != ResultIgnored || out.Ignored != IgnoreDuplicate || after.State != StateDeliveryUnknown || after.Reject != RejectNone || !reflect.DeepEqual(after, rec) {
				t.Errorf("state %s reject %s out %+v err %v", after.State, after.Reject, out, err)
			}
		})
	}
	t.Run("a late acknowledgement is refused for lack of a hand-out", func(t *testing.T) {
		after, v, out, err := rec.Ack(lostAt.Add(time.Minute), Ack{Part: 1, Lease: freshLease(), TokenMatches: true})
		if err != nil || v != AckRefusedNoHandout || len(out.Feedback) != 0 || !reflect.DeepEqual(after, rec) {
			t.Errorf("verdict %v err %v", v, err)
		}
	})
	t.Run("recv never leases it", func(t *testing.T) {
		after, res, err := rec.Handout(lostAt.Add(time.Minute), AuthorizationAllow, 1, 1, freshLease())
		if err != nil || res.Action != HandoutUnavailable || res.Reason != UnavailableNotQueued || !reflect.DeepEqual(after, rec) {
			t.Errorf("result %+v err %v", res, err)
		}
	})
	t.Run("it does not occupy a backlog slot", func(t *testing.T) {
		if rec.OccupiesBacklogSlot(authz.TrafficMessage) {
			t.Error("a lost record occupies a slot")
		}
	})
	t.Run("it closes after its report_by and not before", func(t *testing.T) {
		same, outs, err := rec.Tick(rec.ReportBy)
		if err != nil || len(outs) != 0 || !reflect.DeepEqual(same, rec) {
			t.Fatalf("Tick at ReportBy: %+v %v", outs, err)
		}
		if next := rec.NextTick(); !next.Equal(rec.ReportBy.Add(time.Nanosecond)) {
			t.Errorf("NextTick = %v, want the instant after ReportBy %v", next, rec.ReportBy)
		}
		closed, _, err := rec.Tick(rec.ReportBy.Add(time.Nanosecond))
		if err != nil || closed.State != StateClosedUnconfirmed {
			t.Errorf("state %s err %v", closed.State, err)
		}
	})
	t.Run("a lost control record is still corrected by a committed reply", func(t *testing.T) {
		ctl, _ := newLost(t, SideTarget, true)
		after, _ := mustApply(t, ctl, lostAt.Add(time.Minute), replyCommitted())
		if after.State != StateDelivered || after.Unknown != UnknownCauseNone {
			t.Errorf("state %s cause %s", after.State, after.Unknown)
		}
		same, out, err := ctl.Apply(lostAt.Add(time.Minute), Event{Type: EventRejected, Reject: RejectEndpointClosed})
		if err != nil || out.Ignored != IgnoreDuplicate || !reflect.DeepEqual(same, ctl) {
			t.Errorf("forced closure of a lost control record: %+v %v", out, err)
		}
	})
}

func TestStorageLossWordingVectors(t *testing.T) {
	vectors := []struct {
		kind    Kind
		name    string
		summary string
	}{
		{KindLostAtSender, "lost_at_sender", "this node lost what it had stored about the message to storage corruption, so whether it was delivered, or for a control signal executed, is unknown; do not assume it was not delivered"},
		{KindLostAtTarget, "lost_at_target", "the target node lost what it had stored about the message to storage corruption, so whether it was delivered, or for a control signal executed, is unknown; do not assume it was not delivered"},
	}
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			if v.kind.String() != v.name || v.kind.Summary() != v.summary {
				t.Errorf("%q / %q", v.kind.String(), v.kind.Summary())
			}
			if strings.Contains(v.kind.Summary(), "handed") {
				t.Error("the wording must not say the target handed the delivery over")
			}
			if !v.kind.valid() {
				t.Error("kind is not valid")
			}
		})
	}
	if KindLostAtSender != KindUnknownCorrected+1 || KindLostAtTarget != KindUnknownCorrected+2 {
		t.Error("the new kinds are appended after the existing ones")
	}
	existing := map[Kind][2]string{
		KindUnreachable:        {"unreachable", "the target is not reachable; delivery is still being retried until the delivery deadline"},
		KindDeliveryBlocked:    {"delivery_blocked", "the message is at the target but cannot be put in front of the agent; it stays queued"},
		KindBlockedRecovered:   {"blocked_recovered", "the problem that blocked delivery is gone; the message is no longer blocked"},
		KindExpiredUnconfirmed: {"expired_unconfirmed", "the delivery deadline passed without a confirmation; the target may have received the message, so do not assume it was not delivered"},
		KindExpiredCorrected:   {"expired_corrected", "correction: the target did receive the message that was reported as unconfirmed"},
		KindRejected:           {"rejected", "the target rejected the message; it was not delivered"},
		KindDeliveryUnknown:    {"delivery_unknown", "the target handed it over, to the agent or, for a control signal, to the adapter that executes it, but whether it was seen or executed is unknown; do not assume it was not delivered"},
		KindUnknownCorrected:   {"unknown_corrected", "correction: the message whose result was unknown was delivered"},
	}
	for k, want := range existing {
		if k.String() != want[0] || k.Summary() != want[1] {
			t.Errorf("kind %d changed: %q / %q", k, k.String(), k.Summary())
		}
	}
	if (KindLostAtTarget + 1).valid() || Kind(0).valid() {
		t.Error("validity bounds")
	}
	for c, name := range map[UnknownCause]string{UnknownCauseNone: "none", UnknownHandedOver: "handed_over", UnknownLostAtSender: "lost_at_sender", UnknownLostAtTarget: "lost_at_target", 200: "invalid"} {
		if c.String() != name {
			t.Errorf("cause %d: %q, want %q", c, c.String(), name)
		}
	}
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

func TestStatusReportCarriesUnknownCause(t *testing.T) {
	t.Run("a target's report names the cause of its record", func(t *testing.T) {
		handed := handOut(t, targetRecord(t, t0, time.Hour), at(30*time.Second), 1, 1)
		handed, _ = mustApply(t, handed, at(time.Minute), unknownWith(UnknownHandedOver))
		if got := handed.StatusReport(); got != (StatusReport{State: StateDeliveryUnknown, Unknown: UnknownHandedOver}) {
			t.Errorf("handed over: %+v", got)
		}
		ctl, _ := mustApply(t, controlRecord(), at(time.Minute), unknownWith(UnknownHandedOver))
		if got := ctl.StatusReport(); got.Unknown != UnknownHandedOver {
			t.Errorf("control: %+v", got)
		}
		if got := targetRecord(t, t0, time.Hour).StatusReport(); got.Unknown != UnknownCauseNone {
			t.Errorf("accepted: %+v", got)
		}
	})
	t.Run("applying a report passes the cause into the event", func(t *testing.T) {
		for cause, want := range map[UnknownCause]Kind{UnknownHandedOver: KindDeliveryUnknown, UnknownLostAtTarget: KindLostAtTarget} {
			rec, outs, err := NewSourceRecord(at(10*time.Minute)).ApplyStatusReport(at(time.Minute), StatusReport{State: StateDeliveryUnknown, Unknown: cause})
			if err != nil || rec.Unknown != cause || !sameKinds(kindsOf(outs[0].Feedback), []Kind{want}) {
				t.Errorf("cause %s: record cause %s, feedback %+v, err %v", cause, rec.Unknown, outs, err)
			}
		}
	})
	t.Run("a cause comes only with delivery_unknown", func(t *testing.T) {
		src := NewSourceRecord(at(10 * time.Minute))
		bad := []StatusReport{
			{State: StateAccepted, Unknown: UnknownHandedOver},
			{State: StateDelivered, Unknown: UnknownLostAtTarget},
			{State: StateRejected, Reject: RejectExpired, Unknown: UnknownLostAtTarget},
			{State: StateClosedUnconfirmed, Unknown: UnknownHandedOver},
			{State: StateDeliveryUnknown},
		}
		for _, rep := range bad {
			after, outs, err := src.ApplyStatusReport(at(time.Minute), rep)
			if !errors.Is(err, ErrInvalidInput) || outs != nil || !reflect.DeepEqual(after, src) {
				t.Errorf("report %+v: err %v", rep, err)
			}
			if !errors.Is(rep.Validate(), ErrInvalidInput) {
				t.Errorf("report %+v: Validate = %v", rep, rep.Validate())
			}
		}
		for _, rep := range []StatusReport{{State: StateAccepted}, {State: StateDeliveryUnknown, Unknown: UnknownHandedOver}, {State: StateDeliveryUnknown, Unknown: UnknownLostAtTarget}} {
			if err := rep.Validate(); err != nil {
				t.Errorf("report %+v: Validate = %v", rep, err)
			}
		}
	})
}

func TestLostDedupRecordAnswersEveryRetryAsReplay(t *testing.T) {
	key := DedupKey{SenderNode: node(1), Message: "m1", Recipient: endpoint(9)}
	rec := NewLostDedupRecord(key, lostAt)
	if rec.Key != key || !rec.DigestLost || rec.HasReply || rec.Reply != nil {
		t.Errorf("record %+v", rec)
	}
	if want := lostReportBy(lostAt); !rec.RetainUntil.Equal(want) || !rec.RetainUntil.Equal(DedupRetainUntil(LostReportBy(lostAt))) {
		t.Errorf("RetainUntil %v, want %v", rec.RetainUntil, want)
	}
	for _, d := range []BodyDigest{{}, {1}, {2}, {0xff, 0xff}} {
		if got := CheckDedup(&rec, d); got != DedupReplay {
			t.Errorf("digest %x: %v, want replay", d[:2], got)
		}
	}
	healthy := NewDedupRecord(key, BodyDigest{1}, ReportBy(at(10*time.Minute)))
	if healthy.DigestLost || CheckDedup(&healthy, BodyDigest{2}) != DedupConflict {
		t.Error("an ordinary record still detects a conflict")
	}
}

func TestNeedsBodyByStateAndSide(t *testing.T) {
	src := func(steps ...step) Record {
		r := NewSourceRecord(at(10 * time.Minute))
		for _, s := range steps {
			r, _ = mustApply(t, r, t0.Add(s.at), s.ev)
		}
		return r
	}
	srcLost, _ := newLost(t, SideSource, false)
	tgtLost, _ := newLost(t, SideTarget, false)
	ctlLost, _ := newLost(t, SideTarget, true)
	queued := targetRecord(t, t0, time.Hour)
	notified, _ := mustApply(t, queued, at(time.Second), Event{Type: EventNotified})
	blocked, _ := mustApply(t, queued, at(time.Second), Event{Type: EventBlocked, Cause: CauseAdapterOffline})
	leased := handOut(t, queued, at(time.Minute), 2, 1)
	unknownT, _ := mustApply(t, leased, at(2*time.Minute), unknownWith(UnknownHandedOver))
	rejectedT, _ := mustApply(t, queued, at(time.Second), Event{Type: EventRejected, Reject: RejectBacklogExpired})
	closedT, _, _ := queued.Tick(queued.ReportBy.Add(time.Second))
	ctlUnknown, _ := mustApply(t, controlRecord(), at(time.Minute), unknownWith(UnknownHandedOver))
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
		{"a lost target message", tgtLost, false},
		{"an accepted target control record", controlRecord(), false},
		{"a delivery_unknown target control record", ctlUnknown, false},
		{"a delivered target control record", ctlDelivered, false},
		{"a lost target control record", ctlLost, false},
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

// lostModel is the oracle of the lost-record lifecycle: it knows only the
// instants and the kinds of event, not how the record decides.
type lostModel struct {
	side     Side
	control  bool
	discover time.Time
	closed   bool // the record was closed by a Tick or EventReportByPassed after the end
	final    bool // delivered by a committed reply
	feedback int
}

func (m *lostModel) reportBy() time.Time { return lostReportBy(m.discover) }

func TestPropertyLostRecordLifecycle(t *testing.T) {
	const runs = 3000
	hits := map[string]int{}
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < runs; i++ {
		var m lostModel
		switch i % 3 {
		case 0:
			m.side = SideSource
		case 1:
			m.side = SideTarget
		default:
			m.side, m.control = SideTarget, true
		}
		m.discover = t0.Add(time.Duration(rng.Intn(600)) * time.Minute)
		rec, out, err := NewLostRecord(m.side, m.control, m.discover)
		if err != nil {
			t.Fatal(err)
		}
		m.feedback = len(out.Feedback)
		if m.side == SideSource && m.feedback != 1 {
			t.Fatalf("run %d: a lost source record gave %d notices at birth", i, m.feedback)
		}
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
			switch k := rng.Intn(9); {
			case k == 0 && m.side == SideTarget && !m.control:
				var res HandoutResult
				rec, res, e = rec.Handout(now, AuthorizationAllow, 1, 1, freshLease())
				if res.Action == HandoutGranted || res.Action == HandoutResent {
					t.Fatalf("run %d: a lost record was leased", i)
				}
				hits["handout refused"]++
			case k == 1 && m.side == SideTarget && !m.control:
				var v AckVerdict
				rec, v, o, e = rec.Ack(now, Ack{Part: 1, Lease: freshLease(), TokenMatches: true})
				if v == AckCompleted || v == AckRecorded {
					t.Fatalf("run %d: a lost record acknowledged a chunk", i)
				}
				hits["ack refused"]++
			case k == 2:
				rec, outs, e = rec.Tick(now)
				if pastEnd && !m.final {
					m.closed = true
				}
				hits["tick"]++
			case k == 3:
				rec, o, e = rec.Apply(now, Event{Type: EventReportByPassed})
				if pastEnd && !m.final {
					m.closed = true
				}
				hits["report_by passed"]++
			case k == 4 && m.side == SideTarget:
				rec, o, e = rec.Apply(now, Event{Type: EventRejected, Reject: []RejectReason{RejectEndpointClosed, RejectBacklogExpired, RejectSecondCheckFailed}[rng.Intn(3)]})
				if m.closed || m.final {
					break
				}
				if o.Ignored != IgnoreDuplicate {
					t.Fatalf("run %d: forced closure of a lost target record: %+v", i, o)
				}
				hits["forced closure ignored"]++
			case k == 5 && m.side == SideTarget && m.control:
				rec, o, e = rec.Apply(now, replyCommitted())
				if !m.closed && !m.final {
					m.final = true
					hits["control reply corrects"]++
				}
			case k == 6 && m.side == SideSource:
				facts := []Event{{Type: EventReceipt}, {Type: EventDelivered}, {Type: EventRejected, Reject: RejectUnauthorized}, unknownWith(UnknownHandedOver), unknownWith(UnknownLostAtTarget)}
				rec, o, e = rec.Apply(now, facts[rng.Intn(len(facts))])
				switch {
				case m.closed:
					if o.Ignored != IgnoreAlreadyFinal {
						t.Fatalf("run %d: a fact after closing: %+v", i, o)
					}
				case o.Ignored != IgnoreRecordLost:
					t.Fatalf("run %d: a fact for a lost source record: %+v", i, o)
				}
				hits["source fact ignored"]++
			case k == 7 && m.side == SideSource:
				reps := []StatusReport{{State: StateAccepted}, {State: StateDelivered}, {State: StateDeliveryUnknown, Unknown: UnknownLostAtTarget}}
				var os []Outcome
				rec, os, e = rec.ApplyStatusReport(now, reps[rng.Intn(len(reps))])
				for _, x := range os {
					m.feedback += len(x.Feedback)
				}
				hits["source report applied"]++
			default:
				if m.side == SideTarget {
					rec.StatusReport()
				}
			}
			if e != nil {
				t.Fatalf("run %d: %v", i, e)
			}
			m.feedback += len(o.Feedback)
			for _, x := range outs {
				m.feedback += len(x.Feedback)
			}

			// The oracle's verdicts.
			switch {
			case m.final:
				if rec.State != StateDelivered && !(m.closed) {
					t.Fatalf("run %d: control record state %s after its reply", i, rec.State)
				}
			case m.closed:
				if rec.State != StateClosedUnconfirmed {
					t.Fatalf("run %d: state %s after the end of the record's life", i, rec.State)
				}
			default:
				if rec.State != StateDeliveryUnknown {
					t.Fatalf("run %d: state %s, a lost record stays unknown until it closes", i, rec.State)
				}
			}
			if (rec.State == StateDeliveryUnknown) != (rec.Unknown != UnknownCauseNone) {
				t.Fatalf("run %d: state %s with cause %s", i, rec.State, rec.Unknown)
			}
			if m.side == SideTarget && !m.control && (rec.State == StateDelivered || rec.State == StateAccepted || rec.Lease.Handouts != 0 || rec.Lease.Active) {
				t.Fatalf("run %d: a lost target message is delivered again or leased: %+v", i, rec)
			}
			if m.side == SideTarget && !m.closed && !m.final && rec.StatusReport() != (StatusReport{State: StateDeliveryUnknown, Unknown: UnknownLostAtTarget}) {
				t.Fatalf("run %d: report %+v", i, rec.StatusReport())
			}
			wantFeedback := 0
			if m.side == SideSource {
				wantFeedback = 1
			}
			if m.feedback != wantFeedback {
				t.Fatalf("run %d: %d notices in total, want %d", i, m.feedback, wantFeedback)
			}
		}
		// Run the clock out: everything closes, except a control record whose
		// reply was committed.
		end, outs, err := rec.Tick(m.reportBy().Add(time.Nanosecond))
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range outs {
			if len(x.Feedback) != 0 {
				t.Fatalf("run %d: closing gave feedback", i)
			}
		}
		switch {
		case m.final:
			hits["ended delivered"]++
			if end.State != StateDelivered {
				t.Fatalf("run %d: ended %s", i, end.State)
			}
		default:
			hits["ended closed"]++
			if end.State != StateClosedUnconfirmed || !end.NextTick().IsZero() {
				t.Fatalf("run %d: ended %s", i, end.State)
			}
		}
		hits["side/"+map[Side]string{SideSource: "source", SideTarget: "target"}[m.side]+"/"+map[bool]string{false: "message", true: "control"}[m.control]]++
	}
	for _, p := range []string{
		"handout refused", "ack refused", "tick", "report_by passed", "forced closure ignored", "control reply corrects",
		"source fact ignored", "source report applied", "ended delivered", "ended closed",
		"side/source/message", "side/target/message", "side/target/control",
	} {
		if hits[p] < 100 {
			t.Errorf("path %q reached %d times, want at least 100", p, hits[p])
		}
	}
}
