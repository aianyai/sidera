package preauth

import (
	"bytes"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// FuzzDecodeExchange: any input that decodes re-encodes to the identical bytes,
// carries a known kind and stays within the size bound; nothing panics.
func FuzzDecodeExchange(f *testing.F) {
	f.Add(goldenNodeExchange)
	f.Add(goldenPersonExchange)
	f.Add([]byte{})
	f.Add([]byte{0xa0})
	f.Add(cborw.mapOf(cborw.uint(1), cborw.uint(1), cborw.uint(2), cborw.bstr(goldenNodeExchange[6:])))
	f.Add(append([]byte{0xa2, 0x01, 0x18, 0x01, 0x02}, cborw.bstr(goldenNodeExchange[6:])...))
	f.Fuzz(func(t *testing.T, data []byte) {
		ex, err := DecodeExchange(data)
		if err != nil {
			return
		}
		if !ex.Kind.valid() {
			t.Fatalf("decoded an invalid kind %d", ex.Kind)
		}
		again, err := ex.Encode()
		if err != nil {
			t.Fatalf("a decoded message does not encode: %v", err)
		}
		if !bytes.Equal(again, data) {
			t.Fatalf("re-encoding differs:\n got %x\nwant %x", again, data)
		}
		if len(data) > MaxMessageSize {
			t.Fatalf("decoded %d bytes, above the bound", len(data))
		}
	})
}

// FuzzDecide: whatever decodes, deciding on it never panics, always names a
// state, and reaches a completed state only when the record verifies against
// the descriptor of its kind and names the key the handshake authenticated.
// The facts trust the golden node and list the golden person, so the seeds
// reach the member and authorized-person answers.
func FuzzDecide(f *testing.F) {
	f.Add(goldenNodeExchange, true)
	f.Add(goldenPersonExchange, true)
	f.Add(goldenNodeExchange, false)
	f.Add(goldenPersonExchange, false)
	f.Fuzz(func(t *testing.T, data []byte, rightKey bool) {
		ex, err := DecodeExchange(data)
		if err != nil {
			return
		}
		w := newWorld(t)
		w.view.Credentials = []trust.Credential{{Digest: trust.CredentialDigest{9}, A: w.local, B: nodeOf(t, rfcSigner(t, 1))}}
		l, _ := admin.NewList([]admin.Entry{{Person: personOf(t, rfcSigner(t, 3)), Level: admin.LevelFullControl}})
		w.list = l
		peer := rfcSigner(t, 2).Public()
		if !rightKey {
			peer = seedSigner(t, 0x55).Public()
		}
		d := Decide(w.facts(), peer, ex)
		if !d.State.Valid() {
			t.Fatalf("no state: %+v", d)
		}
		if !d.Completed() {
			return
		}
		switch ex.Kind {
		case KindNode:
			v, err := record.Verify(record.NetworkKeyCertificateDescriptor, ex.Record)
			if err != nil || v.Payload.NetworkKey != peer || d.Node != v.Payload.Node {
				t.Fatalf("completed on a node certificate that does not verify or does not name the peer: %v", err)
			}
			if d.State == StateAuthorizedPerson {
				t.Fatal("a node became an authorized person")
			}
		case KindPerson:
			v, err := record.Verify(record.DeviceCertificateDescriptor, ex.Record)
			if err != nil || v.Payload.DeviceKey != peer || d.Person != v.Payload.Person {
				t.Fatalf("completed on a device certificate that does not verify or does not name the peer: %v", err)
			}
			if d.State == StateMember {
				t.Fatal("a person became a member")
			}
		default:
			t.Fatalf("completed on kind %v", ex.Kind)
		}
	})
}
