package engine

import (
	"bytes"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/store"
)

func handedOut(t testing.TB, in store.InRecord, at time.Time) store.InRecord {
	t.Helper()
	lease, err := delivery.NewLeaseID(bytes.NewReader(bytes.Repeat([]byte{7}, delivery.LeaseIDLen)))
	if err != nil {
		t.Fatal(err)
	}
	rec, _, err := in.Record.Handout(at, delivery.AuthorizationAllow, 1, 1, lease)
	if err != nil {
		t.Fatalf("Handout: %v", err)
	}
	in.Record = rec
	return in
}

// Closing records for a closed endpoint changes each as the delivery rules say,
// drops its reference to its body, and releases the bodies that nothing refers to
// afterwards, no earlier.
func TestCloseBatchClosesRecordsAndReleasesTheBodiesNothingReferToAnyMore(t *testing.T) {
	ep, other := idOf(t, 1), idOf(t, 2)
	a, b, c := inRec(t, 1, ep, delivery.StateAccepted), inRec(t, 2, ep, delivery.StateAccepted), inRec(t, 3, other, delivery.StateAccepted)
	// b and c are two deliveries of one message: they refer to one body.
	b.Body = c.Body
	sc := &store.Scan{In: []store.InRecord{a, b, c}}
	counts := countBodies(sc)
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

	batch, unchanged := closeBatch(counts, now, []store.InRecord{a, b}, nil)
	if len(unchanged) != 0 || len(batch.In) != 2 {
		t.Fatalf("batch %+v, unchanged %v", batch, unchanged)
	}
	for _, in := range batch.In {
		if in.Record.State != delivery.StateRejected || in.Record.Reject != delivery.RejectEndpointClosed || in.Body != nil {
			t.Errorf("record %s: state %v reason %v body %v", in.Key.Message, in.Record.State, in.Record.Reject, in.Body)
		}
	}
	if len(batch.Release) != 1 || batch.Release[0] != *a.Body {
		t.Errorf("released %v, want only the body of a: the body of b is still referred to by c", batch.Release)
	}
	if counts[a.Body.Name()] != 1 || counts[c.Body.Name()] != 2 {
		t.Errorf("the counts were changed: %v", counts)
	}

	// With c closed as well nothing refers to the shared body any more.
	batch, _ = closeBatch(counts, now, []store.InRecord{a, b, c}, nil)
	if len(batch.Release) != 2 {
		t.Errorf("released %v, want both bodies", batch.Release)
	}
}

// A delivery that was handed out is not rejected: the sender must not be told it
// was not delivered when the agent may have read it. The delivery rules say so,
// and the batch only applies them.
func TestCloseBatchLeavesTheRulesAboutAHandedOutDeliveryToTheDeliveryPackage(t *testing.T) {
	ep := idOf(t, 1)
	in := inRec(t, 1, ep, delivery.StateAccepted)
	now := time.Date(2026, 10, 24, 9, 30, 0, 0, time.UTC)
	leased := handedOut(t, in, now)
	batch, unchanged := closeBatch(bodyCounts{}, now, []store.InRecord{leased}, nil)
	if len(unchanged) != 0 || len(batch.In) != 1 || batch.In[0].Record.State != delivery.StateDeliveryUnknown {
		t.Errorf("batch %+v unchanged %v, want delivery_unknown", batch.In, unchanged)
	}
}

func TestCloseBatchSetsAsideWhatAForcedCloseDoesNotChange(t *testing.T) {
	ep := idOf(t, 1)
	done := inRec(t, 1, ep, delivery.StateDelivered)
	batch, unchanged := closeBatch(bodyCounts{}, time.Now(), []store.InRecord{done}, nil)
	if len(batch.In) != 0 || len(unchanged) != 1 || unchanged[0] != done.Key {
		t.Errorf("batch %+v unchanged %v, want the final record set aside", batch, unchanged)
	}
}

func TestCloseBatchClosesControlRecordsAndDropsTheirBody(t *testing.T) {
	ep := idOf(t, 1)
	body := store.BodyRefOf([]byte("initial message"))
	accept := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	k := store.TargetKey{SenderNode: nodeOf(t, 2), Sender: idOf(t, 3), Message: "ctl-0001", Recipient: ep}
	ctl := store.CtlRecord{Key: k, Record: delivery.NewTargetControlRecord(accept, delivery.ReportBy(accept)), Body: &body}
	batch, unchanged := closeBatch(bodyCounts{body.Name(): 1}, accept, nil, []store.CtlRecord{ctl})
	if len(unchanged) != 0 || len(batch.Ctl) != 1 {
		t.Fatalf("batch %+v unchanged %v", batch, unchanged)
	}
	got := batch.Ctl[0]
	// An accepted control signal is never rejected once accepted: whether it took
	// effect is not known, which the delivery rules say with delivery_unknown.
	if got.Record.State != delivery.StateDeliveryUnknown || got.Body != nil {
		t.Errorf("control record: state %v body %v, want delivery_unknown and no body", got.Record.State, got.Body)
	}
	if len(batch.Release) != 1 || batch.Release[0] != body {
		t.Errorf("released %v, want the body of the signal", batch.Release)
	}
}
