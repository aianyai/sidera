package engine_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
)

// The nodes of the trust tests are real ones, made with deterministic keys: the
// engine verifies every record it keeps, so a stand-in would prove nothing. Node n
// has the identity key of the seed byte n (1 to 7); its network key at a serial (1
// to 7) is another seed, so that a certificate of another serial names another key.

func seededSigner(t testing.TB, seed byte) record.Signer {
	t.Helper()
	s, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type peerNode struct {
	t        testing.TB
	n        byte
	identity record.Signer
	node     ident.NodeID
}

func newPeerNode(t testing.TB, n byte) peerNode {
	t.Helper()
	identity := seededSigner(t, n)
	node, err := ident.NodeIDFromKey(identity.Public())
	if err != nil {
		t.Fatal(err)
	}
	return peerNode{t: t, n: n, identity: identity, node: node}
}

// networkSigner is the network key the node has at a serial.
func (p peerNode) networkSigner(serial uint64) record.Signer {
	return seededSigner(p.t, 0x80+8*p.n+byte(serial))
}

// certificate is the node's certificate at a serial, valid for 30 days from the
// start of the test clock, as the engine receives it from the network layer.
func (p peerNode) certificate(serial uint64) engine.TestPeer {
	return p.certificateFor(serial, 30*24*time.Hour)
}

// certificateFor is certificate with a validity of its own: two certificates of one
// serial that differ in their window are two certificates.
func (p peerNode) certificateFor(serial uint64, life time.Duration) engine.TestPeer {
	p.t.Helper()
	cert := record.IssueNetworkKeyCertificate(p.node, p.networkSigner(serial).Public(), serial, t0, t0.Add(life))
	rec, err := record.Seal(record.NetworkKeyCertificateDescriptor, cert, p.identity)
	if err != nil {
		p.t.Fatal(err)
	}
	v, err := record.Verify(record.NetworkKeyCertificateDescriptor, rec)
	if err != nil {
		p.t.Fatal(err)
	}
	return engine.TestPeer{Cert: v, Rec: rec, Seen: record.SerialSeenOf(v)}
}

// report is a status report of a message the node under test sent to this peer,
// signed by the network key the peer has at serial, as the peer's receipt carries it.
func (p peerNode) report(sentBy ident.NodeID, serial uint64) record.Record {
	p.t.Helper()
	cert := p.certificate(serial)
	accepted := revocation.NewSet().NetworkKey(cert.Cert, serial)
	rep := envelope.StatusReport{
		Sender: sentBy, Message: "message-1", Recipient: mustEndpoint(p.t, 1),
		Status: delivery.StatusReport{State: delivery.StateAccepted}, ReportedAt: t0,
	}
	rec, err := record.Seal(envelope.StatusReportDescriptor(p.node, accepted, t0), rep, p.networkSigner(serial))
	if err != nil {
		p.t.Fatal(err)
	}
	return rec
}

// credentialWith is the credential between the node under test, whose identity key
// is own, and the peer, signed by both ends. The credential has the signature of
// the peer's end only when onlyPeer is set: the record a node cannot have made.
func (p peerNode) credentialWith(own record.Signer, onlyPeer bool) store.CredentialRecord {
	p.t.Helper()
	ownNode, err := ident.NodeIDFromKey(own.Public())
	if err != nil {
		p.t.Fatal(err)
	}
	c := credential.New(ownNode, p.node, t0, t0.Add(30*24*time.Hour), credential.Nonce{p.n, 1})
	first, second := own, p.identity
	if c.A != ownNode {
		first, second = p.identity, own
	}
	signers := []record.Signer{first, second}
	if onlyPeer {
		signers = []record.Signer{p.identity}
	}
	rec, err := record.Seal(credential.Descriptor, c, signers[0])
	if err != nil {
		p.t.Fatal(err)
	}
	if len(signers) > 1 {
		if rec, err = record.CoSign(credential.Descriptor, rec, signers[1]); err != nil {
			p.t.Fatal(err)
		}
	}
	return store.CredentialRecord{Digest: rec.Digest(), Record: rec}
}

// fakeNetwork records what the engine pushes: the snapshots of the facts and the
// address books. checkErr is what it says of every address check, and connected
// the nodes it says are connected.
type fakeNetwork struct {
	mu        sync.Mutex
	snaps     []engine.Membership
	books     []map[ident.NodeID][]string
	checkErr  error
	connected map[ident.NodeID]bool
	// fetches are the pairing fetches the engine asked for, by the node asked.
	fetches []ident.NodeID
}

// PairingFetch records the exchange and answers that none came.
func (n *fakeNetwork) PairingFetch(_ context.Context, node ident.NodeID, _ []string, _ []byte) ([]byte, engine.PairingPeer, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.fetches = append(n.fetches, node)
	return nil, engine.PairingPeer{}, errors.New("no answer")
}

func (n *fakeNetwork) fetched() []ident.NodeID {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]ident.NodeID(nil), n.fetches...)
}

func (n *fakeNetwork) SetAddressBook(book map[ident.NodeID][]string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.books = append(n.books, book)
	return nil
}

func (n *fakeNetwork) CheckAddresses(ident.NodeID, []string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.checkErr
}

func (n *fakeNetwork) PeerID() string        { return "peer-id" }
func (n *fakeNetwork) ListenAddrs() []string { return []string{"/ip4/0.0.0.0/tcp/7744"} }
func (n *fakeNetwork) ContactAddrs() []string {
	return []string{"/ip4/10.0.0.9/tcp/7744/p2p/peer-id"}
}

func (n *fakeNetwork) addressBooks() []map[ident.NodeID][]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]map[ident.NodeID][]string(nil), n.books...)
}

func (n *fakeNetwork) SetMembership(m engine.Membership) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.snaps = append(n.snaps, m)
	return nil
}

func (n *fakeNetwork) Connected(node ident.NodeID) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.connected[node]
}

func (n *fakeNetwork) snapshots() []engine.Membership {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]engine.Membership(nil), n.snaps...)
}
