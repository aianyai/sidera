package credential

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// Seconds on the wire of the instants the golden vectors use.
const unixT0 = 1700000000

var t0 = time.Unix(unixT0, 0).UTC()

// The keys of the golden vectors. 1, 2 and 3 are the RFC 8032 section 7.1 test
// keys; any other n is the seed made of the byte n repeated 32 times. In the
// vectors: key 1 is the identity key of node A, key 2 of node B, key 3 the root
// key of the administrator, seed 0x11 node A's network key and seed 0x33 the
// administrator's device key.
const (
	keyA        = 1
	keyB        = 2
	keyRoot     = 3
	keyNetA     = 0x11
	keyNetB     = 0x22
	keyDevice   = 0x33
	keyOther    = 0x44 // a node that is not part of the credential
	keyNetOther = 0x55
)

var rfcSeeds = map[int]string{
	1: "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60",
	2: "4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb",
	3: "c5aa8df43f9f837bedb7442f31dcb7b166d38535076f094b85ce3a2e0b4458f7",
}

func seedOf(t testing.TB, n int) []byte {
	t.Helper()
	if s, ok := rfcSeeds[n]; ok {
		return mustHex(t, s)
	}
	return bytes.Repeat([]byte{byte(n)}, ed25519.SeedSize)
}

func mustHex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func signerOf(t testing.TB, n int) record.Signer {
	t.Helper()
	s, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(seedOf(t, n)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func nodeOf(t testing.TB, n int) ident.NodeID {
	t.Helper()
	id, err := ident.NodeIDFromKey(signerOf(t, n).Public())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func personOf(t testing.TB, n int) ident.PersonID {
	t.Helper()
	id, err := ident.PersonIDFromKey(signerOf(t, n).Public())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// The identifiers of the golden vectors, computed by the independent reference.
const (
	hexNodeA  = "e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d"
	hexNodeB  = "33ece159526afe25ff9f6661c01b7751f0a143821e5b7dd1c051debf37dd13bc"
	hexPerson = "0300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d"
	// goldenCredentialDigest is the digest of goldenCredential and of
	// goldenCredentialPartial (the digest does not cover signatures).
	hexCredentialDigest = "c5f781fa6341bc4b09964ae0002244448d0d605dfcae24543f4bbdc336a81d67"
)

func TestGoldenIdentifiers(t *testing.T) {
	if got := hex.EncodeToString(nodeOf(t, keyA).Bytes()); got != hexNodeA {
		t.Errorf("node A = %s, want %s", got, hexNodeA)
	}
	if got := hex.EncodeToString(nodeOf(t, keyB).Bytes()); got != hexNodeB {
		t.Errorf("node B = %s, want %s", got, hexNodeB)
	}
	if got := hex.EncodeToString(personOf(t, keyRoot).Bytes()); got != hexPerson {
		t.Errorf("person = %s, want %s", got, hexPerson)
	}
}

// sampleNonce is the nonce 00 01 .. 0f of the first golden credential.
func sampleNonce() Nonce {
	var n Nonce
	for i := range n {
		n[i] = byte(i)
	}
	return n
}

// sampleCredential is the credential of goldenCredential, built the way a node
// builds it: from the two node IDs in whichever order it knows them.
func sampleCredential(t testing.TB) Credential {
	t.Helper()
	return New(nodeOf(t, keyA), nodeOf(t, keyB), t0, t0.Add(24*time.Hour), sampleNonce())
}

func mustDecode(t testing.TB, b []byte) record.Record {
	t.Helper()
	r, err := record.Decode(b, record.DefaultMaxInput)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return r
}

func mustEncode(t testing.TB, r record.Record) []byte {
	t.Helper()
	b, err := r.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b
}

// sealCredential returns the complete credential record of c, signed by both
// ends the way pairing does it: the node with key first, the other co-signs.
func sealCredential(t testing.TB, c Credential, first, second int) record.Record {
	t.Helper()
	r, err := record.Seal(Descriptor, c, signerOf(t, first))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	r, err = record.CoSign(Descriptor, r, signerOf(t, second))
	if err != nil {
		t.Fatalf("cosign: %v", err)
	}
	return r
}

func verifiedCredential(t testing.TB, c Credential) record.Verified[Credential] {
	t.Helper()
	v, err := record.Verify(Descriptor, sealCredential(t, c, keyA, keyB))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return v
}

// independentDigest recomputes a record digest with its own framing, not with
// package record: SHA-256 over uvarint(len(domain)) domain uvarint(len(type)) type
// uvarint(len(payload)) payload.
func independentDigest(typ string, payload []byte) [32]byte {
	var b []byte
	for _, f := range [][]byte{[]byte("sidera/record-digest"), []byte(typ), payload} {
		b = append(b, uvarint(uint64(len(f)))...)
		b = append(b, f...)
	}
	return sha256.Sum256(b)
}

func uvarint(n uint64) []byte {
	var out []byte
	for n >= 0x80 {
		out = append(out, byte(n)|0x80)
		n >>= 7
	}
	return append(out, byte(n))
}

// acceptedNetworkKey is the certificate a verifier holds for node n, whose
// network key is the one of seed netSeed, valid for 30 days from t0.
func acceptedNetworkKey(t testing.TB, node, netSeed int) record.AcceptedNetworkKey {
	t.Helper()
	return record.AcceptedNetworkKey{Cert: &record.NetworkKeyCertificate{
		Node:       nodeOf(t, node),
		NetworkKey: signerOf(t, netSeed).Public(),
		Serial:     1,
		NotBefore:  t0,
		NotAfter:   t0.Add(30 * 24 * time.Hour),
	}}
}

// requireErr fails unless err wraps every one of want.
func requireErr(t *testing.T, err error, want ...error) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %v", want)
	}
	for _, w := range want {
		if !errors.Is(err, w) {
			t.Fatalf("error %q does not wrap %q", err, w)
		}
	}
}

// facts of trust, for readability of the end-to-end tests.
func digestOf(v record.Verified[Credential]) trust.CredentialDigest {
	return trust.CredentialDigest(v.Digest)
}
