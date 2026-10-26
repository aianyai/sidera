package engine_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

// FuzzInbound hands the receiving side of the node arbitrary bytes as an envelope
// from a local endpoint. Whatever the bytes are, the node does not panic, it
// answers with an error or with a receipt, and every report in a receipt is one
// the node signed and the sender verifies, as is the control reply of a control
// signal: a receipt that cannot be verified would be a record the node wrote without
// being able to say so.
func FuzzInbound(f *testing.F) {
	seedNode := harness.New(f, harness.Options{})
	seedNode.MustDirect("a")
	seedNode.MustDirect("b")
	b := addrOf(f, seedNode, "b")
	valid := forged(f, seedNode, "fuzz-seed", b, "", "a body")
	f.Add(valid)
	f.Add(forged(f, seedNode, "fuzz-reply", b, "some-message", "an answer"))
	f.Add(forgedControl(f, seedNode, "fuzz-status", b, envelope.KindEndpointStatus, ""))
	f.Add(forgedControl(f, seedNode, "fuzz-create", b, envelope.KindSessionCreate, "default"))
	f.Add(valid[:len(valid)/2])
	f.Add([]byte{})
	long := append([]byte(nil), valid...)
	long[len(long)-1] ^= 0xff
	f.Add(long)
	f.Fuzz(func(t *testing.T, data []byte) {
		// Each input gets a node of its own, so that what one leaves in the
		// queues is not what the next is judged by.
		h := harness.New(t, harness.Options{})
		h.MustDirect("a")
		h.MustDirect("b")
		a := addrOf(t, h, "a")
		rc, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), a.Endpoint, data)
		if err != nil {
			return
		}
		for _, rec := range rc.Reports {
			rep, err := h.VerifyReport(rec)
			if err != nil {
				t.Fatalf("a report of the receipt does not verify: %v", err)
			}
			if rep.Sender != harness.NodeID() {
				t.Fatalf("a report names the sender %s", rep.Sender)
			}
		}
		if rc.ControlReply != nil {
			if _, err := h.VerifyControlReply(*rc.ControlReply); err != nil {
				t.Fatalf("the control reply of the receipt does not verify: %v", err)
			}
		}
		if len(rc.Reports) > envelope.MaxRecipients {
			t.Fatalf("%d reports", len(rc.Reports))
		}
		// The same bytes again are the same request: the receipt has the same
		// reports (the replay is answered from the record, signed anew).
		again, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), a.Endpoint, data)
		if err != nil || len(again.Reports) != len(rc.Reports) {
			t.Fatalf("a repeat of an accepted envelope: %d reports, %v; the first had %d", len(again.Reports), err, len(rc.Reports))
		}
	})
}
