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
// came from; whether it is a local source is that origin's to say. The context
// is the one of the call that brought the request, so ending it ends the
// handler's work too.
//
// The transport defines this interface because it is the consumer of it. The
// other direction, what the engine calls to reach a node, is defined by the
// engine, the consumer there: each side of a seam names what it needs.
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

// Receipt is a target's answer to one envelope: the signed records, as the
// target made them. The transport does not read them; the sender verifies them
// with the envelope package before it applies one.
//
// A receipt may carry fewer records than the envelope has recipients, or none,
// and may lack the control reply: what the sender does not get in the receipt it
// pulls later, as it would for a send whose answer never came. A missing record
// therefore has the meaning of an empty [PullAnswer]: nothing yet, not that it
// does not exist. A transport may leave records out (the loopback does when a
// test holds the reports of a message); a sender must not read the number of
// records of a receipt as a statement about the recipients.
type Receipt struct {
	// Reports are the signed status reports for the recipients of the envelope
	// on the target node, those the target had to give with the receipt.
	Reports []record.Record
	// ControlReply is the signed control reply, nil unless the envelope was a
	// control signal that already has one, and nil when the receipt leaves it to
	// a later pull.
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

// The two errors below are the vocabulary of every transport, not of the
// loopback alone: an implementation of another package returns them, wrapped as
// it likes, with exactly this meaning, so that the engine's retry rules do not
// depend on which transport carried the call. Any other error a call returns is
// the target's own and passes unchanged; it means no receipt exists. The error
// of the caller's own context is returned as it is, neither of these two: the
// target may or may not have seen the call.
var (
	// ErrUnreachable means the call did not reach the target node and the
	// target did not see it: it is safe to send again, by the sender's schedule.
	ErrUnreachable = errors.New("transport: node unreachable")
	// ErrTimeout means the call went out and no answer came back; the target may
	// have processed it. The sender sends the same bytes again and the target
	// deduplicates by message id.
	ErrTimeout = errors.New("transport: no answer in time")
)
