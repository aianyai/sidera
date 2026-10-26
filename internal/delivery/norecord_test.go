package delivery

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/timing"
)

// sourceInState returns the sender's record of a message in one state, reached
// through the transitions, for the tests of EventNoRecordAtTarget.
func sourceInState(t *testing.T, name string) Record {
	t.Helper()
	r := NewSourceRecord(t0, at(10*time.Minute))
	apply := func(r Record, now time.Duration, ev Event) Record {
		n, _ := mustApply(t, r, at(now), ev)
		return n
	}
	switch name {
	case "queued":
		return r
	case "accepted":
		return apply(r, time.Second, Event{Type: EventReceipt})
	case "accepted and blocked":
		return apply(apply(r, time.Second, Event{Type: EventReceipt}), 2*time.Second, Event{Type: EventBlocked, Cause: CauseAdapterOffline})
	case "accepted after a corrected expiry":
		r = apply(r, afterExpiry, Event{Type: EventExpiryQueried, QueriedAt: at(afterExpiry)})
		return apply(r, afterExpiry+time.Second, Event{Type: EventReceipt})
	case "expired_unconfirmed":
		return apply(r, afterExpiry, Event{Type: EventExpiryQueried, QueriedAt: at(afterExpiry)})
	case "delivered":
		return apply(r, time.Second, Event{Type: EventDelivered})
	case "rejected":
		return apply(r, time.Second, Event{Type: EventRejected, Reject: RejectUnauthorized})
	case "delivery_unknown, handed over":
		return apply(r, time.Second, unknown(time.Second).ev)
	case "closed_unconfirmed":
		return apply(r, reportAt+time.Second, Event{Type: EventReportByPassed})
	}
	t.Fatalf("no source record named %q", name)
	return Record{}
}

// TestNoRecordAtTargetIsLostAtTargetOnlyFromAccepted is the table of the one
// rule for a target that answers a pull with "no record of this delivery": every
// state of a record, inside and outside the pull window, and the records that
// are not the sender's own. Only an accepted delivery, inside the window, is lost
// at the target; the rest are ignored, each for its own reason, and the order of
// the reasons is the order of the table.
func TestNoRecordAtTargetIsLostAtTargetOnlyFromAccepted(t *testing.T) {
	noRecord := Event{Type: EventNoRecordAtTarget}
	reportBy := ReportBy(at(10 * time.Minute))
	inside := []time.Time{at(time.Minute), reportBy}
	outside := []time.Time{reportBy.Add(time.Nanosecond), reportBy.Add(time.Hour)}

	senderReplacement, _ := newLost(t)
	senderReplacementClosed := closeAt(t, senderReplacement)

	ignoredRows := []struct {
		state           string
		rec             Record
		inside, outside IgnoreReason
	}{
		{"queued", sourceInState(t, "queued"), IgnoreNotApplicable, IgnoreRetryEnded},
		{"expired_unconfirmed", sourceInState(t, "expired_unconfirmed"), IgnoreNotApplicable, IgnoreRetryEnded},
		{"delivery_unknown, handed over", sourceInState(t, "delivery_unknown, handed over"), IgnoreNotApplicable, IgnoreRetryEnded},
		{"delivered", sourceInState(t, "delivered"), IgnoreAlreadyFinal, IgnoreAlreadyFinal},
		{"rejected", sourceInState(t, "rejected"), IgnoreAlreadyFinal, IgnoreAlreadyFinal},
		{"closed_unconfirmed", sourceInState(t, "closed_unconfirmed"), IgnoreAlreadyFinal, IgnoreAlreadyFinal},
		{"the replacement of the sender's own lost record", senderReplacement, IgnoreRecordLost, IgnoreRecordLost},
		{"the replacement of the sender's own lost record, closed", senderReplacementClosed, IgnoreRecordLost, IgnoreRecordLost},
	}
	for _, row := range ignoredRows {
		for _, window := range []struct {
			name  string
			times []time.Time
			want  IgnoreReason
		}{{"inside the window", inside, row.inside}, {"outside the window", outside, row.outside}} {
			t.Run(row.state+", "+window.name, func(t *testing.T) {
				for _, now := range window.times {
					after, out, err := row.rec.Apply(now, noRecord)
					if err != nil {
						t.Fatalf("at %v: %v", now.Sub(t0), err)
					}
					if out.Result != ResultIgnored || out.Ignored != window.want || len(out.Feedback) != 0 {
						t.Errorf("at %v: outcome %+v, want ignored for %d", now.Sub(t0), out, window.want)
					}
					if !reflect.DeepEqual(after, row.rec) {
						t.Errorf("at %v: an ignored fact changed the record: %+v", now.Sub(t0), after)
					}
				}
			})
		}
	}

	for _, name := range []string{"accepted", "accepted and blocked", "accepted after a corrected expiry"} {
		rec := sourceInState(t, name)
		t.Run(name+" is lost at the target inside the window, once", func(t *testing.T) {
			for _, now := range inside {
				after, out, err := rec.Apply(now, noRecord)
				if err != nil {
					t.Fatal(err)
				}
				if out.Result != ResultApplied || out.From != StateAccepted || out.To != StateDeliveryUnknown {
					t.Errorf("at %v: outcome %+v", now.Sub(t0), out)
				}
				if want := []Feedback{{Kind: KindLostAtTarget, State: StateDeliveryUnknown}}; !reflect.DeepEqual(out.Feedback, want) {
					t.Errorf("at %v: feedback %+v, want %+v", now.Sub(t0), out.Feedback, want)
				}
				if after.State != StateDeliveryUnknown || after.Unknown != UnknownLostAtTarget || after.Lost || !after.StateAt.Equal(now) || after.Blocked {
					t.Errorf("at %v: record %+v", now.Sub(t0), after)
				}
				if !after.Reported.Has(KindLostAtTarget) || after.Reported.Has(KindDeliveryUnknown) {
					t.Errorf("at %v: Reported %b", now.Sub(t0), after.Reported)
				}
				if err := after.Valid(); err != nil {
					t.Errorf("at %v: the record is not valid: %v", now.Sub(t0), err)
				}

				again, out, err := after.Apply(now, noRecord)
				if err != nil || out.Result != ResultIgnored || out.Ignored != IgnoreNotApplicable || len(out.Feedback) != 0 || !reflect.DeepEqual(again, after) {
					t.Errorf("at %v: the same fact again: %+v %v", now.Sub(t0), out, err)
				}
				corrected, out, err := after.Apply(now, Event{Type: EventDelivered})
				wantCorrection := []Kind{KindUnknownCorrected}
				if name == "accepted and blocked" {
					wantCorrection = append(wantCorrection, KindBlockedRecovered) // delivered is silent, so it reports the recovery of the block too
				}
				if err != nil || corrected.State != StateDelivered || !sameKinds(kindsOf(out.Feedback), wantCorrection) {
					t.Errorf("at %v: a later delivery: %+v %v", now.Sub(t0), out, err)
				}
				if err := corrected.Valid(); err != nil {
					t.Errorf("at %v: the corrected record is not valid: %v", now.Sub(t0), err)
				}
			}
		})
		t.Run(name+" outside the window is left alone", func(t *testing.T) {
			for _, now := range outside {
				after, out, err := rec.Apply(now, noRecord)
				if err != nil || out.Result != ResultIgnored || out.Ignored != IgnoreRetryEnded || len(out.Feedback) != 0 || !reflect.DeepEqual(after, rec) {
					t.Errorf("at %v: %+v %v", now.Sub(t0), out, err)
				}
			}
		})
	}

	t.Run("a record of the target is refused, whatever its state", func(t *testing.T) {
		handed := handOut(t, targetRecord(t, t0, time.Hour), at(30*time.Second), 1, 1)
		targets := map[string]Record{
			"accepted message":  targetRecord(t, t0, time.Hour),
			"handed-out":        handed,
			"accepted control":  controlRecord(),
			"delivered message": deliverBody(t, targetRecord(t, t0, time.Hour), at(time.Minute)),
		}
		for name, rec := range targets {
			for _, now := range []time.Time{at(time.Minute), reportBy.Add(time.Hour)} {
				after, out, err := rec.Apply(now, noRecord)
				if !errors.Is(err, ErrWrongSide) {
					t.Errorf("%s at %v: err = %v, want ErrWrongSide", name, now.Sub(t0), err)
				}
				if !reflect.DeepEqual(after, rec) || !reflect.DeepEqual(out, Outcome{}) {
					t.Errorf("%s at %v: a refused fact changed the record or returned an outcome", name, now.Sub(t0))
				}
			}
		}
	})
	t.Run("the fact is no status report: a target never reports it", func(t *testing.T) {
		src := sourceInState(t, "accepted")
		for _, rep := range []StatusReport{{State: StateAccepted}, {State: StateDeliveryUnknown}} {
			after, _, err := src.ApplyStatusReport(at(time.Minute), rep)
			if err != nil || after.Unknown == UnknownLostAtTarget {
				t.Errorf("report %+v: record cause %s, err %v", rep, after.Unknown, err)
			}
		}
	})
}

// TestRecordRetainUntilCoversThePullWindowOfASlowerSender: a source pulls until
// report_by on its own clock and reads a missing record as a loss, so a target
// whose clock is ahead of the source's by the whole tolerance must still hold the
// record at the source's last pull.
func TestRecordRetainUntilCoversThePullWindowOfASlowerSender(t *testing.T) {
	reportBy := ReportBy(at(10 * time.Minute))
	until := RecordRetainUntil(reportBy)
	if want := reportBy.Add(timing.ClockTolerance); !until.Equal(want) {
		t.Fatalf("RecordRetainUntil = %v, want report_by + clock tolerance = %v", until, want)
	}
	for _, skew := range []time.Duration{-timing.ClockTolerance, 0, timing.ClockTolerance} {
		lastPullOnTargetClock := reportBy.Add(skew)
		if lastPullOnTargetClock.After(until) {
			t.Errorf("a target %v ahead of the sender has forgotten the record at the sender's last pull (%v after %v)", skew, lastPullOnTargetClock, until)
		}
	}
	if until.Before(reportBy.Add(timing.ClockTolerance)) {
		t.Error("the retention is shorter than the tolerance")
	}
	later := RecordRetainUntil(reportBy.Add(time.Hour))
	if !later.Equal(until.Add(time.Hour)) {
		t.Errorf("the retention does not follow report_by: %v", later)
	}
}

// TestLossWordingVectors pins the wording of the kinds that tell the sender of a
// loss, word for word, and that of every other kind, which no change may touch.
// None of them says the delivery was handed over, and the loss at the target does
// not name its cause, which the sender does not know.
func TestLossWordingVectors(t *testing.T) {
	vectors := []struct {
		kind    Kind
		name    string
		summary string
	}{
		{KindLostAtSender, "lost_at_sender", "this node lost what it had stored about the message to storage corruption, so whether it was delivered, or for a control signal executed, is unknown; do not assume it was not delivered"},
		{KindLostAtTarget, "lost_at_target", "the target node no longer holds any record of the message it had accepted, so whether it was delivered, or for a control signal executed, is unknown; do not assume it was not delivered"},
		{KindLostUnknownMessage, "lost_unknown_message", "this node lost what it had stored about one of the messages this endpoint sent, to storage corruption, and cannot tell which one; whether it was delivered is unknown; do not assume it was not delivered"},
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
	if strings.Contains(KindLostAtTarget.Summary(), "storage") {
		t.Error("the source knows only that the target holds no record, not why")
	}
	if KindLostAtSender != KindUnknownCorrected+1 || KindLostAtTarget != KindUnknownCorrected+2 || KindLostUnknownMessage != KindLostAtTarget+1 {
		t.Error("the loss kinds follow the existing ones, in this order")
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
	if (KindLostUnknownMessage + 1).valid() || Kind(0).valid() {
		t.Error("validity bounds")
	}
	for c, name := range map[UnknownCause]string{UnknownCauseNone: "none", UnknownHandedOver: "handed_over", UnknownLostAtSender: "lost_at_sender", UnknownLostAtTarget: "lost_at_target", 200: "invalid"} {
		if c.String() != name {
			t.Errorf("cause %d: %q, want %q", c, c.String(), name)
		}
	}
}

// TestLostUnknownMessageKindBelongsToNoRecord: the kind that tells an endpoint
// of a message nobody can name has no delivery behind it, so no transition
// produces it, no record reports it, and the item that carries it has one
// constructor and one shape.
func TestLostUnknownMessageKindBelongsToNoRecord(t *testing.T) {
	if LastRecordKind != KindLostAtTarget || LastRecordKind.valid() == false || (LastRecordKind+1) != KindLostUnknownMessage {
		t.Errorf("LastRecordKind = %v, want the last kind a record's transitions produce", LastRecordKind)
	}
	causes := []UnknownCause{UnknownCauseNone, UnknownHandedOver, UnknownLostAtSender, UnknownLostAtTarget}
	for from := StateInvalid; from <= StateClosedUnconfirmed; from++ {
		for to := StateInvalid; to <= StateClosedUnconfirmed; to++ {
			for _, cause := range causes {
				for _, reported := range []KindSet{0, kindSetOf(KindDeliveryBlocked), kindSetOf(KindExpiredUnconfirmed)} {
					for _, k := range TransitionFeedback(from, to, cause, reported) {
						if k == KindLostUnknownMessage {
							t.Fatalf("TransitionFeedback(%s, %s, %s, %b) returned %s", from, to, cause, reported, k)
						}
					}
				}
			}
		}
	}
	rec := sourceInState(t, "accepted")
	rec.Reported = rec.Reported.With(KindLostUnknownMessage)
	if got := violationOf(t, rec.Valid()); got != ViolationReportedUnknownKind {
		t.Errorf("a record that reports it: violation %s, want %s", got, ViolationReportedUnknownKind)
	}
	if (KindSet(0).With(KindLostUnknownMessage)).known() {
		t.Error("a set with it is a set of kinds a record can have reported")
	}
	if !kindSetOf(KindUnreachable, LastRecordKind).known() {
		t.Error("the kinds a record produces are known")
	}

	sender := addr(7, 3)
	it := NewLostUnknownMessageItem(sender, "record-a", at(time.Minute))
	want := Item{Ref: DeliveryRef{Sender: sender}, Feedback: Feedback{Kind: KindLostUnknownMessage}, At: at(time.Minute), Source: "record-a"}
	if !reflect.DeepEqual(it, want) {
		t.Errorf("item %+v, want %+v", it, want)
	}
	if it.Ref.Message != "" || !it.Ref.Recipient.IsZero() {
		t.Error("the item names no message and no recipient: nobody can tell which one was lost")
	}
}

// TestLostUnknownMessageItemsMergeBySender: the notices of this kind of one
// sender endpoint are one notice however many were lost, and never merge with
// the anomalies of a message.
func TestLostUnknownMessageItemsMergeBySender(t *testing.T) {
	s1, s2 := addr(100, 1), addr(100, 2)
	a := NewLostUnknownMessageItem(s1, "record-a", at(0))
	b := NewLostUnknownMessageItem(s1, "record-b", at(30*time.Second))
	c := NewLostUnknownMessageItem(s2, "record-c", at(0))

	key := MergeKeyOf(a)
	if key.Class != MergeLostUnknownBySender || key.Sender != s1 || key.Message != "" || !key.Node.IsZero() || !key.Target.IsZero() {
		t.Errorf("key %+v", key)
	}
	if MergeKeyOf(b) != key {
		t.Error("two notices of one sender endpoint, decided at different times, have different keys")
	}
	if MergeKeyOf(c) == key {
		t.Error("the notices of two sender endpoints share a key")
	}
	otherKinds := []Item{
		{Ref: DeliveryRef{Sender: s1}, Feedback: Feedback{Kind: KindLostAtSender}, At: t0},
		{Ref: DeliveryRef{Sender: s1}, Feedback: Feedback{Kind: KindLostAtTarget}, At: t0},
	}
	for _, o := range otherKinds {
		if MergeKeyOf(o) == key {
			t.Errorf("a %s item shares the key of a lost_unknown_message item", o.Feedback.Kind)
		}
	}

	q := FeedbackQueue{Pending: []Item{a, c, b, item("s1", "n11", "m1", KindRejected, 0)}}
	got, err := q.Flush(at(time.Minute+30*time.Second), testParams)
	if err != nil || len(got) != 3 {
		t.Fatalf("notices %+v err %v", got, err)
	}
	merged := 0
	for _, n := range got {
		if n.Key.Class != MergeLostUnknownBySender || n.Sender != s1 {
			continue
		}
		merged = len(n.Items)
		if !n.Items[0].At.Before(n.Items[1].At) {
			t.Errorf("the items of a notice are in the order they were decided: %+v", n.Items)
		}
	}
	if merged != 2 {
		t.Errorf("the two notices of one sender endpoint were not merged: %d items", merged)
	}
	if len(q.Pending) != 0 {
		t.Errorf("%d items stayed queued", len(q.Pending))
	}
}
