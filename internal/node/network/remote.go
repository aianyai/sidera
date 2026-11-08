package network

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"

	lnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	msmux "github.com/multiformats/go-multistream"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/nodeproto"
	"github.com/aianyai/sidera/internal/record"
)

// A call to another node is one stream: the request is one frame and the answer
// one frame. The client does not half-close after its request: it reads the
// answer and only then closes the stream, and the server keeps the stream, and
// with it the connection's busy count, until it sees that close. A node before
// 2.0.0 of the protocols uses the end of the stream as the end of the message,
// the request's half-close included, and releases the stream as soon as it has
// answered; both forms are served, and the client falls back to it.

var errFailed = errors.New("network: the target could not handle the call")

// gatedByPeer is what a stream reads when the peer's gate reset it: the peer
// refused to serve the stream before it read anything, so it did not see the call.
var gatedByPeer = &lnet.StreamError{ErrorCode: lnet.StreamGated, Remote: true}

// Send carries one envelope to the target node and returns the receipt. The
// errors are the transport vocabulary: ErrUnreachable when the target did not
// see the call, ErrTimeout when it may have, Refused when it refused with a
// reason, a plain error when it could not handle the call, and the caller's own
// context error as it is.
//
// needs are the features the envelope cannot do without. An envelope that needs
// none is proposed under every send identifier the node speaks, and the call takes
// the newest the target serves. One that needs a feature is proposed under the
// current identifier alone, the only one that carries it: a target that does not
// serve it is told so by the negotiation, before the request is written, and the
// call is ErrFeatureUnsupported, which is not an unreachable target and is not
// made again until the target is updated.
func (n *Network) Send(ctx context.Context, target ident.NodeID, sender ident.EndpointID, data []byte, needs transport.Features) (_ transport.Receipt, err error) {
	defer func() { n.noteCall(ctx, target, "send", err) }()
	req, err := nodeproto.SendRequest{Sender: sender, Envelope: data}.Encode()
	if err != nil {
		return transport.Receipt{}, fmt.Errorf("network: send request: %w", err)
	}
	r, err := newMemberRequest(nodeproto.ProtocolSend, req)
	if err != nil {
		return transport.Receipt{}, err
	}
	if needs != 0 {
		r = r.carryingOnlyTheCurrentIdentifier()
	}
	answer, err := n.callWith(ctx, target, r)
	if err != nil {
		return transport.Receipt{}, err
	}
	ans, err := nodeproto.DecodeSendAnswer(answer.Data)
	if err != nil {
		return transport.Receipt{}, invalidAnswer(err)
	}
	if err := outcomeError(ans.Outcome, ans.Reason); err != nil {
		return transport.Receipt{}, err
	}
	return transport.Receipt{Reports: ans.Reports, ControlReply: ans.ControlReply}, nil
}

// Pull asks the target node for the current reports of a message to the
// recipients of the request. The answer came over a connection the target's key
// authenticated, which is what lets its NoRecord stand.
func (n *Network) Pull(ctx context.Context, target ident.NodeID, req transport.PullRequest) (_ transport.PullAnswer, err error) {
	defer func() { n.noteCall(ctx, target, "pull", err) }()
	raw, err := nodeproto.PullRequest{Message: req.Message, Recipients: req.Recipients}.Encode()
	if err != nil {
		return transport.PullAnswer{}, fmt.Errorf("network: pull request: %w", err)
	}
	answer, err := n.call(ctx, target, nodeproto.ProtocolPull, raw)
	if err != nil {
		return transport.PullAnswer{}, err
	}
	ans, err := nodeproto.DecodePullAnswer(answer.Data)
	if err != nil {
		return transport.PullAnswer{}, invalidAnswer(err)
	}
	if err := outcomeError(ans.Outcome, ans.Reason); err != nil {
		return transport.PullAnswer{}, err
	}
	return transport.PullAnswer{Reports: ans.Reports, ControlReply: ans.ControlReply, NoRecord: ans.NoRecord}, nil
}

// QueryDirectory asks the target node for a page of the endpoints it shows the
// sender. An entry names no node on the wire: it is the node that was called.
func (n *Network) QueryDirectory(ctx context.Context, target ident.NodeID, sender ident.EndpointID, req transport.DirectoryRequest) (_ transport.DirectoryPage, err error) {
	defer func() { n.noteCall(ctx, target, "directory", err) }()
	raw, err := nodeproto.DirectoryRequest{Sender: sender, Cursor: req.Cursor, Limit: req.Limit}.Encode()
	if err != nil {
		return transport.DirectoryPage{}, fmt.Errorf("network: directory request: %w", err)
	}
	answer, err := n.call(ctx, target, nodeproto.ProtocolDirectory, raw)
	if err != nil {
		return transport.DirectoryPage{}, err
	}
	ans, err := nodeproto.DecodeDirectoryAnswerFor(answer.Protocol, answer.Data)
	if err != nil {
		return transport.DirectoryPage{}, invalidAnswer(err)
	}
	if err := outcomeError(ans.Outcome, ans.Reason); err != nil {
		return transport.DirectoryPage{}, err
	}
	page := transport.DirectoryPage{NextCursor: ans.NextCursor}
	for _, e := range ans.Entries {
		page.Endpoints = append(page.Endpoints, transport.DirectoryEntry{
			Address:     ident.Address{Node: target, Endpoint: e.Endpoint},
			Kind:        e.Kind,
			DisplayName: e.DisplayName,
			Description: e.Description,
			Accepts:     e.Actions,
			Adapter:     adapterFromProto(e.Adapter),
		})
	}
	return page, nil
}

func adapterFromProto(a *nodeproto.DirectoryAdapter) *transport.DirectoryAdapter {
	if a == nil {
		return nil
	}
	return &transport.DirectoryAdapter{Instance: a.Instance, ID: a.ID, Version: a.Version, Runtime: a.Runtime}
}

func adapterToProto(a *transport.DirectoryAdapter) *nodeproto.DirectoryAdapter {
	if a == nil {
		return nil
	}
	return &nodeproto.DirectoryAdapter{Instance: a.Instance, ID: a.ID, Version: a.Version, Runtime: a.Runtime}
}

// invalidAnswer is an answer that does not decode. The target may have handled
// the call, so it is read as a call without an answer.
func invalidAnswer(err error) error {
	return fmt.Errorf("%w: the answer is invalid: %v", transport.ErrTimeout, err)
}

// outcomeError turns a decoded answer's outcome into the error of the call.
func outcomeError(o nodeproto.Outcome, reason string) error {
	switch o {
	case nodeproto.OutcomeOK:
		return nil
	case nodeproto.OutcomeRefused:
		return transport.Refused{Reason: reason}
	default:
		return errFailed
	}
}

// memberAnswer is the answer to a member call and the identifier it was made
// under: the first of the identifiers proposed that the target serves. A protocol
// whose newer identifiers carry more reads the answer by it.
type memberAnswer struct {
	Protocol string
	Data     []byte
}

// call makes one request to the target node and returns its answer. The protocol
// is proposed under its current identifier first and its older ones after it,
// newest first, and the call takes the form of the first identifier the target
// serves. It maps what goes wrong to the transport's two kinds of "no answer":
//
//   - nothing the target could have seen was sent (no connection within the
//     budget, a connection the node judges not to be a member's, a protocol the
//     peer's gate or protocol negotiation refused): ErrUnreachable;
//   - the request began to go out and no complete answer came back in time, or
//     the stream broke: ErrTimeout;
//   - the caller's own context ended: its error, as it is.
func (n *Network) call(ctx context.Context, target ident.NodeID, proto string, req []byte) (memberAnswer, error) {
	r, err := newMemberRequest(proto, req)
	if err != nil {
		return memberAnswer{}, err
	}
	return n.callWith(ctx, target, r)
}

// callWith is call for a request already made.
func (n *Network) callWith(ctx context.Context, target ident.NodeID, r memberRequest) (memberAnswer, error) {
	e, err := n.connect(ctx, target)
	if err != nil {
		return memberAnswer{}, err
	}
	return n.callOn(ctx, e, target, r)
}

// memberRequest is a request in the forms a call needs, made before the call
// looks for a connection: a request that cannot be sent must not dial.
type memberRequest struct {
	proto  string
	plain  []byte // the message, for a 1.0.0 stream
	framed []byte // the message as a frame, for a 2.0.0 stream
	limit  nodeproto.Limit
	// offer is what the call proposes, newest first.
	offer []string
	// needsCurrent says that the request carries what only the current identifier
	// does, so a target that does not serve it cannot be called at all.
	needsCurrent bool
}

// carryingOnlyTheCurrentIdentifier is the request proposed under its current
// identifier alone.
func (r memberRequest) carryingOnlyTheCurrentIdentifier() memberRequest {
	r.offer, r.needsCurrent = r.offer[:1], true
	return r
}

// newMemberRequest checks the protocol and the size of the request.
func newMemberRequest(proto string, req []byte) (memberRequest, error) {
	limit, ok := nodeproto.LimitOf(proto)
	if !ok {
		return memberRequest{}, fmt.Errorf("network: %s is not a node protocol", proto)
	}
	framed, err := nodeproto.AppendFrame(nil, req, limit.Request)
	if err != nil {
		return memberRequest{}, fmt.Errorf("network: request: %w", err)
	}
	return memberRequest{proto: proto, plain: req, framed: framed, limit: limit, offer: nodeproto.Offer(proto)}, nil
}

// callOn is call on a connection already chosen: the one this side judged a
// member's connection to the target. A call that needs a connection that is up
// already, and does not dial one, is made with it.
func (n *Network) callOn(ctx context.Context, e *connEntry, target ident.NodeID, r memberRequest) (memberAnswer, error) {
	if !n.acquire(e) {
		return memberAnswer{}, fmt.Errorf("%w: the connection ended", transport.ErrUnreachable)
	}
	defer n.release(e)

	cctx, cancel := context.WithTimeout(ctx, n.cfg.CallTimeout)
	defer cancel()
	s, err := n.openStream(cctx, e, r.offer...)
	if err != nil {
		if ctx.Err() != nil {
			return memberAnswer{}, ctx.Err()
		}
		if connectionFault(cctx, err) {
			n.dropConn(e, target)
		}
		var unsupported msmux.ErrNotSupported[protocol.ID]
		if r.needsCurrent && errors.As(err, &unsupported) {
			// The target is there and answered the negotiation: it is not
			// unreachable, and no later call will find it different.
			return memberAnswer{}, fmt.Errorf("%w: %w", transport.ErrFeatureUnsupported, err)
		}
		// The cause stays in the chain: a caller may need to tell a peer that does
		// not serve the protocol (msmux.ErrNotSupported) from one that is gone.
		return memberAnswer{}, fmt.Errorf("%w: %w", transport.ErrUnreachable, err)
	}
	stop := context.AfterFunc(cctx, func() { s.Reset() })
	defer stop()

	// A call that got no answer may also say that the connection is dead: it is
	// closed then, so that the next call does not wait on it too.
	failed := func(err error) error {
		if ctx.Err() == nil && !errors.Is(err, gatedByPeer) && connectionFault(cctx, err) {
			n.dropConn(e, target)
		}
		return noAnswer(ctx, s, err)
	}
	// A failed write is not judged by itself: the stream is dead, and the read
	// that follows says why with the library's own error, which carries the
	// peer's reset code; a write or half-close error does not always.
	var answer []byte
	if nodeproto.IsLegacy(string(s.Protocol())) {
		if _, err := s.Write(r.plain); err == nil {
			s.CloseWrite()
		}
		answer, err = nodeproto.ReadMessage(s, r.limit.Answer)
	} else {
		// Not half-closed: the server keeps the stream, and with it its
		// connection, until it sees the close that follows the answer.
		_, _ = s.Write(r.framed)
		answer, err = nodeproto.ReadFrame(bufio.NewReader(s), r.limit.Answer)
	}
	if err != nil {
		return memberAnswer{}, failed(err)
	}
	s.Close()
	return memberAnswer{Protocol: string(s.Protocol()), Data: answer}, nil
}

// connectionFault reports whether a failed call says the connection is at fault:
// the call ran out of time, or the error is not one that a live connection
// gives. A live connection also gives a stream the peer reset (its gate, or its
// handler giving up), a protocol the peer does not serve, a state that may not
// use the protocol and an answer over the limit or in a frame that is not one. A peer that has gone away
// gives none of them: its streams do not open or do not answer, and without this
// the connection would be kept and every call to the node would wait out the
// call timeout on it.
func connectionFault(callCtx context.Context, err error) bool {
	if callCtx.Err() != nil {
		return true
	}
	var reset *lnet.StreamError
	var unsupported msmux.ErrNotSupported[protocol.ID]
	switch {
	case errors.Is(err, errNotAllowed), errors.Is(err, record.ErrOversize), errors.Is(err, nodeproto.ErrFrame),
		errors.As(err, &reset), errors.As(err, &unsupported):
		return false
	}
	return true
}

// noAnswer resets the stream of a call that got no answer and says why, in the
// transport's terms: the caller's own context ended, the peer's gate reset the
// stream before it read anything (the call was not seen), or the stream broke
// after the request began to go out.
func noAnswer(ctx context.Context, s lnet.Stream, err error) error {
	s.Reset()
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, gatedByPeer):
		return fmt.Errorf("%w: the peer's gate refused the stream", transport.ErrUnreachable)
	default:
		return fmt.Errorf("%w: %v", transport.ErrTimeout, err)
	}
}

// The server side. A handler runs only on a connection the gate judged a
// member's, so the node the request comes from is the one the exchange accepted.

// runSend handles a send request: it hands the envelope to the engine and
// answers with the receipt.
func runSend(ctx context.Context, node Node, _ *connEntry, proto string, from transport.Origin, raw []byte) ([]byte, error) {
	req, err := nodeproto.DecodeSendRequest(raw)
	if err != nil {
		return nil, errInvalidRequest{err}
	}
	receipt, err := node.Inbound(ctx, from, req.Sender, req.Envelope, featuresOf(proto))
	if err != nil {
		return answerFor(node, err, func(o nodeproto.Outcome, reason string) ([]byte, error) {
			return nodeproto.SendAnswer{Outcome: o, Reason: reason}.Encode()
		})
	}
	return nodeproto.SendAnswer{Outcome: nodeproto.OutcomeOK, Reports: receipt.Reports, ControlReply: receipt.ControlReply}.Encode()
}

// featuresOf are the features a send request carries under the identifier its
// stream was opened with: the current one carries all, the older ones none.
func featuresOf(proto string) transport.Features {
	if proto == nodeproto.ProtocolSend {
		return transport.FeatureModelChoice
	}
	return 0
}

// runPull handles a pull request and answers with the reports and the no-record endpoints.
func runPull(ctx context.Context, node Node, _ *connEntry, _ string, from transport.Origin, raw []byte) ([]byte, error) {
	req, err := nodeproto.DecodePullRequest(raw)
	if err != nil {
		return nil, errInvalidRequest{err}
	}
	ans, err := node.Pull(ctx, from, transport.PullRequest{Message: req.Message, Recipients: req.Recipients})
	if err != nil {
		return answerFor(node, err, func(o nodeproto.Outcome, reason string) ([]byte, error) {
			return nodeproto.PullAnswer{Outcome: o, Reason: reason}.Encode()
		})
	}
	return nodeproto.PullAnswer{Outcome: nodeproto.OutcomeOK, Reports: ans.Reports, NoRecord: ans.NoRecord, ControlReply: ans.ControlReply}.Encode()
}

// runDirectory handles a directory request and answers with a page.
func runDirectory(ctx context.Context, node Node, _ *connEntry, proto string, from transport.Origin, raw []byte) ([]byte, error) {
	req, err := nodeproto.DecodeDirectoryRequest(raw)
	if err != nil {
		return nil, errInvalidRequest{err}
	}
	page, err := node.QueryDirectory(ctx, from, req.Sender, transport.DirectoryRequest{Cursor: req.Cursor, Limit: req.Limit})
	if err != nil {
		return answerFor(node, err, func(o nodeproto.Outcome, reason string) ([]byte, error) {
			return nodeproto.DirectoryAnswer{Outcome: o, Reason: reason}.Encode()
		})
	}
	ans := nodeproto.DirectoryAnswer{Outcome: nodeproto.OutcomeOK, NextCursor: page.NextCursor}
	for _, ep := range page.Endpoints {
		ans.Entries = append(ans.Entries, nodeproto.DirectoryEntry{
			Endpoint:    ep.Address.Endpoint,
			Kind:        ep.Kind,
			DisplayName: ep.DisplayName,
			Description: ep.Description,
			Actions:     ep.Accepts,
			Adapter:     adapterToProto(ep.Adapter),
		})
	}
	return ans.EncodeFor(proto)
}

// errInvalidRequest is a request that does not decode; the stream is reset.
type errInvalidRequest struct{ error }

// answerFor encodes the answer to a call whose handler returned an error: a
// refusal that names its reason when the engine says it is one, and otherwise a
// failure that says nothing more. A reason that does not fit the wire is a failure.
func answerFor(node Node, err error, encode func(nodeproto.Outcome, string) ([]byte, error)) ([]byte, error) {
	if reason, ok := node.RefusalReason(err); ok {
		if b, encErr := encode(nodeproto.OutcomeRefused, reason); encErr == nil {
			return b, nil
		}
	}
	return encode(nodeproto.OutcomeFailed, "")
}

// callFunc is what a server of a member protocol does with a decoded-by-nobody
// request: it decodes it, runs it as a request from the node that was called
// and encodes the answer. It is given the connection the request came over, for
// a protocol whose answer depends on the connection and not only on the node,
// and the identifier the stream was opened under, for a protocol whose answer
// differs between its identifiers. A request that does not decode is an
// errInvalidRequest.
type callFunc func(ctx context.Context, node Node, e *connEntry, proto string, from transport.Origin, request []byte) ([]byte, error)

// handleCall registers the server of a member protocol under every identifier
// it is spoken under, the current one and the older ones it has. All the
// servers are served by serveCall, so what a stream is held for is decided once.
func (n *Network) handleCall(current string, run callFunc) {
	for _, proto := range nodeproto.Offer(current) {
		n.handle(proto, func(ctx context.Context, s lnet.Stream, e *connEntry) {
			n.serveCall(ctx, s, e, proto, run)
		})
	}
}

// serveCall is the frame of every server: read the one request within the call's
// budget, run the handler as a request from the connection's node and write the
// one answer.
//
// On a framed stream (a current identifier) the stream is then kept until the
// client has closed its side, which it does only once it has the answer: the
// handler returning is what lets the connection's busy count fall, and a
// connection marked to close when idle (a renewed certificate, a promotion, a
// call that found the connection at fault) is closed at that moment, which
// discards an answer still on its way. The wait ends when the client's side
// ends, the stream is reset or the connection goes, and at the call's budget,
// from the start of the stream, at the latest. A client that sends anything
// after its request breaks the protocol. A legacy stream is released as soon as
// the answer is written, because its client cannot signal that it has the
// answer: the half-close it made came with its request.
func (n *Network) serveCall(ctx context.Context, s lnet.Stream, e *connEntry, proto string, run callFunc) {
	node := n.currentNode()
	if node == nil {
		s.Reset()
		return
	}
	limit, ok := nodeproto.LimitOf(proto)
	if !ok {
		s.Reset()
		return
	}
	framed := !nodeproto.IsLegacy(proto)
	ctx, cancel := context.WithTimeout(ctx, n.cfg.CallTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { s.Reset() })
	defer stop()

	in := bufio.NewReader(s)
	var raw []byte
	var err error
	if framed {
		raw, err = nodeproto.ReadFrame(in, limit.Request)
	} else {
		raw, err = nodeproto.ReadMessage(s, limit.Request)
	}
	if err != nil {
		s.Reset()
		return
	}
	n.mu.Lock()
	from := e.node
	n.mu.Unlock()
	answer, err := run(ctx, node, e, proto, transport.RemoteOrigin(from), raw)
	if err != nil {
		var invalid errInvalidRequest
		if errors.As(err, &invalid) {
			n.log.Debug("invalid request", "protocol", proto, "peer", e.conn.RemotePeer(), "err", invalid.error)
		} else {
			n.log.Warn("the answer could not be made", "protocol", proto, "peer", e.conn.RemotePeer(), "err", err)
		}
		s.ResetWithError(lnet.StreamProtocolViolation)
		return
	}
	if !framed {
		if _, err := s.Write(answer); err != nil && !errors.Is(err, io.EOF) {
			s.Reset()
			return
		}
		s.Close()
		return
	}
	frame, err := nodeproto.AppendFrame(nil, answer, limit.Answer)
	if err != nil {
		n.log.Warn("the answer does not fit its frame", "protocol", proto, "peer", e.conn.RemotePeer(), "err", err)
		s.Reset()
		return
	}
	if _, err := s.Write(frame); err != nil {
		s.Reset()
		return
	}
	s.CloseWrite()
	switch _, err := in.ReadByte(); {
	case err == io.EOF:
		s.Close()
	case err == nil:
		n.log.Debug("the client sent more than its request", "protocol", proto, "peer", e.conn.RemotePeer())
		s.ResetWithError(lnet.StreamProtocolViolation)
	default:
		if ctx.Err() != nil {
			n.log.Debug("the client did not close the stream within the call's budget", "protocol", proto, "peer", e.conn.RemotePeer())
		}
		s.Reset()
	}
}
