package publication

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
)

const unixT0 = 1700000000

var t0 = time.Unix(unixT0, 0).UTC()

// The keys of the golden vectors: 1, 2 and 3 are the RFC 8032 section 7.1 test
// keys, any other n is the seed made of the byte n repeated 32 times. Key 1 is
// the identity key of node A and key 0x11 its network key; key 3 is the root key
// of the administrator.
const (
	keyA        = 1
	keyRoot     = 3
	keyNetA     = 0x11
	keyOther    = 0x44
	keyNetOther = 0x55
)

var rfcSeeds = map[int]string{
	1: "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60",
	2: "4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb",
	3: "c5aa8df43f9f837bedb7442f31dcb7b166d38535076f094b85ce3a2e0b4458f7",
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
	seed := bytes.Repeat([]byte{byte(n)}, ed25519.SeedSize)
	if s, ok := rfcSeeds[n]; ok {
		seed = mustHex(t, s)
	}
	s, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(seed))
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

const (
	hexNodeA  = "e25aa4dcbc79b8ec49c25d53c2fafd4c36ef52b713f26e20f9f76075561af01d"
	hexPerson = "0300c202af68893402261cf312f5ffb2e956064e997abfe28ef485cfd179e39d"
)

func TestGoldenIdentifiers(t *testing.T) {
	if got := hex.EncodeToString(nodeOf(t, keyA).Bytes()); got != hexNodeA {
		t.Errorf("node A = %s, want %s", got, hexNodeA)
	}
	if got := hex.EncodeToString(personOf(t, keyRoot).Bytes()); got != hexPerson {
		t.Errorf("person = %s, want %s", got, hexPerson)
	}
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

// acceptedA is the certificate a verifier holds for node A: the one of the
// golden vectors, network key seed 0x11, valid for 30 days from t0.
func acceptedA(t testing.TB) record.AcceptedNetworkKey {
	t.Helper()
	v, err := record.Verify(record.NetworkKeyCertificateDescriptor, mustDecode(t, goldenNetworkKeyCertificate))
	if err != nil {
		t.Fatal(err)
	}
	return record.AcceptedNetworkKey{Cert: &v.Payload, Revocation: record.KeyRevocationClear}
}

// networkCertRecord issues a certificate record for node (identity key seed
// idSeed) naming the network key of netSeed.
func networkCertRecord(t testing.TB, idSeed, netSeed int, serial uint64, notBefore, notAfter time.Time) record.Record {
	t.Helper()
	c := record.NetworkKeyCertificate{
		Node: nodeOf(t, idSeed), NetworkKey: signerOf(t, netSeed).Public(), Serial: serial, NotBefore: notBefore, NotAfter: notAfter,
	}
	r, err := record.Seal(record.NetworkKeyCertificateDescriptor, c, signerOf(t, idSeed))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

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

// independentDigest recomputes a record digest with its own framing.
func independentDigest(typ string, payload []byte) record.Digest {
	var b []byte
	for _, f := range [][]byte{[]byte("sidera/record-digest"), []byte(typ), payload} {
		n := uint64(len(f))
		for n >= 0x80 {
			b = append(b, byte(n)|0x80)
			n >>= 7
		}
		b = append(b, byte(n))
		b = append(b, f...)
	}
	return sha256.Sum256(b)
}

var declNow = t0.Add(10 * time.Minute)

func epID(b byte) ident.EndpointID {
	id, err := ident.EndpointIDFromBytes(bytes.Repeat([]byte{b}, ident.EndpointIDLen))
	if err != nil {
		panic(err)
	}
	return id
}

func bootNonce(first byte) BootNonce {
	var n BootNonce
	for i := range n {
		n[i] = first + byte(i)
	}
	return n
}
