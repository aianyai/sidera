package network

import (
	"context"
	"errors"
	"fmt"

	"github.com/libp2p/go-libp2p/core/protocol"
	msmux "github.com/multiformats/go-multistream"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/nodeproto"
	"github.com/aianyai/sidera/internal/record"
)

// The profile protocol carries a node's description of itself to a member that
// trusts it directly. The server hands the request to the engine, which decides
// who may ask and what the answer is; the client asks on the member connection it
// has and dials nothing, since the engine asks only because a connection just came
// up.

// FetchProfile asks a node, on the member connection that is up to it, for the
// signed profile it issued for itself. The record is returned as the node signed
// it, with only its shape checked; the engine verifies it. served is false, with no
// error, when the node does not serve the protocol, as a node of an earlier release
// does not: there is nothing to retry on that connection. The errors are those of
// any call: transport.ErrUnreachable when there is no member connection or the
// call was not seen, transport.ErrTimeout, transport.Refused when the node refused
// for a reason it names.
func (n *Network) FetchProfile(ctx context.Context, node ident.NodeID) (rec record.Record, served bool, err error) {
	defer func() { n.noteCall(ctx, node, "profile", err) }()
	e := n.memberConn(node)
	if e == nil {
		return record.Record{}, false, fmt.Errorf("%w: no member connection to it", transport.ErrUnreachable)
	}
	body, err := nodeproto.ProfileRequest{}.Encode()
	if err != nil {
		return record.Record{}, false, fmt.Errorf("network: profile request: %w", err)
	}
	req, err := newMemberRequest(nodeproto.ProtocolProfile, body)
	if err != nil {
		return record.Record{}, false, err
	}
	raw, err := n.callOn(ctx, e, node, req)
	var unsupported msmux.ErrNotSupported[protocol.ID]
	if errors.As(err, &unsupported) {
		return record.Record{}, false, nil
	}
	if err != nil {
		return record.Record{}, false, err
	}
	ans, err := nodeproto.DecodeProfileAnswer(raw.Data)
	if err != nil {
		return record.Record{}, false, invalidAnswer(err)
	}
	if err := outcomeError(ans.Outcome, ans.Reason); err != nil {
		return record.Record{}, false, err
	}
	return *ans.Profile, true, nil
}

// runProfile handles a profile request and answers with the node's profile.
func runProfile(ctx context.Context, node Node, _ *connEntry, _ string, from transport.Origin, raw []byte) ([]byte, error) {
	if _, err := nodeproto.DecodeProfileRequest(raw); err != nil {
		return nil, errInvalidRequest{err}
	}
	rec, err := node.Profile(ctx, from)
	if err != nil {
		return answerFor(node, err, func(o nodeproto.Outcome, reason string) ([]byte, error) {
			return nodeproto.ProfileAnswer{Outcome: o, Reason: reason}.Encode()
		})
	}
	return nodeproto.ProfileAnswer{Outcome: nodeproto.OutcomeOK, Profile: &rec}.Encode()
}
