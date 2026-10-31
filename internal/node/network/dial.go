package network

import (
	"context"
	"errors"
	"fmt"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/preauth"
)

// Dialing a node starts from what the node's identity cannot give: a node is
// named by its identity key, and a connection by the network key its certificate
// names, so the way to a node is its peer identifier plus an address.
//
// The peer identifier comes first from an accepted certificate (the peer
// identifier is a function of the network key it names), and otherwise from the
// /p2p/ part of an address book entry, which the first contact with a node has
// no other source for: the library refuses to dial a peer it does not know the
// identifier of. Addresses come from the address book, written to the peerstore
// to stay (the host's own connect gives them two minutes), and from what
// identify taught the peerstore.

// SetAddressBook replaces the address book: for each node the multiaddrs it can
// be reached on, each with /p2p/<peer id> unless an accepted certificate gives the
// peer identifier. Nothing is changed when an address does not parse.
func (n *Network) SetAddressBook(book map[ident.NodeID][]string) error {
	parsed := make(map[ident.NodeID][]ma.Multiaddr, len(book))
	for node, addrs := range book {
		for _, a := range addrs {
			m, err := ma.NewMultiaddr(a)
			if err != nil {
				return fmt.Errorf("network: address %q of node %s: %w", a, node, err)
			}
			parsed[node] = append(parsed[node], m)
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.book = parsed
	n.rebuildDialLocked()
	return nil
}

// rebuildDialLocked derives, for every node, the peers to dial in order, and
// writes the address book's addresses to the peerstore for them. A peer whose
// addresses changed, or that the previous book wrote and this one does not, has
// its old addresses cleared.
func (n *Network) rebuildDialLocked() {
	dial := map[ident.NodeID][]peer.ID{}
	add := func(node ident.NodeID, pid peer.ID) {
		for _, have := range dial[node] {
			if have == pid {
				return
			}
		}
		dial[node] = append(dial[node], pid)
	}
	if n.membership != nil {
		for _, c := range n.membership.Held {
			cert := c.Payload()
			pid, err := peerIDOf(cert.NetworkKey)
			if err != nil {
				n.log.Warn("a held certificate names a key that has no peer identifier", "node", cert.Node, "err", err)
				continue
			}
			add(cert.Node, pid)
		}
	}
	transports := map[ident.NodeID][]ma.Multiaddr{}
	for node, addrs := range n.book {
		for _, a := range addrs {
			tr, pid := peer.SplitAddr(a)
			if pid != "" {
				add(node, pid)
			}
			if tr != nil {
				transports[node] = append(transports[node], tr)
			}
		}
	}

	want := map[peer.ID][]ma.Multiaddr{}
	for node, pids := range dial {
		for _, pid := range pids {
			if len(transports[node]) > 0 {
				want[pid] = append(want[pid], transports[node]...)
			}
		}
	}
	ps := n.lib.host.Peerstore()
	for pid, addrs := range want {
		if !sameAddrs(n.bookWritten[pid], addrs) {
			// Addresses are only ever added to a peerstore: the old ones of a
			// changed entry are cleared first, and so is what identify learned,
			// which the next connection teaches again.
			ps.ClearAddrs(pid)
		}
		ps.AddAddrs(pid, addrs, peerstore.PermanentAddrTTL)
	}
	for pid := range n.bookWritten {
		if _, still := want[pid]; !still {
			ps.ClearAddrs(pid)
		}
	}
	n.dialPeers, n.bookWritten = dial, want
}

func sameAddrs(a, b []ma.Multiaddr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equal(b[i]) {
			return false
		}
	}
	return true
}

// connect returns a connection this side judged a member's, to the node: one that
// is up, or a new one, dialed and exchanged within the connect budget. A node
// that cannot be connected is unreachable, and stays so without another attempt
// for a while, so that the engine's single worker does not spend the budget on
// every resend to a node that is down.
//
// The error is transport.ErrUnreachable, or the caller's own context error when
// that is what ended the wait.
func (n *Network) connect(ctx context.Context, node ident.NodeID) (*connEntry, error) {
	if e := n.memberConn(node); e != nil {
		return e, nil
	}
	n.mu.Lock()
	until, memo := n.unreachable[node]
	pids := n.dialPeers[node]
	n.mu.Unlock()
	if memo && n.clock.Now().Before(until) {
		return nil, fmt.Errorf("%w: it could not be connected a moment ago", transport.ErrUnreachable)
	}
	if len(pids) == 0 {
		return nil, fmt.Errorf("%w: no address is known for it", transport.ErrUnreachable)
	}

	bctx, cancel := context.WithTimeout(ctx, n.cfg.ConnectTimeout)
	defer cancel()
	e, err := n.dialMember(bctx, node, pids)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		n.mu.Lock()
		n.unreachable[node] = n.clock.Now().Add(n.cfg.UnreachableFor)
		n.mu.Unlock()
		return nil, fmt.Errorf("%w: %v", transport.ErrUnreachable, err)
	}
	return e, nil
}

// dialMember dials the peers in order until one connects, completes the
// exchange and proves to be the node, as a member.
func (n *Network) dialMember(ctx context.Context, node ident.NodeID, pids []peer.ID) (*connEntry, error) {
	var failures error
	for _, pid := range pids {
		c, err := n.lib.host.Network().DialPeer(ctx, pid)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		e := n.entryOf(c)
		if e == nil {
			failures = errors.Join(failures, errors.New("the connection ended"))
			continue
		}
		select {
		case <-e.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		n.mu.Lock()
		state, got := e.state, e.node
		n.mu.Unlock()
		switch {
		case state == preauth.StateMember && got == node:
			return e, nil
		case state == preauth.StateMember:
			failures = errors.Join(failures, fmt.Errorf("peer %s is another node", pid))
		default:
			failures = errors.Join(failures, fmt.Errorf("peer %s is not a member (%s)", pid, state))
		}
	}
	return nil, failures
}
