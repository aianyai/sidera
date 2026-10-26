package engine

import (
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/timing"
)

func TestOutboundNeedByState(t *testing.T) {
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	accept := t0.Add(10 * time.Minute)
	rec := func(state delivery.State) delivery.Record {
		r := delivery.NewSourceRecord(accept, accept)
		r.State = state
		return r
	}
	for _, c := range []struct {
		name string
		d    delivery.Record
		now  time.Time
		sent bool
		// finalPulled says the pull before the close was made already.
		finalPulled bool
		want        workKind
	}{
		{"a message not sent yet is sent", rec(delivery.StateQueued), t0, false, false, workSend},
		{"a message sent is waited for with a pull", rec(delivery.StateQueued), t0, true, false, workPull},
		{"the instant of the deadline is still sent to", rec(delivery.StateQueued), accept, false, false, workSend},
		{"after the deadline a node that never answered is asked", rec(delivery.StateQueued), accept.Add(time.Second), false, false, workPull},
		{"an accepted delivery is asked about", rec(delivery.StateAccepted), t0, true, false, workPull},
		{"an expired one can still be corrected", rec(delivery.StateExpiredUnconfirmed), t0, true, false, workPull},
		{"one whose result is unknown can still be corrected", rec(delivery.StateDeliveryUnknown), t0, true, false, workPull},
		{"a delivered one is settled", rec(delivery.StateDelivered), t0, true, false, 0},
		{"a rejected one is settled", rec(delivery.StateRejected), t0, true, false, 0},
		{"a closed one is settled", rec(delivery.StateClosedUnconfirmed), t0, true, false, 0},
		{"after the report deadline an accepted delivery is asked once more, before it closes", rec(delivery.StateAccepted), delivery.ReportBy(accept).Add(time.Second), true, false, workFinalPull},
		{"after the report deadline a queued delivery is asked once more too", rec(delivery.StateQueued), delivery.ReportBy(accept).Add(time.Second), false, false, workFinalPull},
		{"the pull before the close is made once", rec(delivery.StateAccepted), delivery.ReportBy(accept).Add(time.Second), true, true, 0},
		{"an expired delivery is not asked once more: the sender was told", rec(delivery.StateExpiredUnconfirmed), delivery.ReportBy(accept).Add(time.Second), true, false, 0},
		{"a delivery whose result is unknown is not asked once more: the sender was told", rec(delivery.StateDeliveryUnknown), delivery.ReportBy(accept).Add(time.Second), true, false, 0},
		{"the target's retention ends the asking", rec(delivery.StateAccepted), finalPullUntil(delivery.ReportBy(accept)).Add(time.Nanosecond), true, false, 0},
		{"the instant the target's retention ends is still asked", rec(delivery.StateAccepted), finalPullUntil(delivery.ReportBy(accept)), true, false, workFinalPull},
		{"the instant of the report deadline is still asked", rec(delivery.StateAccepted), delivery.ReportBy(accept), true, false, workPull},
		{"the tolerance is already in the report deadline", rec(delivery.StateAccepted), delivery.ReportBy(accept).Add(-timing.ClockTolerance), true, false, workPull},
	} {
		if got := outboundNeed(c.d, accept, c.now, c.sent, c.finalPulled); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

// A pull names every delivery on its node that is not settled, the derived one
// included, and none that is: the target is asked about what the source still
// waits for. A node that is owed a send is sent to, and the send carries every
// recipient on it, so no pull is made for it.
func TestAPullNamesTheDeliveriesOfTheNodeThatAreNotSettled(t *testing.T) {
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	accept := t0.Add(10 * time.Minute)
	nodeA, nodeB, nodeC := nodeOf(t, 1), nodeOf(t, 2), nodeOf(t, 3)
	at := func(node ident.NodeID, ep byte, state delivery.State) envelope.Tracked {
		r := delivery.NewSourceRecord(accept, accept)
		r.State = state
		return envelope.Tracked{Address: ident.Address{Node: node, Endpoint: idOf(t, ep)}, Delivery: r}
	}
	o := envelope.Outbound{
		AcceptBefore: accept,
		Recipients: []envelope.Tracked{
			at(nodeA, 10, delivery.StateAccepted), at(nodeA, 11, delivery.StateDelivered),
			at(nodeA, 12, delivery.StateDeliveryUnknown), at(nodeB, 20, delivery.StateQueued),
			at(nodeC, 30, delivery.StateQueued), at(nodeC, 31, delivery.StateAccepted),
		},
	}
	derived := at(nodeA, 13, delivery.StateAccepted)
	o.Derived = &derived
	plan := outboundPlan(o, t0, map[ident.NodeID]bool{nodeA: true, nodeB: false, nodeC: false}, nil)
	if got := plan[nodeA]; got.kind != workPull || !slices.Equal(got.recipients, []ident.EndpointID{idOf(t, 10), idOf(t, 12), idOf(t, 13)}) {
		t.Errorf("node A: %+v, want a pull naming the accepted, the unknown and the derived delivery, not the delivered one", got)
	}
	if got := plan[nodeB]; got.kind != workSend || len(got.recipients) != 0 {
		t.Errorf("node B: %+v, want a send and no pull", got)
	}
	if got := plan[nodeC]; got.kind != workSend || len(got.recipients) != 0 {
		t.Errorf("node C: %+v, want a send that names no recipient even though one of its deliveries is accepted", got)
	}
	// After the report deadline only the pull before the close is left, for the
	// deliveries the sender was told nothing of; one already made is not made again.
	after := delivery.ReportBy(accept).Add(time.Second)
	sent := map[ident.NodeID]bool{nodeA: true, nodeB: true, nodeC: true}
	plan = outboundPlan(o, after, sent, nil)
	if got := plan[nodeA]; got.kind != workFinalPull || !slices.Equal(got.recipients, []ident.EndpointID{idOf(t, 10), idOf(t, 13)}) {
		t.Errorf("node A after the report deadline: %+v, want the pull before the close naming the accepted and the derived delivery", got)
	}
	if got := plan[nodeB]; got.kind != workFinalPull || !slices.Equal(got.recipients, []ident.EndpointID{idOf(t, 20)}) {
		t.Errorf("node B after the report deadline: %+v, want the pull before the close naming the queued delivery", got)
	}
	made := map[ident.EndpointID]bool{idOf(t, 10): true, idOf(t, 13): true, idOf(t, 20): true, idOf(t, 30): true, idOf(t, 31): true}
	if plan = outboundPlan(o, after, sent, made); len(plan) != 0 {
		t.Errorf("after the report deadline and the pulls before the close: %+v, want nothing to do", plan)
	}
}

// The indexes of the view that sending and intake read are kept as the records
// they are made of change: a record that is replaced moves, and never counts
// twice.
func TestTheIndexesOfTheViewFollowTheRecordsTheyAreMadeOf(t *testing.T) {
	b := idOf(t, 40)
	v, err := buildView(nodeOf(t, 1), &store.Scan{})
	if err != nil {
		t.Fatal(err)
	}
	queued, delivered := inRec(t, 1, b, delivery.StateAccepted), inRec(t, 1, b, delivery.StateDelivered)
	other := inRec(t, 2, b, delivery.StateAccepted)
	v.setIn(queued)
	v.setIn(other)
	if got := v.backlog[b]; got != 2 {
		t.Fatalf("backlog %d, want 2", got)
	}
	v.setIn(queued) // written again, as a replay of a write would
	v.setIn(delivered)
	if got := v.backlog[b]; got != 1 {
		t.Errorf("backlog %d after one record was delivered, want 1", got)
	}
	k := msgKey{b, queued.Key.Message}
	if got := v.received[k]; len(got) != 1 || got[0] != queued.Key {
		t.Errorf("received %v: a record written again is still one record", got)
	}
	if got := v.arrived[arrivalKey{queued.Key.SenderNode, queued.Key.Message}]; len(got) != 1 {
		t.Errorf("arrived %v", got)
	}

	o := outRec(t, 1, delivery.StateQueued)
	o.Idempotency = &store.Idempotency{Key: "k"}
	v.setOut(o)
	name := o.Name()
	if v.idem[idemKey{o.Key.Sender, "k"}] != name || v.byMessage[msgKey{o.Key.Sender, o.Key.Message}] != name {
		t.Errorf("idem %v, byMessage %v", v.idem, v.byMessage)
	}
	o.Idempotency = nil
	v.setOut(o)
	if len(v.idem) != 0 {
		t.Errorf("idem %v: the key is gone with the record's claim to it", v.idem)
	}
}

func TestATargetRecordLetsGoOfItsBodyExactlyWhenItNoLongerNeedsIt(t *testing.T) {
	b := idOf(t, 41)
	for _, c := range []struct {
		name string
		in   store.InRecord
		drop bool
	}{
		{"accepted", inRec(t, 1, b, delivery.StateAccepted), false},
		{"delivered", inRec(t, 2, b, delivery.StateDelivered), true},
		{"rejected", inRec(t, 3, b, delivery.StateRejected), true},
		{"delivery_unknown", inRec(t, 4, b, delivery.StateDeliveryUnknown), true},
	} {
		body := store.BodyRefOf([]byte("kept?"))
		c.in.Body = &body
		got := settledIn(c.in)
		switch {
		case c.drop && got.Body != nil:
			t.Errorf("%s: body %v; want the reference dropped", c.name, got.Body)
		case !c.drop && (got.Body == nil || *got.Body != body):
			t.Errorf("%s: body %v; want the reference kept", c.name, got.Body)
		}
		if c.in.Body == nil || *c.in.Body != body {
			t.Errorf("%s: settledIn changed the record it was given", c.name)
		}
	}
	none := inRec(t, 5, b, delivery.StateRejected)
	none.Body = nil
	if got := settledIn(none); got.Body != nil {
		t.Error("a record without a body was given one")
	}
}

func TestTheRefusalOfAMessageForAnEndpointItCannotBeGiven(t *testing.T) {
	ep := func(kind adapterreg.EndpointKind, status store.EndpointStatus) *endpointState {
		return &endpointState{rec: store.EndpointRecord{Kind: kind, Status: status}}
	}
	for _, c := range []struct {
		name string
		st   *endpointState
		want delivery.RejectReason
	}{
		{"an endpoint that is not there", nil, delivery.RejectEndpointMissing},
		{"a closed session endpoint", ep(adapterreg.KindSession, store.EndpointClosed), delivery.RejectEndpointClosed},
		{"a closed direct endpoint", ep(adapterreg.KindDirect, store.EndpointClosed), delivery.RejectEndpointClosed},
		{"a session endpoint whose creation has not ended", ep(adapterreg.KindSession, store.EndpointCreating), delivery.RejectEndpointMissing},
		{"a runtime endpoint takes control signals only", ep(adapterreg.KindRuntime, store.EndpointOpen), delivery.RejectEndpointMissing},
		{"a closed runtime endpoint is none either", ep(adapterreg.KindRuntime, store.EndpointClosed), delivery.RejectEndpointMissing},
		{"an open session endpoint takes messages", ep(adapterreg.KindSession, store.EndpointOpen), delivery.RejectNone},
		{"an open direct endpoint takes messages", ep(adapterreg.KindDirect, store.EndpointOpen), delivery.RejectNone},
	} {
		if got := messageRefusal(c.st); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}
