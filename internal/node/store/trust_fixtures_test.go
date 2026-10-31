package store_test

import (
	"bytes"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/record"
)

// The signed records of the trust and peer kinds are real ones, made with
// deterministic keys: the store checks their structure by the contract packages'
// own decoding, so a stand-in would prove nothing. Key n is the seed of the byte n
// repeated, and a node is the node of the identity key n.

func trustSigner(t testing.TB, n int) record.Signer {
	t.Helper()
	s, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(n)}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func trustNode(t testing.TB, n int) ident.NodeID {
	t.Helper()
	id, err := ident.NodeIDFromKey(trustSigner(t, n).Public())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// credentialOf is the credential between the nodes of keys a and b, signed by both.
func credentialOf(t testing.TB, a, b int) store.CredentialRecord {
	t.Helper()
	nonce := credential.Nonce{byte(a), byte(b), 1}
	c := credential.New(trustNode(t, a), trustNode(t, b), base, base.Add(30*24*time.Hour), nonce)
	first, second := a, b
	if c.A != trustNode(t, a) {
		first, second = b, a
	}
	rec, err := record.Seal(credential.Descriptor, c, trustSigner(t, first))
	if err != nil {
		t.Fatal(err)
	}
	if rec, err = record.CoSign(credential.Descriptor, rec, trustSigner(t, second)); err != nil {
		t.Fatal(err)
	}
	return store.CredentialRecord{Digest: rec.Digest(), Record: rec}
}

// revocationOf is the revocation of the credential between keys a and b, signed by
// the node of key a.
func revocationOf(t testing.TB, a, b int) store.CredRevocationRecord {
	t.Helper()
	cred := credentialOf(t, a, b)
	u, err := record.Peek(credential.Descriptor, cred.Record)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := credential.IssueRevocation(u.Payload(), trustSigner(t, a))
	if err != nil {
		t.Fatal(err)
	}
	return store.CredRevocationRecord{Revoked: cred.Digest, Record: rec}
}

// peerOf is the peer record of the node of key n: its certificate at serial,
// signed by its identity key and naming network key n+100, and serialSeen, with
// the digest of the certificate itself when serialSeen is its serial and a
// digest of some other certificate seen above it otherwise.
func peerOf(t testing.TB, n int, serial, serialSeen uint64) store.PeerRecord {
	t.Helper()
	node := trustNode(t, n)
	cert := record.IssueNetworkKeyCertificate(node, trustSigner(t, n+100).Public(), serial, base, base.Add(30*24*time.Hour))
	rec, err := record.Seal(record.NetworkKeyCertificateDescriptor, cert, trustSigner(t, n))
	if err != nil {
		t.Fatal(err)
	}
	seen := rec.Digest()
	if serialSeen != serial {
		seen = record.Digest{0xa0 | byte(n), byte(serialSeen)}
	}
	return store.PeerRecord{Node: node, Certificate: rec, SerialSeen: serialSeen, SeenDigest: seen}
}

func localRevokedOf(t testing.TB, n int) store.LocalRevokedRecord {
	t.Helper()
	return store.LocalRevokedRecord{Node: trustNode(t, n), At: base, Operation: "trust.revoke-node"}
}
