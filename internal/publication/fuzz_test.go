package publication

import (
	"bytes"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/record"
)

// TestEveryBitIsProtected flips every bit of every golden record, one at a
// time, and requires that the result is no longer accepted.
func TestEveryBitIsProtected(t *testing.T) {
	accepted := acceptedA(t)
	checks := []struct {
		name   string
		golden []byte
		verify func(record.Record) error
	}{
		{"declaration", goldenDeclaration, func(r record.Record) error {
			_, err := record.Verify(DeclarationDescriptor(accepted, declNow), r)
			return err
		}},
		{"reachability record", goldenReachability, func(r record.Record) error {
			_, err := record.Verify(ReachabilityDescriptor(accepted, reachNow), r)
			return err
		}},
		{"reachability proof", goldenProof, func(r record.Record) error {
			_, err := record.Verify(ReachabilityProofDescriptor, r)
			return err
		}},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if err := c.verify(mustDecode(t, c.golden)); err != nil {
				t.Fatalf("the vector itself is rejected: %v", err)
			}
			for i := range c.golden {
				for bit := 0; bit < 8; bit++ {
					m := append([]byte(nil), c.golden...)
					m[i] ^= 1 << bit
					r, err := record.Decode(m, record.DefaultMaxInput)
					if err != nil {
						continue
					}
					if err := c.verify(r); err == nil {
						t.Fatalf("byte %d bit %d flipped and the record is still accepted", i, bit)
					}
				}
			}
		})
	}
}

func payloadRoundTrip[W, P any](t *testing.T, payload []byte, from func(W) (P, error), to func(P) (W, error)) {
	t.Helper()
	var w W
	if err := record.UnmarshalStrict(payload, &w, len(payload)); err != nil {
		return
	}
	p, err := from(w)
	if err != nil {
		return
	}
	w2, err := to(p)
	if err != nil {
		t.Fatalf("a payload that decodes does not convert back: %v", err)
	}
	again, err := record.MarshalCanonical(w2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, payload) {
		t.Fatalf("payload does not re-encode to itself:\n in %x\nout %x", payload, again)
	}
}

func recordRoundTrip(t *testing.T, data []byte) (record.Record, bool) {
	t.Helper()
	r, err := record.Decode(data, record.DefaultMaxInput)
	if err != nil {
		return record.Record{}, false
	}
	again, err := r.Encode()
	if err != nil {
		t.Fatalf("a record that decodes does not encode: %v", err)
	}
	if !bytes.Equal(again, data) {
		t.Fatal("record does not re-encode to itself")
	}
	return r, true
}

func seedRecords(f *testing.F, golden ...[]byte) {
	for _, g := range golden {
		f.Add(g)
		f.Add(g[:len(g)/2])
		mutated := append([]byte(nil), g...)
		mutated[len(mutated)-3] ^= 0x40
		f.Add(mutated)
	}
	f.Add([]byte{})
	f.Add([]byte{0xa3, 0x01, 0x60, 0x02, 0x40, 0x03, 0x80})
}

func FuzzDecodeDeclaration(f *testing.F) {
	seedRecords(f, goldenDeclaration)
	accepted := acceptedA(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		r, ok := recordRoundTrip(t, data)
		if !ok {
			return
		}
		v, err := record.Verify(DeclarationDescriptor(accepted, declNow), r)
		if err != nil {
			return
		}
		d := v.Payload()
		if d.MaxQueue <= 0 || d.LeaseRenewal <= 0 || d.Counter < 1 || len(d.Endpoints) > MaxEndpointsPerNode {
			t.Fatalf("verified declaration outside the limits: %+v", d)
		}
	})
}

func FuzzDecodeReachability(f *testing.F) {
	seedRecords(f, goldenReachability, goldenNetworkKeyCertificate)
	accepted := acceptedA(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		r, ok := recordRoundTrip(t, data)
		if !ok {
			return
		}
		cert, certErr := EmbeddedNetworkKeyCertificate(r)
		v, err := record.Verify(ReachabilityDescriptor(accepted, reachNow), r)
		if err != nil {
			return
		}
		if certErr != nil {
			t.Fatalf("a verified record has no readable embedded certificate: %v", certErr)
		}
		p := v.Payload()
		if cert.Payload().Node != p.Node() || len(p.Addresses) > MaxAddresses || len(p.Relays) > MaxRelays {
			t.Fatalf("verified record outside the limits: %+v", p)
		}
		_ = DecideReachability(nil, v, reachNow.Add(100*time.Hour))
	})
}

func FuzzDecodeProof(f *testing.F) {
	seedRecords(f, goldenProof)
	f.Fuzz(func(t *testing.T, data []byte) {
		r, ok := recordRoundTrip(t, data)
		if !ok {
			return
		}
		v, err := record.Verify(ReachabilityProofDescriptor, r)
		if err != nil {
			return
		}
		if v.Payload().Admin(admin.RetirementClear).NotAfter.IsZero() {
			t.Fatal("verified proof without an end")
		}
	})
}

func FuzzPayloads(f *testing.F) {
	for _, g := range [][]byte{goldenDeclaration, goldenReachability, goldenProof} {
		r, err := record.Decode(g, record.DefaultMaxInput)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(r.Payload)
	}
	f.Add([]byte{0xa0})
	f.Fuzz(func(t *testing.T, payload []byte) {
		payloadRoundTrip(t, payload, declarationFromWire, declarationToWire)
		payloadRoundTrip(t, payload, reachabilityFromWire, reachabilityToWire)
		payloadRoundTrip(t, payload, proofFromWire, proofToWire)
	})
}
