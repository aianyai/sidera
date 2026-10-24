package envelope

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
)

// Handling is what HandleControlReply decided. The caller commits Outbound, and
// whatever the outcomes call for (feedback to the agent, events), in the one
// transaction in which it stored the reply.
type Handling struct {
	// Outbound is the sender's record after the reply: the control signal's
	// delivery ended, the derived delivery created if the reply bound one, the
	// accompanying report applied.
	Outbound Outbound
	// Control is the outcome of ending the delivery of the control signal. It
	// carries the correction feedback when that delivery had been judged
	// expired_unconfirmed.
	Control delivery.Outcome
	// DerivedCreated is true when this reply bound the session endpoint and the
	// derived delivery was created by it (false when it was bound before).
	DerivedCreated bool
	// Report says what became of the accompanying report, and Reported holds the
	// outcomes of applying it.
	Report   ReportEffect
	Reported []delivery.Outcome
}

// HandleControlReply is the one rule by which a sender's node takes in the
// verified control reply to a control signal it sent. The pull path (the
// target's answer when asked about the signal) and the evidence path (a reply
// from the new session that carries the reply) both call it, so a reply from the
// new session that arrives before the pulled control reply sets up exactly the
// state the pulled reply would have.
//
// In order, and all or nothing:
//
//  1. The delivery of the control signal ends as delivered: the target
//     committed the reply, whatever its outcome says, so the signal was
//     delivered and executed or refused by the adapter. (A signal the target
//     rejected or one already closed is not changed by a reply; a rejection is
//     an error, a closed delivery is left alone.)
//  2. For a session.create with a body whose outcome is created or unconfirmed,
//     the initial message goes to the session endpoint the reply names: the
//     delivery of the message to that endpoint is created in state accepted,
//     sharing the signal's deadline (the body and the signal were accepted
//     together), and the endpoint is bound as the one derived from the signal.
//     A reply that names another endpoint than the one already bound, or none,
//     is ErrDerivedConflict.
//  3. The accompanying status report, if any, is applied to the delivery it
//     reports on (Outbound.ApplyReport), which can be the derived one just
//     created.
//
// The reply and the report must be signed by the node the signal was sent to
// and be about this message.
func HandleControlReply(now time.Time, o Outbound, reply SignedControlReply, report *SignedReport) (Handling, error) {
	if err := o.Validate(); err != nil {
		return Handling{}, err
	}
	if reply.isZero() || (report != nil && report.isZero()) {
		return Handling{}, ErrNotVerified
	}
	signal, isControl := o.Kind.Signal()
	if !isControl {
		return Handling{}, fmt.Errorf("%w: the message is not a control signal", ErrMismatch)
	}
	target := o.Recipients[0]
	r := reply.Reply()
	switch {
	case r.Message != o.Message:
		return Handling{}, fmt.Errorf("%w: reply is about message %q, not %q", ErrMismatch, r.Message, o.Message)
	case r.Recipient != target.Address.Endpoint || reply.Node() != target.Address.Node:
		return Handling{}, fmt.Errorf("%w: reply is not from the signal's recipient", ErrMismatch)
	case r.Result.Signal != signal:
		return Handling{}, fmt.Errorf("%w: reply is for %s, the signal was %s", ErrMismatch, r.Result.Signal, signal)
	}
	if err := checkNotFuture(now, r.IssuedAt); err != nil {
		return Handling{}, err
	}

	n := o.clone()
	var h Handling

	d, out, err := target.Delivery.Apply(now, delivery.Event{Type: delivery.EventDelivered})
	if err != nil {
		return Handling{}, fmt.Errorf("ending the control signal: %w", err)
	}
	n.Recipients[0].Delivery = d
	h.Control = out

	endpoint, binds := r.Binds()
	switch {
	case binds && o.BodyNonEmpty && n.Derived == nil:
		rec, _, err := delivery.NewSourceRecord(o.AcceptBefore).Apply(now, delivery.Event{Type: delivery.EventReceipt})
		if err != nil {
			return Handling{}, fmt.Errorf("creating the delivery of the initial message: %w", err)
		}
		n.Derived = &Tracked{Address: ident.Address{Node: target.Address.Node, Endpoint: endpoint}, Delivery: rec}
		h.DerivedCreated = true
	case n.Derived != nil && (!binds || n.Derived.Address.Endpoint != endpoint):
		return Handling{}, fmt.Errorf("%w: bound to %s", ErrDerivedConflict, n.Derived.Address.Endpoint)
	}
	if err := n.Validate(); err != nil {
		return Handling{}, err
	}

	if report != nil {
		if n, h.Report, h.Reported, err = n.ApplyReport(now, *report); err != nil {
			return Handling{}, fmt.Errorf("applying the accompanying report: %w", err)
		}
	}
	h.Outbound = n
	return h, nil
}
