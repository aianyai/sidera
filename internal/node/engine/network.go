package engine

import (
	"errors"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/preauth"
	"github.com/aianyai/sidera/internal/record"
)

// Network is what the engine asks of the layer that carries its calls to other
// nodes: it is told the facts that layer judges a peer by, and it says whether a
// node is connected now. *network.Network is the implementation; the engine
// imports nothing of that package, and the composition root converts between the
// two Membership types, which have the same fields.
type Network interface {
	// SetMembership replaces the snapshot of the facts. The network keeps what it
	// is given as it is, so the engine builds a new one for every change and
	// changes none afterwards.
	SetMembership(Membership) error
	// Connected says whether the node has a connection now. It only reads and
	// does not wait.
	Connected(node ident.NodeID) bool
	// SetAddressBook replaces the addresses the network dials a node on. The
	// engine pushes the whole book after every durable change of it.
	SetAddressBook(book map[ident.NodeID][]string) error
	// CheckAddresses judges addresses the owner enters for a node, by the rule
	// that depends on the certificate held for it. The error is a
	// *config.FieldError that names the offending address.
	CheckAddresses(node ident.NodeID, addrs []string) error
	// PeerID is the identifier of the node's network key that a contact line
	// names after /p2p/; ListenAddrs are the addresses the listeners are open on;
	// ContactAddrs are those a person can hand to another node, each ending in
	// /p2p/<peer id>. They only read.
	PeerID() string
	ListenAddrs() []string
	ContactAddrs() []string
}

// Membership is the snapshot of what the network layer needs to judge a peer: the
// node's own certificate to present, the facts a decision reads, and the
// certificates the node holds. It has the fields of the network layer's own type
// and no more, so that the one converts to the other as it is.
type Membership struct {
	// Own is the node's own certificate in force, and OwnWire its encoded record,
	// which is what the certificate exchange sends.
	Own     record.Verified[record.NetworkKeyCertificate]
	OwnWire []byte
	// Facts are the facts of a decision: the revocation set, the trust view, the
	// serial state of every peer the engine stores and, apart from them, nothing
	// else this node knows. Now is not read; the network layer takes the time of its
	// own clock.
	Facts preauth.Facts
	// Held are the verified certificates the node holds for its peers.
	Held []record.Verified[record.NetworkKeyCertificate]
}

// SetNetwork gives the engine the network layer it pushes its membership to. It is
// for the composition root, before Start, like SetTransport: the network is made
// with the engine as its node, so it cannot exist before the engine does. A nil
// network is allowed, and nothing is pushed then.
func (e *Engine) SetNetwork(n Network) error {
	if e.Phase() != PhaseOpened || e.started.Load() {
		return errors.New("engine: the network is set before Start")
	}
	e.net = n
	return nil
}

// membershipSnapshot is the snapshot of the facts as they are now. It reads state
// that belongs to the loop, so it is made there, or in Start before the loop
// exists. It has none when the engine has no keys: there is no certificate to
// present.
func (e *Engine) membershipSnapshot() (Membership, bool) {
	if e.d.Keys == nil {
		return Membership{}, false
	}
	seen := make(map[ident.NodeID]record.SerialSeen, len(e.trust.peers))
	held := make([]record.Verified[record.NetworkKeyCertificate], 0, len(e.trust.peers))
	for node, p := range e.trust.peers {
		seen[node] = p.seen()
		held = append(held, p.cert)
	}
	own := e.d.Keys.Certificate()
	e.pushedOwn = own.Digest()
	return Membership{
		Own: own, OwnWire: e.d.Keys.CertificateWire(),
		Facts: preauth.Facts{Revocations: e.d.Config.RevocationSet(), Trust: e.trust.view, SerialSeen: seen},
		Held:  held,
	}, true
}

// publishMembership pushes the snapshot to the network. It is the one place that
// does: every change of what the snapshot holds ends in it, after the write that
// made the change durable. A failure is logged and not retried: the next change
// pushes a whole new snapshot, and the network keeps the one it has meanwhile.
// The call waits for the network, which does not call back into the engine's loop
// while it holds a snapshot.
func (e *Engine) publishMembership() {
	if e.net == nil {
		return
	}
	m, ok := e.membershipSnapshot()
	if !ok {
		return
	}
	if err := e.net.SetMembership(m); err != nil {
		e.log.Error("the network did not take the membership", "error", err.Error())
	}
}

// publishRenewedCertificate pushes a snapshot when the node's own certificate is
// not the one the network was last given: the keys renew it in a periodic task of
// the composition root, which the engine does not see happen. A peer holds on to a
// connection's exchange, so the network closes its connections when the
// certificate it presents changes, and this is how it learns of it.
func (e *Engine) publishRenewedCertificate() {
	if e.net != nil && e.d.Keys != nil && e.d.Keys.Certificate().Digest() != e.pushedOwn {
		e.publishMembership()
	}
}
