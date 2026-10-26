package engine_test

import (
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
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

// removeTargetRecords stops the node and removes the target records of the
// message for the named recipients from its disk, and leaves the node down. The
// records are the node's; the sender's record, and its receipt, stay.
func removeTargetRecords(t *testing.T, h *harness.H, msg adapterproto.MessageID, names ...string) {
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
}

// loseTargetRecords makes the node lose the target records of the message for the
// named recipients, the way a deleted file does, and starts it again. The test
// may do something while the node is down (before, which may be nil). The node
// pulls what it has not settled as it starts.
func loseTargetRecords(t *testing.T, h *harness.H, msg adapterproto.MessageID, before func(), names ...string) {
	t.Helper()
	removeTargetRecords(t, h, msg, names...)
	if before != nil {
		before()
	}
	h.Start()
	quiet(t, h)
}

// callsOf counts the calls of the kind ("send" or "pull") that the delay knob
// held, from the ledger. The loopback keeps no count of its calls, and a delay
// every call waits out makes each one a hit that names its kind: it is how a test
// shows that the source made a call, or that it made none.
func callsOf(t testing.TB, h *harness.H, call string) int {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	n := 0
	for _, e := range log.Kind(ledger.KindKnobHit) {
		if e.Attrs.Knob == "delay" && strings.Contains(e.Attrs.Detail, "call="+call+" ") {
			n++
		}
	}
	return n
}

// letTheCallThrough waits for the one call that waits out the delay, lets the
// delay pass and returns when the node has settled. A node that does not settle
// is held by a call the test did not expect, and the failure says which calls
// were made.
func letTheCallThrough(t testing.TB, h *harness.H, delay time.Duration) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for h.Loopback().Waiting() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("the source made no call that waits out the delay (%d pulls and %d sends so far)", callsOf(t, h, "pull"), callsOf(t, h, "send"))
		}
		time.Sleep(time.Millisecond)
	}
	h.Advance(delay)
	if err := h.Quiesce(); err != nil {
		t.Fatalf("the node did not settle after a call (%d pulls and %d sends so far): %v", callsOf(t, h, "pull"), callsOf(t, h, "send"), err)
	}
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
	// The recipient whose record is lost needs no signature to be listed, which is
	// why the refusal has to be the node's and not left to the signing of reports.
	ans, err = pullFor(h, msg, b)
	if reason, ok := engine.ReasonOf(err); !ok || reason != engine.ReasonClockRollback || len(ans.NoRecord) != 0 {
		t.Errorf("a pull for the lost recipient alone while the clock is in doubt: %+v, %v, want the refusal for the clock", ans, err)
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

	// Every call from the restart on waits out a delay, so that the calls the
	// source makes can be counted: it pulls, and it never sends again.
	const delay = time.Second
	removeTargetRecords(t, h, msg, "b")
	h.Delay(delay)
	h.Start()
	// The pull of the start waits out its delay. The node is only quiet once the
	// call is waiting, and a probe does not see it get there, so the call is waited
	// for first.
	letTheCallThrough(t, h, delay)
	for i := 0; i < 3; i++ {
		h.Advance(pullInterval)
		letTheCallThrough(t, h, delay)
	}
	if pulls, sends := callsOf(t, h, "pull"), callsOf(t, h, "send"); pulls != 4 || sends != 0 {
		t.Errorf("after the loss the source made %d pulls and %d sends, want 4 and none: it must not send again", pulls, sends)
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

// A target says at most one thing about a recipient: its report or its absence.
// An answer that says both is a peer breaking the transport's contract, and the
// source does not know which half to believe, so it keeps the report it can
// verify and takes nothing of the other: it does not turn the delivery into a
// loss, which would tell the sender that a record was lost that the target has
// just reported.
func TestAnAnswerThatReportsAndListsARecipientIsNotReadAsALoss(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b", "c")
	res, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b"), h.Addr("c")}, Body: "to two"})
	if err != nil {
		t.Fatal(err)
	}
	msg := res.MessageID
	quiet(t, h)
	b, c := endpointOf(t, h, "b"), endpointOf(t, h, "c")
	ans, err := pullFor(h, msg, b)
	if err != nil || len(ans.Reports) != 1 {
		t.Fatalf("the answer for a held record: %+v, %v", ans, err)
	}
	out := outOf(t, h, msg)

	// b is reported and listed; c is only listed, which the source may believe.
	apply := func() error {
		var applyErr error
		if err := engine.InLoop(h.Ctx(), h.Engine(), func(l engine.LoopSide) {
			applyErr = l.ApplyAnswer(out.Name(), harness.NodeID(), ans.Reports, []ident.EndpointID{b, c})
		}); err != nil {
			t.Fatal(err)
		}
		return applyErr
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	for _, r := range outOf(t, h, msg).Outbound.Recipients {
		switch r.Address.Endpoint {
		case b:
			if r.Delivery.State != delivery.StateAccepted {
				t.Errorf("the delivery that was reported and listed is %s (%s), want accepted: its report stands", r.Delivery.State, r.Delivery.Unknown)
			}
		case c:
			if r.Delivery.State != delivery.StateDeliveryUnknown || r.Delivery.Unknown != delivery.UnknownLostAtTarget {
				t.Errorf("the delivery that was only listed is %s (%s), want lost_at_target", r.Delivery.State, r.Delivery.Unknown)
			}
		}
	}
	if fb := outOf(t, h, msg).Feedback; len(fb) != 1 || fb[0].Ref.Recipient.Endpoint != c || fb[0].Feedback.Kind != delivery.KindLostAtTarget {
		t.Errorf("the notices of the sender: %+v, want one lost_at_target, for c alone", fb)
	}
	if !slices.ContainsFunc(h.Log().Messages(), func(m string) bool { return strings.Contains(m, "both a report and no record") }) {
		t.Errorf("the log %q has no word of the answer that broke the contract", h.Log().Messages())
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
	removeTargetRecords(t, h, msg, "b")
	h.Delay(reportBy.Sub(h.Clock().Now()) + time.Minute)
	h.Start()
	// The restart's pull waits out its delay. The node is only quiet once the call is
	// waiting, and a probe does not see it get there, so the test waits for it first.
	waitUntil(t, func() bool { return h.Loopback().Waiting() == 1 })
	quiet(t, h)

	// The time moves past report_by and the answer comes after it.
	h.Advance(reportBy.Sub(h.Clock().Now()) + time.Minute)
	quiet(t, h)
	if got := feedbackOf(t, h, msg); len(got) != 0 {
		t.Errorf("the sender is told %v by an answer that came after report_by, want nothing", got)
	}
	if d := sourceDelivery(t, h, msg); d.State == delivery.StateDeliveryUnknown || d.Unknown != 0 {
		t.Errorf("the delivery is %s (%s) after a late answer, want it not a loss", d.State, d.Unknown)
	}
}

// A source that holds no receipt cannot tell a target that lost its record from
// one that never took the message in: it is not told the target lost it, and the
// delivery is left to the deadline rules, which say "may have been received"
// without claiming more. After the delivery deadline the source asks a node it
// never reached instead of sending to it, and that node, which holds nothing,
// answers that it has no record.
func TestSourceWithoutAReceiptIsNotToldTheTargetLostTheRecord(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	h.Unreachable(true)
	msg := send(t, h, "a", "b", "never reached the target")
	quiet(t, h)
	h.Advance(delivery.DefaultAcceptBefore + time.Minute)
	quiet(t, h)
	h.Unreachable(false)
	// The answer the source gets to the pull it makes is "no record": the target
	// holds nothing, and it is asked about the recipient.
	ans, err := pullFor(h, msg, endpointOf(t, h, "b"))
	if err != nil || len(ans.Reports) != 0 || !slices.Equal(ans.NoRecord, []ident.EndpointID{endpointOf(t, h, "b")}) {
		t.Fatalf("the answer to a pull for the recipient: %+v, %v, want it listed in NoRecord", ans, err)
	}
	// Every call from here on waits out a delay, so that the pulls of the source can
	// be counted: it asks the node it never reached, and sends nothing.
	const delay = time.Second
	h.Delay(delay)
	for i := 0; i < 3; i++ {
		h.Advance(pullInterval)
		letTheCallThrough(t, h, delay)
	}
	if pulls, sends := callsOf(t, h, "pull"), callsOf(t, h, "send"); pulls != 3 || sends != 0 {
		t.Errorf("the source made %d pulls and %d sends after the deadline, want 3 and none", pulls, sends)
	}
	if ins := inRecords(t, h, msg); len(ins) != 0 {
		t.Fatalf("the target holds %d records: the message was sent after its deadline", len(ins))
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateQueued {
		t.Errorf("the delivery is %s after the answer that the target holds no record, want it left queued", d.State)
	}
	if got := feedbackOf(t, h, msg); len(got) != 0 {
		t.Errorf("the sender is told %v by an answer about a delivery with no receipt, want nothing", got)
	}
}
