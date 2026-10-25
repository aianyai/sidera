package engine

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Every state and every reason of the delivery rules has a name the Agent
// interface knows, the same one.
func TestEveryStateAndRejectReasonHasAWireName(t *testing.T) {
	for s := delivery.StateQueued; s <= delivery.StateClosedUnconfirmed; s++ {
		got, err := wireState(s)
		if err != nil || string(got) != s.String() || !got.Valid() {
			t.Errorf("state %s: %q, %v", s, got, err)
		}
	}
	if _, err := wireState(delivery.StateInvalid); err == nil {
		t.Error("the state that is none has a name on the wire")
	}
	for r := delivery.RejectUnauthorized; r <= delivery.RejectBacklogFull; r++ {
		got, err := wireReject(r)
		if err != nil || string(got) != r.String() || !got.Valid() {
			t.Errorf("reason %s: %q, %v", r, got, err)
		}
	}
	if _, err := wireReject(delivery.RejectNone); err == nil {
		t.Error("no reason has a name on the wire")
	}
}

func TestTheRecipientsOfASourceRecordAreTheOnesOfItsKeyInOrder(t *testing.T) {
	to := []ident.Address{{Node: nodeOf(t, 1), Endpoint: idOf(t, 50)}, {Node: nodeOf(t, 2), Endpoint: idOf(t, 51)}}
	rejected := delivery.NewSourceRecord(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))
	rejected.State, rejected.Reject = delivery.StateRejected, delivery.RejectBacklogFull
	closed := delivery.NewSourceRecord(rejected.AcceptBefore)
	closed.State = delivery.StateClosedUnconfirmed

	normal := outRec(t, 1, delivery.StateQueued)
	normal.Key.Recipients = to
	normal.Outbound.Recipients = nil
	for i, d := range []delivery.Record{rejected, closed} {
		normal.Outbound.Recipients = append(normal.Outbound.Recipients, envelope.Tracked{Address: to[i], Delivery: d})
	}
	lost := store.OutRecord{Key: normal.Key, Lost: &store.LostOut{Deliveries: []delivery.Record{closed, rejected}}}

	for _, c := range []struct {
		name string
		o    store.OutRecord
		want []adapterproto.RecipientStatus
	}{
		{"a record that is whole", normal, []adapterproto.RecipientStatus{
			{To: adapterproto.Address(to[0].String()), State: adapterproto.DeliveryRejected, Reason: adapterproto.RejectBacklogFull},
			{To: adapterproto.Address(to[1].String()), State: adapterproto.DeliveryClosedUnconfirmed},
		}},
		{"a record that replaces one that was lost", lost, []adapterproto.RecipientStatus{
			{To: adapterproto.Address(to[0].String()), State: adapterproto.DeliveryClosedUnconfirmed},
			{To: adapterproto.Address(to[1].String()), State: adapterproto.DeliveryRejected, Reason: adapterproto.RejectBacklogFull},
		}},
	} {
		got, err := statusRecipients(c.o)
		if err != nil || len(got) != len(c.want) {
			t.Fatalf("%s: %+v, %v", c.name, got, err)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: recipient %d is %+v, want %+v", c.name, i, got[i], c.want[i])
			}
		}
	}
}
