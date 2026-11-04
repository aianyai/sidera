package network

import (
	"time"

	"github.com/libp2p/go-libp2p/core/control"
	lnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/preauth"
	"github.com/aianyai/sidera/internal/record"
)

// connEntry is the network layer's record of one live connection: what the
// certificate exchange decided about it and what holds it open.
//
// The fields below conn are guarded by Network.mu.
type connEntry struct {
	conn     lnet.Conn
	key      preauth.ConnKey
	peerKey  ident.PublicKey
	outbound bool

	// state is the connection's pre-authentication state: unauthenticated until
	// an exchange has been decided on it.
	state preauth.State
	// decided is set when an exchange was applied, whatever it decided; a later
	// exchange on the connection is ignored.
	decided bool
	// tracked means the accounting holds this connection and expects its
	// Disconnected: an inbound connection that has not been released yet.
	tracked bool
	// node is the node whose certificate was accepted, for a node exchange.
	node ident.NodeID
	// exchange is the peer's message, kept for deciding again when the facts
	// change.
	exchange *preauth.Exchange
	// peerCert is the certificate Decide accepted on this connection, peerRecord
	// the record it was verified from, peerSeen the serial state to store, and
	// ownCert the node's own certificate as this connection's exchange presented
	// it. They are set with the state, for a node exchange, and are what a
	// pairing stream's parties are built from: the certificates of this
	// connection, not whatever the snapshot holds later.
	peerCert   record.Verified[record.NetworkKeyCertificate]
	peerRecord record.Record
	peerSeen   record.SerialSeen
	ownCert    record.Verified[record.NetworkKeyCertificate]
	// busy counts the calls and handlers in flight on the connection;
	// closeIdle asks for the connection to be closed as soon as none is. A
	// connection with closeIdle set and none in flight is closing (see closing):
	// the node has decided to close it, and the close itself follows outside the
	// lock.
	busy      int
	closeIdle bool
	// idleSince is when busy last fell to zero, or when the connection came up.
	idleSince time.Time
	// seq orders the connections by when they were established.
	seq uint64
	// ready is closed when the outcome of the exchange is known or the
	// connection is gone.
	ready      chan struct{}
	readyClose bool
}

// signalReadyLocked closes ready once.
func (e *connEntry) signalReadyLocked() {
	if !e.readyClose {
		e.readyClose = true
		close(e.ready)
	}
}

// gater is the library's connection gater. Only the secured callback has a
// rule: the accounting decides whether a stranger's connection is let in.
type gater struct{ n *Network }

func (g *gater) InterceptPeerDial(peer.ID) bool                               { return true }
func (g *gater) InterceptAddrDial(peer.ID, ma.Multiaddr) bool                 { return true }
func (g *gater) InterceptAccept(lnet.ConnMultiaddrs) bool                     { return true }
func (g *gater) InterceptUpgraded(lnet.Conn) (bool, control.DisconnectReason) { return true, 0 }

func (g *gater) InterceptSecured(dir lnet.Direction, p peer.ID, addrs lnet.ConnMultiaddrs) bool {
	return g.n.secured(dir, p, addrs)
}

func (n *Network) secured(dir lnet.Direction, p peer.ID, addrs lnet.ConnMultiaddrs) bool {
	var d preauth.Direction
	switch dir {
	case lnet.DirInbound:
		d = preauth.DirectionInbound
	case lnet.DirOutbound:
		d = preauth.DirectionOutbound
	default:
		return false
	}
	key, err := publicKeyOf(p)
	if err != nil {
		n.log.Debug("connection refused: the peer's key is not usable", "peer", p, "err", err)
		return false
	}
	ck := preauth.ConnKey{Peer: key, Addr: addrs.RemoteMultiaddr().String()}
	n.mu.Lock()
	adm := n.acct.Secured(ck, d, n.clock.Now())
	n.mu.Unlock()
	if adm.Verdict != preauth.VerdictAllow {
		n.log.Debug("connection refused", "peer", p, "reason", adm.Reason.String())
		return false
	}
	return true
}

// connected records a connection the swarm established. An outbound connection
// starts the certificate exchange.
func (n *Network) connected(c lnet.Conn) {
	key, err := publicKeyOf(c.RemotePeer())
	if err != nil {
		n.closeAsync(c)
		return
	}
	outbound := c.Stat().Direction == lnet.DirOutbound
	e := &connEntry{
		conn:     c,
		key:      preauth.ConnKey{Peer: key, Addr: c.RemoteMultiaddr().String()},
		peerKey:  key,
		outbound: outbound,
		state:    preauth.StateUnauthenticated,
		ready:    make(chan struct{}),
	}
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return
	}
	n.connSeq++
	e.seq = n.connSeq
	e.idleSince = n.clock.Now()
	e.tracked = n.acct.Connected(e.key, n.clock.Now())
	n.conns[c] = e
	if e.tracked {
		n.byKey[e.key] = e
	}
	n.mu.Unlock()

	switch {
	case !outbound && !e.tracked:
		// Its entry was swept before it came up: no deadline binds it.
		n.log.Warn("closing an inbound connection the accounting does not track", "peer", c.RemotePeer())
		n.closeAsync(c)
	case outbound:
		n.start(func() { n.exchangeOutbound(e) })
	}
}

// disconnected releases whatever a connection held.
func (n *Network) disconnected(c lnet.Conn) {
	n.mu.Lock()
	e := n.conns[c]
	delete(n.conns, c)
	if e != nil {
		if e.tracked {
			n.acct.Disconnected(e.key)
			e.tracked = false
		}
		if n.byKey[e.key] == e {
			delete(n.byKey, e.key)
		}
		e.signalReadyLocked()
	}
	last := !n.hasConnLocked(c.RemotePeer())
	n.mu.Unlock()
	if last {
		n.limiter.reset(c.RemotePeer())
	}
}

// hasConnLocked reports whether the table holds a connection to the peer.
func (n *Network) hasConnLocked(p peer.ID) bool {
	for c := range n.conns {
		if c.RemotePeer() == p {
			return true
		}
	}
	return false
}

// entryOf returns the entry of a connection, nil when it is not in the table.
func (n *Network) entryOf(c lnet.Conn) *connEntry {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.conns[c]
}

// stateOf is the state a stream's connection is in; an unknown connection is
// unauthenticated.
func (n *Network) stateOf(e *connEntry) preauth.State {
	if e == nil {
		return preauth.StateUnauthenticated
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return e.state
}

// closeAsync closes a connection from a goroutine of the package: the library
// calls the callbacks that ask for closing while it holds its own locks.
func (n *Network) closeAsync(c lnet.Conn) {
	n.start(func() { c.Close() })
}

// closing reports whether the node has decided to close the connection and is
// about to: it was asked to close when idle and nothing is in flight. The
// decision is taken under Network.mu and the close follows outside it, so for
// that moment the connection is still in the table but cannot be used; a call
// that took it would fail on a connection the node itself ends. One that has
// something in flight is not closing yet, and takes more until the last
// one is released. Network.mu must be held.
func (e *connEntry) closing() bool { return e.closeIdle && e.busy == 0 }

// acquire marks a call or handler in flight on the connection. It fails when the
// connection is no longer in the table or is closing.
func (n *Network) acquire(e *connEntry) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conns[e.conn] != e || e.closing() {
		return false
	}
	e.busy++
	return true
}

// release ends what acquire began, and closes the connection when it was asked
// to close as soon as it is idle.
func (n *Network) release(e *connEntry) {
	n.mu.Lock()
	e.busy--
	if e.busy == 0 {
		e.idleSince = n.clock.Now()
	}
	closeNow := e.busy == 0 && e.closeIdle
	n.mu.Unlock()
	if closeNow {
		e.conn.Close()
	}
}

// memberOnly and mayPair are the states a call looks for in a connection: the
// member protocols want a member's, a pairing also takes a node that is not a
// member yet (the access table's rule for the pairing protocol, for nodes).
func memberOnly(s preauth.State) bool { return s == preauth.StateMember }

func mayPair(s preauth.State) bool {
	return s == preauth.StateMember || s == preauth.StateNonMemberNode
}

// nodeConn returns a live connection that this side judged to be a connection
// to the node, in a state accept takes: a connection that is closing is never
// returned, the one not asked to close comes first, and of those the one
// established last, since an older one may be left from before the peer went away
// and came back.
func (n *Network) nodeConn(node ident.NodeID, accept func(preauth.State) bool) *connEntry {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.nodeConnLocked(node, accept)
}

// nodeConnLocked is nodeConn with Network.mu held.
func (n *Network) nodeConnLocked(node ident.NodeID, accept func(preauth.State) bool) *connEntry {
	var best *connEntry
	for _, e := range n.conns {
		if !accept(e.state) || e.node != node || e.closing() {
			continue
		}
		if best == nil {
			best = e
			continue
		}
		newer := e.closeIdle == best.closeIdle && e.seq > best.seq
		if (best.closeIdle && !e.closeIdle) || newer {
			best = e
		}
	}
	return best
}

// dropConn closes a connection a call found at fault, and, when no other member
// connection to the node remains, remembers the node as unreachable so that the
// next call does not dial before the memo has run out and does not wait on a
// peer that is gone.
func (n *Network) dropConn(e *connEntry, node ident.NodeID) {
	n.mu.Lock()
	other := false
	for _, o := range n.conns {
		if o != e && o.state == preauth.StateMember && o.node == node {
			other = true
			break
		}
	}
	if !other {
		n.unreachable[node] = n.clock.Now().Add(n.cfg.UnreachableFor)
	}
	n.mu.Unlock()
	n.log.Debug("closing a connection a call found at fault", "peer", e.conn.RemotePeer(), "node", node)
	e.conn.Close()
}

// memberConn returns a live connection that this side judged a member's, for
// the node.
func (n *Network) memberConn(node ident.NodeID) *connEntry { return n.nodeConn(node, memberOnly) }

// Connected reports whether a connection this side judged a member's is up to
// the node.
func (n *Network) Connected(node ident.NodeID) bool { return n.memberConn(node) != nil }

// tickLoop is the wake-up of the accounting: every tick closes the connections
// that are overdue and sweeps what never came up, and sees to the members'
// reachability and the connections kept to them (see tickMembers). A timer only
// wakes the loop; the accounting's Overdue is the truth.
func (n *Network) tickLoop() {
	t := n.clock.NewTimer(n.cfg.TickEvery)
	defer t.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-t.C():
			n.tick()
			t.Reset(n.cfg.TickEvery)
		}
	}
}

func (n *Network) tick() {
	now := n.clock.Now()
	var closing []lnet.Conn

	n.mu.Lock()
	for _, k := range n.acct.Overdue(now) {
		if e := n.byKey[k]; e != nil {
			closing = append(closing, e.conn)
		}
	}
	n.acct.Sweep(now)
	if !n.heldExpiry.IsZero() && !now.Before(n.heldExpiry) {
		n.refreshHeldLocked()
	}
	for node, until := range n.unreachable {
		if !now.Before(until) {
			delete(n.unreachable, node)
		}
	}
	n.mu.Unlock()

	n.tickMembers(now)

	n.limiter.prune(func(p peer.ID) bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.hasConnLocked(p)
	})
	for _, c := range closing {
		n.log.Debug("closing an overdue connection", "peer", c.RemotePeer())
		c.Close()
	}
}
