package engine_test

import (
	"context"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/transport"
)

// A setting the owner changes is read by the engine when it uses it, so the
// change is in force for the next command and needs no restart: a queue limit of
// one holds back the second send with back pressure, a backlog limit of one makes
// the endpoint reject the second message with the reason that names the backlog.
func TestChangedSettingsAreInForceForTheNextCommand(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	e := h.Engine()
	ctx := context.Background()

	if _, err := e.Execute(ctx, endpointSetCommand(101, "backlog of one", h.Config().Instances().Version, addrOf(t, h, "b").Endpoint, `{"backlog_limit":1}`)); err != nil {
		t.Fatal(err)
	}
	first := send(t, h, "a", "b", "fits")
	second := send(t, h, "a", "b", "does not fit")
	quiet(t, h)
	if in := inOf(t, h, first); in.Record.State != delivery.StateAccepted {
		t.Errorf("the first message is %s, want accepted", in.Record.State)
	}
	if d := sourceDelivery(t, h, second); d.State != delivery.StateRejected || d.Reject != delivery.RejectBacklogFull {
		t.Errorf("the second message is %s (%s), want rejected for the backlog", d.State, d.Reject)
	}

	if _, err := e.Execute(ctx, setNodeSettingsCommand(102, "queue of one", h.Config().Node().Version, `{"node_queue_limit":1}`)); err != nil {
		t.Fatal(err)
	}
	_, err := trySend(h, "a", "b", "the queue holds one already", "")
	if fakeagent.CategoryOf(err) != fakeagent.CategoryBackpressure || fakeagent.CodeOf(err) != "node_queue_full" {
		t.Errorf("send with a queue limit of one and a delivery that waits: %v; want backpressure/node_queue_full", err)
	}
}

// Once the owner has retired the identity nothing is signed in the node's name,
// even before the daemon is down: an envelope is not taken in, no report is
// signed, and what was taken in stays as it was.
func TestAfterTheIdentityIsRetiredNothingIsSigned(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	msg := send(t, h, "a", "b", "before the retirement")
	quiet(t, h)
	res, err := h.Engine().Execute(context.Background(), retireCommand(103, "retire", harness.NodeID()))
	if err != nil || !res.Stop {
		t.Fatalf("the retirement: %+v, %v", res, err)
	}
	_, err = h.Loopback().Send(h.Ctx(), harness.NodeID(), addrOf(t, h, "b").Endpoint, forged(t, h, "after", addrOf(t, h, "a"), "", "x"))
	if reason, ok := engine.ReasonOf(err); !ok || reason != engine.ReasonIdentityRetired {
		t.Errorf("an envelope taken in after the retirement: %v; want the refusal for the identity", err)
	}
	if _, err := h.Loopback().Pull(h.Ctx(), harness.NodeID(), transport.PullRequest{Message: delivery.MessageID(msg)}); err == nil {
		t.Error("a report was signed after the retirement")
	}
	if in := inOf(t, h, msg); in.Record.State != delivery.StateAccepted {
		t.Errorf("the message taken in before is %s", in.Record.State)
	}
}
