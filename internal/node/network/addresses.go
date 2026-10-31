package network

import (
	"errors"
	"fmt"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
)

// heldPeerLocked is the peer identifier of the network key in the certificate the
// node holds for the node, and false when it holds none.
func (n *Network) heldPeerLocked(node ident.NodeID) (peer.ID, bool) {
	if n.membership == nil {
		return "", false
	}
	for _, c := range n.membership.Held {
		cert := c.Payload()
		if cert.Node != node {
			continue
		}
		pid, err := peerIDOf(cert.NetworkKey)
		return pid, err == nil
	}
	return "", false
}

// CheckAddresses judges addresses the owner enters for a node, before they are
// kept. An address is first judged by config.ValidatePeerAddress, the one
// definition of what an address may be. Then, while the node holds no
// certificate for the node, at least one address must end in /p2p/<peer id>: the
// library does not dial a peer whose identifier it does not know, and a first
// contact has no other source of it. Once a certificate is held its network key
// gives the peer identifier, so every /p2p/ must be that one (an address with
// none is fine): an address that names another peer could never reach the node.
//
// The error is a *config.FieldError whose Field is the path of the offending
// address in the request ("addresses[1]"), or "addresses" for the list as a
// whole. The rule is here because this package derives peer identifiers from
// certificates and holds the certificates the engine pushed.
func (n *Network) CheckAddresses(node ident.NodeID, addrs []string) error {
	n.mu.Lock()
	want, held := n.heldPeerLocked(node)
	n.mu.Unlock()

	named := false
	for i, a := range addrs {
		field := fmt.Sprintf("addresses[%d]", i)
		if err := config.ValidatePeerAddress(a); err != nil {
			return &config.FieldError{Field: field, Err: err}
		}
		m, err := ma.NewMultiaddr(a)
		if err != nil {
			return &config.FieldError{Field: field, Err: err}
		}
		_, pid := peer.SplitAddr(m)
		if pid == "" {
			continue
		}
		named = true
		if held && pid != want {
			return &config.FieldError{Field: field, Err: fmt.Errorf("the peer identifier is not the one of the certificate held for the node (%s)", want)}
		}
	}
	if !held && !named {
		return &config.FieldError{Field: "addresses", Err: errors.New("no certificate is held for the node, so one address must end in /p2p/<peer id>")}
	}
	return nil
}

// ContactAddrs are the addresses a person can give another to reach this node:
// the addresses the listeners are open on, an unspecified one (0.0.0.0, ::)
// expanded into the addresses of the machine's interfaces of its family, without
// loopback and link-local addresses, each ending in /p2p/<peer id>. An unspecified
// address no interface answers for is left out; a node that listens nowhere has
// none.
func (n *Network) ContactAddrs() []string {
	ifaces, err := manet.InterfaceMultiaddrs()
	if err != nil {
		n.log.Warn("the interfaces of the machine could not be listed", "err", err)
	}
	var out []string
	for _, a := range n.lib.host.Network().ListenAddresses() {
		// A listener on a machine with no interface of the family has nothing to
		// resolve to; the others still count.
		resolved, err := manet.ResolveUnspecifiedAddress(a, ifaces)
		if err != nil {
			continue
		}
		for _, r := range resolved {
			if manet.IsIPLoopback(r) || manet.IsIP6LinkLocal(r) || manet.IsIPUnspecified(r) {
				continue
			}
			out = append(out, r.String()+"/p2p/"+n.peer.String())
		}
	}
	return out
}
