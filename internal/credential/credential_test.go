package credential

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

func TestGoldenCredential(t *testing.T) {
	r := mustDecode(t, goldenCredential)
	v, err := record.Verify(Descriptor, r)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	c := v.Payload()
	if got := hex.EncodeToString(c.A.Bytes()); got != hexNodeB {
		t.Errorf("end a = %s, want the smaller node ID %s", got, hexNodeB)
	}
	if got := hex.EncodeToString(c.B.Bytes()); got != hexNodeA {
		t.Errorf("end b = %s, want the larger node ID %s", got, hexNodeA)
	}
	if !c.CreatedAt.Equal(t0) || !c.NotAfter.Equal(t0.Add(24*time.Hour)) || c.Nonce != sampleNonce() {
		t.Errorf("payload = %+v", c)
	}
	if got := v.Digest().String(); got != goldenCredentialDigest {
		t.Errorf("digest = %s, want %s", got, goldenCredentialDigest)
	}
	if goldenCredentialDigest != hexCredentialDigest {
		t.Errorf("vector digest constants disagree")
	}
	if got := hex.EncodeToString(record.SignedBytes(r.Type, r.Payload)); got != goldenCredentialSignedBytes {
		t.Errorf("signed bytes differ from the vector")
	}
	if len(v.Signers()) != 2 {
		t.Fatalf("signers = %d", len(v.Signers()))
	}
	if !signedBy(v.Signers(), signerOf(t, keyA)) || !signedBy(v.Signers(), signerOf(t, keyB)) {
		t.Errorf("signers are not exactly the identity keys of both ends")
	}
}

func signedBy(keys []ident.PublicKey, s record.Signer) bool {
	for _, k := range keys {
		if k == s.Public() {
			return true
		}
	}
	return false
}

// TestGoldenCredentialIsWhatTheCodeWrites checks the other direction: the bytes
// the code produces are exactly the vector, signatures included (Ed25519 is
// deterministic). The vector was computed independently.
func TestGoldenCredentialIsWhatTheCodeWrites(t *testing.T) {
	c := sampleCredential(t)
	partial, err := record.Seal(Descriptor, c, signerOf(t, keyA))
	if err != nil {
		t.Fatal(err)
	}
	if got := mustEncode(t, partial); !bytes.Equal(got, goldenCredentialPartial) {
		t.Errorf("partial credential differs from the vector:\n got %x\nwant %x", got, goldenCredentialPartial)
	}
	full, err := record.CoSign(Descriptor, partial, signerOf(t, keyB))
	if err != nil {
		t.Fatal(err)
	}
	if got := mustEncode(t, full); !bytes.Equal(got, goldenCredential) {
		t.Errorf("complete credential differs from the vector:\n got %x\nwant %x", got, goldenCredential)
	}

	noEnd := New(nodeOf(t, keyB), nodeOf(t, keyA), t0, time.Time{}, func() Nonce {
		var n Nonce
		for i := range n {
			n[i] = byte(16 + i)
		}
		return n
	}())
	got := mustEncode(t, sealCredential(t, noEnd, keyB, keyA))
	if !bytes.Equal(got, goldenCredentialNoEnd) {
		t.Errorf("credential without end differs from the vector:\n got %x\nwant %x", got, goldenCredentialNoEnd)
	}
}

func TestGoldenCredentialWithoutEndNeverExpires(t *testing.T) {
	v, err := record.Verify(Descriptor, mustDecode(t, goldenCredentialNoEnd))
	if err != nil {
		t.Fatal(err)
	}
	if !v.Payload().NotAfter.IsZero() {
		t.Errorf("NotAfter = %v, want the zero time", v.Payload().NotAfter)
	}
	f := Facts(v)
	if !f.NotAfter.IsZero() {
		t.Errorf("trust.Credential.NotAfter = %v, want zero (never expires)", f.NotAfter)
	}
	if got := v.Digest().String(); got != goldenCredentialNoEndDigest {
		t.Errorf("digest = %s", got)
	}
}

func TestGoldenPartialCredential(t *testing.T) {
	r := mustDecode(t, goldenCredentialPartial)
	p, err := record.VerifyPartial(Descriptor, r)
	if err != nil {
		t.Fatalf("verify partial: %v", err)
	}
	if p.Complete() {
		t.Error("a credential with one signature is complete")
	}
	if p.Digest().String() != goldenCredentialPartialDigest || goldenCredentialPartialDigest != goldenCredentialDigest {
		t.Errorf("the partial and the complete credential must share one digest")
	}
	if _, err := record.Verify(Descriptor, r); err == nil {
		t.Error("Verify accepted a credential signed by one end only")
	} else {
		requireErr(t, err, record.ErrIncomplete)
	}
}

// TestCredentialAsPairingBuildsIt walks the pairing sequence: both sides compute
// the payload from the same facts and order the two node IDs themselves, the
// initiator signs first, the target checks the payload against its own and
// co-signs.
func TestCredentialAsPairingBuildsIt(t *testing.T) {
	a, b := nodeOf(t, keyA), nodeOf(t, keyB)
	// The initiator knows (self, peer) = (A, B), the target (self, peer) = (B, A).
	initiator := New(a, b, t0, t0.Add(24*time.Hour), sampleNonce())
	target := New(b, a, t0, t0.Add(24*time.Hour), sampleNonce())
	pi, err := initiator.Payload()
	if err != nil {
		t.Fatal(err)
	}
	pt, err := target.Payload()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pi, pt) {
		t.Fatalf("the two sides computed different payloads:\n%x\n%x", pi, pt)
	}

	partial, err := record.Seal(Descriptor, initiator, signerOf(t, keyA))
	if err != nil {
		t.Fatalf("initiator seal: %v", err)
	}
	// Over the wire.
	received := mustDecode(t, mustEncode(t, partial))
	if !bytes.Equal(received.Payload, pt) {
		t.Fatal("the payload the target received is not the one it computed")
	}
	checked, err := record.VerifyPartial(Descriptor, received)
	if err != nil {
		t.Fatalf("target verify partial: %v", err)
	}
	if checked.Complete() {
		t.Error("partial record reported complete")
	}
	// Slots are in the rule's order: end a (B) first, then end b (A). Only A signed.
	if checked.Slots()[0].Signed || !checked.Slots()[1].Signed || checked.Slots()[1].Signer != signerOf(t, keyA).Public() {
		t.Errorf("slots = %+v, want only the slot of node A (end b) filled by key A", checked.Slots())
	}
	// The target must not co-sign a payload that is not its own; that comparison
	// is the caller's, and the bytes decide it.
	other := New(a, b, t0, t0.Add(24*time.Hour), Nonce{9})
	po, _ := other.Payload()
	if bytes.Equal(po, received.Payload) {
		t.Fatal("a different nonce gave the same payload")
	}

	full, err := record.CoSign(Descriptor, received, signerOf(t, keyB))
	if err != nil {
		t.Fatalf("target cosign: %v", err)
	}
	v, err := record.Verify(Descriptor, full)
	if err != nil {
		t.Fatalf("verify complete: %v", err)
	}
	// One digest from the first signature to the last, and it is the one each
	// side can compute before anyone signs.
	want, err := initiator.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if v.Digest() != want || checked.Digest() != want || partial.Digest() != want {
		t.Errorf("digest changed between stages: %s %s %s, want %s", partial.Digest(), checked.Digest(), v.Digest(), want)
	}
	if v.Payload() != initiator {
		t.Errorf("payload = %+v", v.Payload())
	}
}

func TestCoSignRefusals(t *testing.T) {
	c := sampleCredential(t)
	partial, err := record.Seal(Descriptor, c, signerOf(t, keyA))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		signer int
		want   error
	}{
		{"the initiator signing twice", keyA, record.ErrDuplicateSigner},
		{"a third node co-signing", keyOther, record.ErrSignerNotAllowed},
		{"the root key of an administrator co-signing", keyRoot, record.ErrSignerNotAllowed},
		{"the network key of an end co-signing", keyNetB, record.ErrSignerNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := record.CoSign(Descriptor, partial, signerOf(t, tc.signer))
			requireErr(t, err, tc.want)
		})
	}
	full := sealCredential(t, c, keyA, keyB)
	if _, err := record.CoSign(Descriptor, full, signerOf(t, keyOther)); err == nil {
		t.Error("co-signing a complete credential succeeded")
	}
}

func TestSealRefusesWrongSigners(t *testing.T) {
	c := sampleCredential(t)
	for _, tc := range []struct {
		name    string
		signers []int
	}{
		{"a third node", []int{keyOther}},
		{"an end and a third node", []int{keyA, keyOther}},
		{"a network key of an end", []int{keyNetA}},
		{"three signers", []int{keyA, keyB, keyOther}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ss []record.Signer
			for _, n := range tc.signers {
				ss = append(ss, signerOf(t, n))
			}
			_, err := record.Seal(Descriptor, c, ss...)
			requireErr(t, err, record.ErrSignerNotAllowed)
		})
	}
}

// TestNodeOrderIsFixed: the order of the two ends does not depend on the
// argument order, and is ascending by the identifier bytes.
func TestNodeOrderIsFixed(t *testing.T) {
	a, b := nodeOf(t, keyA), nodeOf(t, keyB)
	c1 := New(a, b, t0, time.Time{}, Nonce{1})
	c2 := New(b, a, t0, time.Time{}, Nonce{1})
	if c1 != c2 {
		t.Fatalf("New is not symmetric: %+v vs %+v", c1, c2)
	}
	if bytes.Compare(c1.A.Bytes(), c1.B.Bytes()) >= 0 {
		t.Error("ends are not ascending")
	}
	if hex.EncodeToString(c1.A.Bytes()) != hexNodeB {
		t.Errorf("the smaller node ID (%s) must come first", hexNodeB)
	}
}

// TestCredentialPayloadRules: every rule of a credential payload, as a payload
// built by hand and signed by both ends, so that only the payload is at fault.
func TestCredentialPayloadRules(t *testing.T) {
	a, b := nodeOf(t, keyA), nodeOf(t, keyB)
	lo, hi := b.Bytes(), a.Bytes() // ascending, see TestNodeOrderIsFixed
	nonce := sampleNonce()
	ptr := func(v int64) *int64 { return &v }
	good := credentialWire{A: lo, B: hi, CreatedAt: unixT0, NotAfter: ptr(unixT0 + 86400), Nonce: nonce[:]}
	tests := []struct {
		name   string
		mutate func(w *credentialWire)
		want   error
	}{
		{"valid", func(w *credentialWire) {}, nil},
		{"valid without an end", func(w *credentialWire) { w.NotAfter = nil }, nil},
		{"both ends equal", func(w *credentialWire) { w.B = w.A }, ErrEnds},
		{"ends in descending order", func(w *credentialWire) { w.A, w.B = w.B, w.A }, ErrEnds},
		{"end a unset", func(w *credentialWire) { w.A = make([]byte, 32) }, ident.ErrZero},
		{"end b wrong length", func(w *credentialWire) { w.B = w.B[:31] }, ident.ErrLength},
		{"nonce too short", func(w *credentialWire) { w.Nonce = w.Nonce[:15] }, ErrNonce},
		{"nonce too long", func(w *credentialWire) { w.Nonce = append(append([]byte(nil), w.Nonce...), 0) }, ErrNonce},
		{"nonce all zero", func(w *credentialWire) { w.Nonce = make([]byte, NonceLen) }, ErrNonce},
		{"nonce missing", func(w *credentialWire) { w.Nonce = nil }, ErrNonce},
		{"created_at zero", func(w *credentialWire) { w.CreatedAt = 0 }, record.ErrTime},
		{"created_at negative", func(w *credentialWire) { w.CreatedAt = -1 }, record.ErrTime},
		{"created_at beyond the wire range", func(w *credentialWire) { w.CreatedAt = record.MaxUnixSeconds + 1 }, record.ErrTime},
		{"explicit zero not_after", func(w *credentialWire) { w.NotAfter = ptr(0) }, record.ErrTime},
		{"negative not_after", func(w *credentialWire) { w.NotAfter = ptr(-5) }, record.ErrTime},
		{"not_after equal to created_at", func(w *credentialWire) { w.NotAfter = ptr(unixT0) }, ErrWindow},
		{"not_after before created_at", func(w *credentialWire) { w.NotAfter = ptr(unixT0 - 1) }, ErrWindow},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := good
			w.Nonce = append([]byte(nil), good.Nonce...)
			tc.mutate(&w)
			payload, err := record.MarshalCanonical(w)
			if err != nil {
				t.Fatal(err)
			}
			rec, err := record.Sign(Type, payload, signerOf(t, keyA), signerOf(t, keyB))
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(Descriptor, rec)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("rejected: %v", err)
				}
				return
			}
			requireErr(t, err, record.ErrPayload, tc.want)
		})
	}
}

func TestCredentialPayloadEncodingRules(t *testing.T) {
	nonce := sampleNonce()
	type withExtra struct {
		A         []byte `cbor:"1,keyasint"`
		B         []byte `cbor:"2,keyasint"`
		CreatedAt int64  `cbor:"3,keyasint"`
		Nonce     []byte `cbor:"5,keyasint"`
		Extra     int    `cbor:"6,keyasint"`
	}
	lo, hi := nodeOf(t, keyB).Bytes(), nodeOf(t, keyA).Bytes()
	extra, err := record.MarshalCanonical(withExtra{A: lo, B: hi, CreatedAt: unixT0, Nonce: nonce[:], Extra: 1})
	if err != nil {
		t.Fatal(err)
	}
	missing, err := record.MarshalCanonical(struct {
		A []byte `cbor:"1,keyasint"`
		B []byte `cbor:"2,keyasint"`
	}{lo, hi})
	if err != nil {
		t.Fatal(err)
	}
	// created_at written as an 8-byte integer instead of the shortest form.
	good, err := sampleCredential(t).Payload()
	if err != nil {
		t.Fatal(err)
	}
	long := bytes.Replace(good, []byte{0x03, 0x1a, 0x65, 0x53, 0xf1, 0x00}, []byte{0x03, 0x1b, 0, 0, 0, 0, 0x65, 0x53, 0xf1, 0x00}, 1)
	if bytes.Equal(long, good) {
		t.Fatal("test bug: the integer was not rewritten")
	}
	tests := []struct {
		name    string
		payload []byte
		want    error
	}{
		{"unknown field", extra, record.ErrMalformed},
		{"required fields missing", missing, record.ErrNotCanonical},
		{"non-shortest integer", long, record.ErrNotCanonical},
		{"trailing byte", append(append([]byte(nil), good...), 0), record.ErrMalformed},
		{"not a map", []byte{0x01}, record.ErrMalformed},
		{"empty", nil, record.ErrPayload},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.payload) == 0 {
				if _, err := record.Sign(Type, tc.payload, signerOf(t, keyA)); err == nil {
					t.Fatal("an empty payload was signed")
				}
				return
			}
			rec, err := record.Sign(Type, tc.payload, signerOf(t, keyA), signerOf(t, keyB))
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(Descriptor, rec)
			requireErr(t, err, tc.want)
		})
	}
}

func TestSealRefusesInvalidCredentials(t *testing.T) {
	a, b := nodeOf(t, keyA), nodeOf(t, keyB)
	tests := []struct {
		name string
		c    Credential
		want error
	}{
		{"both ends the same node", New(a, a, t0, time.Time{}, Nonce{1}), ErrEnds},
		{"ends in the wrong order", Credential{A: a, B: b, CreatedAt: t0, Nonce: Nonce{1}}, ErrEnds},
		{"no creation time", New(a, b, time.Time{}, time.Time{}, Nonce{1}), record.ErrTime},
		{"zero nonce", New(a, b, t0, time.Time{}, Nonce{}), ErrNonce},
		{"ends before it starts", New(a, b, t0, t0.Add(-time.Hour), Nonce{1}), ErrWindow},
		{"unset end", New(a, ident.NodeID{}, t0, time.Time{}, Nonce{1}), ident.ErrZero},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.c.Payload()
			requireErr(t, err, tc.want)
			_, err = record.Seal(Descriptor, tc.c, signerOf(t, keyA))
			requireErr(t, err, tc.want)
		})
	}
}

func TestCredentialVerifyRejects(t *testing.T) {
	c := sampleCredential(t)
	payload, err := c.Payload()
	if err != nil {
		t.Fatal(err)
	}
	signed := func(typ string, signers ...int) record.Record {
		var ss []record.Signer
		for _, n := range signers {
			ss = append(ss, signerOf(t, n))
		}
		r, err := record.Sign(typ, payload, ss...)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	tests := []struct {
		name string
		r    record.Record
		want error
	}{
		{"wrong signer: a third node in place of end b", signed(Type, keyA, keyOther), record.ErrSignerNotAllowed},
		{"wrong signer: a third node alone", signed(Type, keyOther), record.ErrSignerNotAllowed},
		{"wrong signer: the network key of an end", signed(Type, keyA, keyNetB), record.ErrSignerNotAllowed},
		{"missing signature: end a only", signed(Type, keyB), record.ErrIncomplete},
		{"missing signature: end b only", signed(Type, keyA), record.ErrIncomplete},
		{"extra signature", signed(Type, keyA, keyB, keyOther), record.ErrSignerNotAllowed},
		{"extra signature of the administrator", signed(Type, keyA, keyB, keyRoot), record.ErrSignerNotAllowed},
		{"wrong type string: next version", signed("sidera/trust-credential/2", keyA, keyB), record.ErrType},
		{"wrong type string: revocation", signed(RevocationType, keyA, keyB), record.ErrType},
		{"wrong type string: empty prefix change", signed("sidera/trust-credentials/1", keyA, keyB), record.ErrType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := record.Verify(Descriptor, tc.r)
			requireErr(t, err, tc.want)
		})
	}
}

// TestEveryBitIsProtected flips every bit of every golden record, one at a
// time, and requires that the result is no longer accepted: tampering any byte
// is caught.
func TestEveryBitIsProtected(t *testing.T) {
	accepted := acceptedNetworkKey(t, keyA, keyNetA)
	now := t0.Add(10 * time.Minute)
	checks := []struct {
		name   string
		golden []byte
		verify func(record.Record) error
	}{
		{"credential", goldenCredential, func(r record.Record) error { _, err := record.Verify(Descriptor, r); return err }},
		{"revocation", goldenRevocation, func(r record.Record) error { _, err := record.Verify(RevocationDescriptor, r); return err }},
		{"voucher", goldenVoucher, func(r record.Record) error {
			_, err := record.Verify(VoucherDescriptor(accepted, now), r)
			return err
		}},
		{"directive", goldenDirective, func(r record.Record) error { _, err := record.Verify(DirectiveDescriptor, r); return err }},
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

func TestFactsForTrust(t *testing.T) {
	v, err := record.Verify(Descriptor, mustDecode(t, goldenCredential))
	if err != nil {
		t.Fatal(err)
	}
	f := Facts(v)
	want := trust.Credential{
		Digest:   trust.CredentialDigest(mustHex(t, goldenCredentialDigest)[0:32:32]),
		A:        nodeOf(t, keyB),
		B:        nodeOf(t, keyA),
		NotAfter: t0.Add(24 * time.Hour),
	}
	if f != want {
		t.Errorf("facts = %+v\nwant   %+v", f, want)
	}
}

func TestNewNonce(t *testing.T) {
	n, err := NewNonce(bytes.NewReader(bytes.Repeat([]byte{7}, NonceLen)))
	if err != nil || n != (Nonce{7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7}) {
		t.Fatalf("NewNonce = %x, %v", n, err)
	}
	if _, err := NewNonce(bytes.NewReader(make([]byte, NonceLen))); err == nil {
		t.Error("an all-zero source gave a nonce")
	}
	if _, err := NewNonce(bytes.NewReader(make([]byte, 3))); err == nil {
		t.Error("a short source gave a nonce")
	}
}
