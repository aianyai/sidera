package network

import (
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

// A call to another node is one stream: the request is one message and a half
// close, the answer one message and the end of the stream.

var errFailed = errors.New("network: the target could not handle the call")

// gatedByPeer is what a stream reads when the peer's gate reset it: the peer
// refused to serve the stream before it read anything, so it did not see the call.
var gatedByPeer = &lnet.StreamError{ErrorCode: lnet.StreamGated, Remote: true}

// Send carries one envelope to the target node and returns the receipt. The
// errors are the transport vocabulary: ErrUnreachable when the target did not
// see the call, ErrTimeout when it may have, Refused when it refused with a
// reason, a plain error when it could not handle the call, and the caller's own
// context error as it is.
func (n *Network) Send(ctx context.Context, target ident.NodeID, sender ident.EndpointID, data []byte) (transport.Receipt, error) {
	req, err := nodeproto.SendRequest{Sender: sender, Envelope: data}.Encode()
	if err != nil {
		return transport.Receipt{}, fmt.Errorf("network: send request: %w", err)
	}
	raw, err := n.call(ctx, target, nodeproto.ProtocolSend, req)
	if err != nil {
		return transport.Receipt{}, err
	}
	ans, err := nodeproto.DecodeSendAnswer(raw)
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
func (n *Network) Pull(ctx context.Context, target ident.NodeID, req transport.PullRequest) (transport.PullAnswer, error) {
	raw, err := nodeproto.PullRequest{Message: req.Message, Recipients: req.Recipients}.Encode()
	if err != nil {
		return transport.PullAnswer{}, fmt.Errorf("network: pull request: %w", err)
	}
	raw, err = n.call(ctx, target, nodeproto.ProtocolPull, raw)
	if err != nil {
		return transport.PullAnswer{}, err
	}
	ans, err := nodeproto.DecodePullAnswer(raw)
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
func (n *Network) QueryDirectory(ctx context.Context, target ident.NodeID, sender ident.EndpointID, req transport.DirectoryRequest) (transport.DirectoryPage, error) {
	raw, err := nodeproto.DirectoryRequest{Sender: sender, Cursor: req.Cursor, Limit: req.Limit}.Encode()
	if err != nil {
		return transport.DirectoryPage{}, fmt.Errorf("network: directory request: %w", err)
	}
	raw, err = n.call(ctx, target, nodeproto.ProtocolDirectory, raw)
	if err != nil {
		return transport.DirectoryPage{}, err
	}
	ans, err := nodeproto.DecodeDirectoryAnswer(raw)
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
		})
	}
	return page, nil
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

// call makes one request to the target node and returns the bytes of its
// answer. It maps what goes wrong to the transport's two kinds of "no answer":
//
//   - nothing the target could have seen was sent (no connection within the
//     budget, a connection the node judges not to be a member's, a protocol the
//     peer's gate or protocol negotiation refused): ErrUnreachable;
//   - the request began to go out and no complete answer came back in time, or
//     the stream broke: ErrTimeout;
//   - the caller's own context ended: its error, as it is.
func (n *Network) call(ctx context.Context, target ident.NodeID, proto string, req []byte) ([]byte, error) {
	limit, ok := nodeproto.LimitOf(proto)
	if !ok {
		return nil, fmt.Errorf("network: %s is not a node protocol", proto)
	}
	e, err := n.connect(ctx, target)
	if err != nil {
		return nil, err
	}
	if !n.acquire(e) {
		return nil, fmt.Errorf("%w: the connection ended", transport.ErrUnreachable)
	}
	defer n.release(e)

	cctx, cancel := context.WithTimeout(ctx, n.cfg.CallTimeout)
	defer cancel()
	s, err := n.openStream(cctx, e, proto)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if connectionFault(cctx, err) {
			n.dropConn(e, target)
		}
		return nil, fmt.Errorf("%w: %v", transport.ErrUnreachable, err)
	}
	stop := context.AfterFunc(cctx, func() { s.Reset() })
	defer stop()

	noAnswer := func(err error) error {
		s.Reset()
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, gatedByPeer):
			return fmt.Errorf("%w: the peer's gate refused the stream", transport.ErrUnreachable)
		default:
			if connectionFault(cctx, err) {
				n.dropConn(e, target)
			}
			return fmt.Errorf("%w: %v", transport.ErrTimeout, err)
		}
	}
	// A failed write is not judged by itself: the stream is dead, and the read
	// that follows says why with the library's own error, which carries the
	// peer's reset code; a write or half-close error does not always.
	if _, err := s.Write(req); err == nil {
		s.CloseWrite()
	}
	answer, err := nodeproto.ReadMessage(s, limit.Answer)
	if err != nil {
		return nil, noAnswer(err)
	}
	s.Close()
	return answer, nil
}

// connectionFault reports whether a failed call says the connection is at fault:
// the call ran out of time, or the error is not one that a live connection
// gives. A live connection also gives a stream the peer reset (its gate, or its
// handler giving up), a protocol the peer does not serve, a state that may not
// use the protocol and an answer over the limit. A peer that has gone away
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
	case errors.Is(err, errNotAllowed), errors.Is(err, record.ErrOversize),
		errors.As(err, &reset), errors.As(err, &unsupported):
		return false
	}
	return true
}

// The server side. A handler runs only on a connection the gate judged a
// member's, so the node the request comes from is the one the exchange accepted.

// serveSend reads one envelope and answers with the receipt.
func (n *Network) serveSend(ctx context.Context, s lnet.Stream, e *connEntry) {
	n.serveCall(ctx, s, e, nodeproto.ProtocolSend, func(ctx context.Context, node Node, from transport.Origin, raw []byte) ([]byte, error) {
		req, err := nodeproto.DecodeSendRequest(raw)
		if err != nil {
			return nil, errInvalidRequest{err}
		}
		receipt, err := node.Inbound(ctx, from, req.Sender, req.Envelope)
		if err != nil {
			return answerFor(node, err, func(o nodeproto.Outcome, reason string) ([]byte, error) {
				return nodeproto.SendAnswer{Outcome: o, Reason: reason}.Encode()
			})
		}
		return nodeproto.SendAnswer{Outcome: nodeproto.OutcomeOK, Reports: receipt.Reports, ControlReply: receipt.ControlReply}.Encode()
	})
}

// servePull reads a pull and answers with the reports and the no-record endpoints.
func (n *Network) servePull(ctx context.Context, s lnet.Stream, e *connEntry) {
	n.serveCall(ctx, s, e, nodeproto.ProtocolPull, func(ctx context.Context, node Node, from transport.Origin, raw []byte) ([]byte, error) {
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
	})
}

// serveDirectory reads a directory request and answers with a page.
func (n *Network) serveDirectory(ctx context.Context, s lnet.Stream, e *connEntry) {
	n.serveCall(ctx, s, e, nodeproto.ProtocolDirectory, func(ctx context.Context, node Node, from transport.Origin, raw []byte) ([]byte, error) {
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
			})
		}
		return ans.Encode()
	})
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

// serveCall is the frame of every server: read the one request within the call's
// budget, run the handler as a request from the connection's node, write the
// one answer and close.
func (n *Network) serveCall(
	ctx context.Context, s lnet.Stream, e *connEntry, proto string,
	run func(ctx context.Context, node Node, from transport.Origin, request []byte) ([]byte, error),
) {
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
	ctx, cancel := context.WithTimeout(ctx, n.cfg.CallTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { s.Reset() })
	defer stop()

	raw, err := nodeproto.ReadMessage(s, limit.Request)
	if err != nil {
		s.Reset()
		return
	}
	n.mu.Lock()
	from := e.node
	n.mu.Unlock()
	answer, err := run(ctx, node, transport.RemoteOrigin(from), raw)
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
	if _, err := s.Write(answer); err != nil && !errors.Is(err, io.EOF) {
		s.Reset()
		return
	}
	s.Close()
}
