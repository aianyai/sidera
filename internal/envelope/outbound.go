package envelope

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/timing"
)

// Arrival is what a sender has learned from signed status reports about when a
// message reached one recipient. The reply window starts at the delivery time
// if there is one and otherwise at the earliest report that proves arrival.
type Arrival struct {
	// DeliveredAt is the delivery time a delivered report gave; zero if none.
	DeliveredAt time.Time
	// ArrivedAt is the earliest reported_at of a report that proves arrival
	// (StatusReport.ProvesArrival); zero if none. The earliest is kept so that a
	// later report cannot move the start of the window forward.
	ArrivedAt time.Time
}

// Learn returns the arrival with the facts of one report added. It does not
// judge the report: whether it is genuine and refers to this delivery is the
// caller's (Outbound.ApplyReport).
func (a Arrival) Learn(r StatusReport) Arrival {
	if r.Status.State == delivery.StateDelivered && (a.DeliveredAt.IsZero() || r.DeliveredAt.Before(a.DeliveredAt)) {
		a.DeliveredAt = r.DeliveredAt
	}
	if r.ProvesArrival() && (a.ArrivedAt.IsZero() || r.ReportedAt.Before(a.ArrivedAt)) {
		a.ArrivedAt = r.ReportedAt
	}
	return a
}

// Tracked is the sender's knowledge of one delivery of a message to one
// endpoint: its record and what the target's reports proved about arrival.
type Tracked struct {
	Address  ident.Address
	Delivery delivery.Record
	Arrival  Arrival
}

// Outbound is a sender node's record of one message it sent, as far as reports
// and control replies are concerned. It holds one Tracked per original
// recipient and, for a session.create with a body whose control reply named a
// session endpoint, one for that endpoint. The caller keeps it in storage and
// commits what the functions below return in the transaction of the
// triggering input.
type Outbound struct {
	Message delivery.MessageID
	// From is the local endpoint that sent the message.
	From         ident.EndpointID
	Kind         Kind
	BodyNonEmpty bool
	// AcceptBefore is the delivery deadline of the message; a derived delivery
	// shares it and the report_by that follows from it.
	AcceptBefore time.Time
	Recipients   []Tracked
	// ControlReplyDigest is the digest of the control reply this record
	// accepted; zero until one was. A reply with the same digest is the same
	// reply again and changes nothing, any other is a contradiction by the peer
	// (HandleControlReply). The target keeps one reply per signal, so a second
	// distinct one means it broke that.
	ControlReplyDigest record.Digest
	// Derived is the delivery of the initial message to the session the control
	// signal created. Nil until a control reply binds it, and only possible for
	// a session.create with a body.
	Derived *Tracked
}

// Validate checks the invariants every function of this package relies on.
func (o Outbound) Validate() error {
	switch {
	case o.Message == "" || o.From.IsZero() || !o.Kind.Valid() || o.AcceptBefore.IsZero():
		return fmt.Errorf("%w: message, sender endpoint, kind and deadline are required", ErrOutbound)
	case len(o.Recipients) == 0:
		return fmt.Errorf("%w: no recipients", ErrOutbound)
	case o.Kind.IsControl() && len(o.Recipients) != 1:
		return fmt.Errorf("%w: a control signal has one recipient", ErrOutbound)
	case o.Derived != nil && (o.Kind != KindSessionCreate || !o.BodyNonEmpty):
		return fmt.Errorf("%w: only a session.create with a body has a derived delivery", ErrOutbound)
	case o.Derived != nil && o.Derived.Address.Node != o.Recipients[0].Address.Node:
		return fmt.Errorf("%w: the derived endpoint is on another node than the control's recipient", ErrOutbound)
	case o.Derived != nil && o.ControlReplyDigest.IsZero():
		return fmt.Errorf("%w: a derived delivery is bound by a control reply, whose digest is kept", ErrOutbound)
	case !o.ControlReplyDigest.IsZero() && !o.Kind.IsControl():
		return fmt.Errorf("%w: only a control signal has a control reply", ErrOutbound)
	}
	seen := map[ident.EndpointID]struct{}{}
	for _, t := range o.Deliveries() {
		if t.Address.IsZero() || t.Delivery.Side != delivery.SideSource {
			return fmt.Errorf("%w: every tracked delivery has an address and a source record", ErrOutbound)
		}
		if _, dup := seen[t.Address.Endpoint]; dup {
			return fmt.Errorf("%w: an endpoint is tracked twice", ErrOutbound)
		}
		seen[t.Address.Endpoint] = struct{}{}
	}
	return nil
}

// Deliveries returns every delivery the sender tracks of the message, the
// original ones in recipient order and then the derived one, if there is one.
func (o Outbound) Deliveries() []Tracked {
	return append(slices.Clone(o.Recipients), derivedList(o.Derived)...)
}

func derivedList(d *Tracked) []Tracked {
	if d == nil {
		return nil
	}
	return []Tracked{*d}
}

// Find returns the delivery of the message to an endpoint, original or
// derived, and whether there is one.
func (o Outbound) Find(ep ident.EndpointID) (Tracked, bool) {
	for _, t := range o.Recipients {
		if t.Address.Endpoint == ep {
			return t, true
		}
	}
	if o.Derived != nil && o.Derived.Address.Endpoint == ep {
		return *o.Derived, true
	}
	return Tracked{}, false
}

func (o Outbound) clone() Outbound {
	o.Recipients = slices.Clone(o.Recipients)
	if o.Derived != nil {
		d := *o.Derived
		o.Derived = &d
	}
	return o
}

// replace stores t over the tracked delivery of the same endpoint in o, which
// must be a clone.
func (o *Outbound) replace(t Tracked) {
	for i := range o.Recipients {
		if o.Recipients[i].Address.Endpoint == t.Address.Endpoint {
			o.Recipients[i] = t
			return
		}
	}
	if o.Derived != nil && o.Derived.Address.Endpoint == t.Address.Endpoint {
		*o.Derived = t
	}
}

// ReportEffect says what became of a status report, or of a target's answer
// that it holds no record, offered to an Outbound.
type ReportEffect uint8

const (
	// ReportNone: no report was offered.
	ReportNone ReportEffect = iota
	// ReportApplied: the report belongs to a delivery of the message and was
	// applied; its outcomes are in the result. An answer that the target holds no
	// record is applied the same way, and the delivery may ignore it.
	ReportApplied
	// ReportNotTracked: the sender holds no delivery of the message to the
	// reported endpoint, so there is nothing to apply the report to. A sender
	// applies reports only to deliveries it has records for.
	ReportNotTracked
	// ReportTooLate: the report is dated after the delivery's report_by plus the
	// clock tolerance. Nothing was applied and nothing learned from it: a report
	// that late cannot move the arrival facts, and the reply window with them.
	// It is not a fault of the peer (see ApplyReport).
	ReportTooLate
	// ReportAwaitsControlReply: the report says a control signal was delivered,
	// and the control signal's delivery ends only through its control reply
	// (HandleControlReply), which this record has not accepted yet. Nothing was
	// applied; the caller keeps pulling the signal until the reply arrives.
	ReportAwaitsControlReply
)

// ApplyReport applies a verified status report to the delivery it reports on:
// the state through delivery.Record.ApplyStatusReport, and the arrival facts
// through Arrival.Learn. It is the one rule by which a sender takes a report in,
// whether pulled from the target or attached to a reply as evidence.
//
// The report must be about this message, signed by the node the delivery's
// endpoint is on, and not dated later than now plus the clock tolerance. A
// report for an endpoint with no delivery here changes nothing (ReportNotTracked).
//
// A report dated after the delivery's report_by plus the clock tolerance also
// changes nothing (ReportTooLate) and is not an error. A target that slept past
// report_by settles the delivery when it wakes, at that later moment, and its
// honest report carries that moment; what the sender must not do is let such a
// report move the arrival facts, and the reply window with them.
//
// A control signal's own delivery ends as delivered only through its control
// reply, because only the reply carries the result and the session endpoint the
// sender needs: until HandleControlReply has accepted one (ControlReplyDigest is
// zero), a delivered report for the signal's recipient changes nothing
// (ReportAwaitsControlReply), so that the sender does not stop pulling before it
// has the reply. Every other state of the report is applied as usual.
//
// A report whose state cannot follow the delivery's (a rejection of a delivery
// already delivered, say) is ErrPeerContradiction. On an error nothing changes.
func (o Outbound) ApplyReport(now time.Time, rep SignedReport) (Outbound, ReportEffect, []delivery.Outcome, error) {
	if err := o.Validate(); err != nil {
		return o, ReportNone, nil, err
	}
	if rep.isZero() {
		return o, ReportNone, nil, ErrNotVerified
	}
	r := rep.Report()
	if r.Message != o.Message {
		return o, ReportNone, nil, fmt.Errorf("%w: report is about message %q, not %q", ErrMismatch, r.Message, o.Message)
	}
	t, ok := o.Find(r.Recipient)
	if !ok {
		return o, ReportNotTracked, nil, nil
	}
	if t.Address.Node != rep.Node() {
		return o, ReportNone, nil, fmt.Errorf("%w: the delivery is on another node than the report's signer", ErrMismatch)
	}
	for _, at := range []time.Time{r.ReportedAt, r.DeliveredAt} {
		if at.IsZero() {
			continue
		}
		if err := checkNotFuture(now, at); err != nil {
			return o, ReportNone, nil, err
		}
	}
	if r.ReportedAt.After(t.Delivery.ReportBy.Add(timing.ClockTolerance)) {
		return o, ReportTooLate, nil, nil
	}
	// Without a control reply the only delivery tracked is the signal's own (a
	// derived one exists only once a reply bound it, Validate).
	if o.Kind.IsControl() && o.ControlReplyDigest.IsZero() && r.Status.State == delivery.StateDelivered {
		return o, ReportAwaitsControlReply, nil, nil
	}
	d, outs, err := t.Delivery.ApplyStatusReport(now, r.Status)
	if errors.Is(err, delivery.ErrIllegalTransition) {
		return o, ReportNone, nil, fmt.Errorf("%w: %w", ErrPeerContradiction, err)
	}
	if err != nil {
		return o, ReportNone, nil, err
	}
	t.Delivery = d
	t.Arrival = t.Arrival.Learn(r)
	n := o.clone()
	n.replace(t)
	return n, ReportApplied, outs, nil
}

// ApplyNoRecord applies the answer of the target node that it holds no record of
// the delivery of the message to recipient, to the delivery the sender tracks
// for that endpoint, original or derived. The delivery must be on node, the node
// that answered. An endpoint with no delivery here changes nothing
// (ReportNotTracked). Whether the answer means a loss is the rule of
// delivery.Record.Apply for EventNoRecordAtTarget, which the delivery may
// ignore; the outcome says which. On an error nothing changes.
//
// Like a status report, the answer is believed because it came over the
// connection the network key authenticates; it is not signed and is no evidence
// for a third party.
func (o Outbound) ApplyNoRecord(now time.Time, node ident.NodeID, recipient ident.EndpointID) (Outbound, ReportEffect, delivery.Outcome, error) {
	if err := o.Validate(); err != nil {
		return o, ReportNone, delivery.Outcome{}, err
	}
	t, ok := o.Find(recipient)
	if !ok {
		return o, ReportNotTracked, delivery.Outcome{}, nil
	}
	if t.Address.Node != node {
		return o, ReportNone, delivery.Outcome{}, fmt.Errorf("%w: the delivery is on another node than the one that answered", ErrMismatch)
	}
	d, out, err := t.Delivery.Apply(now, delivery.Event{Type: delivery.EventNoRecordAtTarget})
	if err != nil {
		return o, ReportNone, delivery.Outcome{}, err
	}
	t.Delivery = d
	n := o.clone()
	n.replace(t)
	return n, ReportApplied, out, nil
}
