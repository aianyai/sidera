package network

import (
	"context"
	"errors"
	"fmt"

	lnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	msmux "github.com/multiformats/go-multistream"

	"github.com/aianyai/sidera/internal/nodeproto"
	"github.com/aianyai/sidera/internal/preauth"
)

// The gate is the one way a stream handler is registered and the one way a
// member protocol is opened, so the rule of who may use which protocol has one
// home, preauth.Allowed. The library's own protocols (identify, ping) are
// registered by the host itself and stay open in every state, as Allowed says.

// errNotAllowed is why this side did not open a stream: the connection is not
// in a state that may use the protocol.
var errNotAllowed = errors.New("the connection is not in a state that may use the protocol")

// streamHandler serves one stream that the gate let through. The context ends
// with the network.
type streamHandler func(ctx context.Context, s lnet.Stream, e *connEntry)

// registerHandlers registers every protocol this package serves, through the gate.
func (n *Network) registerHandlers() {
	n.handle(string(preauth.ProtocolCertExchange), n.serveExchange)
	n.handleCall(nodeproto.ProtocolSend, runSend)
	n.handleCall(nodeproto.ProtocolPull, runPull)
	n.handleCall(nodeproto.ProtocolDirectory, runDirectory)
	n.handle(string(preauth.ProtocolPairing), n.servePairing)
}

// handle registers a handler for a protocol behind the gate: a stream whose
// connection is not in a state that may use the protocol is reset, after
// negotiation, because the library offers no earlier point. The reset carries the
// code the peer reads as "the call was not seen".
//
// It is the only place a stream handler is registered on the host.
func (n *Network) handle(proto string, h streamHandler) {
	n.lib.host.SetStreamHandler(protocol.ID(proto), func(s lnet.Stream) { n.serve(proto, s, h) })
}

func (n *Network) serve(proto string, s lnet.Stream, h streamHandler) {
	e := n.entryOf(s.Conn())
	access := preauth.Allowed(n.stateOf(e), preauth.Use{Protocol: preauth.ProtocolID(proto)})
	if e == nil || access.Verdict != preauth.VerdictAllow {
		n.log.Debug("stream refused", "protocol", proto, "peer", s.Conn().RemotePeer(), "reason", access.Reason.String())
		s.ResetWithError(lnet.StreamGated)
		return
	}
	if !n.begin() {
		s.Reset()
		return
	}
	defer n.wg.Done()
	if !n.acquire(e) {
		s.Reset()
		return
	}
	defer n.release(e)
	h(n.ctx, s, e)
}

// openStream opens a stream on one connection, the one this side judged to may
// use the protocol; the host's own stream function would pick any connection to
// the peer. The protocols are proposed in order and the first the peer serves is
// the stream's (lnet.Stream.Protocol). A protocol the state does not allow is not
// opened.
func (n *Network) openStream(ctx context.Context, e *connEntry, protos ...string) (lnet.Stream, error) {
	state := n.stateOf(e)
	for _, proto := range protos {
		if access := preauth.Allowed(state, preauth.Use{Protocol: preauth.ProtocolID(proto)}); access.Verdict != preauth.VerdictAllow {
			return nil, fmt.Errorf("%w (%s)", errNotAllowed, access.Reason)
		}
	}
	return openRaw(ctx, e.conn, protos...)
}

// openRaw opens a stream on the connection and negotiates the first of the
// protocols the peer serves, under the context. Nothing of the request has been
// written when it returns.
func openRaw(ctx context.Context, c lnet.Conn, protos ...string) (lnet.Stream, error) {
	s, err := c.NewStream(ctx)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { s.Reset() })
	ids := make([]protocol.ID, len(protos))
	for i, p := range protos {
		ids[i] = protocol.ID(p)
	}
	selected, err := msmux.SelectOneOf(ids, s)
	if !stop() {
		err = errors.Join(ctx.Err(), err)
	}
	if err == nil {
		err = s.SetProtocol(selected)
	}
	if err != nil {
		s.Reset()
		return nil, err
	}
	return s, nil
}
