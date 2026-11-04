package network

import (
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/preauth"
)

// A node cannot tell from the inside whether a member can dial it: that depends
// on the member's address book and on every firewall and NAT between them. So it
// asks (dialback.go) and remembers the answer for each member: whether that
// member, if it had to find this node, could. Only the members that could not
// need a connection kept to them (see keep.go); for the others a connection is
// made when something needs one.
//
// The answers are kept in memory only: after a restart they may be out of date,
// and the first round of asking, on the connections the keep dials make, gets
// them again at once.

const (
	// reachabilityTTL is how long an answer stands. A firewall rule can change
	// with nothing on this node noticing, so an answer is asked for again.
	reachabilityTTL = time.Hour
	// retryBackoffFirst and retryBackoffCap bound the wait between attempts that
	// fail: a question that got no answer, and a dial that kept a connection to
	// a member.
	retryBackoffFirst = 5 * time.Second
	retryBackoffCap   = 60 * time.Second
)

// retryBackoff is how long to wait after the given number of failures in a row:
// 5 seconds, doubled for each failure after the first, up to 60 seconds. The
// schedule is fixed and has no jitter, because a question goes over a connection
// that is up and cannot collide with anything. A dial that keeps a connection
// can: two nodes that dial each other at the same moment over TCP both fail, so
// each such dial waits a random time first (see keepDialWait).
func retryBackoff(failures int) time.Duration {
	d := retryBackoffFirst
	for i := 1; i < failures && d < retryBackoffCap; i++ {
		d *= 2
	}
	return min(d, retryBackoffCap)
}

// reachState is what is known of whether a member can dial this node.
type reachState uint8

const (
	// reachUnknown: no answer yet, or it has expired, or the member cannot be
	// asked. A member in this state is treated as one that cannot dial.
	reachUnknown reachState = iota
	// reachReachable: the member dialed this node on an address of its own book.
	reachReachable
	// reachUnreachable: the member holds no address for this node, or none of
	// its addresses connected.
	reachUnreachable
)

// reachRecord is what this node knows of one member. All of it is guarded by
// Network.mu.
type reachRecord struct {
	// state and address are the last answer, and decided is when it was given;
	// stateAt reads them with the expiry applied.
	state   reachState
	address ma.Multiaddr // the address that connected, while reachable
	decided time.Time

	// asking is set while a question is in flight, so there is one at a time.
	asking bool
	// failures counts the questions that got no answer since the last one that
	// did, and nextAsk is when the next may be put.
	failures int
	nextAsk  time.Time

	// cannotAsk is set when the member does not serve the question, as a node of
	// an earlier release does not. Asking again on the same connection would get
	// the same answer, so it is asked again only on a connection newer than
	// askedOn (a member that was upgraded comes back on a new one).
	cannotAsk bool
	askedOn   uint64
}

// stateAt is the record's state at a time: an answer older than reachabilityTTL
// is no longer one.
func (r *reachRecord) stateAt(now time.Time) reachState {
	if r.state != reachUnknown && !now.Before(r.decided.Add(reachabilityTTL)) {
		return reachUnknown
	}
	return r.state
}

// reachabilityLocked is what is known of the member at a time.
func (n *Network) reachabilityLocked(node ident.NodeID, now time.Time) reachState {
	if r := n.reach[node]; r != nil {
		return r.stateAt(now)
	}
	return reachUnknown
}

// askMembers puts the question to every member that is connected and whose
// reachability is not known. The question goes on the connection the member
// already has, whoever dialed it, and runs in a goroutine of the package, not on
// the engine's outbound worker. Nothing is asked before the node listens: until
// then the answer would be "no" whatever the network does.
func (n *Network) askMembers(now time.Time, listening bool) {
	if !listening {
		return
	}
	type question struct {
		node ident.NodeID
		on   uint64
	}
	var asks []question

	n.mu.Lock()
	newest := map[ident.NodeID]uint64{}
	for _, e := range n.conns {
		if e.state == preauth.StateMember && !e.node.IsZero() {
			newest[e.node] = max(newest[e.node], e.seq)
		}
	}
	for node, seq := range newest {
		r := n.reach[node]
		if r == nil {
			r = &reachRecord{}
			n.reach[node] = r
		}
		switch {
		case r.asking, now.Before(r.nextAsk), r.stateAt(now) != reachUnknown:
		case r.cannotAsk && seq <= r.askedOn:
		default:
			r.asking = true
			asks = append(asks, question{node, seq})
		}
	}
	n.mu.Unlock()

	for _, q := range asks {
		n.start(func() { n.askMember(q.node, q.on) })
	}
}

// askMember asks one member and records what it said. One answer decides:
// the question is whether this member can dial this node, so there is nothing to
// vote on. "Reached" can only come from a real dial from a new port that
// completed the handshake, while a lost packet can only make a member look like
// one that cannot dial, which costs one connection kept that was not needed.
func (n *Network) askMember(node ident.NodeID, on uint64) {
	reply, err := n.askDialBack(n.ctx, node)
	if n.ctx.Err() != nil {
		return
	}
	now := n.clock.Now()

	n.mu.Lock()
	r := n.reach[node]
	r.asking = false
	if err != nil {
		r.failures++
		r.nextAsk = now.Add(retryBackoff(r.failures))
		failures, next := r.failures, r.nextAsk
		n.mu.Unlock()
		n.log.Debug("a member could not be asked whether it can dial this node", "node", node, "err", err, "failed", failures, "next", next)
		return
	}
	// The last answer, expiry not applied: an answer that is asked again and
	// comes out the same is not news, whatever its age.
	before, hadCannotAsk := r.state, r.cannotAsk
	r.failures, r.nextAsk = 0, time.Time{}
	r.cannotAsk, r.askedOn = false, 0
	switch reply.Outcome {
	case dialBackReached:
		r.state, r.address, r.decided = reachReachable, reply.Address, now
	case dialBackNotReached, dialBackNoAddress:
		r.state, r.address, r.decided = reachUnreachable, nil, now
	default: // dialBackCannotAsk
		r.state, r.address = reachUnknown, nil
		r.cannotAsk, r.askedOn = true, on
	}
	haveAddress := len(n.dialPeers[node]) > 0 && n.hasBookAddrLocked(node)
	n.mu.Unlock()

	switch reply.Outcome {
	case dialBackReached:
		if before != reachReachable {
			n.log.Info("a member can dial this node, so a connection to it is made when needed", "node", node, "address", reply.Address)
		}
	case dialBackNotReached, dialBackNoAddress:
		if before != reachUnreachable {
			why := "the member holds addresses for this node and none connected"
			if reply.Outcome == dialBackNoAddress {
				why = "the member holds no address for this node"
			}
			n.log.Info("a member cannot dial this node, so a connection to it is kept", "node", node, "why", why)
			if !haveAddress {
				n.log.Info("there is no way between this node and a member: it cannot dial this node and this node holds no address for it, until an address or a firewall changes", "node", node)
			}
		}
	default:
		if !hadCannotAsk {
			n.log.Info("a member does not answer whether it can dial this node (an earlier release), so a connection to it is kept", "node", node)
		}
	}
}

// hasBookAddrLocked reports whether the address book gives an address to dial
// the node on.
func (n *Network) hasBookAddrLocked(node ident.NodeID) bool {
	for _, pid := range n.dialPeers[node] {
		if len(n.bookWritten[pid]) > 0 {
			return true
		}
	}
	return false
}

// localAddressesChanged forgets every answer and every wait: the answers were
// about the addresses this node had, and a change of network can change what a
// member's address book and firewalls make of them. It is the same as a start,
// and the members are asked and dialed again at the next tick.
func (n *Network) localAddressesChanged() {
	n.mu.Lock()
	for _, r := range n.reach {
		r.state, r.address = reachUnknown, nil
		r.failures, r.nextAsk = 0, time.Time{}
	}
	for _, k := range n.keeps {
		k.failures, k.next = 0, time.Time{}
	}
	n.mu.Unlock()
	n.log.Info("the addresses of this node changed, so whether the members can dial it is asked again")
}

// watchLocalAddresses calls localAddressesChanged for each event of the host
// that adds or removes a listen address, until the network closes.
func (n *Network) watchLocalAddresses(sub event.Subscription) {
	defer sub.Close()
	for {
		select {
		case <-n.ctx.Done():
			return
		case ev, ok := <-sub.Out():
			if !ok {
				return
			}
			if u, ok := ev.(event.EvtLocalAddressesUpdated); ok && addressesChanged(u) {
				n.localAddressesChanged()
			}
		}
	}
}

// addressesChanged reports whether the event adds or removes an address; an
// event that does not say what changed is taken as a change.
func addressesChanged(u event.EvtLocalAddressesUpdated) bool {
	if !u.Diffs || len(u.Removed) > 0 {
		return true
	}
	for _, a := range u.Current {
		if a.Action == event.Added {
			return true
		}
	}
	return false
}
