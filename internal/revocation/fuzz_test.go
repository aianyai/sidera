package revocation

import (
	"bytes"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

func seedGoldens(f *testing.F) {
	for _, g := range goldenCases() {
		f.Add(g.bytes)
	}
	f.Add([]byte{})
	f.Add([]byte{0xa0})
}

// Whatever decodes as a record re-encodes to the identical bytes; verifying it
// as a statement never panics; and a statement that verifies can be kept,
// queried and classified.
func FuzzDecodeStatement(f *testing.F) {
	seedGoldens(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		rec, err := record.Decode(data, record.DefaultMaxInput)
		if err != nil {
			return
		}
		again, err := rec.Encode()
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("decoded record does not re-encode identically: %v", err)
		}
		rev, err := Verify(rec)
		if err != nil {
			return
		}
		if rev.Digest() != rec.Digest() || rev.Kind() == KindInvalid {
			t.Fatalf("verified record gives %v %v", rev.Kind(), rev.Digest())
		}
		s := NewSet()
		if err := s.Add(rev); err != nil {
			t.Fatalf("a verified statement cannot be added: %v", err)
		}
		if rev.Kind().NodeLevel() == rev.Node().IsZero() {
			t.Fatalf("kind %v with node %v", rev.Kind(), rev.Node())
		}
		got := Classify(rev, Context{}, 0, Params{MaxForwardOnce: 1})
		if got.Class == ClassInvalid || got.Reason == ReasonInvalid {
			t.Fatalf("classification %v", got)
		}
	})
}

// Whatever bytes are put in the payload of a record of each type, signed by
// some key, verification never panics, and a payload that is accepted is the
// canonical encoding of what it says (a record built from the verified fields
// has the same payload).
func FuzzPayloads(f *testing.F) {
	for _, g := range goldenCases() {
		f.Add(mustHexBytes(f, g.payload))
	}
	f.Add([]byte{})
	f.Add(cborw.mapOf(cborw.uint(1), cborw.uint(0)))
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) == 0 {
			return
		}
		types := []string{NodeSelfRevocationType, PersonSelfRevocationType, NetworkKeyCertificateRevocationType, DeviceKeyRevocationType}
		for _, n := range []int{1, 2, 3} {
			signer := rfcSigner(t, n)
			for _, typ := range types {
				rec, err := record.Sign(typ, payload, signer)
				if err != nil {
					t.Fatal(err)
				}
				rev, err := Verify(rec)
				if err != nil {
					continue
				}
				// Accepted: the statement is exactly what Issue* would write for it.
				var again record.Record
				switch rev.Kind() {
				case KindNodeSelf:
					again, err = IssueNodeSelfRevocation(signer)
				case KindPersonSelf:
					again, err = IssuePersonSelfRevocation(signer)
				default:
					continue
				}
				if err != nil || !bytes.Equal(again.Payload, payload) {
					t.Fatalf("accepted payload %x differs from the canonical one: %v", payload, err)
				}
			}
		}
	})
}

// Structured fuzzing of the encode and decode pair: whatever field values are
// given, the statement is sealed exactly when the rules allow it, and what is
// sealed verifies to the values it was built from.
func FuzzStatementRoundTrip(f *testing.F) {
	f.Add(uint8(0), uint64(1), make([]byte, 32), make([]byte, 32))
	f.Add(uint8(2), uint64(0), bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	f.Add(uint8(3), uint64(7), mustHexBytes(f, "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"), mustHexBytes(f, "3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c"))
	f.Fuzz(func(t *testing.T, kind uint8, serial uint64, digest, key []byte) {
		root, identity := rfcSigner(t, 3), rfcSigner(t, 1)
		switch kind % 4 {
		case 0:
			rec, err := IssueNodeSelfRevocation(identity)
			rev := mustRevocation(t, rec, err)
			if rev.Node() != nodeOf(t, identity) {
				t.Fatal("wrong node")
			}
		case 1:
			rec, err := IssuePersonSelfRevocation(root)
			rev := mustRevocation(t, rec, err)
			if rev.Person() != personOf(t, root) {
				t.Fatal("wrong person")
			}
		case 2:
			var d record.Digest
			copy(d[:], digest)
			payload := NetworkKeyCertificateRevocation{Node: nodeOf(t, identity), Serial: serial, Certificate: d}
			rec, err := record.Seal(NetworkKeyCertificateRevocationDescriptor, payload, identity)
			wantOK := serial >= 1 && !d.IsZero()
			if (err == nil) != wantOK {
				t.Fatalf("serial %d digest %x: err %v, want ok=%v", serial, d, err, wantOK)
			}
			if err != nil {
				return
			}
			v, err := record.Verify(NetworkKeyCertificateRevocationDescriptor, rec)
			if err != nil || v.Payload != payload {
				t.Fatalf("round trip: %+v %v", v.Payload, err)
			}
		case 3:
			k, kerr := ident.NewPublicKey(ident.AlgorithmEd25519, key)
			if kerr != nil {
				return
			}
			rec, err := IssueDeviceKeyRevocation(root, k)
			isRoot := k == root.Public()
			if (err == nil) == isRoot {
				t.Fatalf("device key is the root key: %v, err %v", isRoot, err)
			}
			if err != nil {
				return
			}
			rev := mustRevocation(t, rec, nil)
			if rev.Person() != personOf(t, root) {
				t.Fatal("wrong person")
			}
			s := NewSet()
			if err := s.Add(rev); err != nil {
				t.Fatal(err)
			}
			cert := deviceCertificateFor(t, root, k)
			if !s.DeviceCertificate(cert).CertRevoked {
				t.Fatal("the revoked device key is not revoked")
			}
		}
	})
}

// deviceCertificateFor is deviceCertificate for a bare public key.
func deviceCertificateFor(t testing.TB, root record.Signer, device ident.PublicKey) record.Verified[record.DeviceCertificate] {
	t.Helper()
	c := record.DeviceCertificate{Person: personOf(t, root), DeviceKey: device, MaxLevel: admin.LevelView, NotBefore: epoch, NotAfter: endTime}
	r, err := record.Seal(record.DeviceCertificateDescriptor, c, root)
	if err != nil {
		t.Fatal(err)
	}
	v, err := record.Verify(record.DeviceCertificateDescriptor, r)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The retention decision on arbitrary counts: it never panics and follows the
// arithmetic of the cap.
func FuzzClassifyCounts(f *testing.F) {
	f.Add(0, 0)
	f.Add(3, 3)
	f.Add(-1, 5)
	f.Add(1<<62, -1<<62)
	f.Fuzz(func(t *testing.T, held, capN int) {
		rev := nodeSelfOf(nodeIDOf(t, 77), 1)
		got := Classify(rev, Context{TrustGraph: trust.NodeSet{}}, held, Params{MaxForwardOnce: capN})
		switch {
		case held < 0 || capN < 1:
			if got != (Retention{Dropped, ReasonInvalidInput}) {
				t.Fatalf("held %d cap %d: %v", held, capN, got)
			}
		case held >= capN:
			if got != (Retention{Dropped, ReasonCapReached}) {
				t.Fatalf("held %d cap %d: %v", held, capN, got)
			}
		default:
			if got != (Retention{ForwardOnce, ReasonUnrelatedNode}) {
				t.Fatalf("held %d cap %d: %v", held, capN, got)
			}
		}
	})
}
