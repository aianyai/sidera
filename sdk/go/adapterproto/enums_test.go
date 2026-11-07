package adapterproto_test

import (
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// TestItemAckAcknowledged classifies every outcome. The table is checked against the full list
// of outcomes, so a new outcome cannot be added without deciding whether it acknowledges the
// part.
func TestItemAckAcknowledged(t *testing.T) {
	want := map[adapterproto.ItemAck]bool{
		adapterproto.ItemRecorded:         true,
		adapterproto.ItemCompleted:        true,
		adapterproto.ItemDuplicate:        true,
		adapterproto.ItemRefusedNoHandout: false,
		adapterproto.ItemRefusedLease:     false,
		adapterproto.ItemTooLate:          false,
	}
	for _, name := range adapterproto.EnumTables()["ItemAck"] {
		ack := adapterproto.ItemAck(name)
		acknowledged, classified := want[ack]
		if !classified {
			t.Errorf("outcome %q is not classified by this test", name)
			continue
		}
		if got := ack.Acknowledged(); got != acknowledged {
			t.Errorf("ItemAck(%q).Acknowledged() = %v, want %v", name, got, acknowledged)
		}
	}
	if ack := adapterproto.ItemAck("nope"); ack.Acknowledged() {
		t.Error(`an outcome that is not valid is reported as acknowledged`)
	}
}
