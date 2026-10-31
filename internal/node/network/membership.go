package network

import (
	"errors"
	"fmt"
	"maps"

	lnet "github.com/libp2p/go-libp2p/core/network"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/preauth"
	"github.com/aianyai/sidera/internal/record"
)

// Membership is the snapshot of what the network layer needs to judge a peer:
// the node's own certificate to present, the facts a decision reads, and the
// certificates the node holds, whose nodes it can dial and whose members the
// accounting lets through before they have exchanged anything on this run.
//
// The engine owns every one of these facts and pushes a new snapshot whenever
// one changes; the network layer keeps the snapshot as given, so the caller
// does not change it afterwards.
type Membership struct {
	// Own is the node's own certificate in force, and OwnWire its encoded
	// record, which is what the exchange sends.
	Own     record.Verified[record.NetworkKeyCertificate]
	OwnWire []byte
	// Facts are the facts of a decision: revocations, the trust view, the
	// serials seen of the nodes the engine stores and the authorized persons.
	// Now is not read; every decision takes the time of the injected clock.
	Facts preauth.Facts
	// Held are the certificates the node holds for other nodes. They give the
	// peer to dial for a node and, through preauth.HeldMembers, the members
	// that are exempt from the unauthenticated cap.
	Held []record.Verified[record.NetworkKeyCertificate]
}

// membership is the validated snapshot.
type membership struct {
	Membership
	node     ident.NodeID
	serial   uint64
	exchange []byte // the encoded message that carries the node's own certificate
}

// SetMembership replaces the snapshot. It sets the members the accounting lets
// through, decides every live connection again with the new facts and closes
// those that no longer keep (a refused one, or a member that is now a
// non-member) or that are a node's non-member connection while the node is now a
// member, and, when the node's own certificate has a new serial, closes
// every connection as soon as it is idle so that the next call exchanges the
// new certificate: an exchange on an authenticated connection is ignored, so a
// renewed certificate reaches a peer only on a new connection.
func (n *Network) SetMembership(m Membership) error {
	snap, err := n.checkMembership(m)
	if err != nil {
		return err
	}

	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return errors.New("network: closed")
	}
	renewed := n.membership != nil && n.membership.serial != snap.serial
	n.membership = snap
	n.refreshHeldLocked()
	n.rebuildDialLocked()
	facts, _ := n.factsLocked()
	var idle []lnet.Conn
	var rejudge []*connEntry
	for _, e := range n.conns {
		if renewed {
			e.closeIdle = true
			if e.busy == 0 {
				idle = append(idle, e.conn)
			}
		}
		if e.decided && e.exchange != nil && e.state != preauth.StateUnauthenticated {
			rejudge = append(rejudge, e)
		}
	}
	n.mu.Unlock()

	for _, c := range idle {
		c.Close()
	}
	n.rejudge(facts, rejudge)
	return nil
}

func (n *Network) checkMembership(m Membership) (*membership, error) {
	if m.Own.Digest() == (record.Digest{}) {
		return nil, errors.New("network: membership without the node's own certificate")
	}
	own := m.Own.Payload()
	selfKey, err := publicKeyOf(n.peer)
	if err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}
	if own.NetworkKey != selfKey {
		return nil, errors.New("network: the node's own certificate names another network key than the one the node authenticates with")
	}
	rec, err := record.Decode(m.OwnWire, record.DefaultMaxInput)
	if err != nil {
		return nil, fmt.Errorf("network: the node's own certificate record: %w", err)
	}
	if rec.Digest() != m.Own.Digest() {
		return nil, errors.New("network: the node's own certificate record is not the certificate given")
	}
	ex, err := preauth.NewNodeExchange(rec).Encode()
	if err != nil {
		return nil, fmt.Errorf("network: the node's own certificate message: %w", err)
	}
	if m.Facts.Revocations == nil || m.Facts.SerialSeen == nil {
		return nil, errors.New("network: membership facts without the revocation set or the serials seen")
	}
	return &membership{Membership: m, node: own.Node, serial: own.Serial, exchange: ex}, nil
}

// factsLocked returns the facts of a decision made now: the snapshot's, with the
// time of the injected clock and the serials of the nodes the engine stores
// together with those the network remembers for strangers. It reports false
// before the first snapshot.
func (n *Network) factsLocked() (preauth.Facts, bool) {
	if n.membership == nil {
		return preauth.Facts{}, false
	}
	f := n.membership.Facts
	f.Now = n.clock.Now()
	seen := make(map[ident.NodeID]record.SerialSeen, len(f.SerialSeen)+len(n.strangers))
	maps.Copy(seen, f.SerialSeen)
	for node, s := range n.strangers {
		if cur, ok := seen[node]; !ok || s.Serial > cur.Serial {
			seen[node] = s
		}
	}
	f.SerialSeen = seen
	return f, true
}

// noteSerialLocked remembers, in memory only, the highest serial a decision saw
// for a node, so that an older certificate of a node that is not stored does not
// come back. Storing it on disk for every stranger would let anyone grow the
// disk; the price is that a restart forgets it, and such a node can only pair.
// The table is bounded: past maxStrangerSerials a new node is not remembered.
func (n *Network) noteSerialLocked(s record.SerialSeen) {
	if s.Node.IsZero() {
		return
	}
	cur, ok := n.strangers[s.Node]
	switch {
	case ok && s.Serial <= cur.Serial:
	case !ok && len(n.strangers) >= maxStrangerSerials:
	default:
		n.strangers[s.Node] = s
	}
}

// refreshHeldLocked gives the accounting the members of the held certificates as
// of now, takes away the standing of those that stopped being members, and
// remembers when the answer changes with time alone.
func (n *Network) refreshHeldLocked() {
	f, ok := n.factsLocked()
	if !ok {
		return
	}
	held := n.membership.Held
	keys := preauth.HeldMembers(f, held)
	now := make(map[ident.PublicKey]struct{}, len(keys))
	for _, k := range keys {
		now[k] = struct{}{}
	}
	for k := range n.heldKeys {
		if _, still := now[k]; !still {
			n.acct.Forget(k)
		}
	}
	n.acct.SetHeld(keys)
	n.heldKeys = now
	n.heldExpiry = preauth.NextHeldExpiry(f, held)
}

// rejudge decides the exchanges of live connections again with the facts and
// closes the connections that no longer keep. A connection to a node that is now
// a member but was judged a non-member is closed as soon as it is idle, and the
// node is no longer remembered as unreachable: the connection would otherwise
// stay a non-member's for as long as it lives, and a dial returns it, so every
// call would fail as not a member until it ended. The next call runs the
// exchange again, which also hands the node's certificate to the engine.
func (n *Network) rejudge(f preauth.Facts, entries []*connEntry) {
	type verdict struct {
		e     *connEntry
		state preauth.State
		d     preauth.Decision
	}
	var results []verdict
	var promoted []*connEntry
	for _, e := range entries {
		n.mu.Lock()
		ex, state := e.exchange, e.state
		n.mu.Unlock()
		if ex == nil {
			continue
		}
		d := preauth.Decide(f, e.peerKey, *ex)
		switch {
		case !d.Keeps(state):
			results = append(results, verdict{e, state, d})
		case state == preauth.StateNonMemberNode && d.State == preauth.StateMember:
			promoted = append(promoted, e)
		}
	}
	for _, r := range results {
		n.mu.Lock()
		n.noteSerialLocked(r.d.SerialSeen)
		n.acct.Forget(r.e.peerKey)
		n.mu.Unlock()
		n.log.Info("closing a connection that no longer keeps its state",
			"peer", r.e.conn.RemotePeer(), "was", r.state.String(), "now", r.d.State.String(), "reason", r.d.Reason.String())
		r.e.conn.Close()
	}
	for _, e := range promoted {
		n.mu.Lock()
		delete(n.unreachable, e.node)
		e.closeIdle = true
		closeNow := e.busy == 0
		n.mu.Unlock()
		n.log.Info("closing a non-member connection to a node that is now a member", "peer", e.conn.RemotePeer())
		if closeNow {
			e.conn.Close()
		}
	}
}

// ownNode is the node whose certificate the network presents; zero before the
// first snapshot.
func (n *Network) ownNode() ident.NodeID {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.membership == nil {
		return ident.NodeID{}
	}
	return n.membership.node
}
