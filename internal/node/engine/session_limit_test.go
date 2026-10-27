package engine_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/publication"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The node has one runtime and one sender; direct endpoints take the remaining
// places so a creation is the first operation that would exceed the node cap.
func nodeAtEndpointLimit(t *testing.T) *harness.H {
	t.Helper()
	h := sessionNode(t, harness.Options{}, "a")
	for i := 2; i < publication.MaxEndpointsPerNode; i++ {
		h.MustDirect(keyName(i))
	}
	quiet(t, h)
	if n := openEndpoints(t, h.Engine()); n != publication.MaxEndpointsPerNode {
		t.Fatalf("%d endpoints, want the node cap %d", n, publication.MaxEndpointsPerNode)
	}
	return h
}

func TestSessionCreateAtNodeEndpointLimitFailsAtOnce(t *testing.T) {
	h := nodeAtEndpointLimit(t)
	msg := signal(t, h, "a", h.RuntimeAddr(), adapterproto.SignalSessionCreate, "")
	quiet(t, h)
	wantFailed(t, controlReply(t, h, "a", msg), adapterproto.CreateReasonNodeEndpointLimit)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateDelivered {
		t.Errorf("signal delivery = %s, want delivered by the failed creation reply", d.State)
	}
	ctl := ctlRecords(t, h)
	if len(ctl) != 1 || ctl[0].Record.State != delivery.StateDelivered || !ctl[0].Dedup.HasReply || ctl[0].Intent != nil || !ctl[0].SessionEndpoint.IsZero() || ctl[0].Initial != nil {
		t.Errorf("control records = %+v, want one delivered reply without an endpoint, intent or initial message", ctl)
	}
	nothingWasMade(t, h)
	if n := createRequests(t, h); n != 0 {
		t.Errorf("%d creation requests reached the adapter, want none", n)
	}
	if n := openEndpoints(t, h.Engine()); n != publication.MaxEndpointsPerNode {
		t.Errorf("%d endpoints after the failed creation, want %d", n, publication.MaxEndpointsPerNode)
	}
}

func TestSessionCreateAtNodeEndpointLimitKeepsItsReplyAfterRoomIsFreedAndRestart(t *testing.T) {
	h := nodeAtEndpointLimit(t)
	rt := runtimeEndpoint(t, h)
	to := ident.Address{Node: harness.NodeID(), Endpoint: rt}
	from := endpointOf(t, h, "a")
	data := forgedControl(t, h, "create-at-limit", to, envelope.KindSessionCreate, "default")
	first, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), from, data)
	if err != nil || first.ControlReply == nil {
		t.Fatalf("first receipt = %+v, %v, want the failed creation reply", first, err)
	}
	if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: endpointOf(t, h, keyName(2))}); err != nil {
		t.Fatal(err)
	}
	h.Restart()
	quiet(t, h)
	rc, err := pullFor(h, "create-at-limit", rt)
	if err != nil || len(rc.NoRecord) != 0 {
		t.Fatalf("pull = %+v, %v, want the persisted failure after room was freed", rc, err)
	}
	if rc.ControlReply == nil || rc.ControlReply.Digest() != first.ControlReply.Digest() {
		t.Fatalf("reply = %+v, want the original failure after room was freed", rc.ControlReply)
	}
	r, err := h.VerifyControlReply(*rc.ControlReply)
	if err != nil {
		t.Fatal(err)
	}
	v := r.Result.AdapterForm()
	wantFailed(t, &v, adapterproto.CreateReasonNodeEndpointLimit)
	nothingWasMade(t, h)
	if n := createRequests(t, h); n != 0 {
		t.Errorf("%d creation requests reached the adapter after room was freed, want none", n)
	}
	if n := openEndpoints(t, h.Engine()); n != publication.MaxEndpointsPerNode-1 {
		t.Errorf("%d endpoints after room was freed, want %d", n, publication.MaxEndpointsPerNode-1)
	}
	// A new signal can use the room; the failed signal itself never executes later.
	msg := create(t, h, "a", "default")
	if r := replyAfterMoving(t, h, "a", msg); r == nil || r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyCreated {
		t.Fatalf("a new creation with room = %+v, want created", r)
	}
	if n := nativeSessions(t, h); n != 1 {
		t.Errorf("%d native sessions, want only the new signal's session", n)
	}
}
