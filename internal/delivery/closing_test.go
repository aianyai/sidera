package delivery

import (
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/timing"
)

// The tests of what a delivery that comes to report_by without an outcome tells
// its sender, and of the reports that reach the sender after report_by while the
// delivery is not yet closed. A delivery the sender was never told an outcome of
// must not close in silence, because silence reads as delivered.

// closeInstant is the first instant a record closes: the nanosecond after report_by.
func closeInstant(r Record) time.Time { return r.ReportBy.Add(time.Nanosecond) }

// sourceBeforeClose is the sender's record in the state it can be in when
// report_by comes, reached through the transitions.
func sourceBeforeClose(t *testing.T, name string) Record {
	t.Helper()
	if name == "accepted, lost at the target" {
		r := sourceInState(t, "accepted")
		r, _ = mustApply(t, r, at(2*time.Minute), Event{Type: EventNoRecordAtTarget})
		return r
	}
	if name == "queued after the unreachable hint" {
		r := NewSourceRecord(t0, at(10*time.Minute))
		r, _ = mustApply(t, r, at(3*time.Minute), Event{Type: EventContactFailing, Cause: CauseNetwork, Since: t0})
		return r
	}
	return sourceInState(t, name)
}

// TestAClosingSourceDeliveryTellsTheSenderWhatItHasNotBeenTold: closing at
// report_by from queued or accepted gives the notice of that, once, by Tick and
// by the event alike; closing from a state the sender was told about gives
// nothing, and so does the close of a final record (which does not close).
func TestAClosingSourceDeliveryTellsTheSenderWhatItHasNotBeenTold(t *testing.T) {
	rows := []struct {
		state string
		want  []Kind
	}{
		{"queued", []Kind{KindExpiredUnconfirmed}},
		{"queued after the unreachable hint", []Kind{KindExpiredUnconfirmed}},
		{"accepted", []Kind{KindAcceptedUnconfirmed}},
		{"accepted and blocked", []Kind{KindAcceptedUnconfirmed}},
		{"accepted after a corrected expiry", []Kind{KindAcceptedUnconfirmed}},
		{"expired_unconfirmed", nil},
		{"delivery_unknown, handed over", nil},
		{"accepted, lost at the target", nil},
	}
	closers := []struct {
		name  string
		close func(Record, time.Time) (Record, []Outcome, error)
	}{
		{"the event", func(r Record, now time.Time) (Record, []Outcome, error) {
			n, out, err := r.Apply(now, Event{Type: EventReportByPassed})
			return n, []Outcome{out}, err
		}},
		{"Tick", func(r Record, now time.Time) (Record, []Outcome, error) { return r.Tick(now) }},
	}
	for _, row := range rows {
		for _, c := range closers {
			t.Run(row.state+" closed by "+c.name, func(t *testing.T) {
				before := sourceBeforeClose(t, row.state)
				if err := before.Valid(); err != nil {
					t.Fatalf("the record before the close is not valid: %v", err)
				}
				closed, outs, err := c.close(before, closeInstant(before))
				if err != nil || closed.State != StateClosedUnconfirmed {
					t.Fatalf("state %s, %v, want closed_unconfirmed", closed.State, err)
				}
				var got []Kind
				for _, o := range outs {
					got = append(got, kindsOf(o.Feedback)...)
				}
				if !sameKinds(got, row.want) {
					t.Errorf("told %v, want %v", got, row.want)
				}
				if err := closed.Valid(); err != nil {
					t.Errorf("the closed record is not valid: %v", err)
				}
				for _, k := range row.want {
					if !closed.Reported.Has(k) {
						t.Errorf("the record does not remember having told %s", k)
					}
				}
				// Nothing is told again, by any later look at the clock.
				again, out, err := closed.Apply(closeInstant(before).Add(time.Hour), Event{Type: EventReportByPassed})
				if err != nil || out.Result != ResultIgnored || out.Ignored != IgnoreAlreadyFinal || len(out.Feedback) != 0 || !reflect.DeepEqual(again, closed) {
					t.Errorf("a second look: %+v %v", out, err)
				}
			})
		}
	}
}

// TestTheTargetsCloseIsSilent: only the sender's node produces feedback, so a
// target record that closes tells nobody, whatever state it was in.
func TestTheTargetsCloseIsSilent(t *testing.T) {
	handedOut := handOut(t, targetRecord(t, t0, time.Hour), at(time.Minute), 1, 1)
	targets := map[string]Record{
		"accepted message":    targetRecord(t, t0, time.Hour),
		"handed-out message":  handedOut,
		"accepted control":    controlRecord(),
		"accepted notice":     noticeRecord(t, t0, time.Hour),
		"delivered message":   deliverBody(t, targetRecord(t, t0, time.Hour), at(time.Minute)),
		"rejected message":    mustRecord(t, targetRecord(t, t0, time.Hour), at(time.Minute), Event{Type: EventRejected, Reject: RejectEndpointClosed}),
		"unknown after lease": mustTick(t, handedOut, SettleBy(handedOut.ReportBy)),
	}
	for name, r := range targets {
		t.Run(name, func(t *testing.T) {
			closed, outs, err := r.Tick(closeInstant(r).Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range outs {
				if len(o.Feedback) != 0 {
					t.Errorf("the target told %v on %s to %s", kindsOf(o.Feedback), o.From, o.To)
				}
			}
			if closed.Reported != 0 {
				t.Errorf("the target remembers having reported %b", closed.Reported)
			}
		})
	}
}

func mustRecord(t *testing.T, r Record, now time.Time, ev Event) Record {
	t.Helper()
	n, _ := mustApply(t, r, now, ev)
	return n
}

// TestAReportAfterReportByIsAppliedWhileTheRecordIsNotClosed: the sender's last
// look at the target comes after report_by, because the node was asleep or the
// target answered late, and the target keeps its records for longer. What it says
// is applied with its true meaning: a rejection is told as a rejection, a delivery
// stays silent, a hand-over is told. Only the close ends it.
func TestAReportAfterReportByIsAppliedWhileTheRecordIsNotClosed(t *testing.T) {
	late := []time.Duration{time.Nanosecond, time.Hour, 20 * time.Hour}
	rows := []struct {
		name   string
		before string
		report StatusReport
		want   State
		told   []Kind
	}{
		{"a rejection of an accepted delivery", "accepted", StatusReport{State: StateRejected, Reject: RejectBacklogExpired}, StateRejected, []Kind{KindRejected}},
		{"a rejection of a queued delivery", "queued", StatusReport{State: StateRejected, Reject: RejectBacklogExpired}, StateRejected, []Kind{KindRejected}},
		{"a rejection of a delivery that was told unreachable", "queued after the unreachable hint", StatusReport{State: StateRejected, Reject: RejectUnauthorized}, StateRejected, []Kind{KindRejected}},
		{"a delivery of an accepted delivery", "accepted", StatusReport{State: StateDelivered}, StateDelivered, nil},
		{"a delivery of a queued delivery", "queued", StatusReport{State: StateDelivered}, StateDelivered, nil},
		{"a hand-over of an accepted delivery", "accepted", StatusReport{State: StateDeliveryUnknown}, StateDeliveryUnknown, []Kind{KindDeliveryUnknown}},
		{"a hand-over of a queued delivery", "queued", StatusReport{State: StateDeliveryUnknown}, StateDeliveryUnknown, []Kind{KindDeliveryUnknown}},
		{"a delivery of an accepted and blocked delivery", "accepted and blocked", StatusReport{State: StateDelivered}, StateDelivered, []Kind{KindBlockedRecovered}},
		{"a hand-over of a delivery whose expiry was corrected", "accepted after a corrected expiry", StatusReport{State: StateDeliveryUnknown}, StateDeliveryUnknown, []Kind{KindDeliveryUnknown}},
		{"a rejection of an expired delivery", "expired_unconfirmed", StatusReport{State: StateRejected, Reject: RejectBacklogExpired}, StateRejected, []Kind{KindRejected}},
		{"a delivery of an expired delivery", "expired_unconfirmed", StatusReport{State: StateDelivered}, StateDelivered, []Kind{KindExpiredCorrected}},
		{"a delivery of a delivery that was unknown", "delivery_unknown, handed over", StatusReport{State: StateDelivered}, StateDelivered, []Kind{KindUnknownCorrected}},
	}
	for _, row := range rows {
		for _, after := range late {
			t.Run(row.name+" "+after.String()+" after report_by", func(t *testing.T) {
				before := sourceBeforeClose(t, row.before)
				now := before.ReportBy.Add(after)
				got, outs, err := before.ApplyStatusReport(now, row.report)
				if err != nil {
					t.Fatal(err)
				}
				if got.State != row.want {
					t.Fatalf("state %s, want %s", got.State, row.want)
				}
				var told []Kind
				for _, o := range outs {
					told = append(told, kindsOf(o.Feedback)...)
				}
				if !sameKinds(told, row.told) {
					t.Errorf("told %v, want %v", told, row.told)
				}
				if err := got.Valid(); err != nil {
					t.Errorf("the record is not valid: %v", err)
				}
				// The record the report settled does not close into a second notice.
				closed, out, err := got.Apply(closeInstant(got).Add(after), Event{Type: EventReportByPassed})
				switch {
				case err != nil:
					t.Fatal(err)
				case got.State.IsTerminal():
					if out.Ignored != IgnoreAlreadyFinal || !reflect.DeepEqual(closed, got) {
						t.Errorf("a final record closed: %+v", out)
					}
				case len(out.Feedback) != 0:
					t.Errorf("the close of a record that was told its outcome told %v", kindsOf(out.Feedback))
				}
			})
		}
	}
}

// TestAnAnswerOfNoRecordAfterReportByIsNotALoss: after report_by the target may
// have forgotten the record legitimately, so "no record" is not read as a loss;
// the delivery is closed by the clock and the sender is told that the result
// could not be learned, which is what it is.
func TestAnAnswerOfNoRecordAfterReportByIsNotALoss(t *testing.T) {
	for _, state := range []string{"accepted", "accepted and blocked", "queued"} {
		t.Run(state, func(t *testing.T) {
			r := sourceBeforeClose(t, state)
			late := r.ReportBy.Add(time.Hour)
			after, out, err := r.Apply(late, Event{Type: EventNoRecordAtTarget})
			if err != nil || out.Result != ResultIgnored || out.Ignored != IgnoreRetryEnded || len(out.Feedback) != 0 || !reflect.DeepEqual(after, r) {
				t.Fatalf("the answer: %+v %v, want it ignored, the record unchanged", out, err)
			}
			closed, outs, err := after.Tick(late)
			if err != nil || closed.State != StateClosedUnconfirmed || closed.Unknown != UnknownCauseNone {
				t.Fatalf("closed %s (%s), %v", closed.State, closed.Unknown, err)
			}
			want := KindAcceptedUnconfirmed
			if state == "queued" {
				want = KindExpiredUnconfirmed
			}
			if len(outs) != 1 || !sameKinds(kindsOf(outs[0].Feedback), []Kind{want}) {
				t.Errorf("outcomes %+v, want the one notice %s", outs, want)
			}
		})
	}
}

// TestALateReportMovesNoDeadline: the retention a late look can count on is the
// target's alone; nothing in the rules of the sender's record depends on how
// long after report_by a report is applied, so the one rule serves a report an
// instant late and one a day late.
func TestALateReportMovesNoDeadline(t *testing.T) {
	r := sourceBeforeClose(t, "accepted")
	for _, after := range []time.Duration{time.Nanosecond, timing.ClockTolerance, 24 * time.Hour, 48 * time.Hour} {
		got, _, err := r.ApplyStatusReport(r.ReportBy.Add(after), StatusReport{State: StateDelivered})
		if err != nil || got.State != StateDelivered {
			t.Errorf("%v after report_by: state %s, %v", after, got.State, err)
		}
	}
	if !r.NextTick().Equal(closeInstant(r)) {
		t.Errorf("NextTick = %v, want the close at the nanosecond after report_by: the final look is the caller's to make before it", r.NextTick())
	}
}
