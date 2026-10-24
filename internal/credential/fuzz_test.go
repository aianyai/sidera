package credential

import (
	"bytes"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/record"
)

// payloadRoundTrip decodes payload as the wire struct W and converts it to P as
// a receiver does. Whatever decodes must encode back to the identical bytes: a
// payload has exactly one encoding, which is what signatures and digests rely on.
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

// recordRoundTrip decodes data as a record. Whatever decodes must encode back to
// the identical bytes.
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
		t.Fatalf("record does not re-encode to itself")
	}
	return r, true
}

func seedRecords(f *testing.F, golden ...[]byte) {
	for _, g := range golden {
		f.Add(g)
		f.Add(g[:len(g)/2])
		if len(g) > 8 {
			mutated := append([]byte(nil), g...)
			mutated[len(mutated)-3] ^= 0x40
			f.Add(mutated)
		}
	}
	f.Add([]byte{})
	f.Add([]byte{0xa3, 0x01, 0x60, 0x02, 0x40, 0x03, 0x80})
}

func FuzzDecodeCredential(f *testing.F) {
	seedRecords(f, goldenCredential, goldenCredentialPartial, goldenCredentialNoEnd)
	f.Fuzz(func(t *testing.T, data []byte) {
		r, ok := recordRoundTrip(t, data)
		if !ok {
			return
		}
		if _, err := record.VerifyPartial(Descriptor, r); err != nil {
			return
		}
		c, err := decodePayload(r.Payload)
		if err != nil {
			t.Fatalf("a verified payload does not decode: %v", err)
		}
		if p, err := c.Payload(); err != nil || !bytes.Equal(p, r.Payload) {
			t.Fatalf("payload does not re-encode to itself: %v", err)
		}
		if v, err := record.Verify(Descriptor, r); err == nil {
			if got := Facts(v); got.Digest == ([32]byte{}) || got.A == got.B {
				t.Fatalf("unusable facts from a verified credential: %+v", got)
			}
		}
	})
}

func FuzzDecodeRevocation(f *testing.F) {
	seedRecords(f, goldenRevocation)
	f.Fuzz(func(t *testing.T, data []byte) {
		r, ok := recordRoundTrip(t, data)
		if !ok {
			return
		}
		v, err := record.Verify(RevocationDescriptor, r)
		if err != nil {
			return
		}
		// The digest is the one of the credential the payload embeds.
		if want := independentDigest(Type, mustEmbedded(t, r.Payload)); [32]byte(v.Payload().Digest()) != want {
			t.Fatalf("digest %s is not the digest of the embedded payload", v.Payload().Digest())
		}
		if _, err := NewRevocation(v.Payload().Credential); err != nil {
			t.Fatalf("a verified revocation does not rebuild: %v", err)
		}
	})
}

// mustEmbedded returns the embedded credential payload of a revocation payload.
func mustEmbedded(t *testing.T, payload []byte) []byte {
	t.Helper()
	var w revocationWire
	if err := record.UnmarshalStrict(payload, &w, len(payload)); err != nil {
		t.Fatal(err)
	}
	return w.Credential
}

func FuzzDecodeVoucher(f *testing.F) {
	seedRecords(f, goldenVoucher)
	accepted := acceptedNetworkKey(f, keyA, keyNetA)
	f.Fuzz(func(t *testing.T, data []byte) {
		r, ok := recordRoundTrip(t, data)
		if !ok {
			return
		}
		v, err := record.Verify(VoucherDescriptor(accepted, t0.Add(10*time.Minute)), r)
		if err != nil {
			return
		}
		if v.Payload().Counter < 1 || v.Payload().IssuedAt.IsZero() || v.Payload().Issuer.IsZero() {
			t.Fatalf("verified voucher with unset fields: %+v", v.Payload())
		}
	})
}

func FuzzDecodeDirective(f *testing.F) {
	seedRecords(f, goldenDirective, goldenDeviceCertificate)
	f.Fuzz(func(t *testing.T, data []byte) {
		r, ok := recordRoundTrip(t, data)
		if !ok {
			return
		}
		v, err := record.Verify(DirectiveDescriptor, r)
		if err != nil {
			return
		}
		d := v.Payload()
		if d.AdminDirective(clearFacts).SignedAt.IsZero() || d.TrustDirective().A == d.TrustDirective().B {
			t.Fatalf("unusable facts from a verified directive: %+v", d)
		}
	})
}

func FuzzPayloads(f *testing.F) {
	for _, g := range [][]byte{goldenCredential, goldenCredentialNoEnd, goldenRevocation, goldenVoucher, goldenDirective} {
		f.Add(mustDecodeF(f, g).Payload)
	}
	f.Add([]byte{0xa0})
	f.Fuzz(func(t *testing.T, payload []byte) {
		payloadRoundTrip(t, payload, credentialFromWire, credentialToWire)
		payloadRoundTrip(t, payload, revocationFromWire, revocationToWire)
		payloadRoundTrip(t, payload, voucherFromWire, voucherToWire)
		payloadRoundTrip(t, payload, directiveFromWire, directiveToWire)
	})
}

func mustDecodeF(f *testing.F, b []byte) record.Record {
	f.Helper()
	r, err := record.Decode(b, record.DefaultMaxInput)
	if err != nil {
		f.Fatal(err)
	}
	return r
}
