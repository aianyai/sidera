package reply

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// Window is how long reply permission lasts, counted from the start the sender
// can prove (see Request).
const Window = 24 * time.Hour

// ErrInvalidRequest reports an argument no correct caller produces: a missing
// time or identity, or an outbound record that is not the one named by ReplyTo.
// A reply that is merely not allowed is a Decision, not an error.
var ErrInvalidRequest = errors.New("invalid reply request")

// Reason says why a reply is allowed or not. Granted reasons say where the
// window started.
type Reason uint8

const (
	// ReasonInvalid is the zero value; it is never a decision.
	ReasonInvalid Reason = iota
	// ReasonDeliveredWindow: allowed; the window runs from the delivery time
	// the target reported.
	ReasonDeliveredWindow
	// ReasonArrivalWindow: allowed; no delivery time is known, so the window
	// runs from the issue time of the earliest signed report that shows the
	// message arrived.
	ReasonArrivalWindow
	// ReasonDisabled: refused; the node's owner turned reply permission off.
	ReasonDisabled
	// ReasonNoOutbound: refused; this node has no record of the message the
	// reply names.
	ReasonNoOutbound
	// ReasonNotSender: refused; the reply is not addressed to the endpoint that
	// sent the message.
	ReasonNotSender
	// ReasonNotRecipient: refused; the replier is neither a recipient of the
	// message nor the session endpoint its control signal created.
	ReasonNotRecipient
	// ReasonEvidenceInvalid: refused; the reply carries evidence that is not
	// signed by the target node, is about another message or recipient, or
	// contradicts what the sender knows.
	ReasonEvidenceInvalid
	// ReasonNoArrivalProof: refused; nothing shows that the message arrived.
	ReasonNoArrivalProof
	// ReasonWindowClosed: refused; the window has run out.
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
	ReasonEvidenceInvalid: "evidence_invalid",
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
// authz.Request.Reply for the message.
func (d Decision) Granted() bool {
	return d.Reason == ReasonDeliveredWindow || d.Reason == ReasonArrivalWindow
}

// Request is a reply that arrived, with everything the decision reads.
type Request struct {
	// Now is the time of the decision (UTC).
	Now time.Time
	// Enabled is the node owner's switch for reply permission. The zero value
	// is off: a caller that forgets it gets refusals, not permission.
	Enabled bool
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
// one transaction in which it admits or refuses the reply, and then evaluates
// the policy with Request.Reply set to Decision.Granted().
type Admission struct {
	Decision Decision
	// Outbound is the record after applying the evidence. It is the request's
	// own record, unchanged, when there was no evidence, when the evidence was
	// refused, or when the reply was refused before the evidence was read.
	Outbound *envelope.Outbound
	// Handling is set when the evidence held a control reply and it was
	// handled (envelope.HandleControlReply).
	Handling *envelope.Handling
	// Report says what became of a status report among the evidence, and
	// Reported holds the outcomes of applying it.
	Report   envelope.ReportEffect
	Reported []delivery.Outcome
	// EvidenceError is why the evidence was refused, when Decision.Reason is
	// ReasonEvidenceInvalid. For logs; callers decide on the reason.
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
//     must be signed by the replier's node, name this message (both entries
//     check it against the record), and a report must be about the replier's
//     own delivery. Evidence that is not is not
//     skipped: the reply is refused.
//  5. The replier is a recipient of the message, or the session endpoint a
//     verified control reply bound to a session.create that had a body.
//     Anyone else, including another endpoint of the same node, is refused:
//     this is what makes a third party's reply_to useless.
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
	case req.Now.IsZero() || req.Replier.IsZero() || req.Target.IsZero() || req.ReplyTo == "":
		return Admission{}, fmt.Errorf("%w: time, replier, target and reply_to are required", ErrInvalidRequest)
	case req.Outbound != nil && req.Outbound.Message != req.ReplyTo:
		return Admission{}, fmt.Errorf("%w: the outbound record is of %q, not %q", ErrInvalidRequest, req.Outbound.Message, req.ReplyTo)
	}
	adm := Admission{Outbound: req.Outbound}
	refuse := func(r Reason) (Admission, error) {
		adm.Decision = Decision{Reason: r}
		return adm, nil
	}
	if !req.Enabled {
		return refuse(ReasonDisabled)
	}
	if req.Outbound == nil {
		return refuse(ReasonNoOutbound)
	}
	if err := req.Outbound.Validate(); err != nil {
		return Admission{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	if req.Outbound.From != req.Target {
		return refuse(ReasonNotSender)
	}
	if !isRecipientNode(*req.Outbound, req.Replier.Node) {
		return refuse(ReasonNotRecipient)
	}

	current := *req.Outbound
	if len(req.Evidence) > 0 {
		next, err := applyEvidence(req, current, &adm)
		if err != nil {
			adm.EvidenceError = err
			adm.Handling, adm.Report, adm.Reported = nil, envelope.ReportNone, nil
			return refuse(ReasonEvidenceInvalid)
		}
		current = next
		adm.Outbound = &current
	}

	party, ok := current.Find(req.Replier.Endpoint)
	if !ok || party.Address != req.Replier {
		return refuse(ReasonNotRecipient)
	}
	start, reason := windowStart(party.Arrival)
	if reason == ReasonInvalid {
		return refuse(ReasonNoArrivalProof)
	}
	until := start.Add(Window)
	if !req.Now.Before(until) {
		return refuse(ReasonWindowClosed)
	}
	adm.Decision = Decision{Reason: reason, Until: until}
	return adm, nil
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
// applied into adm and returns the new record. Any problem is an error and
// leaves adm for the caller to reset.
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
			v, err := envelope.VerifyControlReply(r, node, req.Accepted, req.Now)
			if err != nil {
				return o, fmt.Errorf("control reply: %w", err)
			}
			signedReply = &v
		case envelope.StatusReportType:
			v, err := envelope.VerifyStatusReport(r, node, req.Accepted, req.Now)
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
