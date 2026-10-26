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

// Closing records for a closed endpoint changes each as the delivery rules say
// and drops its reference to its body (the store removes the file with the record
// write), and only for the records it closes: a record it does not close keeps
// the reference.
func TestCloseBatchClosesRecordsAndDropsTheBodyReferenceOfEachRecordItClosesAndOfNoOther(t *testing.T) {
	ep, other := idOf(t, 1), idOf(t, 2)
	a, b, c := inRec(t, 1, ep, delivery.StateAccepted), inRec(t, 2, ep, delivery.StateAccepted), inRec(t, 3, other, delivery.StateAccepted)
	if a.Body == nil || b.Body == nil || c.Body == nil {
		t.Fatal("the fixture records hold no body")
	}
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

	batch, unchanged := closeBatch(now, []store.InRecord{a, b}, nil)
	if len(unchanged) != 0 || len(batch.In) != 2 {
		t.Fatalf("batch %+v, unchanged %v", batch, unchanged)
	}
	for _, in := range batch.In {
		if in.Record.State != delivery.StateRejected || in.Record.Reject != delivery.RejectEndpointClosed || in.Body != nil {
			t.Errorf("record %s: state %v reason %v body %v", in.Key.Message, in.Record.State, in.Record.Reject, in.Body)
		}
	}
	if len(batch.In) == 2 && (batch.In[0].Key != a.Key || batch.In[1].Key != b.Key) {
		t.Errorf("the batch holds %v and %v, want a and b", batch.In[0].Key, batch.In[1].Key)
	}
	// The inputs are values: the records the caller holds still refer to their bodies.
	if a.Body == nil || b.Body == nil || c.Body == nil {
		t.Errorf("closeBatch changed its inputs")
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
	batch, unchanged := closeBatch(now, []store.InRecord{leased}, nil)
	if len(unchanged) != 0 || len(batch.In) != 1 || batch.In[0].Record.State != delivery.StateDeliveryUnknown {
		t.Errorf("batch %+v unchanged %v, want delivery_unknown", batch.In, unchanged)
	}
}

func TestCloseBatchSetsAsideWhatAForcedCloseDoesNotChange(t *testing.T) {
	ep := idOf(t, 1)
	done := inRec(t, 1, ep, delivery.StateDelivered)
	batch, unchanged := closeBatch(time.Now(), []store.InRecord{done}, nil)
	if len(batch.In) != 0 || len(unchanged) != 1 || unchanged[0] != done.Key {
		t.Errorf("batch %+v unchanged %v, want the final record set aside", batch, unchanged)
	}
}

func TestCloseBatchClosesControlRecordsAsUnknown(t *testing.T) {
	ep := idOf(t, 1)
	accept := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	k := store.TargetKey{SenderNode: nodeOf(t, 2), Sender: idOf(t, 3), Message: "ctl-0001", Recipient: ep}
	ctl := store.CtlRecord{Key: k, Record: delivery.NewTargetControlRecord(accept, accept, delivery.ReportBy(accept))}
	batch, unchanged := closeBatch(accept, nil, []store.CtlRecord{ctl})
	if len(unchanged) != 0 || len(batch.Ctl) != 1 {
		t.Fatalf("batch %+v unchanged %v", batch, unchanged)
	}
	got := batch.Ctl[0]
	// An accepted control signal is never rejected once accepted: whether it took
	// effect is not known, which the delivery rules say with delivery_unknown.
	if got.Record.State != delivery.StateDeliveryUnknown {
		t.Errorf("control record: state %v, want delivery_unknown", got.Record.State)
	}
}
