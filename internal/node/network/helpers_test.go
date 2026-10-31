package network

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/preauth"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
	"github.com/aianyai/sidera/internal/trust"
)

// eventually polls until cond holds, on the real clock: the libp2p runtime runs
// on its own goroutines and the fake clock cannot wait for it.
func eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// syncBuffer is a log sink that tests read while the network writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeNode is the engine's side of the seam, with a behavior per call.
type fakeNode struct {
	mu sync.Mutex

	inbound func(ctx context.Context, from transport.Origin, sender ident.EndpointID, envelope []byte) (transport.Receipt, error)
	pull    func(ctx context.Context, from transport.Origin, req transport.PullRequest) (transport.PullAnswer, error)
	dir     func(ctx context.Context, from transport.Origin, sender ident.EndpointID, req transport.DirectoryRequest) (transport.DirectoryPage, error)
	refusal func(err error) (string, bool)

	// noteBlock, when set, holds NotePeerCertificate until it is closed.
	noteBlock chan struct{}
	noteErr   error
	notes     []record.SerialSeen
	origins   []transport.Origin
}

func (f *fakeNode) Inbound(ctx context.Context, from transport.Origin, sender ident.EndpointID, envelope []byte) (transport.Receipt, error) {
	f.mu.Lock()
	f.origins = append(f.origins, from)
	h := f.inbound
	f.mu.Unlock()
	if h == nil {
		return transport.Receipt{}, nil
	}
	return h(ctx, from, sender, envelope)
}

func (f *fakeNode) Pull(ctx context.Context, from transport.Origin, req transport.PullRequest) (transport.PullAnswer, error) {
	f.mu.Lock()
	f.origins = append(f.origins, from)
	h := f.pull
	f.mu.Unlock()
	if h == nil {
		return transport.PullAnswer{}, nil
	}
	return h(ctx, from, req)
}

func (f *fakeNode) QueryDirectory(ctx context.Context, from transport.Origin, sender ident.EndpointID, req transport.DirectoryRequest) (transport.DirectoryPage, error) {
	f.mu.Lock()
	f.origins = append(f.origins, from)
	h := f.dir
	f.mu.Unlock()
	if h == nil {
		return transport.DirectoryPage{}, nil
	}
	return h(ctx, from, sender, req)
}

func (f *fakeNode) NotePeerCertificate(ctx context.Context, cert record.Verified[record.NetworkKeyCertificate], seen record.SerialSeen) error {
	f.mu.Lock()
	f.notes = append(f.notes, seen)
	block, err := f.noteBlock, f.noteErr
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (f *fakeNode) RefusalReason(err error) (string, bool) {
	f.mu.Lock()
	h := f.refusal
	f.mu.Unlock()
	if h == nil {
		return "", false
	}
	return h(err)
}

func (f *fakeNode) noted() []record.SerialSeen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]record.SerialSeen(nil), f.notes...)
}

func (f *fakeNode) setBlock(c chan struct{}) {
	f.mu.Lock()
	f.noteBlock = c
	f.mu.Unlock()
}

// peerNode is one node of a test: its keys, its network and its fake engine.
type peerNode struct {
	t        testing.TB
	clk      *clocktest.Fake
	identity record.Signer
	netKey   ed25519.PrivateKey
	id       ident.NodeID
	net      *Network
	node     *fakeNode
	log      *syncBuffer
}

func seedKey(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
}

// newPeerNode makes the keys of a node from a seed. The network key is the
// identity's seed plus 0x80, unless netSeed says otherwise.
func newPeerNode(t testing.TB, clk *clocktest.Fake, identitySeed, netSeed byte) *peerNode {
	t.Helper()
	identity, err := record.NewEd25519Signer(seedKey(identitySeed))
	if err != nil {
		t.Fatal(err)
	}
	id, err := ident.NodeIDFromKey(identity.Public())
	if err != nil {
		t.Fatal(err)
	}
	return &peerNode{t: t, clk: clk, identity: identity, netKey: seedKey(netSeed), id: id, node: &fakeNode{}, log: &syncBuffer{}}
}

// start builds the network of the node and listens on a TCP address and,
// when quic is set, a QUIC address, both on the loopback.
func (p *peerNode) start(quic bool, tune func(*Config)) {
	p.t.Helper()
	cfg := Config{
		Key:   p.netKey,
		Clock: p.clk,
		Log:   slog.New(slog.NewTextHandler(p.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if tune != nil {
		tune(&cfg)
	}
	n, err := New(cfg)
	if err != nil {
		p.t.Fatal(err)
	}
	p.t.Cleanup(func() { n.Close() })
	p.net = n
	n.SetNode(p.node)
	addrs := []string{"/ip4/127.0.0.1/tcp/0"}
	if quic {
		addrs = append(addrs, "/ip4/127.0.0.1/udp/0/quic-v1")
	}
	if err := n.Listen(addrs); err != nil {
		p.t.Fatal(err)
	}
}

// cert issues the node's network-key certificate at a serial, valid from the
// fake clock's now for a month.
func (p *peerNode) cert(serial uint64) (record.Verified[record.NetworkKeyCertificate], []byte) {
	p.t.Helper()
	netPub, err := ident.NewPublicKey(ident.AlgorithmEd25519, p.netKey.Public().(ed25519.PublicKey))
	if err != nil {
		p.t.Fatal(err)
	}
	c := record.IssueNetworkKeyCertificate(p.id, netPub, serial, p.clk.Now(), p.clk.Now().Add(30*24*time.Hour))
	rec, err := record.Seal(record.NetworkKeyCertificateDescriptor, c, p.identity)
	if err != nil {
		p.t.Fatal(err)
	}
	v, err := record.Verify(record.NetworkKeyCertificateDescriptor, rec)
	if err != nil {
		p.t.Fatal(err)
	}
	wire, err := rec.Encode()
	if err != nil {
		p.t.Fatal(err)
	}
	return v, wire
}

// membership is the snapshot of the node at a certificate serial, trusting the
// given nodes directly and holding their certificates when held is set.
func (p *peerNode) membership(serial uint64, held bool, trusted ...*peerNode) Membership {
	p.t.Helper()
	own, wire := p.cert(serial)
	view := trust.View{
		Local:                      p.id,
		Settings:                   trust.DefaultSettings(),
		LocallyRevoked:             trust.NodeSet{},
		Retired:                    trust.NodeSet{},
		RevokedCredentials:         trust.DigestSet{},
		Vouchers:                   trust.Vouchers{},
		DeclaredAcceptsInheritance: map[ident.NodeID]bool{},
	}
	var heldCerts []record.Verified[record.NetworkKeyCertificate]
	for i, o := range trusted {
		view.Credentials = append(view.Credentials, trust.Credential{Digest: trust.CredentialDigest{byte(i + 1)}, A: p.id, B: o.id})
		if held {
			c, _ := o.cert(1)
			heldCerts = append(heldCerts, c)
		}
	}
	return Membership{
		Own:     own,
		OwnWire: wire,
		Facts:   preauth.Facts{Revocations: revocation.NewSet(), Trust: view, SerialSeen: map[ident.NodeID]record.SerialSeen{}},
		Held:    heldCerts,
	}
}

func (p *peerNode) setMembership(serial uint64, trusted ...*peerNode) {
	p.t.Helper()
	if err := p.net.SetMembership(p.membership(serial, false, trusted...)); err != nil {
		p.t.Fatal(err)
	}
}

// contact is the address of the node with its peer identifier, the line a
// peer's address book holds.
func (p *peerNode) contact(tcp bool) string {
	p.t.Helper()
	for _, a := range p.net.ListenAddrs() {
		if strings.Contains(a, "/tcp/") == tcp {
			return a + "/p2p/" + p.net.PeerID()
		}
	}
	p.t.Fatal("no listen address of the wanted kind")
	return ""
}

// knows writes the other nodes' addresses into the node's address book.
func (p *peerNode) knows(others ...*peerNode) {
	p.t.Helper()
	book := map[ident.NodeID][]string{}
	for _, o := range others {
		book[o.id] = []string{o.contact(true)}
	}
	if err := p.net.SetAddressBook(book); err != nil {
		p.t.Fatal(err)
	}
}

// dialRaw connects to the other node with the library's own dial, bypassing the
// router's rule that only a member is called, and returns this side's entry of
// the connection once its exchange has an outcome.
func (p *peerNode) dialRaw(other *peerNode) *connEntry {
	p.t.Helper()
	m, err := ma.NewMultiaddr(other.contact(true))
	if err != nil {
		p.t.Fatal(err)
	}
	tr, pid := peer.SplitAddr(m)
	p.net.lib.host.Peerstore().AddAddrs(pid, []ma.Multiaddr{tr}, peerstore.PermanentAddrTTL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := p.net.lib.host.Network().DialPeer(ctx, pid)
	if err != nil {
		p.t.Fatal(err)
	}
	e := p.net.entryOf(c)
	if e == nil {
		p.t.Fatal("the connection is not in the table")
	}
	return e
}

// entryFor returns the node's entry of its connection to the other node, nil
// when there is none.
func (p *peerNode) entryFor(other *peerNode) *connEntry {
	p.net.mu.Lock()
	defer p.net.mu.Unlock()
	for c, e := range p.net.conns {
		if c.RemotePeer() == other.net.peer {
			return e
		}
	}
	return nil
}

func (p *peerNode) stateOf(other *peerNode) preauth.State {
	e := p.entryFor(other)
	if e == nil {
		return preauth.StateUnset
	}
	return p.net.stateOf(e)
}

func (p *peerNode) connCount() int {
	p.net.mu.Lock()
	defer p.net.mu.Unlock()
	return len(p.net.conns)
}
