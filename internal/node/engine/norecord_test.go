package engine_test

import (
	"path"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The tests of the answer "I hold no record of that delivery". The target says
// it in a pull, for a recipient the source named, when it has nothing under the
// message for that recipient; the source that holds a receipt reads it as a
// loss. They run on one node whose source and target sides are the same
// process, over the real store and the loopback, and make the target lose a
// record the way a deleted file does: the node is stopped, the file removed, and
// the node started again.

// pullFor asks the node, as a source on it would, for the reports of its
// deliveries of the message to the endpoints.
func pullFor(h *harness.H, msg adapterproto.MessageID, to ...ident.EndpointID) (transport.PullAnswer, error) {
	return h.Loopback().Pull(h.Ctx(), harness.NodeID(), transport.PullRequest{Message: delivery.MessageID(msg), Recipients: to})
}

// endpointOf is the endpoint registered as name.
func endpointOf(t testing.TB, h *harness.H, name string) ident.EndpointID {
	t.Helper()
	return addrOf(t, h, name).Endpoint
}

// loseTargetRecords stops the node, removes the target records of the message
// for the named recipients from its disk and starts it again, unless the test
// has a step to do while it is down (before, called with the node stopped and
// the records removed). The records are the node's; the sender's record, and its
// receipt, stay.
func loseTargetRecords(t *testing.T, h *harness.H, msg adapterproto.MessageID, before func(), names ...string) {
	t.Helper()
	var files []string
	for _, in := range inRecords(t, h, msg) {
		for _, n := range names {
			if in.Key.Recipient == endpointOf(t, h, n) {
				files = append(files, path.Join(store.InDir, in.Name()))
			}
		}
	}
	if len(files) != len(names) {
		t.Fatalf("found the target records of %d of the %d recipients %v", len(files), len(names), names)
	}
	h.Stop()
	for _, f := range files {
		if err := h.Disk().Remove(fsx.Tag{Tx: "offline", Role: "f"}, f); err != nil {
			t.Fatalf("removing %s: %v", f, err)
		}
	}
	if before != nil {
		before()
	}
	h.Start()
	quiet(t, h)
}

// feedbackOf are the kinds of the notices the source record of the message holds
// for the sender, in order.
func feedbackOf(t testing.TB, h *harness.H, msg adapterproto.MessageID) []delivery.Kind {
	t.Helper()
	var kinds []delivery.Kind
	for _, it := range outOf(t, h, msg).Feedback {
		kinds = append(kinds, it.Feedback.Kind)
	}
	return kinds
}

// A pull names the recipients the source tracks. For each of them the target
// answers with its signed report, or, when it holds no record of a delivery of
// the message from the asking node to it, with the recipient in NoRecord: never
// both, and never neither (for a recipient it may answer for). What it does not
// hold under the message at all is not a report of another message, and a
// recipient that was not asked about is not answered.
func TestPullAnswersNoRecordForARequestedRecipientTheTargetDoesNotHold(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b", "c", "d")
	res, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b"), h.Addr("c")}, Body: "to two"})
	if err != nil {
		t.Fatal(err)
	}
	msg := res.MessageID
	quiet(t, h)
	b, c, d := endpointOf(t, h, "b"), endpointOf(t, h, "c"), endpointOf(t, h, "d")

	t.Run("a held record is reported and not listed", func(t *testing.T) {
		ans, err := pullFor(h, msg, b, c)
		if err != nil || len(ans.Reports) != 2 || len(ans.NoRecord) != 0 {
			t.Fatalf("the answer for two held records: %+v, %v", ans, err)
		}
	})
	t.Run("a recipient of the node that has no record of the message is listed", func(t *testing.T) {
		ans, err := pullFor(h, msg, d)
		if err != nil || len(ans.Reports) != 0 || !slices.Equal(ans.NoRecord, []ident.EndpointID{d}) {
			t.Fatalf("the answer for a recipient with no record: %+v, %v", ans, err)
		}
	})
	t.Run("a message the node holds none of lists every recipient asked about", func(t *testing.T) {
		ans, err := pullFor(h, "never-sent", b, d)
		if err != nil || len(ans.Reports) != 0 || !slices.Equal(ans.NoRecord, []ident.EndpointID{b, d}) {
			t.Fatalf("the answer for an unknown message: %+v, %v", ans, err)
		}
	})
	t.Run("only the recipients asked about are answered", func(t *testing.T) {
		ans, err := pullFor(h, msg, c)
		if err != nil || len(ans.Reports) != 1 || len(ans.NoRecord) != 0 {
			t.Fatalf("the answer for one of two held records: %+v, %v", ans, err)
		}
		rep, err := h.VerifyReport(ans.Reports[0])
		if err != nil || rep.Recipient != c {
			t.Fatalf("the report is for %v (%v), want %v", rep.Recipient, err, c)
		}
	})
	t.Run("a recipient asked about twice is answered once", func(t *testing.T) {
		ans, err := pullFor(h, msg, c, c, d, d)
		if err != nil || len(ans.Reports) != 1 || !slices.Equal(ans.NoRecord, []ident.EndpointID{d}) {
			t.Fatalf("the answer for a repeated recipient: %+v, %v", ans, err)
		}
	})
	t.Run("a request that names no recipient is refused", func(t *testing.T) {
		_, err := pullFor(h, msg)
		if reason, ok := engine.ReasonOf(err); !ok || reason != engine.ReasonInvalidArgument {
			t.Fatalf("a pull for no recipient: %v, want the refusal for an invalid argument", err)
		}
	})

	// One of two records is lost: its recipient moves from the reports to NoRecord,
	// and the other is answered as before.
	loseTargetRecords(t, h, msg, nil, "b")
	ans, err := pullFor(h, msg, b, c)
	if err != nil || len(ans.Reports) != 1 || !slices.Equal(ans.NoRecord, []ident.EndpointID{b}) {
		t.Fatalf("the answer when one of two records is lost: %+v, %v", ans, err)
	}
	if rep, err := h.VerifyReport(ans.Reports[0]); err != nil || rep.Recipient != c {
		t.Errorf("the report that is left is for %v (%v), want %v", rep.Recipient, err, c)
	}
}

// A transport that holds the reports of a message back (the loopback's knob)
// gives an answer with nothing in it, "nothing yet". It never turns into "no
// record", not even for a recipient the target holds no record of: the source
// would read that as a loss.
func TestHeldReportsAreNeverNoRecord(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	msg := send(t, h, "a", "b", "its record will be lost")
	quiet(t, h)
	loseTargetRecords(t, h, msg, func() { h.HoldReports(string(msg)) }, "b")

	ans, err := pullFor(h, msg, endpointOf(t, h, "b"))
	if err != nil || len(ans.Reports) != 0 || len(ans.NoRecord) != 0 || ans.ControlReply != nil {
		t.Fatalf("the answer while the reports are held: %+v, %v; want an empty one", ans, err)
	}
	for i := 0; i < 3; i++ {
		h.Advance(pullInterval)
		quiet(t, h)
	}
	if got := knobHits(t, h)["hold-reports"]; got < 3 {
		t.Fatalf("%d pulls were held, want the source's pulls to have gone on", got)
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateAccepted {
		t.Errorf("the delivery is %s while the answers are held, want it unchanged", d.State)
	}
	if got := feedbackOf(t, h, msg); len(got) != 0 {
		t.Errorf("the sender is told %v while the answers are held, want nothing", got)
	}

	// Once the answers are given back, the same pull says what it is.
	h.Release(string(msg))
	h.Advance(pullInterval)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateDeliveryUnknown || d.Unknown != delivery.UnknownLostAtTarget {
		t.Errorf("after the release the delivery is %s (%s), want delivery_unknown, lost_at_target", d.State, d.Unknown)
	}
}

// A node that may not sign refuses the whole answer to a pull. It does not give
// the half of it that needs no signature: a "no record" with no report beside it
// would read as a loss the node cannot yet vouch for the rest of. The pause ends
// when the owner clears it, and then the same pull gets its answer.
func TestNoRecordIsNotAnsweredWhileTheNodeMayNotSign(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b", "c")
	res, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b"), h.Addr("c")}, Body: "to two"})
	if err != nil {
		t.Fatal(err)
	}
	msg := res.MessageID
	quiet(t, h)
	h.Advance(time.Hour)
	send(t, h, "a", "b", "raises the mark of the clock") // a command reads the clock and raises the mark
	quiet(t, h)
	mark := h.Clock().Now()
	// The restart's own pull is held, so that the record is lost with the source
	// still unaware of it when the clock is set back.
	loseTargetRecords(t, h, msg, func() { h.HoldReports(string(msg)) }, "b")
	h.Release(string(msg))
	h.Set(mark.Add(-30 * time.Minute)) // the wall clock was set back by half an hour

	b, c := endpointOf(t, h, "b"), endpointOf(t, h, "c")
	ans, err := pullFor(h, msg, b, c)
	if reason, ok := engine.ReasonOf(err); !ok || reason != engine.ReasonClockRollback {
		t.Fatalf("a pull while the clock is in doubt: %v, want the refusal for the clock", err)
	}
	if len(ans.Reports) != 0 || len(ans.NoRecord) != 0 {
		t.Errorf("a refused pull gave %+v, want nothing of an answer", ans)
	}
	// Time moves on past the mark, which does not lift the pause; the source's own
	// pulls are refused as well and it learns nothing from them.
	h.Set(mark.Add(2 * time.Hour))
	h.Advance(pullInterval)
	quiet(t, h)
	if got := feedbackOf(t, h, msg); len(got) != 0 {
		t.Errorf("the sender is told %v while the node may not sign, want nothing", got)
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateAccepted {
		t.Errorf("the delivery is %s while the node may not sign, want it unchanged", d.State)
	}

	// The owner clears the pause; the pull is answered as it always is.
	if _, err := h.Engine().Execute(h.Ctx(), clearCommand(81, "clear")); err != nil {
		t.Fatal(err)
	}
	ans, err = pullFor(h, msg, b, c)
	if err != nil || len(ans.Reports) != 1 || !slices.Equal(ans.NoRecord, []ident.EndpointID{b}) {
		t.Errorf("the pull after the clear: %+v, %v; want c's report and %v listed", ans, err, b)
	}
}

// A source that holds a receipt and is told that the target has no record of the
// delivery learns that the record was lost: the delivery becomes delivery_unknown
// with that cause, and the sender gets one notice of it. The source does not send
// the message again, and the same answer a second time, and a third, tells the
// sender nothing more. A delivery of the same message to a recipient that still
// has its record is not touched.
func TestSourceWithAReceiptGivesLostAtTargetOnce(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b", "c")
	res, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b"), h.Addr("c")}, Body: "to two"})
	if err != nil {
		t.Fatal(err)
	}
	msg := res.MessageID
	quiet(t, h)
	for i, r := range outOf(t, h, msg).Outbound.Recipients {
		if r.Delivery.State != delivery.StateAccepted {
			t.Fatalf("delivery %d is %s before the loss, want accepted (a receipt held)", i, r.Delivery.State)
		}
	}

	loseTargetRecords(t, h, msg, nil, "b")
	for i := 0; i < 3; i++ {
		h.Advance(pullInterval)
		quiet(t, h)
	}

	out := outOf(t, h, msg)
	for _, r := range out.Outbound.Recipients {
		switch r.Address.Endpoint {
		case endpointOf(t, h, "b"):
			if r.Delivery.State != delivery.StateDeliveryUnknown || r.Delivery.Unknown != delivery.UnknownLostAtTarget {
				t.Errorf("the lost delivery is %s (%s), want delivery_unknown, lost_at_target", r.Delivery.State, r.Delivery.Unknown)
			}
		default:
			if r.Delivery.State != delivery.StateAccepted {
				t.Errorf("the delivery whose record is held is %s, want accepted", r.Delivery.State)
			}
		}
	}
	if len(out.Feedback) != 1 || out.Feedback[0].Feedback.Kind != delivery.KindLostAtTarget ||
		out.Feedback[0].Ref.Recipient.Endpoint != endpointOf(t, h, "b") || out.Feedback[0].Ref.Message != delivery.MessageID(msg) {
		t.Errorf("the notices of the sender: %+v, want one lost_at_target for b", out.Feedback)
	}
	if ins := inRecords(t, h, msg); len(ins) != 1 || ins[0].Key.Recipient != endpointOf(t, h, "c") {
		t.Errorf("the target holds %d records after the loss, want only c's: the source must not send the message again", len(ins))
	}
}

// The source pulls until the delivery's report_by and no longer. An answer that
// the target holds no record, which reaches the source after that moment (it was
// asked before, and the answer was slow), changes nothing: the pull window the
// target keeps its records for is over, and the delivery is settled as it would
// have been had the answer never come.
func TestNoRecordAfterReportByChangesNothing(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	msg := send(t, h, "a", "b", "its record will be lost")
	quiet(t, h)
	reportBy := sourceDelivery(t, h, msg).ReportBy
	loseTargetRecords(t, h, msg, func() { h.Delay(reportBy.Sub(h.Clock().Now()) + time.Minute) }, "b")

	// The restart's pull is waiting out its delay; the time moves past report_by
	// and the answer comes after it.
	h.Advance(reportBy.Sub(h.Clock().Now()) + time.Minute)
	quiet(t, h)
	if got := feedbackOf(t, h, msg); len(got) != 0 {
		t.Errorf("the sender is told %v by an answer that came after report_by, want nothing", got)
	}
	if d := sourceDelivery(t, h, msg); d.State == delivery.StateDeliveryUnknown || d.Unknown != 0 {
		t.Errorf("the delivery is %s (%s) after a late answer, want it not a loss", d.State, d.Unknown)
	}
}
