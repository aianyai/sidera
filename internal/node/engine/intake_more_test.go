package engine_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
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
		{"a thread that is not an identifier", func(in *fakeagent.SendInput) { in.Thread = "two words" }, fakeagent.CategoryInvalidArgument, "malformed_request"},
		{"a content type of control characters", func(in *fakeagent.SendInput) { in.ContentType = "text/\x01" }, fakeagent.CategoryInvalidArgument, "malformed_request"},
		{"no recipient", func(in *fakeagent.SendInput) { in.To = nil }, fakeagent.CategoryInvalidArgument, "malformed_request"},
		{"a recipient that is not an address", func(in *fakeagent.SendInput) { in.To = []string{"nobody"} }, fakeagent.CategoryInvalidArgument, "malformed_request"},
		{"the system endpoint as a recipient", func(in *fakeagent.SendInput) {
			in.To = []string{node + "/" + ident.SystemEndpoint().String()}
		}, fakeagent.CategoryInvalidArgument, "malformed_request"},
		{"a disclosed list of several recipients", func(in *fakeagent.SendInput) {
			in.To = []string{h.Addr("b"), h.Addr("c")}
			in.DiscloseRecipients = true
		}, "", ""},
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

// The envelope is what refuses a request that no node would take, and the refusal
// names what it was refused for.
func TestARequestTheEnvelopeRefusesIsRefusedForTheFieldItNames(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	ref := adapterproto.EndpointRef(addrOf(t, h, "a").Endpoint.String())
	good := engine.SendRequest{To: []ident.Address{addrOf(t, h, "b")}, Body: []byte("x"), ContentType: "text/plain"}
	for _, c := range []struct {
		name   string
		mutate func(r *engine.SendRequest)
		reason engine.Reason
		field  string
	}{
		{"no recipient", func(r *engine.SendRequest) { r.To = nil }, engine.ReasonInvalidArgument, "to"},
		{"the system endpoint as a recipient", func(r *engine.SendRequest) {
			r.To = []ident.Address{{Node: harness.NodeID(), Endpoint: ident.SystemEndpoint()}}
		}, engine.ReasonInvalidArgument, "to"},
		{"no address at all as a recipient", func(r *engine.SendRequest) { r.To = []ident.Address{{}} }, engine.ReasonInvalidArgument, "to"},
		{"a recipient twice", func(r *engine.SendRequest) { r.To = []ident.Address{good.To[0], good.To[0]} }, engine.ReasonInvalidArgument, "to"},
		{"no body", func(r *engine.SendRequest) { r.Body = nil }, engine.ReasonInvalidArgument, "body"},
		{"no content type", func(r *engine.SendRequest) { r.ContentType = "" }, engine.ReasonInvalidArgument, "body"},
		{"a body that is not valid UTF-8", func(r *engine.SendRequest) { r.Body = []byte("ab\xffcd") }, engine.ReasonInvalidArgument, "body"},
		{"a body over the limit", func(r *engine.SendRequest) { r.Body = make([]byte, envelope.MaxBody+1) }, engine.ReasonTooLarge, "body"},
		{"a thread that is not an identifier", func(r *engine.SendRequest) { r.Thread = "two words" }, engine.ReasonInvalidArgument, "thread"},
		{"a negative deadline", func(r *engine.SendRequest) { r.AcceptBefore = -time.Second }, engine.ReasonInvalidArgument, "accept_before"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := good
			c.mutate(&r)
			_, err := h.Engine().Send(h.Ctx(), engine.ByAdapter(harness.Instance, ref), r)
			var refusal *engine.Refusal
			if !errors.As(err, &refusal) || refusal.Reason != c.reason || refusal.Field != c.field {
				t.Fatalf("got %v, want a refusal for %s naming %q", err, c.reason, c.field)
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

func TestNoAnswerIsMadeToAMessageTheEndpointRefusedAtIntake(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	if err := h.Agent("b").Declare(h.Ctx(), fakeagent.DeclareInput{Paused: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	refused := send(t, h, "a", "b", "to an endpoint that takes nothing")
	quiet(t, h)
	if in := inOf(t, h, refused); in.Record.State != delivery.StateRejected {
		t.Fatalf("the message is %s, want rejected: the premise of the test", in.Record.State)
	}
	_, err := h.Agent("b").Reply(h.Ctx(), fakeagent.ReplyInput{MessageID: refused, Body: "an answer to what I never had"})
	if fakeagent.CategoryOf(err) != fakeagent.CategoryNotFound || fakeagent.CodeOf(err) != "message_unknown" {
		t.Errorf("the answer: %v, want not_found/message_unknown", err)
	}
	// Control: once the endpoint takes messages, the next one can be answered.
	if err := h.Agent("b").Declare(h.Ctx(), fakeagent.DeclareInput{Paused: boolp(false)}); err != nil {
		t.Fatal(err)
	}
	taken := send(t, h, "a", "b", "to an endpoint that takes it")
	quiet(t, h)
	if _, err := h.Agent("b").Reply(h.Ctx(), fakeagent.ReplyInput{MessageID: taken, Body: "an answer"}); err != nil {
		t.Errorf("the answer to a message the endpoint has: %v", err)
	}
}

func TestADeadlineOfPartOfASecondIsKeptToTheSecondAnEnvelopeCanSay(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	ref := adapterproto.EndpointRef(addrOf(t, h, "a").Endpoint.String())
	res, err := h.Engine().Send(h.Ctx(), engine.ByAdapter(harness.Instance, ref), engine.SendRequest{
		To: []ident.Address{addrOf(t, h, "b")}, Body: []byte("x"), ContentType: "text/plain", AcceptBefore: 90*time.Second + 700*time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	src, tgt := sourceDelivery(t, h, adapterproto.MessageID(res.Message)), inOf(t, h, adapterproto.MessageID(res.Message))
	if src.AcceptBefore.Nanosecond() != 0 || !src.AcceptBefore.Equal(tgt.Record.AcceptBefore) {
		t.Errorf("the deadline is %v at the source and %v at the target: the source must keep what the envelope says", src.AcceptBefore, tgt.Record.AcceptBefore)
	}
}

func TestAMessageToAClosedEndpointIsRejectedAsClosed(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	id, _ := ident.ParseAddress(h.Addr("b"))
	if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: id.Endpoint}); err != nil {
		t.Fatal(err)
	}
	msg := send(t, h, "a", "b", "anyone there")
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateRejected || d.Reject != delivery.RejectEndpointClosed {
		t.Errorf("the sender's record is %s (%s), want rejected (endpoint_closed)", d.State, d.Reject)
	}
	if in := inOf(t, h, msg); in.Record.State != delivery.StateRejected || in.Record.Reject != delivery.RejectEndpointClosed {
		t.Errorf("the target record is %s (%s), want rejected (endpoint_closed)", in.Record.State, in.Record.Reject)
	}
	if sc := scanOf(t, h); len(sc.Bodies) != 0 {
		t.Errorf("%d bodies kept for a message nobody holds", len(sc.Bodies))
	}
}

func TestAMessageToAnEndpointThatIsNotThereIsRejectedAsMissing(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	a := addrOf(t, h, "a")
	// An endpoint identifier nobody registered, on this node.
	stranger := ident.Address{Node: harness.NodeID(), Endpoint: mustEndpoint(t, 200)}
	rc, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), a.Endpoint, forged(t, h, "to-nobody", stranger, "", "x"))
	if err != nil || len(rc.Reports) != 1 {
		t.Fatalf("receipt: %+v, %v", rc, err)
	}
	rep, err := h.VerifyReport(rc.Reports[0])
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status.State != delivery.StateRejected || rep.Status.Reject != delivery.RejectEndpointMissing {
		t.Errorf("the answer is %s (%s), want rejected (endpoint_missing)", rep.Status.State, rep.Status.Reject)
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
	if _, err := pullFor(h, "m", endpointOf(t, h, "b")); err == nil {
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
	if in := inOf(t, h, taken); in.Body == nil || len(sc.Bodies) != 1 || sc.Bodies[0] != store.BodyFileName(in.Name()) {
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

// A body file belongs to one record: a message to two endpoints leaves one body
// file for each target record and one for the source record, each named after
// its record and each holding the message bytes. A target record that reaches a
// final state loses its own body file and no other file is touched.
func TestEachRecordOfAMessageHasABodyFileOfItsOwnThatGoesWithItsFinalState(t *testing.T) {
	const text = "one message, three body files"
	h := nodeWith(t, harness.Options{}, "a", "b", "c")
	for _, name := range []string{"b", "c"} {
		if err := h.Agent(name).Declare(h.Ctx(), fakeagent.DeclareInput{Paused: boolp(false)}); err != nil {
			t.Fatal(err)
		}
	}
	// The reports of the targets are held back, so the source record keeps its body.
	h.Unreachable(true)
	res, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b"), h.Addr("c")}, Body: text})
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	h.HoldReports(string(res.MessageID))
	h.Unreachable(false)
	h.Advance(resendBackoff)
	quiet(t, h)

	ins := inRecords(t, h, res.MessageID)
	if len(ins) != 2 {
		t.Fatalf("%d target records, want 2", len(ins))
	}
	src := outOf(t, h, res.MessageID)
	want := map[string]bool{store.BodyFileName(src.Name()): true}
	for _, in := range ins {
		if in.Record.State != delivery.StateAccepted || in.Body == nil {
			t.Fatalf("target %s is %s with body %v, want accepted and holding a body", in.Key.Recipient, in.Record.State, in.Body)
		}
		want[store.BodyFileName(in.Name())] = true
	}
	if src.Body == nil || len(want) != 3 {
		t.Fatalf("source body %v, %d distinct body file names, want a body and 3 names", src.Body, len(want))
	}
	checkBodyFiles := func(when string) {
		t.Helper()
		got := scanOf(t, h).Bodies
		if len(got) != len(want) {
			t.Errorf("%s: body files %v, want %d of them", when, got, len(want))
		}
		for _, name := range got {
			if !want[name] {
				t.Errorf("%s: unexpected body file %s", when, name)
			}
		}
		for name := range want {
			data, err := h.Disk().ReadFile(path.Join(store.BodiesDir, name))
			if err != nil {
				t.Errorf("%s: body file %s: %v", when, name, err)
			} else if string(data) != text {
				t.Errorf("%s: body file %s holds %q, want the message bytes", when, name, data)
			}
		}
	}
	checkBodyFiles("after the delivery")

	// Closing an endpoint takes its target record to a final state.
	for _, in := range ins {
		if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: in.Key.Recipient}); err != nil {
			t.Fatalf("CloseEndpoint: %v", err)
		}
		quiet(t, h)
		delete(want, store.BodyFileName(in.Name()))
		checkBodyFiles("after " + in.Key.Recipient.String() + " was closed")
	}
}

func TestHeldReportsAreAskedForUntilTheyCome(t *testing.T) {
	var written inWriteCounter
	h := nodeWith(t, harness.Options{Observer: &written}, "a", "b")
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
	inOf(t, h, msg)
	if got := written.writes(); got != 1 {
		t.Errorf("the target record was written %d times, want once: the asks for the held reports leave it", got)
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

func TestNoEvidenceIsSignedWhileTheWallClockCannotBeTrusted(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	// a takes nobody's messages: only an answer to one of its own can reach it, so the
	// answer has to carry evidence that the question arrived.
	if err := h.Agent("a").Declare(h.Ctx(), fakeagent.DeclareInput{RestrictSenders: boolp(true), AcceptFrom: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	question := send(t, h, "a", "b", "a question")
	quiet(t, h)

	// The answer cannot be delivered, and is sent again with evidence signed anew each
	// time.
	h.Unreachable(true)
	if _, err := h.Agent("b").Reply(h.Ctx(), fakeagent.ReplyInput{MessageID: question, Body: "an answer"}); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	h.Advance(resendBackoff)
	quiet(t, h)
	node := harness.NodeID()
	if n := callHits(t, h, "send", node); n < 2 {
		t.Fatalf("the answer was sent %d times while the clock was trusted, want it tried again: the test would prove nothing", n)
	}

	// The wall clock is set back beyond the tolerance, and then runs on past the
	// time the answer is due again.
	h.Set(h.Clock().Now().Add(-10 * time.Minute))
	if _, err := h.Engine().Status(h.Ctx()); err != nil { // a command, in which the loop reads the clock
		t.Fatal(err)
	}
	quiet(t, h)
	before := callHits(t, h, "send", node)
	h.Advance(10 * time.Minute)
	quiet(t, h)
	for i := 0; i < 4; i++ {
		h.Advance(2 * resendBackoff)
		quiet(t, h)
	}
	if after := callHits(t, h, "send", node); after != before {
		t.Errorf("the answer was sent %d more times while the wall clock could not be trusted: its evidence was signed", after-before)
	}
}

func TestANodeThatHasNotAnsweredIsSentToAfterAnotherNodeDid(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	other, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{7}, ident.NodeIDLen))
	if err != nil {
		t.Fatal(err)
	}
	h.UnreachableNode(other, true)
	far := ident.Address{Node: other, Endpoint: addrOf(t, h, "a").Endpoint}
	msg, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b"), far.String()}, Body: "to two nodes"})
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	// The node of b answered at once; the other has not, and is still owed the message.
	if d := sourceDelivery(t, h, msg.MessageID); d.State != delivery.StateAccepted {
		t.Fatalf("the record of the delivery to b is %s, want accepted: the premise of the test", d.State)
	}
	for i := 0; i < 4; i++ {
		h.Advance(resendBackoff)
		quiet(t, h)
	}
	if sends := callHits(t, h, "send", other); sends < 4 {
		t.Errorf("%d sends to the node that did not answer, want one after each wait: it was asked, not sent to, once another node had answered", sends)
	}
	if pulls := callHits(t, h, "pull", other); pulls != 0 {
		t.Errorf("%d pulls of a node that was never sent the message", pulls)
	}
}

func TestTheReportsAreAskedForUntilTheReportDeadlineAndNoLonger(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	h.Unreachable(true)
	msg, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b")}, Body: "x", AcceptBeforeS: 120})
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	reportBy := outOf(t, h, msg.MessageID).Envelope.ReportBy
	// The deadline for sending passes, and the node is asked from then on.
	h.Advance(reportBy.Add(-3 * time.Minute).Sub(h.Clock().Now()))
	quiet(t, h)
	node := harness.NodeID()
	for !h.Clock().Now().After(reportBy) {
		h.Advance(10 * time.Second)
		quiet(t, h)
	}
	asked := callHits(t, h, "pull", node)
	if asked == 0 {
		t.Fatal("the node was not asked before the report deadline: the test would prove nothing")
	}
	// The delivery rules end the sender's interest at the report deadline, and the
	// tolerance of the clock is part of it already.
	for i := 0; i < 8; i++ {
		h.Advance(30 * time.Second)
		quiet(t, h)
	}
	if now := callHits(t, h, "pull", node); now != asked {
		t.Errorf("%d pulls after the report deadline", now-asked)
	}
}

// foreignKeys are the keys of a node that is not the one the engine is built for.
type foreignKeys struct {
	engine.Keys
	node ident.NodeID
}

func (k foreignKeys) Node() ident.NodeID { return k.node }

func TestAnEngineWithTheKeysOfAnotherNodeDoesNotStart(t *testing.T) {
	r := newRig(t, 1)
	d := r.deps()
	d.Keys = foreignKeys{node: mustNode(t, 9)}
	if _, _, err := engine.Open(context.Background(), d, r.params); err == nil {
		t.Fatal("an engine was opened to sign as one node with the keys of another")
	}
	// The same dependencies with the keys of this node do open it, so that the
	// refusal above is about the keys.
	d.Keys = foreignKeys{node: d.Node}
	e, _, err := engine.Open(context.Background(), d, r.params)
	if err != nil {
		t.Fatalf("Open with the keys of the node: %v", err)
	}
	e.Start()
	r.stop(e)
}

// bodyHeldBySourceAlone makes a message from a that b rejected and a never heard
// of, the way a lost report leaves it: the source record of a is the one record
// that refers to the body, and the report of b about its rejection is signed and
// in the hands of the test. It returns the message and that report.
func bodyHeldBySourceAlone(t *testing.T, h *harness.H, body string) (adapterproto.MessageID, record.Record) {
	t.Helper()
	if err := h.Agent("b").Declare(h.Ctx(), fakeagent.DeclareInput{Paused: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	h.Unreachable(true)
	msg := send(t, h, "a", "b", body)
	quiet(t, h)
	h.HoldReports(string(msg))
	h.Unreachable(false)
	h.Advance(resendBackoff)
	quiet(t, h)
	if o := outOf(t, h, msg); o.Body == nil {
		t.Fatal("the source record does not refer to its body while no report has come")
	}
	if in := inOf(t, h, msg); in.Body != nil {
		t.Fatal("the target record of a rejected message refers to a body")
	}
	h.Release(string(msg))
	ans, err := pullFor(h, msg, endpointOf(t, h, "b"))
	if err != nil || len(ans.Reports) != 1 {
		t.Fatalf("the report of the rejection: %+v, %v", ans, err)
	}
	return msg, ans.Reports[0]
}

// Evidence that finishes the delivery it answers drops the source record's
// reference to the body of the message, and the store removes that body in the
// transaction that writes the reply; it does not wait for the sweep at the next
// start. A reply has a body file of its own whatever it says, the bytes of the
// question included, so the body file removed is the source record's and the
// reply keeps its own.
func TestEvidenceThatFinishesASourceDeliveryRemovesTheBodyOfTheSourceRecord(t *testing.T) {
	const question = "a question nobody holds after the evidence"
	for _, c := range []struct{ name, reply string }{
		{"a reply with another body", "another body"},
		{"a reply that says the same bytes", question},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := nodeWith(t, harness.Options{}, "a", "b")
			msg, report := bodyHeldBySourceAlone(t, h, question)
			out := outOf(t, h, msg)
			if got := scanOf(t, h).Bodies; len(got) != 1 || got[0] != store.BodyFileName(out.Name()) {
				t.Fatalf("bodies %v before the reply, want the one of the question", got)
			}
			if _, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), addrOf(t, h, "b").Endpoint,
				forged(t, h, "the-reply", addrOf(t, h, "a"), msg, c.reply, report)); err != nil {
				t.Fatal(err)
			}
			quiet(t, h)
			in := inOf(t, h, "the-reply")
			sc := scanOf(t, h)
			if in.Body == nil || len(sc.Bodies) != 1 || sc.Bodies[0] != store.BodyFileName(in.Name()) {
				t.Fatalf("bodies %v, the reply's reference %v: only the body of the reply may remain", sc.Bodies, in.Body)
			}
			kept, err := h.Disk().ReadFile(path.Join(store.BodiesDir, sc.Bodies[0]))
			if err != nil || string(kept) != c.reply {
				t.Errorf("the body of the reply is %q, %v, want %q", kept, err, c.reply)
			}
			if o := outOf(t, h, msg); o.Body != nil {
				t.Error("the source record still refers to the body of the question")
			}
		})
	}
}
