package engine_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/reply"
	"github.com/aianyai/sidera/internal/trust"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func boolp(b bool) *bool { return &b }

// wouldRefuseARemoteSender is the premise of the tests about node rules: the
// policy, applied the way it is to a node elsewhere that is trusted directly,
// refuses a message to the endpoint. What the tests then show is that a local
// sender is not judged by it.
func wouldRefuseARemoteSender(t *testing.T, p authz.Policy, now time.Time, from, to ident.Address) {
	t.Helper()
	v := authz.Evaluate(p, authz.Request{
		Now: now, Trust: trust.AllowedDirect, Stage: authz.StageIntake,
		Sender: authz.Sender{Node: from.Node, Endpoint: from.Endpoint},
		Action: authz.ActionMessageSend, Target: authz.Target{Kind: authz.TargetEndpoint, Endpoint: to.Endpoint},
	})
	if v.Outcome != authz.OutcomeReject {
		t.Fatalf("the policy does not refuse a remote sender (%v): the test would prove nothing", v)
	}
}

func TestLocalSourcesIgnoreNodeRules(t *testing.T) {
	empty, err := authz.NewPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	denied, err := authz.NewPolicy(append(authz.DefaultPolicy().Rules(),
		authz.Deny(authz.Members(), authz.AllLocalEndpoints(), authz.ActionMessageSend, authz.Conditions{})))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		policy authz.Policy
	}{
		{"a policy that allows nothing", empty},
		{"a policy that denies every message to every local endpoint", denied},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := nodeWith(t, harness.Options{Policy: &config.PolicyFile{Version: 1, Policy: c.policy}}, "a", "b")
			wouldRefuseARemoteSender(t, h.Config().Policy().Policy, h.Clock().Now(), addrOf(t, h, "a"), addrOf(t, h, "b"))

			msg := send(t, h, "a", "b", "under rules that would stop a stranger")
			quiet(t, h)

			in := inOf(t, h, msg)
			if in.Record.State != delivery.StateAccepted {
				t.Errorf("the target record is %s (%s), want accepted: the node rules were applied to a local sender", in.Record.State, in.Record.Reject)
			}
			if d := sourceDelivery(t, h, msg); d.State != delivery.StateAccepted {
				t.Errorf("the sender's record is %s (%s), want accepted: the receipt did not come back", d.State, d.Reject)
			}
		})
	}
}

func TestEndpointDeclarationRestrictsLocalSenders(t *testing.T) {
	for _, c := range []struct {
		name    string
		declare func(h *harness.H) fakeagent.DeclareInput
		want    delivery.State
		reject  delivery.RejectReason
	}{
		{"a paused endpoint declines", func(*harness.H) fakeagent.DeclareInput { return fakeagent.DeclareInput{Paused: boolp(true)} }, delivery.StateRejected, delivery.RejectEndpointDeclined},
		{"a sender that is not listed is declined", func(*harness.H) fakeagent.DeclareInput {
			return fakeagent.DeclareInput{RestrictSenders: boolp(true), AcceptFrom: &[]string{}}
		}, delivery.StateRejected, delivery.RejectEndpointDeclined},
		{"a sender that is listed is accepted", func(h *harness.H) fakeagent.DeclareInput {
			return fakeagent.DeclareInput{RestrictSenders: boolp(true), AcceptFrom: &[]string{h.Addr("a")}}
		}, delivery.StateAccepted, delivery.RejectNone},
		{"an endpoint that declares nothing accepts", func(*harness.H) fakeagent.DeclareInput {
			return fakeagent.DeclareInput{HiddenFromDirectory: boolp(true)}
		}, delivery.StateAccepted, delivery.RejectNone},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := nodeWith(t, harness.Options{}, "a", "b")
			if err := h.Agent("b").Declare(h.Ctx(), c.declare(h)); err != nil {
				t.Fatalf("declare: %v", err)
			}
			msg := send(t, h, "a", "b", "does the endpoint take me")
			quiet(t, h)

			got := inOf(t, h, msg)
			if got.Record.State != c.want || got.Record.Reject != c.reject {
				t.Errorf("target record: %s (%s), want %s (%s)", got.Record.State, got.Record.Reject, c.want, c.reject)
			}
			// The sender hears the same thing, from the signed report of the receipt.
			if d := sourceDelivery(t, h, msg); d.State != c.want || d.Reject != c.reject {
				t.Errorf("sender's record: %s (%s), want %s (%s)", d.State, d.Reject, c.want, c.reject)
			}
		})
	}
}

func TestReplyWithEvidenceAdmittedBeforeReportPulled(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	// a takes nobody's messages: only an answer to one of its own can reach it.
	if err := h.Agent("a").Declare(h.Ctx(), fakeagent.DeclareInput{RestrictSenders: boolp(true), AcceptFrom: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	// Control: a plain message from b to a is declined.
	plain := send(t, h, "b", "a", "an unprompted message")

	// Hold the sender's calls, learn the message id, arm the knob that keeps the
	// reports from a, and let the message go.
	h.Unreachable(true)
	msg := send(t, h, "a", "b", "a question")
	quiet(t, h)
	h.HoldReports(string(msg))
	h.Unreachable(false)
	h.Advance(resendBackoff)
	quiet(t, h)

	if d := sourceDelivery(t, h, msg); d.State != delivery.StateQueued {
		t.Fatalf("a's record of the question is %s, want queued: the report was not held", d.State)
	}
	// a asks for the report and is not given it.
	h.Advance(pullInterval)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateQueued {
		t.Fatalf("a's record of the question is %s after a pull, want queued", d.State)
	}
	if got := knobHits(t, h)["hold-reports"]; got < 2 {
		t.Fatalf("the held reports were reached %d times, want the receipt and the pull", got)
	}

	// b answers. The answer carries b's own report of the question.
	res, err := h.Agent("b").Reply(h.Ctx(), fakeagent.ReplyInput{MessageID: msg, Body: "an answer"})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	quiet(t, h)

	in := inOf(t, h, res.MessageID)
	if in.Record.State != delivery.StateAccepted {
		t.Errorf("the answer is %s (%s) at a, want accepted", in.Record.State, in.Record.Reject)
	}
	if !in.ReplyDecision.Granted() || in.ReplyDecision.Reason != reply.ReasonArrivalWindow {
		t.Errorf("the reply permission is %v, want one that starts at the arrival the evidence proved", in.ReplyDecision.Reason)
	}
	// The evidence taught a the state of its question, which no pull had.
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateAccepted {
		t.Errorf("a's record of the question is %s after the answer, want accepted from the evidence", d.State)
	}
	if p := inOf(t, h, plain); p.Record.State != delivery.StateRejected || p.Record.Reject != delivery.RejectEndpointDeclined {
		t.Errorf("the unprompted message is %s (%s), want rejected (endpoint_declined)", p.Record.State, p.Record.Reject)
	}
}

func TestForgedReplyToGetsNoPermission(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b", "c")
	// a takes nobody's messages: only a reply with permission can reach it.
	if err := h.Agent("a").Declare(h.Ctx(), fakeagent.DeclareInput{RestrictSenders: boolp(true), AcceptFrom: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	msg := send(t, h, "a", "b", "a question for b")
	quiet(t, h)

	// c was never sent the question and says it answers it.
	aAddr, cAddr := addrOf(t, h, "a"), addrOf(t, h, "c")
	rc, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), cAddr.Endpoint, forged(t, h, "forged-1", aAddr, msg, "I answer what I never received"))
	if err != nil {
		t.Fatalf("the forged envelope was refused outright: %v", err)
	}
	if len(rc.Reports) != 1 {
		t.Fatalf("the receipt has %d reports, want 1", len(rc.Reports))
	}
	rep, err := h.VerifyReport(rc.Reports[0])
	if err != nil {
		t.Fatalf("the report of the forged envelope: %v", err)
	}
	if rep.Message != "forged-1" || rep.Recipient != aAddr.Endpoint {
		t.Errorf("the report is about %s for %s", rep.Message, rep.Recipient)
	}
	if rep.Status.State != delivery.StateRejected || rep.Status.Reject != delivery.RejectEndpointDeclined {
		t.Errorf("the forged answer got %s (%s), want rejected (endpoint_declined): the declaration must stop it", rep.Status.State, rep.Status.Reject)
	}
	in := inOf(t, h, "forged-1")
	if in.ReplyDecision.Granted() {
		t.Errorf("the forged reply_to gave permission: %v", in.ReplyDecision.Reason)
	}

	// Control: b, who was sent the question, is let in by the same declaration.
	res, err := h.Agent("b").Reply(h.Ctx(), fakeagent.ReplyInput{MessageID: msg, Body: "the real answer"})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	quiet(t, h)
	real := inOf(t, h, res.MessageID)
	if real.Record.State != delivery.StateAccepted || !real.ReplyDecision.Granted() {
		t.Errorf("the real answer is %s (%s) with permission %v, want accepted with permission", real.Record.State, real.Record.Reject, real.ReplyDecision.Reason)
	}
}

func TestLostReceiptRetryKeepsOneRecord(t *testing.T) {
	var written inWriteCounter
	h := nodeWith(t, harness.Options{Observer: &written}, "a", "b")
	h.DropReceipt(1)
	msg := send(t, h, "a", "b", "once")
	quiet(t, h)

	// The target processed it; the sender heard nothing and still waits.
	first := inOf(t, h, msg)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateQueued {
		t.Fatalf("the sender's record is %s, want queued: it has no receipt", d.State)
	}

	writtenBefore := written.writes()
	h.Advance(resendBackoff)
	quiet(t, h)

	if got := knobHits(t, h)["drop-receipt"]; got != 1 {
		t.Errorf("the dropped receipt was hit %d times, want 1", got)
	}
	ins := inRecords(t, h, msg)
	if len(ins) != 1 {
		t.Fatalf("the target holds %d records of the message, want 1", len(ins))
	}
	if got := written.writes(); got != writtenBefore || !ins[0].AcceptedAt.Equal(first.AcceptedAt) {
		t.Errorf("the target record was written again (%d writes of target records since the resend, accepted at %v, was %v): a resend must find the record and leave it",
			got-writtenBefore, ins[0].AcceptedAt, first.AcceptedAt)
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateAccepted {
		t.Errorf("the sender's record is %s after the resend, want accepted", d.State)
	}
}

func TestSameIDDifferentBodyConflicts(t *testing.T) {
	t.Run("the same idempotency key with other content is refused at the source", func(t *testing.T) {
		h := nodeWith(t, harness.Options{}, "a", "b")
		first, err := trySend(h, "a", "b", "one body", "key-1")
		if err != nil {
			t.Fatal(err)
		}
		again, err := trySend(h, "a", "b", "one body", "key-1")
		if err != nil || again.MessageID != first.MessageID || !again.Replayed {
			t.Fatalf("the same content again: %+v, %v; want the first message, replayed", again, err)
		}
		_, err = trySend(h, "a", "b", "another body", "key-1")
		if fakeagent.CategoryOf(err) != fakeagent.CategoryConflict || fakeagent.CodeOf(err) != "idempotency_conflict" {
			t.Fatalf("other content under the key: %v, want conflict/idempotency_conflict", err)
		}
		quiet(t, h)
		if n := len(scanOf(t, h).Out); n != 1 {
			t.Errorf("%d source records, want the one the first send made", n)
		}
	})

	t.Run("the same message id with another body is refused at the target", func(t *testing.T) {
		h := nodeWith(t, harness.Options{}, "a", "b")
		original := "the body the sender wrote"
		// Keep the sender's first call off the wire, arm the replay for the id it
		// chose, then let the call go: the replay is made when the target has taken
		// the next send of that id.
		h.Unreachable(true)
		msg := send(t, h, "a", "b", original)
		quiet(t, h)
		h.Mutate(string(msg))
		h.Unreachable(false)
		h.Advance(resendBackoff)
		quiet(t, h)

		replay, ok := h.Mutated(string(msg))
		if !ok {
			t.Fatal("the replay with another body was not made")
		}
		if reason, isRefusal := engine.ReasonOf(replay.Err); !isRefusal || reason != engine.ReasonMessageConflict {
			t.Fatalf("the replay got %v, want a refusal for a conflict", replay.Err)
		}
		in := inOf(t, h, msg)
		want := store.BodyRefOf([]byte(original))
		if in.Body == nil || in.Body.Digest != want.Digest || in.Record.State != delivery.StateAccepted {
			t.Errorf("the target record changed: body %v, state %s; want the original body, accepted", in.Body, in.Record.State)
		}
		if d := sourceDelivery(t, h, msg); d.State != delivery.StateAccepted {
			t.Errorf("the sender's record is %s: the replay was not the sender's and must not touch it", d.State)
		}
		if sum := sha256.Sum256([]byte(original)); in.Body.Digest != sum {
			t.Errorf("the stored body is not the original")
		}
	})
}

// A message identifier names one kind of request for a sender and a recipient. A
// message sent under the identifier of a control signal the node holds is refused:
// taken in, it would have a record whose report no pull could reach, because a pull
// answers from the control record that holds the identifier.
func TestAMessageUnderTheIdentifierOfAControlSignalIsAConflict(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a", "b")
	toAddr := addrOf(t, h, "b")
	from := endpointOf(t, h, "a")
	if _, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), from, forgedControl(t, h, "shared-id", toAddr, envelope.KindEndpointStatus, "")); err != nil {
		t.Fatalf("the control signal: %v", err)
	}
	_, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), from, forged(t, h, "shared-id", toAddr, "", "a message"))
	if reason, ok := engine.ReasonOf(err); !ok || reason != engine.ReasonMessageConflict {
		t.Fatalf("a message under the identifier of a control signal got %v, want a refusal for a conflict", err)
	}
	if sc := scanOf(t, h); len(sc.In) != 0 || len(sc.Ctl) != 1 {
		t.Errorf("the target holds %d message records and %d control records, want none and the one", len(sc.In), len(sc.Ctl))
	}
}

// The request digest leaves the recipients out when they are not disclosed, so the
// digest alone cannot tell a send again from the same key to someone else.
func TestTheSameIdempotencyKeyForOtherRecipientsIsAConflict(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b", "c")
	first, err := trySend(h, "a", "b", "the same words", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		to   []string
	}{
		{"another recipient", []string{h.Addr("c")}},
		{"more recipients", []string{h.Addr("b"), h.Addr("c")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := len(scanOf(t, h).Out)
			res, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: c.to, Body: "the same words", IdemKey: "key-1"})
			if fakeagent.CategoryOf(err) != fakeagent.CategoryConflict || fakeagent.CodeOf(err) != "idempotency_conflict" {
				t.Fatalf("the key again for %s: %+v, %v; want conflict/idempotency_conflict, not the first message replayed", c.name, res, err)
			}
			quiet(t, h)
			if after := len(scanOf(t, h).Out); after != before {
				t.Errorf("a refused send left %d new source records", after-before)
			}
		})
	}
	again, err := trySend(h, "a", "b", "the same words", "key-1")
	if err != nil || !again.Replayed || again.MessageID != first.MessageID {
		t.Errorf("the same request again: %+v, %v; want the first message, replayed", again, err)
	}
	// Two recipients are one request in the order they were given.
	pair, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b"), h.Addr("c")}, Body: "to two", IdemKey: "key-2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("c"), h.Addr("b")}, Body: "to two", IdemKey: "key-2"}); fakeagent.CodeOf(err) != "idempotency_conflict" {
		t.Errorf("the recipients in another order: %v, want idempotency_conflict", err)
	}
	if got, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b"), h.Addr("c")}, Body: "to two", IdemKey: "key-2"}); err != nil || got.MessageID != pair.MessageID || !got.Replayed {
		t.Errorf("the same two recipients again: %+v, %v; want the first message, replayed", got, err)
	}
}

// Whether the recipients are disclosed to each other is part of the request.
func TestTheSameIdempotencyKeyDisclosingTheRecipientsDifferentlyIsAConflict(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b", "c")
	send := func(disclose bool) (fakeagent.SendResult, error) {
		return h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b"), h.Addr("c")}, Body: "to two", IdemKey: "key-1", DiscloseRecipients: disclose})
	}
	first, err := send(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := send(false); fakeagent.CodeOf(err) != "idempotency_conflict" {
		t.Errorf("the key again without the disclosure: %v, want idempotency_conflict", err)
	}
	if again, err := send(true); err != nil || !again.Replayed || again.MessageID != first.MessageID {
		t.Errorf("the same request again: %+v, %v; want the first message, replayed", again, err)
	}
}

func TestEndpointBacklogFullIsRejectedWithReason(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	var ids []adapterproto.MessageID
	for i := 0; i <= delivery.DefaultEndpointBacklogLimit; i++ {
		ids = append(ids, send(t, h, "a", "b", fmt.Sprintf("message %d", i)))
	}
	quiet(t, h)

	accepted, full := 0, 0
	for _, in := range scanOf(t, h).In {
		switch {
		case in.Record.State == delivery.StateAccepted:
			accepted++
		case in.Record.State == delivery.StateRejected && in.Record.Reject == delivery.RejectBacklogFull:
			full++
		default:
			t.Errorf("a record of %s is %s (%s)", in.Key.Message, in.Record.State, in.Record.Reject)
		}
	}
	if accepted != delivery.DefaultEndpointBacklogLimit || full != 1 {
		t.Fatalf("%d accepted and %d rejected, want %d and 1", accepted, full, delivery.DefaultEndpointBacklogLimit)
	}
	last := ids[len(ids)-1]
	if in := inOf(t, h, last); in.Record.Reject != delivery.RejectBacklogFull || in.Body != nil {
		t.Errorf("the last message: %s (%s), body kept %v; want rejected (backlog_full) with no body", in.Record.State, in.Record.Reject, in.Body != nil)
	}
	// The sender learns it at once, with the reason, from the signed report.
	d := sourceDelivery(t, h, last)
	if d.State != delivery.StateRejected || d.Reject != delivery.RejectBacklogFull {
		t.Errorf("the sender's record of the last message is %s (%s), want rejected (backlog_full)", d.State, d.Reject)
	}
}

func TestNodeQueueFullBackpressuresNewSends(t *testing.T) {
	h := nodeWith(t, harness.Options{NodeSettings: `{"node_queue_limit": 3}`}, "a", "b")
	for i := 0; i < 3; i++ {
		send(t, h, "a", "b", fmt.Sprintf("message %d", i))
		quiet(t, h)
	}
	before := scanOf(t, h)

	_, err := trySend(h, "a", "b", "one too many", "")
	if fakeagent.CategoryOf(err) != fakeagent.CategoryBackpressure || fakeagent.CodeOf(err) != "node_queue_full" {
		t.Fatalf("the fourth send: %v, want backpressure/node_queue_full", err)
	}
	quiet(t, h)
	after := scanOf(t, h)
	if len(after.Out) != len(before.Out) || len(after.In) != len(before.In) || len(after.Bodies) != len(before.Bodies) {
		t.Errorf("a refused send left a trace: %d/%d/%d source, target records and bodies, were %d/%d/%d",
			len(after.Out), len(after.In), len(after.Bodies), len(before.Out), len(before.In), len(before.Bodies))
	}
	results := clientResults(t, h, "a")
	if last := results[len(results)-1]; last.Attrs.Category != "backpressure" || last.Attrs.Code != "node_queue_full" {
		t.Errorf("the ledger holds %s/%s for the refused send", last.Attrs.Category, last.Attrs.Code)
	}
}

func TestDiskFullGivesNoFalseAcknowledgement(t *testing.T) {
	enospc := fsxtest.DiskFull
	eio := errors.New("fsync: input/output error")
	// The volume of the test is the machine's own; a machine with less than the
	// reserve free would refuse the send before the write the fault waits for.
	roomy := func() uint64 { return 1 << 40 }
	for _, c := range []struct {
		name     string
		arm      func(h *harness.H) *fsxtest.Rule
		diskFree func() uint64
		code     string
	}{
		{"a write that finds the disk full", func(h *harness.H) *fsxtest.Rule {
			return h.Faults().Fail(fsxtest.Match{Tx: "intake", Step: fsx.StepWrite}, enospc)
		}, roomy, "storage_full"},
		{"an fsync of the new file that fails", func(h *harness.H) *fsxtest.Rule {
			return h.Faults().Fail(fsxtest.Match{Tx: "intake", Step: fsx.StepSync}, eio)
		}, roomy, "storage_unavailable"},
		{"a volume with less free space than the reserve", nil, func() uint64 { return 1 }, "storage_full"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := nodeWith(t, harness.Options{FS: harness.RealFS, DiskFree: c.diskFree}, "a", "b")
			var rule *fsxtest.Rule
			if c.arm != nil {
				rule = c.arm(h)
			}
			_, err := trySend(h, "a", "b", "does this reach the disk", "")
			if fakeagent.CategoryOf(err) != fakeagent.CategoryBackpressure || fakeagent.CodeOf(err) != c.code {
				t.Fatalf("the send: %v, want backpressure/%s", err, c.code)
			}
			if rule != nil && rule.Hits() == 0 {
				t.Error("the fault was never injected")
			}
			quiet(t, h)
			sc := scanOf(t, h)
			if len(sc.Out) != 0 || len(sc.In) != 0 {
				t.Errorf("%d source and %d target records after a send that was refused", len(sc.Out), len(sc.In))
			}
			for _, e := range clientResults(t, h, "a") {
				if e.Attrs.Category == "ok" {
					t.Errorf("the ledger holds an acknowledged call (%s)", e.Attrs.Action)
				}
			}
			if n := knobHits(t, h); len(n) != 0 {
				t.Errorf("the loopback was reached for a message that was never accepted: %v", n)
			}
		})
	}
}

// The disk of the target is full as the target writes the record of a message: the
// target answers with an error and no receipt, so the sender does not take the
// message for delivered, keeps it queued, and a send again, once the disk has room,
// is taken in.
func TestDiskFullAtTheTargetGivesNoReceipt(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	// The first write of the target record fails; the next one finds room.
	rule := h.Faults().Fail(fsxtest.Match{Tx: "deliver", Role: "in", Step: fsx.StepWrite, Hit: 1}, fsxtest.DiskFull)
	msg := send(t, h, "a", "b", "does it reach the target's disk")
	quiet(t, h)
	if rule.Hits() == 0 {
		t.Fatal("the fault was never injected")
	}
	if got := len(inRecords(t, h, msg)); got != 0 {
		t.Errorf("%d target records written on a disk that was full", got)
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateQueued {
		t.Errorf("the sender's record is %s, want queued: no receipt came", d.State)
	}

	h.Advance(resendBackoff)
	quiet(t, h)
	if in := inOf(t, h, msg); in.Record.State != delivery.StateAccepted {
		t.Errorf("the target record is %s after the disk had room, want accepted", in.Record.State)
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateAccepted {
		t.Errorf("the sender's record is %s after the resend, want accepted", d.State)
	}
}

// A message has a body, as the envelope has it: a send without one is refused as a
// request that is not well formed, and nothing is recorded.
func TestASendWithAnEmptyBodyIsRefused(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	for _, body := range []string{"", string([]byte{})} {
		_, err := trySend(h, "a", "b", body, "")
		if fakeagent.CategoryOf(err) != fakeagent.CategoryInvalidArgument || fakeagent.CodeOf(err) != "malformed_request" {
			t.Errorf("a send with the body %q: %v, want invalid_argument/malformed_request", body, err)
		}
	}
	quiet(t, h)
	if sc := scanOf(t, h); len(sc.Out) != 0 || len(sc.Bodies) != 0 {
		t.Errorf("%d source records and %d bodies after sends that were refused", len(sc.Out), len(sc.Bodies))
	}
}

// The questions below are rules that the contract packages or the store do not
// answer, kept in sight until they are settled where they belong.

// A status report carries the moment the target recorded the state it reports,
// and a delivered one the moment of delivery: both are what the record kept when
// it entered the state, never the time it was accepted or the time of the report.
func TestAReportIsDatedWithTheMomentTheTargetRecordedTheStateItReports(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	b := addrOf(t, h, "b")
	accepted := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	entered := accepted.Add(7 * time.Minute)
	key := store.TargetKey{SenderNode: harness.NodeID(), Sender: addrOf(t, h, "a").Endpoint, Message: "dated", Recipient: b.Endpoint}
	for _, c := range []struct {
		name      string
		state     delivery.State
		reject    delivery.RejectReason
		unknown   delivery.UnknownCause
		delivered bool
	}{
		{"accepted", delivery.StateAccepted, 0, 0, false},
		{"rejected at intake", delivery.StateRejected, delivery.RejectEndpointDeclined, 0, false},
		{"rejected later, when the endpoint closed", delivery.StateRejected, delivery.RejectEndpointClosed, 0, false},
		{"rejected later, when the backlog deadline passed", delivery.StateRejected, delivery.RejectBacklogExpired, 0, false},
		{"rejected later, by the second check", delivery.StateRejected, delivery.RejectSecondCheckFailed, 0, false},
		{"delivered", delivery.StateDelivered, 0, 0, true},
		{"delivery_unknown", delivery.StateDeliveryUnknown, 0, delivery.UnknownHandedOver, false},
		{"closed_unconfirmed", delivery.StateClosedUnconfirmed, 0, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec, err := delivery.NewTargetRecord(accepted, accepted.Add(time.Hour), delivery.ReportBy(accepted.Add(time.Hour)), delivery.DefaultEndpointMaxQueue)
			if err != nil {
				t.Fatal(err)
			}
			rec.State, rec.Reject, rec.Unknown, rec.StateAt = c.state, c.reject, c.unknown, entered
			signed, err := engine.SignReport(h.Ctx(), h.Engine(), store.InRecord{Key: key, Record: rec, AcceptedAt: accepted})
			if err != nil {
				t.Fatal(err)
			}
			rep, err := h.VerifyReport(signed)
			if err != nil {
				t.Fatal(err)
			}
			if !rep.ReportedAt.Equal(entered) {
				t.Errorf("reported at %v, want the moment the state was entered, %v", rep.ReportedAt, entered)
			}
			if c.delivered != !rep.DeliveredAt.IsZero() || (c.delivered && !rep.DeliveredAt.Equal(entered)) {
				t.Errorf("delivered at %v, want %v exactly when the state is delivered", rep.DeliveredAt, entered)
			}
		})
	}

	t.Run("a pull after the endpoint closed reports the rejection with the time of the closing", func(t *testing.T) {
		msg := "taken-then-closed"
		accepted := h.Clock().Now().Truncate(time.Second)
		if _, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), addrOf(t, h, "a").Endpoint, forged(t, h, msg, b, "", "x")); err != nil {
			t.Fatal(err)
		}
		pull := func() envelope.StatusReport {
			t.Helper()
			ans, err := pullFor(h, adapterproto.MessageID(msg), b.Endpoint)
			if err != nil || len(ans.Reports) != 1 {
				t.Fatalf("the answer to a pull: %+v, %v", ans, err)
			}
			rep, err := h.VerifyReport(ans.Reports[0])
			if err != nil {
				t.Fatal(err)
			}
			return rep
		}
		if rep := pull(); rep.Status.State != delivery.StateAccepted || !rep.ReportedAt.Equal(accepted) {
			t.Fatalf("before the closing: %s at %v, want accepted at %v", rep.Status.State, rep.ReportedAt, accepted)
		}
		h.Advance(5 * time.Minute)
		closedAt := h.Clock().Now().Truncate(time.Second)
		if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: b.Endpoint}); err != nil {
			t.Fatal(err)
		}
		quiet(t, h)
		h.Advance(time.Minute)
		rep := pull()
		if rep.Status.State != delivery.StateRejected || rep.Status.Reject != delivery.RejectEndpointClosed || !rep.ReportedAt.Equal(closedAt) {
			t.Errorf("after the closing: %s (%s) at %v, want rejected (endpoint_closed) at %v", rep.Status.State, rep.Status.Reject, rep.ReportedAt, closedAt)
		}
		h.Advance(time.Minute)
		if again := pull(); !again.ReportedAt.Equal(closedAt) {
			t.Errorf("a pull again for an unchanged state is dated %v, want the same %v", again.ReportedAt, closedAt)
		}
	})
}

// The target compares a message sent again by the digest of its request, which
// covers the sender's choice to disclose the recipients to each other. The source
// record keeps the choice, so the message sent again is the same request, and a
// disclosed list of several recipients is taken like any other.
func TestAMessageToSeveralRecipientsSentAgainIsTheSameRequestWhetherOrNotTheyAreDisclosed(t *testing.T) {
	for _, disclose := range []bool{true, false} {
		t.Run(fmt.Sprintf("disclosed %v", disclose), func(t *testing.T) {
			h := nodeWith(t, harness.Options{}, "a", "b", "c")
			to := []string{h.Addr("b"), h.Addr("c")}
			// The target takes the message in, the sender hears nothing and sends again.
			h.DropReceipt(1)
			res, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: to, Body: "to two", DiscloseRecipients: disclose})
			if err != nil {
				t.Fatal(err)
			}
			quiet(t, h)
			if d := sourceDelivery(t, h, res.MessageID); d.State != delivery.StateQueued {
				t.Fatalf("the sender's record is %s, want queued: it has no receipt", d.State)
			}
			h.Advance(resendBackoff)
			quiet(t, h)

			o := outOf(t, h, res.MessageID)
			if o.Envelope.DiscloseRecipients != disclose {
				t.Errorf("the source record keeps disclose_recipients %v, want %v", o.Envelope.DiscloseRecipients, disclose)
			}
			for _, r := range o.Outbound.Recipients {
				if r.Delivery.State != delivery.StateAccepted {
					t.Errorf("the delivery to %s is %s after the resend, want accepted: the target must have taken it for the same request", r.Address, r.Delivery.State)
				}
			}
			// The digest the target stored is the one of the request as the sender
			// made it, worked out here from the request alone.
			want := envelope.Envelope{
				ID: o.Outbound.Message, To: []ident.Address{addrOf(t, h, "b"), addrOf(t, h, "c")}, MultiRecipient: true, DiscloseRecipients: disclose,
				Kind: envelope.KindMessage, ContentType: "text/plain", Body: []byte("to two"),
				AcceptBefore: o.Outbound.AcceptBefore, ReportBy: o.Envelope.ReportBy,
			}.Digest()
			ins := inRecords(t, h, res.MessageID)
			if len(ins) != 2 {
				t.Fatalf("%d target records, want 2", len(ins))
			}
			for _, in := range ins {
				if in.Dedup.Digest != want {
					t.Errorf("the target of %s stored another request digest than the sender's request has", in.Key.Recipient)
				}
			}
		})
	}
}
