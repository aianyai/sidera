package envelope

import (
	"errors"
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
//  0. A reply that names the control signal's own recipient as the session
//     endpoint it created is ErrPeerContradiction: a session is a new endpoint,
//     and tracking one endpoint twice would corrupt the record. It is judged
//     before anything else is built.
//  1. A reply other than the one already accepted for the signal (a different
//     digest) is ErrPeerContradiction: the target keeps one reply per signal and
//     sends that same payload every time, so a second distinct one means it broke
//     that. The same reply again changes nothing.
//  2. The delivery of the control signal ends as delivered: the target
//     committed the reply, whatever its outcome says, so the signal was
//     delivered and executed or refused by the adapter. A signal judged
//     expired_unconfirmed is corrected; one already closed_unconfirmed is left
//     as it is and the reply still takes effect below (the derived delivery,
//     which shares the signal's deadlines, then closes silently at its next
//     tick). A signal the target is known to have rejected cannot have a reply:
//     that is ErrPeerContradiction.
//  3. For a session.create with a body whose outcome is created or unconfirmed,
//     the initial message goes to the session endpoint the reply names: the
//     delivery of the message to that endpoint is created in state accepted,
//     sharing the signal's deadline (the body and the signal were accepted
//     together), and the endpoint is bound as the one derived from the signal.
//  4. The accompanying status report, if any, is applied to the delivery it
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
	if endpoint, binds := r.Binds(); binds && endpoint == target.Address.Endpoint {
		return Handling{}, fmt.Errorf("%w: the reply names the signal's own recipient as the session endpoint", ErrPeerContradiction)
	}
	if !o.ControlReplyDigest.IsZero() && o.ControlReplyDigest != reply.Digest() {
		return Handling{}, fmt.Errorf("%w: another control reply was already accepted for the signal", ErrPeerContradiction)
	}

	n := o.clone()
	n.ControlReplyDigest = reply.Digest()
	var h Handling

	d, out, err := target.Delivery.Apply(now, delivery.Event{Type: delivery.EventDelivered})
	if errors.Is(err, delivery.ErrIllegalTransition) {
		return Handling{}, fmt.Errorf("%w: ending the control signal: %w", ErrPeerContradiction, err)
	}
	if err != nil {
		return Handling{}, fmt.Errorf("ending the control signal: %w", err)
	}
	n.Recipients[0].Delivery = d
	h.Control = out

	if endpoint, binds := r.Binds(); binds && o.BodyNonEmpty && n.Derived == nil {
		rec, _, err := delivery.NewSourceRecord(now, o.AcceptBefore).Apply(now, delivery.Event{Type: delivery.EventReceipt})
		if err != nil {
			return Handling{}, fmt.Errorf("creating the delivery of the initial message: %w", err)
		}
		n.Derived = &Tracked{Address: ident.Address{Node: target.Address.Node, Endpoint: endpoint}, Delivery: rec}
		h.DerivedCreated = true
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
