package reply

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/timing"
)

// Window is how long reply permission lasts, counted from the start the sender
// can prove (see Request).
const Window = 24 * time.Hour

// RetainFactsUntil returns how long the facts reply permission reads are kept
// for a message whose report_by is reportBy: report_by plus the clock tolerance
// (a report dated that late is still believed) plus Window (a reply admitted
// at the end of the window is still decided from them). It is the one place
// this retention is computed: the source keeps the message's envelope.Outbound
// with its arrival facts and the digest of the accepted control reply, and the
// target keeps the stored control reply and its delivery facts, until then.
// That is longer than delivery keeps its deduplication records
// (delivery.DedupRetainUntil), so the stored control reply must not be purged
// with them.
func RetainFactsUntil(reportBy time.Time) time.Time {
	return reportBy.Add(timing.ClockTolerance + Window)
}

// ErrInvalidRequest reports an argument no correct caller produces: a missing
// time or identity, or an outbound record that is not the one named by ReplyTo.
// A reply that is merely not allowed is a Decision, not an error.
var ErrInvalidRequest = errors.New("invalid reply request")

// Reason says why a reply has reply permission or not. Without it the message
// is not refused: it is judged by the node's ordinary policy, which may still
// admit it. Granted reasons say where the window started.
type Reason uint8

const (
	// ReasonInvalid is the zero value; it is never a decision.
	ReasonInvalid Reason = iota
	// ReasonDeliveredWindow: permission; the window runs from the delivery time
	// the target reported.
	ReasonDeliveredWindow
	// ReasonArrivalWindow: permission; no delivery time is known, so the window
	// runs from the recorded time of the earliest signed report that shows the
	// message arrived.
	ReasonArrivalWindow
	// ReasonDisabled: no permission; the node's owner turned reply permission off.
	ReasonDisabled
	// ReasonNoOutbound: no permission; this node has no record of the message the
	// reply names.
	ReasonNoOutbound
	// ReasonNotSender: no permission; the reply is not addressed to the endpoint that
	// sent the message.
	ReasonNotSender
	// ReasonNotRecipient: no permission; the replier is neither a recipient of the
	// message nor the session endpoint its control signal created.
	ReasonNotRecipient
	// ReasonControlSignal: no permission; the message is a control signal and the
	// replier is its original recipient. Only the session endpoint a
	// session.create created can reply, because the result of a control signal
	// already returns as its control reply.
	ReasonControlSignal
	// ReasonNoArrivalProof: no permission; nothing shows that the message arrived.
	ReasonNoArrivalProof
	// ReasonWindowClosed: no permission; the window has run out.
	ReasonWindowClosed
)

var reasonNames = [...]string{
	ReasonInvalid:         "invalid",
	ReasonDeliveredWindow: "delivered_window",
	ReasonArrivalWindow:   "arrival_window",
	ReasonDisabled:        "disabled",
	ReasonNoOutbound:      "no_outbound",
	ReasonNotSender:       "not_sender",
	ReasonNotRecipient:    "not_recipient",
	ReasonControlSignal:   "control_signal",
	ReasonNoArrivalProof:  "no_arrival_proof",
	ReasonWindowClosed:    "window_closed",
}

// String returns a stable machine-readable name.
func (r Reason) String() string {
	if int(r) < len(reasonNames) && reasonNames[r] != "" {
		return reasonNames[r]
	}
	return "invalid"
}

// Decision is the verdict on one reply.
type Decision struct {
	Reason Reason
	// Until is the end of the window; set only when the reply is allowed.
	Until time.Time
}

// Granted reports whether the reply is allowed. It is the value of
// authz.Request.Reply for the message. The target stores it with the record of
// the message it admitted at intake, and passes that stored value unchanged as
// authz.Request.Reply at the second check (authz.StageRecv): the window
// governs entry, not reading, so a reply admitted inside the window is still
// handed to its endpoint after the window has ended. An explicit deny still
// wins there.
func (d Decision) Granted() bool {
	return d.Reason == ReasonDeliveredWindow || d.Reason == ReasonArrivalWindow
}

// Request is a reply that arrived, with everything the decision reads.
type Request struct {
	// Now is the time of the decision (UTC).
	Now time.Time
	// Enabled is the node owner's switch for reply permission. The zero value
	// is off: a caller that forgets it gets no permission, not permission.
	Enabled bool
	// Local is this node: the sending node the evidence has to name.
	Local ident.NodeID
	// Replier is who sent the reply, from the connection identity and the
	// sending node's endpoint: never from the message body.
	Replier ident.Address
	// Target is the local endpoint the reply is addressed to.
	Target ident.EndpointID
	// ReplyTo is the message the reply says it answers.
	ReplyTo delivery.MessageID
	// Outbound is this node's own record of ReplyTo, nil if there is none. It
	// must be the record of ReplyTo.
	Outbound *envelope.Outbound
	// Evidence is the evidence list the reply carries, as decoded from the
	// envelope.
	Evidence []record.Record
	// Accepted is the network key this node accepts for Replier.Node, with its
	// revocation status. The evidence has to be signed by it.
	Accepted record.AcceptedNetworkKey
}

// Admission is the result of Admit: the verdict, and the sender's record after
// the evidence was applied. The caller commits Outbound and the outcomes in the
// one transaction in which it admits the reply, and then evaluates the policy
// with Request.Reply set to Decision.Granted().
type Admission struct {
	Decision Decision
	// Outbound is the record after applying the evidence. It is the request's
	// own record, unchanged, when there was no evidence, when the evidence was
	// discarded, or when the decision was made before the evidence was read.
	Outbound *envelope.Outbound
	// Handling is set when the evidence held a control reply and it was
	// handled (envelope.HandleControlReply).
	Handling *envelope.Handling
	// Report says what became of a status report among the evidence, and
	// Reported holds the outcomes of applying it.
	Report   envelope.ReportEffect
	Reported []delivery.Outcome
	// EvidenceError is why the evidence was discarded: it did not verify or
	// could not be applied. It does not change the decision, which was made as if
	// the reply had no evidence. A caller logs it and raises an alert: it means a
	// malfunctioning or hostile peer, or a network key that has changed since the
	// evidence was signed.
	EvidenceError error
}

// Admit decides one reply and applies the evidence it carries.
//
// The decision, in order:
//
//  1. The owner's switch is on.
//  2. The node has a record of the message the reply names, and the reply is
//     addressed to the endpoint that sent it.
//  3. The replier's node is the node of one of the message's recipients. Only
//     then is the evidence read: it is signed by that node and nobody else.
//  4. The evidence is verified and applied before the replier is looked up, so
//     that a reply from a session endpoint that arrives before the control
//     reply is judged on the state the control reply sets up. A control reply
//     goes through envelope.HandleControlReply, the same entry the pull path
//     uses, and a status report through Outbound.ApplyReport. The evidence
//     must be signed by the replier's node, name this node as the sender and
//     this message (the verifiers and both entries check it against the
//     record), and a report must be about the replier's own delivery. Evidence
//     that fails any of this is discarded whole, reported in EvidenceError, and
//     the decision goes on with the facts the record already holds: a reply
//     without valid evidence is judged as if it had none. It matters when the
//     evidence is stale rather than hostile (the target rotated its network key
//     since it signed it) and the record already shows the message arrived.
//  5. The replier is a recipient of the message, or the session endpoint a
//     verified control reply bound to a session.create that had a body. Anyone
//     else, including another endpoint of the same node, has no permission:
//     this is what makes a third party's reply_to useless. The original
//     recipient of a control signal has none either, whatever it holds: the
//     result of a control signal already returns as its control reply, and only
//     the session it created has anything to say back.
//  6. The window starts at the delivery time known for that replier; failing
//     that at the earliest signed report that shows arrival (accepted, handed
//     out or delivered); with neither there is no permission. It lasts Window,
//     and ends at the instant start+Window.
//
// Facts that arrive from the pull path before or after the reply are in
// Outbound already; the decision does not depend on the order in which they
// arrived.
func Admit(req Request) (Admission, error) {
	switch {
	case req.Now.IsZero() || req.Local.IsZero() || req.Replier.IsZero() || req.Target.IsZero() || req.ReplyTo == "":
		return Admission{}, fmt.Errorf("%w: time, local node, replier, target and reply_to are required", ErrInvalidRequest)
	case req.Outbound != nil && req.Outbound.Message != req.ReplyTo:
		return Admission{}, fmt.Errorf("%w: the outbound record is of %q, not %q", ErrInvalidRequest, req.Outbound.Message, req.ReplyTo)
	}
	adm := Admission{Outbound: req.Outbound}
	noPermission := func(r Reason) (Admission, error) {
		adm.Decision = Decision{Reason: r}
		return adm, nil
	}
	if !req.Enabled {
		return noPermission(ReasonDisabled)
	}
	if req.Outbound == nil {
		return noPermission(ReasonNoOutbound)
	}
	if err := req.Outbound.Validate(); err != nil {
		return Admission{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	if req.Outbound.From != req.Target {
		return noPermission(ReasonNotSender)
	}
	if !isRecipientNode(*req.Outbound, req.Replier.Node) {
		return noPermission(ReasonNotRecipient)
	}

	current := *req.Outbound
	if len(req.Evidence) > 0 {
		if next, err := applyEvidence(req, current, &adm); err != nil {
			adm.EvidenceError = err
		} else {
			current = next
			adm.Outbound = &current
		}
	}

	party, ok := current.Find(req.Replier.Endpoint)
	if !ok || party.Address != req.Replier {
		return noPermission(ReasonNotRecipient)
	}
	if current.Kind.IsControl() && !isDerived(current, party) {
		return noPermission(ReasonControlSignal)
	}
	start, reason := windowStart(party.Arrival)
	if reason == ReasonInvalid {
		return noPermission(ReasonNoArrivalProof)
	}
	until := start.Add(Window)
	if !req.Now.Before(until) {
		return noPermission(ReasonWindowClosed)
	}
	adm.Decision = Decision{Reason: reason, Until: until}
	return adm, nil
}

// isDerived reports whether t is the delivery of the initial message to the
// session a control signal created.
func isDerived(o envelope.Outbound, t envelope.Tracked) bool {
	return o.Derived != nil && o.Derived.Address == t.Address
}

// windowStart returns when the window began and which kind of fact began it.
func windowStart(a envelope.Arrival) (time.Time, Reason) {
	switch {
	case !a.DeliveredAt.IsZero():
		return a.DeliveredAt, ReasonDeliveredWindow
	case !a.ArrivedAt.IsZero():
		return a.ArrivedAt, ReasonArrivalWindow
	}
	return time.Time{}, ReasonInvalid
}

func isRecipientNode(o envelope.Outbound, node ident.NodeID) bool {
	for _, t := range o.Recipients {
		if t.Address.Node == node {
			return true
		}
	}
	return false
}

// applyEvidence verifies the evidence and applies it to o. It writes what was
// applied into adm and returns the new record. The evidence is applied whole or
// not at all: adm is written only after every record verified and applied, so a
// problem returns an error and leaves adm as it was.
func applyEvidence(req Request, o envelope.Outbound, adm *Admission) (envelope.Outbound, error) {
	if err := envelope.CheckEvidence(req.Evidence); err != nil {
		return o, err
	}
	var (
		signedReply  *envelope.SignedControlReply
		signedReport *envelope.SignedReport
	)
	node := req.Replier.Node
	for _, r := range req.Evidence {
		switch r.Type {
		case envelope.ControlReplyType:
			v, err := envelope.VerifyControlReply(r, node, req.Local, req.Accepted, req.Now)
			if err != nil {
				return o, fmt.Errorf("control reply: %w", err)
			}
			signedReply = &v
		case envelope.StatusReportType:
			v, err := envelope.VerifyStatusReport(r, node, req.Local, req.Accepted, req.Now)
			if err != nil {
				return o, fmt.Errorf("status report: %w", err)
			}
			if v.Report().Recipient != req.Replier.Endpoint {
				return o, fmt.Errorf("status report: %w: it is about another endpoint than the replier", envelope.ErrMismatch)
			}
			signedReport = &v
		}
	}
	if signedReply != nil {
		h, err := envelope.HandleControlReply(req.Now, o, *signedReply, signedReport)
		if err != nil {
			return o, fmt.Errorf("control reply: %w", err)
		}
		adm.Handling, adm.Report, adm.Reported = &h, h.Report, h.Reported
		return h.Outbound, nil
	}
	n, effect, outcomes, err := o.ApplyReport(req.Now, *signedReport)
	if err != nil {
		return o, fmt.Errorf("status report: %w", err)
	}
	adm.Report, adm.Reported = effect, outcomes
	return n, nil
}
