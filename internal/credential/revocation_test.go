package credential

import (
	"bytes"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

func TestGoldenRevocation(t *testing.T) {
	r := mustDecode(t, goldenRevocation)
	v, err := record.Verify(RevocationDescriptor, r)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if v.Payload.Credential != sampleCredential(t) {
		t.Errorf("embedded credential = %+v", v.Payload.Credential)
	}
	// The digest is recomputed from the embedded payload and equals the
	// credential record's digest, which the independent reference computed.
	if got := trust.CredentialDigest(mustHex(t, goldenCredentialDigest)[0:32:32]); v.Payload.Digest() != got {
		t.Errorf("revoked digest = %s, want %s", v.Payload.Digest(), got)
	}
	if v.Signers[0] != signerOf(t, keyB).Public() {
		t.Error("signed by an unexpected key")
	}
	if got, want := r.Signatures[0].Key, signerOf(t, keyB).Public(); got != want {
		t.Errorf("signer = %v", got)
	}
}

func TestGoldenRevocationIsWhatTheCodeWrites(t *testing.T) {
	rev, err := NewRevocation(sampleCredential(t))
	if err != nil {
		t.Fatal(err)
	}
	r, err := record.Seal(RevocationDescriptor, rev, signerOf(t, keyB))
	if err != nil {
		t.Fatal(err)
	}
	if got := mustEncode(t, r); !bytes.Equal(got, goldenRevocation) {
		t.Errorf("revocation differs from the vector:\n got %x\nwant %x", got, goldenRevocation)
	}
}

// Either end may revoke; the digest is the same whoever signs.
func TestEitherEndMayRevoke(t *testing.T) {
	rev, err := NewRevocation(sampleCredential(t))
	if err != nil {
		t.Fatal(err)
	}
	var digests []trust.CredentialDigest
	for _, signer := range []int{keyA, keyB} {
		r, err := record.Seal(RevocationDescriptor, rev, signerOf(t, signer))
		if err != nil {
			t.Fatalf("seal by %d: %v", signer, err)
		}
		v, err := record.Verify(RevocationDescriptor, r)
		if err != nil {
			t.Fatalf("verify by %d: %v", signer, err)
		}
		digests = append(digests, v.Payload.Digest())
	}
	if digests[0] != digests[1] {
		t.Error("the revoked digest depends on who signed")
	}
}

func TestRevocationSigners(t *testing.T) {
	rev, err := NewRevocation(sampleCredential(t))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := rev.Credential.Payload()
	if err != nil {
		t.Fatal(err)
	}
	wirePayload, err := record.MarshalCanonical(revocationWire{Credential: payload})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		signers []int
		want    error
	}{
		{"revocation signed by a third party", []int{keyOther}, record.ErrSignerNotAllowed},
		{"signed by the network key of an end", []int{keyNetA}, record.ErrSignerNotAllowed},
		{"signed by the administrator's root key", []int{keyRoot}, record.ErrSignerNotAllowed},
		{"signed by both ends: a second signature has no slot", []int{keyA, keyB}, record.ErrSignerNotAllowed},
		{"signed by an end and a third party", []int{keyA, keyOther}, record.ErrSignerNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ss []record.Signer
			for _, n := range tc.signers {
				ss = append(ss, signerOf(t, n))
			}
			// A signature made by hand, as an attacker would, bypassing Seal.
			forged, err := record.Sign(RevocationType, wirePayload, ss...)
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(RevocationDescriptor, forged)
			requireErr(t, err, tc.want)
			// Seal refuses the same signers at the signer's own side.
			_, err = record.Seal(RevocationDescriptor, rev, ss...)
			requireErr(t, err, tc.want)
		})
	}
	t.Run("wrong type string", func(t *testing.T) {
		r, err := record.Sign(Type, wirePayload, signerOf(t, keyA))
		if err != nil {
			t.Fatal(err)
		}
		_, err = record.Verify(RevocationDescriptor, r)
		requireErr(t, err, record.ErrType)
	})
	t.Run("a credential record is not a revocation", func(t *testing.T) {
		_, err := record.Verify(RevocationDescriptor, mustDecode(t, goldenCredential))
		requireErr(t, err, record.ErrType)
	})
	t.Run("a revocation is not a credential", func(t *testing.T) {
		_, err := record.Verify(Descriptor, mustDecode(t, goldenRevocation))
		requireErr(t, err, record.ErrType)
	})
}

// A revocation names a credential by embedding it, so the signer is checked
// against the ends the embedded payload shows: the same signer is allowed or
// not depending on whose credential is embedded.
func TestRevocationSignerFollowsTheEmbeddedEnds(t *testing.T) {
	other := New(nodeOf(t, keyOther), nodeOf(t, keyA), t0, time.Time{}, Nonce{3})
	rev, err := NewRevocation(other)
	if err != nil {
		t.Fatal(err)
	}
	// Node B is a party to goldenCredential but not to this one.
	if _, err := record.Seal(RevocationDescriptor, rev, signerOf(t, keyB)); err == nil {
		t.Error("node B revoked a credential it is not a party to")
	}
	if _, err := record.Seal(RevocationDescriptor, rev, signerOf(t, keyOther)); err != nil {
		t.Errorf("an end could not revoke: %v", err)
	}
}

// The digest comes from the embedded payload, not from anything the sender
// states: a payload that differs in one byte is another credential.
func TestRevokedDigestIsRecomputedFromThePayload(t *testing.T) {
	c := sampleCredential(t)
	payload, err := c.Payload()
	if err != nil {
		t.Fatal(err)
	}
	want := independentDigest(Type, payload)
	rev, err := NewRevocation(c)
	if err != nil {
		t.Fatal(err)
	}
	if [32]byte(rev.Digest()) != want {
		t.Errorf("NewRevocation digest = %s, independent = %x", rev.Digest(), want)
	}
	v, err := record.Verify(RevocationDescriptor, mustDecode(t, goldenRevocation))
	if err != nil {
		t.Fatal(err)
	}
	if [32]byte(v.Payload.Digest()) != want {
		t.Errorf("decoded digest = %s, independent = %x", v.Payload.Digest(), want)
	}

	c2 := c
	c2.Nonce[0] ^= 1
	rev2, err := NewRevocation(c2)
	if err != nil {
		t.Fatal(err)
	}
	if rev2.Digest() == rev.Digest() {
		t.Error("two credentials that differ in the nonce share a digest")
	}
}

func TestRevocationEmbeddedPayloadRules(t *testing.T) {
	good, err := sampleCredential(t).Payload()
	if err != nil {
		t.Fatal(err)
	}
	a, b := nodeOf(t, keyA), nodeOf(t, keyB)
	equalEnds, err := record.MarshalCanonical(credentialWire{A: a.Bytes(), B: a.Bytes(), CreatedAt: unixT0, Nonce: bytes.Repeat([]byte{1}, NonceLen)})
	if err != nil {
		t.Fatal(err)
	}
	zeroNonce, err := record.MarshalCanonical(credentialWire{A: b.Bytes(), B: a.Bytes(), CreatedAt: unixT0, Nonce: make([]byte, NonceLen)})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		embedded []byte
	}{
		{"empty", nil},
		{"garbage", []byte{0xde, 0xad}},
		{"trailing byte", append(append([]byte(nil), good...), 0)},
		{"both ends equal", equalEnds},
		{"zero nonce", zeroNonce},
		{"truncated", good[:len(good)-1]},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := record.MarshalCanonical(revocationWire{Credential: tc.embedded})
			if err != nil {
				t.Fatal(err)
			}
			r, err := record.Sign(RevocationType, payload, signerOf(t, keyA))
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(RevocationDescriptor, r)
			requireErr(t, err, record.ErrPayload, ErrEmbedded)
		})
	}
}

// An expired credential is still a credential: it can be revoked, and nothing
// here judges its end.
func TestExpiredCredentialCanBeRevoked(t *testing.T) {
	expired := New(nodeOf(t, keyA), nodeOf(t, keyB), t0, t0.Add(time.Hour), Nonce{4})
	rev, err := NewRevocation(expired)
	if err != nil {
		t.Fatal(err)
	}
	r, err := record.Seal(RevocationDescriptor, rev, signerOf(t, keyA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := record.Verify(RevocationDescriptor, r); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// IssueRevocation writes the golden revocation when the second end signs, and
// refuses a signer that is neither end as the verifier would.
func TestIssueRevocation(t *testing.T) {
	r, err := IssueRevocation(sampleCredential(t), signerOf(t, keyB))
	if err != nil {
		t.Fatal(err)
	}
	if got := mustEncode(t, r); !bytes.Equal(got, goldenRevocation) {
		t.Errorf("revocation differs from the vector:\n got %x\nwant %x", got, goldenRevocation)
	}
	if _, err := IssueRevocation(sampleCredential(t), signerOf(t, keyA)); err != nil {
		t.Errorf("the first end may revoke too: %v", err)
	}
	_, err = IssueRevocation(sampleCredential(t), signerOf(t, keyOther))
	requireErr(t, err, record.ErrSignerNotAllowed)
	_, err = IssueRevocation(Credential{}, signerOf(t, keyA))
	requireErr(t, err, record.ErrPayload)
}
