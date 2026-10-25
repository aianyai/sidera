package engine_test

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/reply"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestASendThatNoNodeWouldTakeIsRefusedAndLeavesNothing(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b", "c")
	a := h.Agent("a")
	good := fakeagent.SendInput{To: []string{h.Addr("b")}, Body: "x"}
	node := harness.NodeID().String()
	for _, c := range []struct {
		name     string
		mutate   func(in *fakeagent.SendInput)
		category fakeagent.Category
		code     string
	}{
		{"a message has a body", func(in *fakeagent.SendInput) { in.Body = "" }, fakeagent.CategoryInvalidArgument, "malformed_request"},
		{"a body over the protocol's limit", func(in *fakeagent.SendInput) { in.Body = strings.Repeat("x", envelope.MaxBody+1) }, fakeagent.CategoryRefused, "too_large"},
		{"a body of exactly the limit", func(in *fakeagent.SendInput) { in.Body = strings.Repeat("x", envelope.MaxBody) }, "", ""},
		{"a deadline beyond the longest", func(in *fakeagent.SendInput) { in.AcceptBeforeS = int64(delivery.MaxAcceptBefore/time.Second) + 1 }, fakeagent.CategoryInvalidArgument, "malformed_request"},
		{"a deadline of the longest", func(in *fakeagent.SendInput) { in.AcceptBeforeS = int64(delivery.MaxAcceptBefore / time.Second) }, "", ""},
		{"no recipient", func(in *fakeagent.SendInput) { in.To = nil }, fakeagent.CategoryInvalidArgument, "malformed_request"},
		{"a recipient that is not an address", func(in *fakeagent.SendInput) { in.To = []string{"nobody"} }, fakeagent.CategoryInvalidArgument, "malformed_request"},
		{"the system endpoint as a recipient", func(in *fakeagent.SendInput) {
			in.To = []string{node + "/" + ident.SystemEndpoint().String()}
		}, fakeagent.CategoryInvalidArgument, "malformed_request"},
		{"a disclosed list of several recipients", func(in *fakeagent.SendInput) {
			in.To = []string{h.Addr("b"), h.Addr("c")}
			in.DiscloseRecipients = true
		}, fakeagent.CategoryInvalidArgument, "malformed_request"},
		{"several recipients that are not disclosed", func(in *fakeagent.SendInput) { in.To = []string{h.Addr("b"), h.Addr("c")} }, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := good
			c.mutate(&in)
			before := len(scanOf(t, h).Out)
			_, err := a.Send(h.Ctx(), in)
			if c.category == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if fakeagent.CategoryOf(err) != c.category || fakeagent.CodeOf(err) != c.code {
				t.Fatalf("got %v, want %s/%s", err, c.category, c.code)
			}
			if after := len(scanOf(t, h).Out); after != before {
				t.Errorf("a refused send left %d new source records", after-before)
			}
		})
	}
}

func TestSequenceNumbersCountEachPairOfSenderAndRecipientFromOne(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b", "c")
	seqOf := func(msg adapterproto.MessageID, to string) uint64 {
		return outOf(t, h, msg).Envelope.Seq[addrOf(t, h, to).Endpoint]
	}
	m1, m2 := send(t, h, "a", "b", "one"), send(t, h, "a", "b", "two")
	c1 := send(t, h, "a", "c", "to the other")
	if _, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b")}, Body: ""}); err == nil {
		t.Fatal("a send without a body was taken")
	}
	m3 := send(t, h, "a", "b", "three")
	b1 := send(t, h, "b", "a", "from the other side")
	quiet(t, h)
	for _, c := range []struct {
		msg  adapterproto.MessageID
		to   string
		want uint64
	}{{m1, "b", 1}, {m2, "b", 2}, {c1, "c", 1}, {m3, "b", 3}, {b1, "a", 1}} {
		if got := seqOf(c.msg, c.to); got != c.want {
			t.Errorf("message %s to %s has number %d, want %d (a refused send takes none)", c.msg, c.to, got, c.want)
		}
	}
	if in := inOf(t, h, m3); in.Handout.Seq != 3 {
		t.Errorf("the target holds number %d for the third message", in.Handout.Seq)
	}
}

func TestAnswersOnlyToMessagesTheEndpointReceived(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	msg := send(t, h, "a", "b", "q")
	quiet(t, h)
	for _, c := range []struct{ name, from, id string }{
		{"an identifier nobody sent", "b", "no-such-message"},
		{"the sender of the message cannot answer it", "a", string(msg)},
	} {
		_, err := h.Agent(c.from).Reply(h.Ctx(), fakeagent.ReplyInput{MessageID: adapterproto.MessageID(c.id), Body: "x"})
		if fakeagent.CategoryOf(err) != fakeagent.CategoryNotFound || fakeagent.CodeOf(err) != "message_unknown" {
			t.Errorf("%s: %v, want not_found/message_unknown", c.name, err)
		}
	}
	res, err := h.Agent("b").Reply(h.Ctx(), fakeagent.ReplyInput{MessageID: msg, Body: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.To; len(got) != 1 || got[0] != h.Addr("a") {
		t.Errorf("the answer is addressed to %v, want the sender of the question", got)
	}
}

func TestAMessageToAClosedEndpointIsRejectedAsMissing(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	id, _ := ident.ParseAddress(h.Addr("b"))
	if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: id.Endpoint}); err != nil {
		t.Fatal(err)
	}
	msg := send(t, h, "a", "b", "anyone there")
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateRejected || d.Reject != delivery.RejectEndpointMissing {
		t.Errorf("the sender's record is %s (%s), want rejected (endpoint_missing): it cannot tell a closed endpoint from none", d.State, d.Reject)
	}
	if sc := scanOf(t, h); len(sc.Bodies) != 0 {
		t.Errorf("%d bodies kept for a message nobody holds", len(sc.Bodies))
	}
}

func TestARemoteSenderIsJudgedByTheNodeRulesAndALocalOneIsNot(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	aAddr := addrOf(t, h, "a")
	other, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{9}, ident.NodeIDLen))
	if err != nil {
		t.Fatal(err)
	}
	sender := addrOf(t, h, "b").Endpoint
	data := forged(t, h, "from-afar", aAddr, "", "a stranger's message")
	if _, err := h.Engine().Inbound(h.Ctx(), transport.RemoteOrigin(other), sender, data); err != nil {
		t.Fatalf("a remote envelope: %v", err)
	}
	var found bool
	for _, in := range scanOf(t, h).In {
		if in.Key.Message == "from-afar" {
			found = true
			if in.Key.SenderNode != other || in.Record.State != delivery.StateRejected || in.Record.Reject != delivery.RejectUnauthorized {
				t.Errorf("the remote message is %s (%s) from %s, want rejected (unauthorized): no node is trusted yet", in.Record.State, in.Record.Reject, in.Key.SenderNode)
			}
		}
	}
	if !found {
		t.Fatal("the remote envelope left no record")
	}
	// The same envelope from the loopback is judged by the endpoint's declarations only.
	if _, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), sender, forged(t, h, "from-here", aAddr, "", "a neighbour's message")); err != nil {
		t.Fatal(err)
	}
	if in := inOf(t, h, "from-here"); in.Record.State != delivery.StateAccepted {
		t.Errorf("the local message is %s (%s)", in.Record.State, in.Record.Reject)
	}
}

func TestNothingIsSignedOrAcceptedWhileTheWallClockCannotBeTrusted(t *testing.T) {
	h := nodeWith(t, harness.Options{RollbackPaused: true}, "a", "b")
	_, err := trySend(h, "a", "b", "x", "")
	if fakeagent.CategoryOf(err) != fakeagent.CategoryBackpressure || fakeagent.CodeOf(err) != "clock_rollback" {
		t.Errorf("send: %v, want backpressure/clock_rollback", err)
	}
	_, err = h.Loopback().Send(h.Ctx(), harness.NodeID(), addrOf(t, h, "b").Endpoint, forged(t, h, "m", addrOf(t, h, "a"), "", "x"))
	if reason, ok := engine.ReasonOf(err); !ok || reason != engine.ReasonClockRollback {
		t.Errorf("an envelope taken in: %v, want a refusal for the clock", err)
	}
	if _, err := h.Loopback().Pull(h.Ctx(), harness.NodeID(), transport.PullRequest{Message: "m"}); err == nil {
		t.Error("a pull was answered with a signature")
	}
	if sc := scanOf(t, h); len(sc.In)+len(sc.Out) != 0 {
		t.Errorf("records were written: %d target, %d source", len(sc.In), len(sc.Out))
	}
}

func TestTheBodyIsHeldOnlyByWhatStillNeedsIt(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	if err := h.Agent("b").Declare(h.Ctx(), fakeagent.DeclareInput{Paused: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	taken := send(t, h, "a", "b", "kept by the target")
	quiet(t, h)
	sc := scanOf(t, h)
	if o := outOf(t, h, taken); o.Body != nil {
		t.Error("the source still refers to the body after the receipt")
	}
	if in := inOf(t, h, taken); in.Body == nil || len(sc.Bodies) != 1 || sc.Bodies[0] != in.Body.Name() {
		t.Errorf("bodies %v, the target's reference %v: the one body must stay for the target that waits for it", sc.Bodies, in.Body)
	}
	if err := h.Agent("b").Declare(h.Ctx(), fakeagent.DeclareInput{Paused: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	refused := send(t, h, "a", "b", "kept by nobody")
	quiet(t, h)
	if o := outOf(t, h, refused); o.Body != nil {
		t.Error("the source of a rejected message still refers to its body")
	}
	if sc := scanOf(t, h); len(sc.Bodies) != 1 {
		t.Errorf("%d bodies, want only the one of the message that was taken in", len(sc.Bodies))
	}
}

func TestHeldReportsAreAskedForUntilTheyCome(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	h.Unreachable(true)
	msg := send(t, h, "a", "b", "x")
	quiet(t, h)
	h.HoldReports(string(msg))
	h.Unreachable(false)
	h.Advance(resendBackoff)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateQueued {
		t.Fatalf("the sender's record is %s while the reports are held", d.State)
	}
	h.Advance(pullInterval)
	quiet(t, h)
	h.Release(string(msg))
	h.Advance(pullInterval)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateAccepted {
		t.Errorf("the sender's record is %s after the report was let through, want accepted", d.State)
	}
	if got := inOf(t, h, msg); got.Rev != 1 {
		t.Errorf("the target record was written again (revision %d)", got.Rev)
	}
}

func TestAnUnreachableNodeIsSentToUntilTheDeadlineAndAskedAfterIt(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	h.Unreachable(true)
	if _, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b")}, Body: "x", AcceptBeforeS: 120}); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	calls := func() (sends, pulls int) {
		log, err := h.Ledger()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range log.Kind(ledger.KindKnobHit) {
			switch {
			case strings.Contains(e.Attrs.Detail, "call=send"):
				sends++
			case strings.Contains(e.Attrs.Detail, "call=pull"):
				pulls++
			}
		}
		return
	}
	for i := 0; i < 4; i++ {
		h.Advance(resendBackoff)
		quiet(t, h)
	}
	sends, pulls := calls()
	if sends != 5 || pulls != 0 {
		t.Fatalf("%d sends and %d pulls inside the deadline, want 5 and 0", sends, pulls)
	}
	h.Advance(resendBackoff)
	quiet(t, h)
	h.Advance(resendBackoff)
	quiet(t, h)
	sends2, pulls2 := calls()
	if sends2 != sends || pulls2 < 1 {
		t.Errorf("after the deadline: %d sends (was %d) and %d pulls, want no more sends and pulls: a node that was never reached is asked, not sent to", sends2, sends, pulls2)
	}
}

func TestEvidenceThatDoesNotVerifyIsDiscardedAndTheOwnerIsToldOfIt(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	if err := h.Agent("a").Declare(h.Ctx(), fakeagent.DeclareInput{RestrictSenders: boolp(true), AcceptFrom: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	// The reports of the question are held, so that nothing but evidence could show
	// that it arrived.
	h.Unreachable(true)
	msg := send(t, h, "a", "b", "q")
	quiet(t, h)
	h.HoldReports(string(msg))
	h.Unreachable(false)
	h.Advance(resendBackoff)
	quiet(t, h)
	_, priv, err := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{5}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := record.NewEd25519Signer(priv)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := record.Sign(envelope.StatusReportType, []byte{0xa0}, signer)
	if err != nil {
		t.Fatal(err)
	}
	b := addrOf(t, h, "b")
	rc, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), b.Endpoint, forged(t, h, "with-bad-evidence", addrOf(t, h, "a"), msg, "an answer", bad))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := h.VerifyReport(rc.Reports[0])
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status.State != delivery.StateRejected || rep.Status.Reject != delivery.RejectEndpointDeclined {
		t.Errorf("the answer got %s (%s): evidence that does not verify earns no permission", rep.Status.State, rep.Status.Reject)
	}
	if in := inOf(t, h, "with-bad-evidence"); in.ReplyDecision.Reason != reply.ReasonNoArrivalProof {
		t.Errorf("the decision is %v, want no proof of arrival", in.ReplyDecision.Reason)
	}
	alerts, err := h.Engine().Alerts(h.Ctx())
	if err != nil {
		t.Fatal(err)
	}
	var told bool
	for _, al := range alerts {
		told = told || al.Kind == "evidence_error"
	}
	if !told {
		t.Error("no alert says that evidence was discarded")
	}
}

func TestARestartGoesOnWithAMessageThatWasNotSettled(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	h.Unreachable(true)
	msg := send(t, h, "a", "b", "before the restart")
	quiet(t, h)
	h.Unreachable(false)
	h.Restart()
	h.MustDirect("a")
	h.MustDirect("b")
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateAccepted {
		t.Errorf("the sender's record is %s after the restart, want accepted: the start goes on with outgoing messages", d.State)
	}
	if n := len(inRecords(t, h, msg)); n != 1 {
		t.Errorf("%d target records", n)
	}
}

func TestAnIdempotencyKeyIsRememberedUntilItsRetentionEnds(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	first, err := trySend(h, "a", "b", "x", "k")
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	again, err := trySend(h, "a", "b", "x", "k")
	if err != nil || !again.Replayed || again.MessageID != first.MessageID {
		t.Fatalf("a repeat inside the retention: %+v, %v", again, err)
	}
	h.Advance(delivery.IdempotencyKeyRetainUntil(delivery.ReportBy(h.Clock().Now().Add(delivery.DefaultAcceptBefore))).Sub(h.Clock().Now()) + time.Hour)
	quiet(t, h)
	late, err := trySend(h, "a", "b", "x", "k")
	if err != nil || late.Replayed || late.MessageID == first.MessageID {
		t.Errorf("a repeat after the retention: %+v, %v; want a new message", late, err)
	}
}

func TestReportsOfASendAreSignedByTheNodeAndNameTheMessage(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	aEP := addrOf(t, h, "a").Endpoint
	rc, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), aEP, forged(t, h, "plain-1", addrOf(t, h, "b"), "", "x"))
	if err != nil || len(rc.Reports) != 1 {
		t.Fatalf("receipt: %+v, %v", rc, err)
	}
	rep, err := h.VerifyReport(rc.Reports[0])
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprint(delivery.StateAccepted)
	if rep.Message != "plain-1" || rep.Sender != harness.NodeID() || rep.Recipient != addrOf(t, h, "b").Endpoint || fmt.Sprint(rep.Status.State) != want {
		t.Errorf("report: %+v", rep)
	}
	if rep.ReportedAt.IsZero() || !rep.DeliveredAt.IsZero() {
		t.Errorf("report times: %v, %v", rep.ReportedAt, rep.DeliveredAt)
	}
}
