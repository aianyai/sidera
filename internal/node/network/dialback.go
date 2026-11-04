package network

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
	msmux "github.com/multiformats/go-multistream"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/nodeproto"
)

// Dial-back answers one question a member asks over a connection it has: "if you
// had to find me, could you?". The member dials the asker on the addresses it
// holds for it, on a connection that is new to every firewall on the way (see
// dialBackDialer), and says whether it got through. The request names no
// address, so the member can be made to dial nothing but its own address book's
// entries for the asker, which are also the addresses it would use to find the
// asker for real.

// maxDialBacks is how many dial-backs a node makes at once, for all askers. A
// member is trusted, so this bounds only what a member that misbehaves can take
// from the dialer; every dial-back is bounded by the connect budget besides, and
// reaches no address but the asker's own.
const maxDialBacks = 4

// dialBackSlots is the set of askers a node is dialing back for: one at a time
// for an asker, maxDialBacks in all.
type dialBackSlots struct {
	mu     sync.Mutex
	asking map[peer.ID]struct{}
}

// take claims a slot for the asker, and reports false when it already has one or
// none is left.
func (s *dialBackSlots) take(asker peer.ID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.asking[asker]; taken || len(s.asking) >= maxDialBacks {
		return false
	}
	if s.asking == nil {
		s.asking = map[peer.ID]struct{}{}
	}
	s.asking[asker] = struct{}{}
	return true
}

func (s *dialBackSlots) release(asker peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.asking, asker)
}

// runDialBack is the server of the dial-back protocol. The asker is the peer of
// the connection the request came over, which the secure handshake
// authenticated: dialing that peer identifier and no other is what makes a
// connection that succeeds proof that the asker's key is behind the address.
func (n *Network) runDialBack(ctx context.Context, _ Node, e *connEntry, _ transport.Origin, raw []byte) ([]byte, error) {
	if _, err := nodeproto.DecodeDialBackRequest(raw); err != nil {
		return nil, errInvalidRequest{err}
	}
	asker := e.conn.RemotePeer()
	if !n.dialBacks.take(asker) {
		return nodeproto.DialBackAnswer{Outcome: nodeproto.OutcomeRefused, Reason: nodeproto.ReasonDialBackBusy}.Encode()
	}
	defer n.dialBacks.release(asker)

	addrs := n.dialBackAddrs(asker)
	if len(addrs) == 0 {
		n.log.Debug("no address is held to dial a node back on", "peer", asker)
		return nodeproto.DialBackAnswer{Outcome: nodeproto.OutcomeOK, Result: nodeproto.DialBackNoAddress}.Encode()
	}
	dctx, cancel := context.WithTimeout(ctx, n.cfg.ConnectTimeout)
	defer cancel()
	conn, err := n.lib.dialBack.dial(dctx, asker, addrs)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		n.log.Debug("a node could not be dialed back", "peer", asker, "err", err)
		return nodeproto.DialBackAnswer{Outcome: nodeproto.OutcomeOK, Result: nodeproto.DialBackNotReached}.Encode()
	}
	n.log.Debug("a node was dialed back", "peer", asker, "address", conn.Remote, "from", conn.Local)
	return nodeproto.DialBackAnswer{Outcome: nodeproto.OutcomeOK, Result: nodeproto.DialBackReached, Address: conn.Remote}.Encode()
}

// dialBackAddrs are the addresses to dial a peer back on: the ones the address
// book wrote to the peerstore for it, which are the ones a call to its node
// would dial. What identify taught the peerstore is not among them, since it
// lapses with the connection it came from and a dial after a restart would not
// have it; nor is an address through a relay, because reaching a node through a
// relay says nothing about whether it can be reached directly.
func (n *Network) dialBackAddrs(p peer.ID) []ma.Multiaddr {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []ma.Multiaddr
	for _, a := range n.bookWritten[p] {
		if _, err := a.ValueForProtocol(ma.P_CIRCUIT); err == nil {
			continue
		}
		out = append(out, a)
	}
	return out
}

// dialBackOutcome is what asking a member to dial this node back came to.
type dialBackOutcome uint8

// The outcomes of asking. The zero value is not an outcome.
const (
	// dialBackReached: the member connected to this node on an address of its
	// own book, from a new connection.
	dialBackReached dialBackOutcome = iota + 1
	// dialBackNotReached: the member holds addresses for this node and none of
	// them connected.
	dialBackNotReached
	// dialBackNoAddress: the member holds no address for this node.
	dialBackNoAddress
	// dialBackCannotAsk: the member does not serve the protocol, as a node before
	// it does not. Nothing is known of whether it could dial this node back, and
	// asking again on the same connection would get the same answer.
	dialBackCannotAsk
)

// dialBackReply is the answer to a question put to a member.
type dialBackReply struct {
	Outcome dialBackOutcome
	// Address is the address of this node that connected; set exactly when the
	// outcome is dialBackReached.
	Address ma.Multiaddr
}

// askDialBack asks a member, on the member connection this node has to it,
// whether it can dial this node back. It dials nothing itself: the question is
// put on a connection that is up, and the error is transport.ErrUnreachable when
// there is none. A member that refuses (it is busy) gives transport.Refused, and
// one that does not answer in time or breaks the stream the errors of any call.
// A member that does not serve the protocol is an outcome, not an error.
func (n *Network) askDialBack(ctx context.Context, node ident.NodeID) (dialBackReply, error) {
	e := n.memberConn(node)
	if e == nil {
		return dialBackReply{}, fmt.Errorf("%w: no member connection to it", transport.ErrUnreachable)
	}
	req, err := nodeproto.DialBackRequest{}.Encode()
	if err != nil {
		return dialBackReply{}, fmt.Errorf("network: dial-back request: %w", err)
	}
	raw, err := n.callOn(ctx, e, node, nodeproto.ProtocolDialBack, req)
	var unsupported msmux.ErrNotSupported[protocol.ID]
	if errors.As(err, &unsupported) {
		return dialBackReply{Outcome: dialBackCannotAsk}, nil
	}
	if err != nil {
		return dialBackReply{}, err
	}
	ans, err := nodeproto.DecodeDialBackAnswer(raw)
	if err != nil {
		return dialBackReply{}, invalidAnswer(err)
	}
	if err := outcomeError(ans.Outcome, ans.Reason); err != nil {
		return dialBackReply{}, err
	}
	switch ans.Result {
	case nodeproto.DialBackReached:
		return dialBackReply{Outcome: dialBackReached, Address: ans.Address}, nil
	case nodeproto.DialBackNotReached:
		return dialBackReply{Outcome: dialBackNotReached}, nil
	default:
		return dialBackReply{Outcome: dialBackNoAddress}, nil
	}
}
