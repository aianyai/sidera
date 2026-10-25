package engine

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/timing"
)

func TestOutboundNeedByState(t *testing.T) {
	t0 := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	accept := t0.Add(10 * time.Minute)
	rec := func(state delivery.State) delivery.Record {
		r := delivery.NewSourceRecord(accept)
		r.State = state
		return r
	}
	for _, c := range []struct {
		name string
		d    delivery.Record
		now  time.Time
		sent bool
		want workKind
	}{
		{"a message not sent yet is sent", rec(delivery.StateQueued), t0, false, workSend},
		{"a message sent is waited for with a pull", rec(delivery.StateQueued), t0, true, workPull},
		{"the instant of the deadline is still sent to", rec(delivery.StateQueued), accept, false, workSend},
		{"after the deadline a node that never answered is asked", rec(delivery.StateQueued), accept.Add(time.Second), false, workPull},
		{"an accepted delivery is asked about", rec(delivery.StateAccepted), t0, true, workPull},
		{"an expired one can still be corrected", rec(delivery.StateExpiredUnconfirmed), t0, true, workPull},
		{"one whose result is unknown can still be corrected", rec(delivery.StateDeliveryUnknown), t0, true, workPull},
		{"a delivered one is settled", rec(delivery.StateDelivered), t0, true, 0},
		{"a rejected one is settled", rec(delivery.StateRejected), t0, true, 0},
		{"a closed one is settled", rec(delivery.StateClosedUnconfirmed), t0, true, 0},
		{"the report deadline ends the asking", rec(delivery.StateAccepted), delivery.ReportBy(accept).Add(time.Second), true, 0},
		{"the instant of the report deadline is still asked", rec(delivery.StateAccepted), delivery.ReportBy(accept), true, workPull},
		{"the tolerance is already in the report deadline", rec(delivery.StateAccepted), delivery.ReportBy(accept).Add(-timing.ClockTolerance), true, workPull},
	} {
		if got := outboundNeed(c.d, accept, c.now, c.sent); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

func TestOnlyTheStatesTheRecordDatesAreReported(t *testing.T) {
	at := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	mk := func(state delivery.State, reject delivery.RejectReason) store.InRecord {
		return store.InRecord{AcceptedAt: at, Record: delivery.Record{State: state, Reject: reject}}
	}
	// A record is written once when it is taken in, and again each time it changes;
	// its report is signed before the first write, when it has no revision yet.
	writtenAgain := func(in store.InRecord) store.InRecord { in.Rev = 2; return in }
	atIntake := func(in store.InRecord) store.InRecord { in.Rev = 1; return in }
	aboutToBeWritten := func(in store.InRecord) store.InRecord { in.Rev = 0; return in }
	for _, c := range []struct {
		name string
		in   store.InRecord
		ok   bool
	}{
		{"accepted", mk(delivery.StateAccepted, 0), true},
		{"rejected at intake as declined", mk(delivery.StateRejected, delivery.RejectEndpointDeclined), true},
		{"rejected at intake as backlog_full", mk(delivery.StateRejected, delivery.RejectBacklogFull), true},
		{"rejected at intake because the endpoint was closed", atIntake(mk(delivery.StateRejected, delivery.RejectEndpointClosed)), true},
		{"about to be rejected at intake because the endpoint was closed", aboutToBeWritten(mk(delivery.StateRejected, delivery.RejectEndpointClosed)), true},
		{"rejected later, when the endpoint closed", writtenAgain(mk(delivery.StateRejected, delivery.RejectEndpointClosed)), false},
		{"rejected later, when the backlog deadline passed", mk(delivery.StateRejected, delivery.RejectBacklogExpired), false},
		{"rejected later, by the second check", mk(delivery.StateRejected, delivery.RejectSecondCheckFailed), false},
		{"delivered", mk(delivery.StateDelivered, 0), false},
		{"delivery_unknown", mk(delivery.StateDeliveryUnknown, 0), false},
		{"closed_unconfirmed", mk(delivery.StateClosedUnconfirmed, 0), false},
	} {
		got, ok := reportedAt(c.in)
		if ok != c.ok || (ok && !got.Equal(at)) {
			t.Errorf("%s: %v, %v; want ok=%v at the acceptance time", c.name, got, ok, c.ok)
		}
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
		got, ref := settledIn(c.in)
		switch {
		case c.drop && (got.Body != nil || ref == nil || *ref != body):
			t.Errorf("%s: body %v, released %v; want the reference dropped and returned", c.name, got.Body, ref)
		case !c.drop && (got.Body == nil || ref != nil):
			t.Errorf("%s: body %v, released %v; want the reference kept", c.name, got.Body, ref)
		}
	}
	none, ref := settledIn(inRec(t, 5, b, delivery.StateRejected))
	none.Body = nil
	if _, ref = settledIn(none); ref != nil {
		t.Error("a record without a body released one")
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
