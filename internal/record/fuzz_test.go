package record

import (
	"bytes"
	"testing"
)

func seedRecords(f *testing.F) {
	for _, g := range [][]byte{goldenNetworkKeyCertificate, goldenDeviceCertificate, goldenPairComplete, goldenPairFirstOnly} {
		f.Add(g)
	}
	f.Add([]byte{})
	f.Add([]byte{0xa0})
}

// Whatever decodes re-encodes to the identical bytes, and verifying anything
// that decodes never panics.
func FuzzDecodeRecord(f *testing.F) {
	seedRecords(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		rec, err := Decode(data, DefaultMaxInput)
		if err != nil {
			return
		}
		again, err := rec.Encode()
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("decoded record does not re-encode identically: %v", err)
		}
		if rec.Digest() != DigestOf(rec.Type, rec.Payload) {
			t.Fatal("digest differs")
		}
		_, _ = Verify(NetworkKeyCertificateDescriptor, rec)
		_, _ = Verify(DeviceCertificateDescriptor, rec)
		_, _ = VerifyPartial(pairDescriptor, rec)
		_, _ = VerifyPartial(noteDescriptor, rec)
		// Peeking never panics, and a record that verifies peeks to the same payload.
		if v, err := VerifyPartial(pairDescriptor, rec); err == nil {
			if u, err := Peek(pairDescriptor, rec); err != nil || u.Payload() != v.Payload() {
				t.Fatalf("a record that verifies does not peek to its payload: %v", err)
			}
		}
		_, _ = Peek(NetworkKeyCertificateDescriptor, rec)
		_, _ = Peek(DeviceCertificateDescriptor, rec)
		_, _ = Peek(noteDescriptor, rec)
		// A decoded record may be co-signed and encoded again.
		for _, n := range []int{1, 2} {
			if more, err := CoSign(pairDescriptor, rec, rfcSigner(t, n)); err == nil {
				if _, err := more.Encode(); err != nil {
					t.Fatalf("co-signed record does not encode: %v", err)
				}
			}
		}
	})
}

// Whatever a payload decoder accepts must be the exact bytes its encoder writes
// for the result, for every record type.
func FuzzPayloads(f *testing.F) {
	f.Add(mustHexBytes(f, goldenNetworkKeyCertificatePayload))
	f.Add(mustHexBytes(f, goldenDeviceCertificatePayload))
	f.Add(mustHexBytes(f, goldenPairCompletePayload))
	f.Add(cborw.mapOf(cborw.uint(1), cborw.tstr("hello"), cborw.uint(2), cborw.uint(1700000000)))
	f.Fuzz(func(t *testing.T, data []byte) {
		if p, err := NetworkKeyCertificateDescriptor.decode(data); err == nil {
			if b, err := NetworkKeyCertificateDescriptor.encode(p); err != nil || !bytes.Equal(b, data) {
				t.Fatalf("network-key certificate payload does not round-trip: %v", err)
			}
		}
		if p, err := DeviceCertificateDescriptor.decode(data); err == nil {
			if b, err := DeviceCertificateDescriptor.encode(p); err != nil || !bytes.Equal(b, data) {
				t.Fatalf("device certificate payload does not round-trip: %v", err)
			}
		}
		if p, err := pairDescriptor.decode(data); err == nil {
			if b, err := pairDescriptor.encode(p); err != nil || !bytes.Equal(b, data) {
				t.Fatalf("pair payload does not round-trip: %v", err)
			}
		}
		if p, err := noteDescriptor.decode(data); err == nil {
			if b, err := noteDescriptor.encode(p); err != nil || !bytes.Equal(b, data) {
				t.Fatalf("note payload does not round-trip: %v", err)
			}
		}
	})
}

func FuzzUnmarshalStrict(f *testing.F) {
	f.Add(cborw.mapOf(cborw.uint(1), cborw.uint(7)))
	f.Add(goldenPairComplete)
	f.Fuzz(func(t *testing.T, data []byte) {
		var w recordWire
		if err := UnmarshalStrict(data, &w, DefaultMaxInput); err != nil {
			return
		}
		again, err := MarshalCanonical(&w)
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("strictly decoded input does not re-encode identically: %v", err)
		}
	})
}

// Converting a wire time and back is the identity for every accepted value.
func FuzzTimeConversion(f *testing.F) {
	for _, v := range []int64{0, 1, 1700000000, MaxUnixSeconds, MaxUnixSeconds + 1, -1} {
		f.Add(v)
	}
	f.Fuzz(func(t *testing.T, sec int64) {
		tm, err := TimeFromWire(sec)
		if err != nil {
			return
		}
		back, err := TimeToWire(tm)
		if err != nil || back != sec {
			t.Fatalf("round trip of %d = %d, %v", sec, back, err)
		}
	})
}
