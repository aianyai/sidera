package envelope

import (
	"bytes"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// The properties of every decoder: an input that decodes re-encodes to the
// identical bytes, and nothing panics, whatever the input.

func FuzzDecodeEnvelope(f *testing.F) {
	for _, g := range [][]byte{goldenEnvelopeMessage, goldenEnvelopeSessionCreate, goldenEnvelopeReply} {
		f.Add(g)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		e, err := Decode(data, testLimits)
		if err != nil {
			return
		}
		again, err := e.Encode(testLimits)
		if err != nil {
			t.Fatalf("a decoded envelope does not encode: %v", err)
		}
		if !bytes.Equal(again, data) {
			t.Fatalf("re-encoding changed the bytes:\n in %x\nout %x", data, again)
		}
		// what a receiver does next must not panic either
		_ = e.Digest()
		for _, a := range e.To {
			_ = e.ValidateFor(a.Node)
			_ = e.TrimmedFor(a.Node)
		}
		_ = e.ValidateFor(ident.NodeID{})
		_ = CheckEvidence(e.Evidence)
	})
}

func FuzzVerifyStatusReport(f *testing.F) {
	for _, g := range [][]byte{goldenReportDelivered, goldenReportBlocked, goldenReportRejected} {
		f.Add(g)
	}
	node := nodeOf(f, keyIdentityA)
	accepted := acceptedFor(f, keyIdentityA, keyNetworkA)
	d := StatusReportDescriptor(node, accepted, t0.Add(1000))
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := record.Decode(data, record.DefaultMaxInput)
		if err != nil {
			return
		}
		if u, err := record.Peek(d, r); err == nil {
			again, err := record.EncodePayload(d, u.Payload())
			if err != nil {
				t.Fatalf("a decoded payload does not encode: %v", err)
			}
			if !bytes.Equal(again, r.Payload) {
				t.Fatalf("re-encoding the payload changed it:\n in %x\nout %x", r.Payload, again)
			}
		}
		if sr, err := VerifyStatusReport(r, node, accepted, t0.Add(1000)); err == nil {
			_ = sr.Report().ProvesArrival()
			_ = Arrival{}.Learn(sr.Report())
		}
	})
}

func FuzzVerifyControlReply(f *testing.F) {
	for _, g := range [][]byte{goldenReplyCreated, goldenReplyFailed, goldenReplyClose, goldenReplyInterrupt, goldenReplyStatus} {
		f.Add(g)
	}
	node := nodeOf(f, keyIdentityA)
	accepted := acceptedFor(f, keyIdentityA, keyNetworkA)
	d := ControlReplyDescriptor(node, accepted, t0.Add(1000))
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := record.Decode(data, record.DefaultMaxInput)
		if err != nil {
			return
		}
		if u, err := record.Peek(d, r); err == nil {
			again, err := record.EncodePayload(d, u.Payload())
			if err != nil {
				t.Fatalf("a decoded payload does not encode: %v", err)
			}
			if !bytes.Equal(again, r.Payload) {
				t.Fatalf("re-encoding the payload changed it:\n in %x\nout %x", r.Payload, again)
			}
			_, _ = u.Payload().Binds()
		}
		_, _ = VerifyControlReply(r, node, accepted, t0.Add(1000))
	})
}
