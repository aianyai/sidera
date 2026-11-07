package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
)

// The operations that change network.json: the addresses of one node and the list
// the node listens on. Like a change of the node settings they are the owner's
// alone (admin.OpChangeNodeSettings) and versioned: the version the request names
// is checked inside the change, and the operation with its result is kept in
// network.json in the same replacement. encode makes the result from the state
// the change produces; it runs in the loop and must not wait.

// networkOp is the part the three operations share.
type networkOp struct {
	encode func(config.Network) ([]byte, error)
}

func (networkOp) Admin() admin.Operation { return admin.Operation{Kind: admin.OpChangeNodeSettings} }
func (networkOp) ends() Ending           { return EndingNone }

// result is the ResultFunc of a change of network.json.
func (o networkOp) result(x *opRun) config.ResultFunc[config.Network] {
	return func(after config.Network) (json.RawMessage, error) {
		b, err := o.encode(after)
		if err != nil {
			return nil, fmt.Errorf("the result of %s: %w", x.name, err)
		}
		return b, nil
	}
}

// pushAddressBook gives the network the address book as it is now. It is called
// after the write that made the book durable. A failure is logged and not retried:
// the book was judged by config before it was written, and the next start gives
// the network the whole book again.
func (e *Engine) pushAddressBook(n config.Network) {
	if e.net == nil {
		return
	}
	if err := e.net.SetAddressBook(n.Peers); err != nil {
		e.log.Error("the network did not take the address book", "error", err.Error())
	}
}

// SetPeerAddressesOp replaces the addresses kept for a node, adding the node to
// the book when it is not there. The addresses are judged by the network first,
// by the rule that depends on the certificate held for the node; a refusal is a
// ChangeInvalid change, as the refusals of config's own rules are, and writes
// nothing. The new book is pushed to the network once the write is durable.
func SetPeerAddressesOp(node ident.NodeID, addrs []string, encode func(config.Network) ([]byte, error)) Operation {
	return &setPeerAddressesOp{networkOp{encode}, node, addrs}
}

type setPeerAddressesOp struct {
	networkOp
	node  ident.NodeID
	addrs []string
}

func (o *setPeerAddressesOp) run(e *Engine, x *opRun) ([]byte, error) {
	if e.net != nil {
		if err := e.net.CheckAddresses(o.node, o.addrs); err != nil {
			return nil, invalidAddresses(err)
		}
	}
	n, err := e.d.Config.SetPeerAddresses(x.configRequest(), o.node, o.addrs, o.result(x))
	if err != nil {
		return nil, err
	}
	e.pushAddressBook(n)
	return n.LastOperation.Result, nil
}

// invalidAddresses turns the network's refusal of addresses into the refusal of a
// change of the configuration, so that it reaches the owner as every other
// invalid value does, with the path of the address.
func invalidAddresses(err error) error {
	var fe *config.FieldError
	if errors.As(err, &fe) {
		return &config.ChangeError{Reason: config.ChangeInvalid, Field: fe.Field, Err: fe.Err}
	}
	return &config.ChangeError{Reason: config.ChangeInvalid, Err: err}
}

// RemovePeerOp removes a node's addresses from the book. A node with no address
// there is refused (ChangePeerUnknown). The new book is pushed to the network
// once the write is durable.
func RemovePeerOp(node ident.NodeID, encode func(config.Network) ([]byte, error)) Operation {
	return &removePeerOp{networkOp{encode}, node}
}

type removePeerOp struct {
	networkOp
	node ident.NodeID
}

func (o *removePeerOp) run(e *Engine, x *opRun) ([]byte, error) {
	n, err := e.d.Config.RemovePeer(x.configRequest(), o.node, o.result(x))
	if err != nil {
		return nil, err
	}
	e.pushAddressBook(n)
	return n.LastOperation.Result, nil
}

// SetListenOp replaces the list of addresses the node listens on. The listeners
// are opened once, at the end of the start, so the change is kept for the next
// start and nothing is pushed anywhere.
func SetListenOp(listen []string, encode func(config.Network) ([]byte, error)) Operation {
	return &setListenOp{networkOp{encode}, listen}
}

type setListenOp struct {
	networkOp
	listen []string
}

func (o *setListenOp) run(e *Engine, x *opRun) ([]byte, error) {
	n, err := e.d.Config.SetListen(x.configRequest(), o.listen, o.result(x))
	if err != nil {
		return nil, err
	}
	return n.LastOperation.Result, nil
}

// NetworkPeer is what the engine and the network know about one node the owner
// or a pairing made known.
type NetworkPeer struct {
	Node ident.NodeID
	// Addresses are the ones the owner entered; none for a node that is only
	// known by its certificate.
	Addresses []string
	// CertificateHeld: the node has a certificate the engine stored.
	CertificateHeld bool
	// Connected: a member connection to the node is up now.
	Connected bool
	// Profile is what the node says about itself; nil when this node has not
	// fetched one since it started.
	Profile *ProfileView
}

// NetworkView is network.json with the facts of the running network layer.
type NetworkView struct {
	// Config is network.json: the version a change names, the configured listen
	// list and the address book.
	Config config.Network
	// PeerID, Listening and Contact are the network's: its identifier, the
	// addresses its listeners are open on (empty before it listens or without a
	// network), and the addresses a person can hand to another node.
	PeerID    string
	Listening []string
	Contact   []string
	// Peers are the nodes of the address book and the nodes whose certificate is
	// held, in the order of their identifiers.
	Peers []NetworkPeer
}

type cmdNetworkView struct{ call[NetworkView] }

func (c *cmdNetworkView) gate() gate { return gateRead }
func (c *cmdNetworkView) run(e *Engine, now time.Time) {
	v := NetworkView{Config: e.d.Config.Network()}
	if e.net != nil {
		v.PeerID, v.Listening, v.Contact = e.net.PeerID(), e.net.ListenAddrs(), e.net.ContactAddrs()
	}
	known := map[ident.NodeID]bool{}
	for node := range v.Config.Peers {
		known[node] = true
	}
	for node := range e.trust.peers {
		known[node] = true
	}
	for node := range known {
		_, held := e.trust.peers[node]
		v.Peers = append(v.Peers, NetworkPeer{
			Node: node, Addresses: v.Config.Peers[node], CertificateHeld: held,
			Connected: e.net != nil && e.net.Connected(node), Profile: e.profileOf(node, now),
		})
	}
	slices.SortFunc(v.Peers, func(a, b NetworkPeer) int { return strings.Compare(a.Node.String(), b.Node.String()) })
	c.done(v, nil)
}

// NetworkView is network.json and what the network layer knows, as the loop sees
// them: the version a change names, where the node listens, the address book and,
// per node, whether its certificate is held and whether it is connected.
func (e *Engine) NetworkView(ctx context.Context) (NetworkView, error) {
	c := &cmdNetworkView{call: newCall[NetworkView]()}
	return roundTrip(ctx, e, submitExternal, c, c.call)
}
