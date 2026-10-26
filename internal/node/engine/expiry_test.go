package engine_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/timing"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// What the passing of time does to a delivery, and what the sender is told of it:
// the target's queue time runs out, the sender's deadline passes without a receipt,
// contact with the target keeps failing, and report_by passes.

// A sender that holds no receipt judges its message expired only after the deadline
// and the clock tolerance, and only when a query it made after that, which got an
// answer, found no receipt. The notice says the target may have received the message;
// when the receipt turns out to exist, the correction is told once and the expiry is
// not told again.
func TestExpiredUnconfirmedCorrectedOnce(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	// The message reaches b, and the report of it is kept from a: a has no receipt.
	h.Unreachable(true)
	msg := send(t, h, "a", "b", "its receipt is held")
	quiet(t, h)
	h.HoldReports(string(msg))
	h.Unreachable(false)
	h.Advance(resendBackoff)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateQueued {
		t.Fatalf("the source record is %s, want queued: the report was not held", d.State)
	}
	if in := inOf(t, h, msg); in.Record.State != delivery.StateAccepted {
		t.Fatalf("the target record is %s, want accepted", in.Record.State)
	}

	// At the deadline, and even a minute after it, a delivery with no receipt is not
	// judged: the target's clock may run behind.
	h.Advance(delivery.DefaultAcceptBefore)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateQueued {
		t.Fatalf("the source record is %s at the deadline, want queued", d.State)
	}
	// A query that comes after the tolerance and finds no receipt judges it.
	h.Advance(timing.ClockTolerance + pullInterval)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateExpiredUnconfirmed {
		t.Fatalf("the source record is %s after the tolerance and a query, want expired_unconfirmed", d.State)
	}
	h.Advance(mergeWindow)
	quiet(t, h)
	ns := noticesFor(t, h, "a")
	if len(ns) != 1 || !slices.Equal(ns[0].kinds(), []delivery.Kind{delivery.KindExpiredUnconfirmed}) {
		t.Fatalf("a was told %v, want the expiry", toldKinds(t, h, "a"))
	}
	if !strings.Contains(ns[0].text, "may have received the message") {
		t.Errorf("the notice does not say that the target may have the message:\n%s", ns[0].text)
	}

	// The report is let through: the target did accept the message.
	h.Release(string(msg))
	h.Advance(pullInterval)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateAccepted {
		t.Fatalf("the source record is %s once the report comes, want accepted", d.State)
	}
	h.Advance(mergeWindow)
	quiet(t, h)
	if got := toldKinds(t, h, "a"); !slices.Equal(got, []delivery.Kind{delivery.KindExpiredUnconfirmed, delivery.KindExpiredCorrected}) {
		t.Fatalf("a was told %v, want the expiry and then its correction", got)
	}

	// Neither more pulls nor a restart tell it again.
	for i := 0; i < 3; i++ {
		h.Advance(pullInterval)
		quiet(t, h)
	}
	h.Restart()
	quiet(t, h)
	h.Advance(2 * pullInterval)
	quiet(t, h)
	if got := toldKinds(t, h, "a"); len(got) != 2 {
		t.Errorf("a was told %v after more pulls and a restart, want the same two", got)
	}
}

// A message nobody reads is rejected by its target when the endpoint's longest
// queue time has run out. The source learns it from a pull and the sender is told
// once, with the reason.
func TestBacklogExpiryRejectsAndIsReportedOnce(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	msg := send(t, h, "a", "b", "nobody reads it")
	quiet(t, h)
	if in := inOf(t, h, msg); in.Record.State != delivery.StateAccepted || in.Body == nil {
		t.Fatalf("the target record is %s, body kept %v, want accepted with its body", in.Record.State, in.Body != nil)
	}

	h.Advance(delivery.DefaultEndpointMaxQueue - time.Minute)
	quiet(t, h)
	if in := inOf(t, h, msg); in.Record.State != delivery.StateAccepted {
		t.Fatalf("the target record is %s before its queue time ran out, want accepted", in.Record.State)
	}
	h.Advance(2 * time.Minute)
	quiet(t, h)
	in := inOf(t, h, msg)
	if in.Record.State != delivery.StateRejected || in.Record.Reject != delivery.RejectBacklogExpired || in.Body != nil {
		t.Fatalf("the target record is %s (%s), body kept %v, want rejected (backlog_expired) holding no body", in.Record.State, in.Record.Reject, in.Body != nil)
	}
	h.Advance(pullInterval)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateRejected || d.Reject != delivery.RejectBacklogExpired {
		t.Fatalf("the source record is %s (%s), want rejected (backlog_expired)", d.State, d.Reject)
	}
	h.Advance(mergeWindow)
	quiet(t, h)
	ns := noticesFor(t, h, "a")
	if len(ns) != 1 || !slices.Equal(ns[0].kinds(), []delivery.Kind{delivery.KindRejected}) || !strings.Contains(ns[0].text, "backlog_expired") {
		t.Fatalf("a was told %v, want one notice that its message was rejected as backlog_expired", toldKinds(t, h, "a"))
	}

	for i := 0; i < 3; i++ {
		h.Advance(pullInterval)
		quiet(t, h)
	}
	h.Restart()
	quiet(t, h)
	h.Advance(time.Hour)
	quiet(t, h)
	if got := toldKinds(t, h, "a"); len(got) != 1 {
		t.Errorf("a was told %v after more time and a restart, want it once", got)
	}
}

// Twenty messages sent at once to a node that cannot be reached are one anomaly
// to the sender: one notice, after the contact has failed long enough and the merge
// window is over.
func TestTwentyToUnreachableTargetMergeIntoOneNotice(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	h.Unreachable(true)
	var sent []adapterproto.MessageID
	for i := 0; i < 20; i++ {
		sent = append(sent, send(t, h, "a", "b", fmt.Sprintf("message %d to a node nobody reaches", i)))
	}
	quiet(t, h)

	h.Advance(delivery.UnreachableHintAfter - time.Second)
	quiet(t, h)
	if got := pending(t, h, "a"); got != 0 {
		t.Fatalf("%d items are decided before contact has failed for %v", got, delivery.UnreachableHintAfter)
	}
	h.Advance(resendBackoff)
	quiet(t, h)
	if got := pending(t, h, "a"); got != 20 {
		t.Fatalf("%d items are decided after contact has failed for %v, want one for each message", got, delivery.UnreachableHintAfter)
	}
	h.Advance(mergeWindow)
	quiet(t, h)
	ns := noticesFor(t, h, "a")
	if len(ns) != 1 {
		t.Fatalf("a holds %d notices, want the 20 merged into one", len(ns))
	}
	if got := toldKinds(t, h, "a"); len(got) != 20 || slices.ContainsFunc(got, func(k delivery.Kind) bool { return k != delivery.KindUnreachable }) {
		t.Errorf("the notice stands for %v, want twenty unreachable items", got)
	}
	for _, m := range sent {
		if !strings.Contains(ns[0].text, string(m)) {
			t.Errorf("the notice does not name message %s", m)
		}
	}
	if !strings.Contains(ns[0].text, "cause: network") {
		t.Errorf("the notice does not say the network is to blame:\n%s", ns[0].text)
	}
	// It is not told again as the sender keeps trying.
	h.Advance(5 * time.Minute)
	quiet(t, h)
	if got := len(noticesFor(t, h, "a")); got != 1 {
		t.Errorf("a holds %d notices after the node kept trying, want 1", got)
	}
}

// A target that cannot be reached for the whole life of the delivery is the one
// failure that has no report to carry it, so the sender is told by the failure of
// the contact itself. It is told once, while the node still retries; the deadline
// passes with no query that could have been answered, so no expiry is judged; and
// the delivery ends closed at report_by, which is silent, with the sender told. A
// delivery that nobody ever said a word about would end the same way and be read as
// delivered.
func TestAnUnreachableTargetIsToldOnceAndTheDeliveryClosesAfterReportBy(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	h.Unreachable(true)
	msg := send(t, h, "a", "b", "to a node nobody reaches")
	quiet(t, h)
	reportBy := sourceDelivery(t, h, msg).ReportBy

	h.Advance(time.Minute)
	quiet(t, h)
	if got := pending(t, h, "a"); got != 0 {
		t.Fatalf("%d items are decided after a minute of failed contact, want none", got)
	}
	h.Advance(time.Minute)
	quiet(t, h)
	if got := pending(t, h, "a"); got != 1 {
		t.Fatalf("%d items are decided after two minutes of failed contact, want the hint", got)
	}
	h.Advance(mergeWindow)
	quiet(t, h)
	if got := toldKinds(t, h, "a"); !slices.Equal(got, []delivery.Kind{delivery.KindUnreachable}) {
		t.Fatalf("a was told %v, want that the target is unreachable", got)
	}

	// The deadline passes and the target is still not reached: no query was answered,
	// so nothing is judged expired.
	h.Advance(delivery.DefaultAcceptBefore)
	quiet(t, h)
	h.Advance(time.Hour)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateQueued {
		t.Fatalf("the source record is %s with no contact to the end of the deadline, want queued", d.State)
	}
	h.Advance(reportBy.Sub(h.Clock().Now()) + time.Minute)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateClosedUnconfirmed {
		t.Fatalf("the source record is %s after report_by, want closed_unconfirmed", d.State)
	}
	if got := toldKinds(t, h, "a"); !slices.Equal(got, []delivery.Kind{delivery.KindUnreachable}) {
		t.Errorf("a was told %v by the end, want the one unreachable notice and nothing else", got)
	}
	if o := outOf(t, h, msg); o.Body != nil {
		t.Error("the closed source record still refers to a body it has no use for")
	}
}

// An accepted delivery whose target cannot be reached from some moment on to report_by
// ends closed and silent, and the target may meanwhile have rejected it: a failure the
// sender is never told of. The delivery rules have no feedback for contact that fails
// after a receipt, so this is a requirement kept in sight, skipped until it has a rule.
func TestAnAcceptedDeliveryWhoseTargetIsLostToTheEndIsNotClosedSilently(t *testing.T) {
	t.Skip("pending a decision: delivery.Record.Apply ignores a failing contact for every state but queued " +
		"(noteUnreachable), so an accepted delivery whose target is unreachable until report_by closes with no word, " +
		"even when the target rejected or lost it in the meantime")
}

// A target record that is not final when its queue time runs out or report_by comes
// is closed by the passing of time, at start as well as while the node runs; the
// records the sender holds close at report_by, silently.
func TestARecordThatIsNotFinalIsClosedByTheEndOfItsTime(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	msg := send(t, h, "a", "b", "accepted and never answered")
	quiet(t, h)
	reportBy := sourceDelivery(t, h, msg).ReportBy
	// The target is unreachable from now on, so the source learns nothing more.
	h.Unreachable(true)
	h.Stop()
	h.Advance(reportBy.Sub(h.Clock().Now()) + time.Minute)
	h.Start()
	quiet(t, h)

	if in := inOf(t, h, msg); in.Record.State != delivery.StateRejected || in.Record.Reject != delivery.RejectBacklogExpired {
		t.Errorf("the target record is %s (%s) at the start after its time ran out, want rejected (backlog_expired)", in.Record.State, in.Record.Reject)
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateClosedUnconfirmed {
		t.Errorf("the source record is %s at the start after report_by, want closed_unconfirmed", d.State)
	}
}

// A clock that was set back and then moved on is not one to judge a deadline by
// until the owner has cleared it: nothing is told about contact that has been failing
// for longer than the hint's two minutes, by the clock in doubt. When the owner clears
// the pause the judgement is made, from the moment the failure began.
func TestNothingIsJudgedByAClockThatIsInDoubt(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	h.Unreachable(true)
	msg := send(t, h, "a", "b", "to a node nobody reaches")
	quiet(t, h)
	h.Advance(time.Minute)
	quiet(t, h)
	mark := h.Clock().Now()

	h.Set(mark.Add(-30 * time.Minute)) // set back: the next command finds it
	if _, err := trySend(h, "a", "b", "refused while the clock is in doubt", ""); err == nil {
		t.Fatal("a send was taken in while the clock is behind the mark")
	}
	h.Set(mark.Add(3 * time.Minute)) // ahead of the mark again, the deadline not yet; the owner has cleared nothing
	h.Advance(resendBackoff)
	quiet(t, h)
	if got := pending(t, h, "a"); got != 0 {
		t.Fatalf("%d items are decided by a clock in doubt, want none", got)
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateQueued {
		t.Fatalf("the source record is %s, want queued", d.State)
	}

	if _, err := h.Engine().Execute(h.Ctx(), clearCommand(91, "clear")); err != nil {
		t.Fatal(err)
	}
	h.Advance(resendBackoff)
	quiet(t, h)
	if got := pending(t, h, "a"); got != 1 {
		t.Errorf("%d items are decided after the owner cleared the pause, want the hint for contact that has failed since the start", got)
	}
}

// Every step of the transaction that records what the passing of time did to a
// record is crashed at, for a target record whose queue time ran out and for the
// record a sender holds when report_by comes. Wherever the node died, the record
// ends settled and the sender is told what it is owed once.
func TestTheRecordsOfTimeRecoverFromACrashAtEveryPoint(t *testing.T) {
	cov := &coverage{hit: map[fsx.Key]int{}}
	walk := func(t *testing.T, taker *diskTaker, check func(t *testing.T, h *harness.H)) {
		t.Helper()
		if len(taker.disks) == 0 {
			t.Fatal("no step of the watched transactions was reached")
		}
		for _, td := range taker.disks {
			t.Run(td.where, func(t *testing.T) { check(t, restartedAt(t, td.disk, td.at, "a", "b")) })
		}
	}
	t.Run("a target record whose queue time ran out", func(t *testing.T) {
		taker := newDiskTaker(cov, "tick")
		h := nodeWith(t, harness.Options{Observer: taker}, "a", "b")
		taker.h = h
		msg := send(t, h, "a", "b", "nobody reads it")
		quiet(t, h)
		h.Advance(delivery.DefaultEndpointMaxQueue + time.Minute)
		quiet(t, h)
		walk(t, taker, func(t *testing.T, r *harness.H) {
			if in := inOf(t, r, msg); in.Record.State != delivery.StateRejected || in.Record.Reject != delivery.RejectBacklogExpired || in.Body != nil {
				t.Errorf("the target record is %s (%s), body kept %v", in.Record.State, in.Record.Reject, in.Body != nil)
			}
			if d := sourceDelivery(t, r, msg); d.State != delivery.StateRejected {
				t.Errorf("the source record is %s, want rejected", d.State)
			}
			if got := toldKinds(t, r, "a"); !slices.Equal(got, []delivery.Kind{delivery.KindRejected}) {
				t.Errorf("a was told %v, want the rejection once", got)
			}
		})
	})
	t.Run("a source record when report_by comes", func(t *testing.T) {
		taker := newDiskTaker(cov, "tick")
		h := nodeWith(t, harness.Options{Observer: taker}, "a", "b")
		taker.h = h
		msg := send(t, h, "a", "b", "accepted and never answered")
		quiet(t, h)
		reportBy := sourceDelivery(t, h, msg).ReportBy
		h.Unreachable(true)
		h.Advance(reportBy.Sub(h.Clock().Now()) + time.Minute)
		quiet(t, h)
		walk(t, taker, func(t *testing.T, r *harness.H) {
			if d := sourceDelivery(t, r, msg); d.State != delivery.StateClosedUnconfirmed {
				t.Errorf("the source record is %s, want closed_unconfirmed", d.State)
			}
			if o := outOf(t, r, msg); o.Body != nil {
				t.Error("the closed source record still refers to its body")
			}
		})
	})
	for _, k := range harness.ProgramTable(t).Keys() {
		// Control records are not ticked by the passing of time yet: their steps have no
		// scenario until a control signal is settled by the engine.
		if k.Tag.Tx == "tick" && k.Tag.Role != "ctl" && k.Tag.Role != "ctl-dir" && cov.hit[k.Key] == 0 {
			t.Errorf("the step %s %q of transaction tick was never reached", k.Step, k.Tag.Role)
		}
	}
}
