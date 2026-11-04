package network

import (
	"encoding/binary"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/aianyai/sidera/internal/ident"
)

// A node keeps a connection to each member that cannot be relied on to dial it:
// the members its reachability record (reach.go) does not say can. A node that
// blocks inbound connections can still dial out, so when it restarts, or the
// member does, the connection comes back at once, without waiting for something
// to be sent. A member that can dial this node needs no connection kept: when
// the node has to be found, the member finds it.
//
// At the start every record is unknown, so every member is in the set and is
// dialed once; the ones that answer that they can dial this node leave it.
// There is no separate start-up round.
//
// The dials are made on the network's tick, go through the same path as a call's
// (dialWithin: the connect budget, the certificate exchange, the library's own
// backoff bypassed), and neither read nor write the memo of unreachable nodes,
// which exists to spare the engine's one outbound worker. They are not made on
// that worker, and wake nothing in the engine: a member that has something for
// this node sends it on the engine's own schedule over the connection.

// maxKeepDials is how many dials that keep a connection are in flight at once,
// for all members: a network of many members that are all down is dialed in
// batches, not all at once. One member has at most one.
const maxKeepDials = 8

// keepDial is the state of the dials that keep a connection to one member.
// Guarded by Network.mu.
type keepDial struct {
	// failures counts the dials that failed in a row, since is the first of
	// them, and next is when the next may be made.
	failures int
	since    time.Time
	next     time.Time
	inFlight bool
}

// keepTarget is a member to keep a connection to, and the peers to dial.
type keepTarget struct {
	node ident.NodeID
	pids []peer.ID
}

// keepSetLocked lists the members to keep a connection to: those whose
// certificate is held and which are members by the current facts, that
// have an address to dial, and that cannot be relied on to dial this node.
func (n *Network) keepSetLocked(now time.Time) []keepTarget {
	if n.membership == nil {
		return nil
	}
	var set []keepTarget
	for _, c := range n.membership.Held {
		cert := c.Payload()
		if _, member := n.heldKeys[cert.NetworkKey]; !member {
			continue
		}
		if n.reachabilityLocked(cert.Node, now) == reachReachable || !n.hasBookAddrLocked(cert.Node) {
			continue
		}
		set = append(set, keepTarget{cert.Node, n.dialPeers[cert.Node]})
	}
	return set
}

// tickMembers is the part of the tick that is about the members: it asks the
// ones connected whether they can dial this node, and dials the ones in the
// keep set that are not connected.
func (n *Network) tickMembers(now time.Time) {
	n.askMembers(now, len(n.lib.host.Network().ListenAddresses()) > 0)
	n.keepMembers(now)
}

func (n *Network) keepMembers(now time.Time) {
	type recovery struct {
		node   ident.NodeID
		failed int
		since  time.Time
	}
	var dials []keepTarget
	var recovered []recovery

	n.mu.Lock()
	inSet := map[ident.NodeID]struct{}{}
	for _, t := range n.keepSetLocked(now) {
		inSet[t.node] = struct{}{}
		k := n.keeps[t.node]
		if k == nil {
			k = &keepDial{}
			n.keeps[t.node] = k
		}
		if n.nodeConnLocked(t.node, memberOnly) != nil {
			// However the connection came about, the next one that is lost is
			// dialed again at once, from a clean slate.
			if k.failures > 0 {
				recovered = append(recovered, recovery{t.node, k.failures, k.since})
			}
			k.failures, k.next = 0, time.Time{}
			continue
		}
		if k.inFlight || now.Before(k.next) || n.keepsActive >= maxKeepDials {
			continue
		}
		k.inFlight = true
		n.keepsActive++
		dials = append(dials, t)
	}
	for node, k := range n.keeps {
		if _, in := inSet[node]; !in && !k.inFlight {
			delete(n.keeps, node)
		}
	}
	n.mu.Unlock()

	for _, r := range recovered {
		n.log.Info("a connection kept to a member is up again", "node", r.node, "failed", r.failed, "for", now.Sub(r.since))
	}
	for _, t := range dials {
		n.start(func() { n.keepDial(t) })
	}
}

// keepDial makes one dial of a member in the keep set, after a random wait, and
// sets when the next may be made if it failed. The first failure of a run is
// logged at info with the error of each address; the ones after it at debug.
func (n *Network) keepDial(t keepTarget) {
	err := n.keepDialWait()
	if err == nil {
		_, err = n.dialWithin(n.ctx, t.node, t.pids, memberOnly)
	}
	now := n.clock.Now()

	n.mu.Lock()
	n.keepsActive--
	k := n.keeps[t.node]
	k.inFlight = false
	if err != nil && n.ctx.Err() == nil {
		if k.failures == 0 {
			k.since = now
		}
		k.failures++
		k.next = now.Add(retryBackoff(k.failures))
	}
	failures, next := k.failures, k.next
	n.mu.Unlock()

	switch {
	case n.ctx.Err() != nil:
	case err == nil:
		n.log.Debug("dialed a member to keep a connection to it", "node", t.node)
	case failures == 1:
		n.log.Info("a member could not be dialed to keep a connection to it", "node", t.node, "err", err, "next", next)
	default:
		n.log.Debug("a member could not be dialed to keep a connection to it", "node", t.node, "err", err, "failed", failures, "next", next)
	}
}

// keepDialWait waits a random time of up to a tick, on the clock, before a dial
// that keeps a connection. Two members that cannot dial each other's listeners
// try each other on ticks that fall at the same moment (a retry is a fixed time
// after the last failure, and a test's clock wakes every node at once), and two
// TCP dials that cross at the same moment both fail: the connection each side
// opens is taken by the other as its own. Spreading the dials over a tick makes
// a crossing rare and not repeating. It returns the context's error if the
// network closes first.
func (n *Network) keepDialWait() error {
	var b [8]byte
	var wait time.Duration
	if _, err := io.ReadFull(n.cfg.Rand, b[:]); err != nil {
		n.log.Warn("no randomness for the wait before a dial that keeps a connection, so it is made at once", "err", err)
	} else {
		wait = time.Duration(binary.BigEndian.Uint64(b[:]) % uint64(n.cfg.TickEvery))
	}
	timer := n.clock.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C():
		return nil
	case <-n.ctx.Done():
		return n.ctx.Err()
	}
}
