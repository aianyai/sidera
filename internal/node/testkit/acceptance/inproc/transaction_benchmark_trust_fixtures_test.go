package inproc

import (
	"bytes"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/pairing"
	"github.com/aianyai/sidera/internal/preauth"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
)

// The trust, peer and pairing records are real signed or decoded forms, made
// through the contract packages' own public constructors with deterministic
// keys: the store and the engine verify what they keep, so a stand-in would
// not reach the write the sample is about. The store fixtures and the engine
// fixtures share these builders.

const transactionBenchmarkPairingProtocol = string(preauth.ProtocolPairing)

func transactionBenchmarkSigner(b *testing.B, seed byte) record.Signer {
	b.Helper()
	s, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
	if err != nil {
		b.Fatal(err)
	}
	return s
}

// transactionBenchmarkPeer is a node other than the one under test: the node of
// the identity key of seed n, and the network key of seed n+100.
type transactionBenchmarkPeer struct {
	n        byte
	identity record.Signer
	network  record.Signer
	node     ident.NodeID
}

func newTransactionBenchmarkPeer(b *testing.B, n byte) transactionBenchmarkPeer {
	b.Helper()
	identity := transactionBenchmarkSigner(b, n)
	node, err := ident.NodeIDFromKey(identity.Public())
	if err != nil {
		b.Fatal(err)
	}
	return transactionBenchmarkPeer{n: n, identity: identity, network: transactionBenchmarkSigner(b, n+100), node: node}
}

// certificate is the peer's network-key certificate at serial 1, valid for 30
// days from the start of the benchmark clock, as the record and its verified form.
func (p transactionBenchmarkPeer) certificate(b *testing.B) (record.Record, record.Verified[record.NetworkKeyCertificate]) {
	b.Helper()
	at := harness.DefaultStart
	cert := record.IssueNetworkKeyCertificate(p.node, p.network.Public(), 1, at, at.Add(30*24*time.Hour))
	rec, err := record.Seal(record.NetworkKeyCertificateDescriptor, cert, p.identity)
	if err != nil {
		b.Fatal(err)
	}
	verified, err := record.Verify(record.NetworkKeyCertificateDescriptor, rec)
	if err != nil {
		b.Fatal(err)
	}
	return rec, verified
}

func (p transactionBenchmarkPeer) peerRecord(b *testing.B) store.PeerRecord {
	b.Helper()
	rec, verified := p.certificate(b)
	seen := record.SerialSeenOf(verified)
	return store.PeerRecord{Node: p.node, Certificate: rec, SerialSeen: seen.Serial, SeenDigest: rec.Digest()}
}

// transactionBenchmarkCredential is the credential between the nodes of two
// identity keys, signed by both ends.
func transactionBenchmarkCredential(b *testing.B, x, y record.Signer) store.CredentialRecord {
	b.Helper()
	xNode, err := ident.NodeIDFromKey(x.Public())
	if err != nil {
		b.Fatal(err)
	}
	yNode, err := ident.NodeIDFromKey(y.Public())
	if err != nil {
		b.Fatal(err)
	}
	at := harness.DefaultStart
	c := credential.New(xNode, yNode, at, at.Add(30*24*time.Hour), credential.Nonce{1})
	first, second := x, y
	if c.A != xNode {
		first, second = y, x
	}
	rec, err := record.Seal(credential.Descriptor, c, first)
	if err != nil {
		b.Fatal(err)
	}
	if rec, err = record.CoSign(credential.Descriptor, rec, second); err != nil {
		b.Fatal(err)
	}
	return store.CredentialRecord{Digest: rec.Digest(), Record: rec}
}

// transactionBenchmarkRevocation is the revocation of cred, signed by issuer, one
// of its two ends.
func transactionBenchmarkRevocation(b *testing.B, cred store.CredentialRecord, issuer record.Signer) store.CredRevocationRecord {
	b.Helper()
	unverified, err := record.Peek(credential.Descriptor, cred.Record)
	if err != nil {
		b.Fatal(err)
	}
	rec, err := credential.IssueRevocation(unverified.Payload(), issuer)
	if err != nil {
		b.Fatal(err)
	}
	return store.CredRevocationRecord{Revoked: cred.Digest, Record: rec}
}

// transactionBenchmarkParty is the end of a pairing that a certificate and the
// key of its connection make.
func transactionBenchmarkParty(b *testing.B, cert record.Verified[record.NetworkKeyCertificate], network ident.PublicKey) pairing.Party {
	b.Helper()
	serial := cert.Payload().Serial
	party, err := pairing.NewParty(cert, revocation.NewSet().NetworkKey(cert, serial).Revocation, serial, network, harness.DefaultStart)
	if err != nil {
		b.Fatal(err)
	}
	return party
}

func (p transactionBenchmarkPeer) party(b *testing.B) pairing.Party {
	b.Helper()
	_, cert := p.certificate(b)
	return transactionBenchmarkParty(b, cert, p.network.Public())
}

// transactionBenchmarkTranscript is the transcript both sides of one request
// bound; salt makes one request's identifier differ from another's.
func transactionBenchmarkTranscript(b *testing.B, initiator, target pairing.Party, salt byte) pairing.Transcript {
	b.Helper()
	at := harness.DefaultStart
	terms := pairing.Terms{Protocol: transactionBenchmarkPairingProtocol, Initiator: initiator, Target: target, CreatedAt: at, NotAfter: at.Add(24 * time.Hour)}
	nA, nB := pairing.Nonce{salt}, pairing.Nonce{salt + 1}
	commitment, err := terms.Commit(nA)
	if err != nil {
		b.Fatal(err)
	}
	transcript, err := pairing.Open(terms, commitment, nB, nA)
	if err != nil {
		b.Fatal(err)
	}
	return transcript
}

// A request that waits for its owner, on the target and on the initiator.
func transactionBenchmarkTargetRecord(t pairing.Transcript) pairing.TargetRecord {
	at := harness.DefaultStart
	return pairing.TargetRecord{Transcript: t, ReceivedAt: at, ExpiresAt: at.Add(5 * time.Minute), Status: pairing.TargetPending}
}

func transactionBenchmarkInitiatorRecord(t pairing.Transcript) pairing.InitiatorRecord {
	at := harness.DefaultStart
	return pairing.InitiatorRecord{Transcript: t, ReceivedAt: at, ExpiresAt: at.Add(5 * time.Minute), Status: pairing.InitiatorActive}
}

func transactionBenchmarkExpectations(node ident.NodeID) []pairing.Expectation {
	return []pairing.Expectation{{Node: node, Author: pairing.LocalAuthor(), Operation: admin.OperationID{1}, CreatedAt: harness.DefaultStart}}
}
