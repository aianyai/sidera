package pairing

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// Seeds of the Ed25519 test keys of RFC 8032 section 7.1 (TEST 1, TEST 2,
// TEST 3, TEST SHA(abc)).
const (
	seedTest1   = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	seedTest2   = "4ccd089b28ff96da9db6c346ec114e0f5b8a319f35aba624da8cf6ed4fb8a6fb"
	seedTest3   = "c5aa8df43f9f837bedb7442f31dcb7b166d38535076f094b85ce3a2e0b4458f7"
	seedTestSHA = "833fe62409237b9d62ec77587520911e9a759cec1d19755b7da901b96dca3d42"
)

func mustHex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func v(t testing.TB, key string) []byte {
	t.Helper()
	s, ok := vec[key]
	if !ok {
		t.Fatalf("no vector %q", key)
	}
	return mustHex(t, s)
}

func signerFromSeed(t testing.TB, seedHex string) record.Signer {
	t.Helper()
	s, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(mustHex(t, seedHex)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// testNow is the instant at which the vector certificates are valid and the
// state machines start.
var testNow = time.Unix(1798761600, 0).UTC()

// partyFrom builds a party from an identity seed, a network seed and a serial,
// through a real signed certificate and the real binding check.
func partyFrom(t testing.TB, identSeed, netSeed string, serial uint64) Party {
	t.Helper()
	idSigner, netSigner := signerFromSeed(t, identSeed), signerFromSeed(t, netSeed)
	node, err := ident.NodeIDFromKey(idSigner.Public())
	if err != nil {
		t.Fatal(err)
	}
	cert := record.IssueNetworkKeyCertificate(node, netSigner.Public(), serial, testNow.Add(-time.Hour), testNow.Add(24*time.Hour*365))
	rec, err := record.Seal(record.NetworkKeyCertificateDescriptor, cert, idSigner)
	if err != nil {
		t.Fatal(err)
	}
	ver, err := record.Verify(record.NetworkKeyCertificateDescriptor, rec)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewParty(ver, record.KeyRevocationClear, serial, netSigner.Public(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func nonceOf(first byte) Nonce {
	var n Nonce
	for i := range n {
		n[i] = first + byte(i)
	}
	return n
}

func hexDecode(s string) ([]byte, error) { return hex.DecodeString(s) }
