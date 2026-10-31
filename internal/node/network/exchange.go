package network

import (
	"context"
	"errors"
	"fmt"

	lnet "github.com/libp2p/go-libp2p/core/network"

	"github.com/aianyai/sidera/internal/nodeproto"
	"github.com/aianyai/sidera/internal/preauth"
	"github.com/aianyai/sidera/internal/record"
)

// The certificate exchange turns a connection that only the handshake has
// authenticated into a member's, a non-member's or a refused one. The side whose
// connection is outbound opens a stream on that very connection, sends its
// certificate message and closes its write side; the other side reads it, decides,
// and answers with its own. Each side decides on the other's message with
// preauth.Decide and the facts of the current snapshot.

// applyResult says what applying a decision did.
type applyResult uint8

const (
	// applied: the connection is in the state the decision gave.
	applied applyResult = iota + 1
	// ignored: an exchange was already applied on the connection, or the
	// connection is gone; nothing changed.
	ignored
	// rejected: the connection was refused and is being closed.
	rejected
)

func (n *Network) ownExchange() []byte {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.membership == nil {
		return nil
	}
	return n.membership.exchange
}

// serveExchange is the handler of the exchange protocol on the side that read
// the peer's message first: it decides on it and answers with the node's own.
func (n *Network) serveExchange(ctx context.Context, s lnet.Stream, e *connEntry) {
	ctx, cancel := context.WithTimeout(ctx, preauth.ExchangeDeadline)
	defer cancel()
	// A connection the accounting tracks is closed at its deadline, which ends
	// the read; the timeout also bounds a stream on a connection that is not
	// tracked.
	stop := context.AfterFunc(ctx, func() { s.Reset() })
	defer stop()

	if n.isDecided(e) {
		s.Reset()
		return
	}
	data, err := nodeproto.ReadMessage(s, preauth.MaxMessageSize)
	if err != nil {
		s.Reset()
		return
	}
	ex, err := preauth.DecodeExchange(data)
	if err != nil {
		n.log.Debug("closing a connection that sent an invalid exchange", "peer", e.conn.RemotePeer(), "err", err)
		s.Reset()
		n.refuseUndecided(e)
		return
	}
	own := n.ownExchange()
	if own == nil {
		s.Reset() // the node has no certificate to present yet
		return
	}
	if n.decideAndApply(ctx, e, ex) != applied {
		s.Reset()
		return
	}
	if _, err := s.Write(own); err != nil {
		s.Reset()
		return
	}
	s.Close()
}

// exchangeOutbound starts the exchange on a connection this side dialed. A
// connection that fails it is closed: it would stay unauthenticated.
func (n *Network) exchangeOutbound(e *connEntry) {
	ctx, cancel := context.WithTimeout(n.ctx, n.cfg.ConnectTimeout)
	defer cancel()
	if err := n.runOutbound(ctx, e); err != nil {
		n.log.Debug("the certificate exchange failed", "peer", e.conn.RemotePeer(), "err", err)
		n.refuseUndecided(e)
	}
}

func (n *Network) runOutbound(ctx context.Context, e *connEntry) error {
	own := n.ownExchange()
	if own == nil {
		return errors.New("the node has no certificate to present yet")
	}
	if !n.acquire(e) {
		return errors.New("the connection is gone")
	}
	defer n.release(e)

	s, err := openRaw(ctx, e.conn, string(preauth.ProtocolCertExchange))
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { s.Reset() })
	defer stop()
	if _, err := s.Write(own); err != nil {
		s.Reset()
		return err
	}
	if err := s.CloseWrite(); err != nil {
		s.Reset()
		return err
	}
	data, err := nodeproto.ReadMessage(s, preauth.MaxMessageSize)
	if err != nil {
		s.Reset()
		return err
	}
	ex, err := preauth.DecodeExchange(data)
	if err != nil {
		s.Reset()
		return fmt.Errorf("the peer's exchange message: %w", err)
	}
	res := n.decideAndApply(ctx, e, ex)
	s.Close()
	if res == rejected {
		return errors.New("the peer's certificate was refused")
	}
	return nil
}

func (n *Network) isDecided(e *connEntry) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return e.decided
}

// refuseUndecided closes a connection that has not been decided: an exchange
// that failed or never came leaves it unauthenticated for good.
func (n *Network) refuseUndecided(e *connEntry) {
	n.mu.Lock()
	decided := e.decided
	e.signalReadyLocked()
	n.mu.Unlock()
	if !decided {
		e.conn.Close()
	}
}

// decideAndApply decides on the peer's message with the current facts and
// applies the decision to the connection.
func (n *Network) decideAndApply(ctx context.Context, e *connEntry, ex preauth.Exchange) applyResult {
	n.mu.Lock()
	facts, ok := n.factsLocked()
	decided := e.decided
	n.mu.Unlock()
	if decided {
		return ignored
	}
	if !ok {
		n.log.Debug("no membership snapshot yet; the exchange is refused", "peer", e.conn.RemotePeer())
		return n.settleRefusal(e, preauth.Decision{State: preauth.StateUnauthenticated})
	}
	return n.apply(ctx, e, ex, preauth.Decide(facts, e.peerKey, ex))
}

// apply puts a connection in the state its exchange decided. A member's
// certificate is handed to the engine first, and the connection is a member's
// only once the engine has taken it.
func (n *Network) apply(ctx context.Context, e *connEntry, ex preauth.Exchange, d preauth.Decision) applyResult {
	refused := preauth.Decision{State: preauth.StateUnauthenticated, Reason: d.Reason}
	if ex.Kind == preauth.KindNode && d.Completed() && d.Node == n.ownNode() {
		// A node that presents this node's identity is a second instance of it,
		// or a copy of its data directory: what it signs, this node would take
		// for its own.
		n.log.Warn("another instance uses this node's identity", "peer", e.conn.RemotePeer())
		return n.settleRefusal(e, refused)
	}
	if d.State == preauth.StateMember && ex.Kind == preauth.KindNode {
		node := n.currentNode()
		if node == nil {
			n.log.Warn("a member's certificate arrived while no node is set", "peer", e.conn.RemotePeer())
			return n.settleRefusal(e, refused)
		}
		seen := d.SerialSeen
		if seen.Node.IsZero() {
			seen = record.SerialSeenOf(d.NodeCertificate)
		}
		if err := node.NotePeerCertificate(ctx, d.NodeCertificate, seen); err != nil {
			n.log.Warn("the engine did not take a member's certificate", "peer", e.conn.RemotePeer(), "err", err)
			return n.settleRefusal(e, refused)
		}
	}
	if !d.Completed() {
		n.log.Debug("certificate exchange refused", "peer", e.conn.RemotePeer(), "reason", d.Reason.String())
		return n.settleRefusal(e, d)
	}

	now := n.clock.Now()
	n.mu.Lock()
	n.noteSerialLocked(d.SerialSeen)
	if n.conns[e.conn] != e || e.decided {
		n.mu.Unlock()
		return ignored
	}
	e.decided = true
	place := n.acct.Completed(e.key, d.State, now)
	if place.Verdict != preauth.VerdictAllow {
		if place.Reason == preauth.RefuseNonMemberFull {
			// The accounting dropped the entry, so the connection's end must not
			// report it.
			n.untrackLocked(e)
		}
		e.signalReadyLocked()
		n.mu.Unlock()
		n.log.Debug("connection refused after the exchange", "peer", e.conn.RemotePeer(), "reason", place.Reason.String())
		e.conn.Close()
		return rejected
	}
	if d.State.HasStanding() {
		n.untrackLocked(e) // released from the accounting
	}
	e.state, e.node = d.State, d.Node
	e.exchange = &ex
	e.signalReadyLocked()
	n.mu.Unlock()
	if d.State.HasStanding() {
		n.limiter.setMember(e.conn.RemotePeer(), true)
	}
	n.log.Debug("certificate exchange completed", "peer", e.conn.RemotePeer(), "state", d.State.String())
	return applied
}

// untrackLocked ends the accounting's hold on the connection: its end is not
// reported any more.
func (n *Network) untrackLocked(e *connEntry) {
	e.tracked = false
	if n.byKey[e.key] == e {
		delete(n.byKey, e.key)
	}
}

// settleRefusal records that the exchange on a connection was refused and closes
// the connection. The accounting forgets the standing of the peer, and the end
// of the connection releases its entry.
func (n *Network) settleRefusal(e *connEntry, d preauth.Decision) applyResult {
	n.mu.Lock()
	if n.conns[e.conn] != e || e.decided {
		n.mu.Unlock()
		return ignored
	}
	e.decided = true
	n.noteSerialLocked(d.SerialSeen)
	n.acct.Completed(e.key, preauth.StateUnauthenticated, n.clock.Now())
	e.signalReadyLocked()
	n.mu.Unlock()
	e.conn.Close()
	return rejected
}
