package envelope

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

const unixT0 = 1700000000

var t0 = time.Unix(unixT0, 0).UTC()

// Seeds of the golden vectors: 1 is the RFC 8032 section 7.1 test key 1 (the
// identity key of node A), any other n is the seed of the byte n repeated 32
// times. 0x11 is the network key of node A, 0x44 the identity key of node B.
const (
	keyIdentityA = 1
	keyNetworkA  = 0x11
	keyIdentityB = 0x44
	keyNetworkB  = 0x55
	keyThird     = 0x66
)

const rfcSeed1 = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"

func hexcat(parts ...string) []byte {
	var all string
	for _, p := range parts {
		all += p
	}
	b, err := hex.DecodeString(all)
	if err != nil {
		panic(err)
	}
	return b
}

func signerOf(t testing.TB, n int) record.Signer {
	t.Helper()
	seed := bytes.Repeat([]byte{byte(n)}, ed25519.SeedSize)
	if n == 1 {
		seed, _ = hex.DecodeString(rfcSeed1)
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

func ep(b byte) ident.EndpointID {
	id, err := ident.EndpointIDFromBytes(bytes.Repeat([]byte{b}, ident.EndpointIDLen))
	if err != nil {
		panic(err)
	}
	return id
}

func addr(node ident.NodeID, b byte) ident.Address { return ident.Address{Node: node, Endpoint: ep(b)} }

// acceptedFor is the certificate a verifier holds for a node: the network key
// of netSeed, valid for 30 days from a day before t0, not revoked.
func acceptedFor(t testing.TB, idSeed, netSeed int) record.AcceptedNetworkKey {
	t.Helper()
	return record.AcceptedNetworkKey{
		Cert: &record.NetworkKeyCertificate{
			Node: nodeOf(t, idSeed), NetworkKey: signerOf(t, netSeed).Public(), Serial: 1,
			NotBefore: t0.Add(-24 * time.Hour), NotAfter: t0.Add(30 * 24 * time.Hour),
		},
		Revocation: record.KeyRevocationClear,
	}
}

var testLimits = Limits{MaxBody: 1 << 20, MaxRecipients: 64}

func mustDecodeRecord(t testing.TB, b []byte) record.Record {
	t.Helper()
	r, err := record.Decode(b, record.DefaultMaxInput)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return r
}

func mustEncodeRecord(t testing.TB, r record.Record) []byte {
	t.Helper()
	b, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func requireErr(t testing.TB, err error, want ...error) {
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

func requireNoErr(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// localNode is the node the tests' messages are sent from: node B.
func localNode(t testing.TB) ident.NodeID { return nodeOf(t, keyIdentityB) }

// sealReport signs a status report with the network key of netSeed. A report
// without a sender names the local node.
func sealReport(t testing.TB, r StatusReport, netSeed int) record.Record {
	t.Helper()
	if r.Sender.IsZero() {
		r.Sender = localNode(t)
	}
	d := StatusReportDescriptor(nodeOf(t, keyIdentityA), acceptedFor(t, keyIdentityA, keyNetworkA), t0)
	rec, err := record.Seal(d, r, signerOf(t, netSeed))
	if err != nil {
		t.Fatalf("seal report: %v", err)
	}
	return rec
}

// sealReply signs a control reply with the network key of netSeed. A reply
// without a sender names the local node.
func sealReply(t testing.TB, r ControlReply, netSeed int) record.Record {
	t.Helper()
	if r.Sender.IsZero() {
		r.Sender = localNode(t)
	}
	d := ControlReplyDescriptor(nodeOf(t, keyIdentityA), acceptedFor(t, keyIdentityA, keyNetworkA), t0)
	rec, err := record.Seal(d, r, signerOf(t, netSeed))
	if err != nil {
		t.Fatalf("seal reply: %v", err)
	}
	return rec
}

// signRaw signs an arbitrary payload under a type, bypassing the descriptor's
// encoding checks, so a test can put an invalid payload on the wire.
func signRaw(t testing.TB, typ string, wire any, netSeed int) record.Record {
	t.Helper()
	payload, err := record.MarshalCanonical(wire)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := record.Sign(typ, payload, signerOf(t, netSeed))
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

var _ = delivery.MessageID("")
