package transport

import (
	"context"
	"errors"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// Handler is the engine's side of every transport: what an implementation calls
// when something arrives for this node. Both methods are told where the request
// came from; whether it is a local source is that origin's to say.
type Handler interface {
	// Inbound takes one envelope for this node, sent from the endpoint sender of
	// the node the origin names, and returns the signed status reports (and the
	// control reply, if the envelope was a control signal that has one) as the
	// receipt. The envelope is the bytes the sender encoded: the receiving side
	// decodes and judges it. An error means no receipt exists: the target could
	// not answer, and the sender may send the same envelope again.
	Inbound(ctx context.Context, from Origin, sender ident.EndpointID, envelope []byte) (Receipt, error)
	// Pull answers a source's pull for the current reports of its deliveries of
	// one message.
	Pull(ctx context.Context, from Origin, req PullRequest) (PullAnswer, error)
}

// Sender is what the engine calls to reach a node, outside its loop. An error
// that wraps [ErrUnreachable] or [ErrTimeout] says the sender got no answer; any
// other error is the target's own and passes unchanged.
type Sender interface {
	// Send delivers one encoded envelope from the endpoint sender to the target
	// node and returns its receipt. After ErrTimeout the target may have
	// processed the envelope: send the same bytes again and the target
	// deduplicates. After ErrUnreachable it certainly did not.
	Send(ctx context.Context, target ident.NodeID, sender ident.EndpointID, envelope []byte) (Receipt, error)
	// Pull asks the target node for the current signed reports of the
	// deliveries of one message that the caller sent it.
	Pull(ctx context.Context, target ident.NodeID, req PullRequest) (PullAnswer, error)
}

// Receipt is a target's answer to one envelope: the signed records, as the
// target made them. The transport does not read them; the sender verifies them
// with the envelope package before it applies one.
type Receipt struct {
	// Reports are the signed status reports, one for each recipient of the
	// envelope on the target node.
	Reports []record.Record
	// ControlReply is the signed control reply, nil unless the envelope was a
	// control signal that already has one.
	ControlReply *record.Record
}

// PullRequest names what a source pulls: the reports of its deliveries of one
// message, for every recipient of it on the target. The target knows the sender
// from the request's origin, not from the request.
type PullRequest struct {
	Message delivery.MessageID
}

// PullAnswer is the answer to a pull and has the shape of a [Receipt]: the
// current signed reports of the message's deliveries, and its control reply if
// there is one. An empty answer means the target has nothing to report yet, not
// that the deliveries do not exist.
type PullAnswer Receipt

var (
	// ErrUnreachable means the call did not reach the target node and the
	// target did not see it.
	ErrUnreachable = errors.New("transport: node unreachable")
	// ErrTimeout means the call went out and no answer came back; the target may
	// have processed it.
	ErrTimeout = errors.New("transport: no answer in time")
)
