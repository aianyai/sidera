package authz

import (
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// TrustState is the connection-trust verdict about the sender, computed by the
// trust rules and handed in by the caller.
//
// This is a minimal local stand-in for the trust package's result: only the
// three outcomes and the direct/inherited distinction are read here.
type TrustState uint8

// The three trust outcomes. The zero value is invalid, so a caller that forgets
// to fill it in is told off instead of being let through.
const (
	// TrustAllowed: the sender is a cluster member.
	TrustAllowed TrustState = iota + 1
	// TrustSuspended: the sender would be a member but a guarantee on the path
	// has lapsed; it can recover without anyone acting.
	TrustSuspended
	// TrustDenied: the sender is not a member, or was revoked or released.
	TrustDenied
)

// ConnectionTrust is the trust input of a request.
type ConnectionTrust struct {
	State TrustState
	// Direct is true when the sender is a directly trusted node and false when
	// it is a member through inheritance. It is read when State is TrustAllowed.
	// A suspended sender is always treated as inherited, whatever Direct says: a
	// direct tie whose guarantee has lapsed is not a direct tie while it lapsed.
	Direct bool
}

// Sender names who is asking: a node, and the endpoint on it when the request
// comes from one. Node-level requests, such as a directory query or a relay
// reservation, leave Endpoint zero.
type Sender struct {
	Node     ident.NodeID
	Endpoint ident.EndpointID
}

// Target names what is asked about. Endpoint is zero for TargetNode and set
// otherwise.
type Target struct {
	Kind     TargetKind
	Endpoint ident.EndpointID
}

// Declaration is what the target endpoint (the agent itself) says about its own
// reception. Declarations can only narrow what the node policy allows; they
// never widen it.
type Declaration struct {
	// Paused: the endpoint accepts no messages for now.
	Paused bool
	// RefuseMultiRecipient: the endpoint rejects messages addressed to several
	// recipients.
	RefuseMultiRecipient bool
	// RestrictSenders: the endpoint accepts messages only from AcceptFrom.
	RestrictSenders bool
	AcceptFrom      []ident.Address
	// HiddenFromDirectory: the endpoint does not appear in directory listings.
	// It affects the see action only, never reachability.
	HiddenFromDirectory bool
}

// Stage says which of the two checks a request is evaluated for.
type Stage uint8

// The two checks. The zero value is invalid, so a caller that forgets to say
// which check it is making is told off instead of getting one of them silently.
const (
	// StageIntake is the check made when the message arrives. Whatever the
	// endpoint has declared about new messages applies, including a pause.
	StageIntake Stage = iota + 1
	// StageRecv is the check made when recv hands out the body. The endpoint is
	// pulling its own queued messages, so its pause does not apply: a pause
	// stops new intake, not the endpoint's reading of what it already holds.
	StageRecv
)

// Request is one thing a sender asks of this node. It serves both checks: at
// intake, and again when the body is handed out by recv.
type Request struct {
	// Now is the evaluation time (UTC). The caller reads the clock.
	Now   time.Time
	Trust ConnectionTrust
	// Stage says which check this is. See Stage.
	Stage Stage

	Sender Sender
	Action Action
	Target Target

	// BodySize is the size of the message body in bytes (message.send only).
	BodySize int64
	// Preset is the runtime preset to start (session.create only; required).
	Preset string
	// MultiRecipient is true when the message is addressed to several
	// recipients (message.send only).
	MultiRecipient bool

	// Reply is the caller's verified fact that this message is a reply inside an
	// outstanding reply permission: the evidence has been checked against the
	// node's own send records. It is read for message.send only.
	Reply bool

	// TargetDeclaration is the target endpoint's self-declaration, looked up by
	// the caller. The zero value declares nothing.
	TargetDeclaration Declaration
}

// Outcome is the three-way authorization result.
type Outcome uint8

// The outcomes.
const (
	// OutcomeAllow: proceed.
	OutcomeAllow Outcome = iota + 1
	// OutcomeSuspend: not now, but without a final answer. At intake the sender
	// is told to retry; at recv the message stays queued, neither handed out nor
	// prompted for.
	OutcomeSuspend
	// OutcomeReject: refused for good; the delivery ends as rejected.
	OutcomeReject
)

// String returns a readable name for the outcome.
func (o Outcome) String() string {
	switch o {
	case OutcomeAllow:
		return "allow"
	case OutcomeSuspend:
		return "suspend"
	case OutcomeReject:
		return "reject"
	}
	return "invalid"
}

// Reason says why a verdict came out as it did. Every reason belongs to exactly
// one outcome.
type Reason uint8

// The reasons.
const (
	// ReasonGranted: an allow rule covers the request.
	ReasonGranted Reason = iota + 1
	// ReasonReplyPermission: no allow rule covers the request, but it is a
	// reply within an outstanding reply permission.
	ReasonReplyPermission

	// ReasonTrustSuspended: connection trust is suspended.
	ReasonTrustSuspended

	// ReasonTrustDenied: connection trust is denied.
	ReasonTrustDenied
	// ReasonMalformedRequest: the request itself is malformed, or its trust
	// state is unset.
	ReasonMalformedRequest
	// ReasonExplicitDeny: a deny rule covers the request.
	ReasonExplicitDeny
	// ReasonNotAuthorized: no allow rule covers the request, or a restriction
	// admits only other nodes than the sender's.
	ReasonNotAuthorized
	// ReasonBodyTooLarge: allow rules cover the request, but the body exceeds
	// every body size cap among them.
	ReasonBodyTooLarge
	// ReasonPresetNotAllowed: allow rules cover the request, but none of them
	// grants the requested preset.
	ReasonPresetNotAllowed
	// ReasonEndpointPaused: the target endpoint declared itself paused and the
	// request is a new message at intake.
	ReasonEndpointPaused
	// ReasonEndpointRefusesMultiRecipient: the target endpoint declared that it
	// refuses multi-recipient messages.
	ReasonEndpointRefusesMultiRecipient
	// ReasonSenderNotAccepted: the target endpoint accepts only listed senders
	// and this one is not listed.
	ReasonSenderNotAccepted
	// ReasonEndpointHidden: the target endpoint declared itself hidden from the
	// directory.
	ReasonEndpointHidden
)

var reasonNames = [...]string{
	ReasonGranted:                       "granted",
	ReasonReplyPermission:               "reply_permission",
	ReasonTrustSuspended:                "trust_suspended",
	ReasonTrustDenied:                   "trust_denied",
	ReasonMalformedRequest:              "malformed_request",
	ReasonExplicitDeny:                  "explicit_deny",
	ReasonNotAuthorized:                 "not_authorized",
	ReasonBodyTooLarge:                  "body_too_large",
	ReasonPresetNotAllowed:              "preset_not_allowed",
	ReasonEndpointPaused:                "endpoint_paused",
	ReasonEndpointRefusesMultiRecipient: "endpoint_refuses_multi_recipient",
	ReasonSenderNotAccepted:             "sender_not_accepted",
	ReasonEndpointHidden:                "endpoint_hidden",
}

// String returns a stable machine-readable name.
func (r Reason) String() string {
	if int(r) >= len(reasonNames) || reasonNames[r] == "" {
		return "invalid"
	}
	return reasonNames[r]
}

// Outcome returns the outcome this reason belongs to.
func (r Reason) Outcome() Outcome {
	switch {
	case r >= ReasonGranted && r <= ReasonReplyPermission:
		return OutcomeAllow
	case r == ReasonTrustSuspended:
		return OutcomeSuspend
	case r >= ReasonTrustDenied && r <= ReasonEndpointHidden:
		return OutcomeReject
	}
	return 0
}

// RateKey identifies one counter of a stateful rate limiter: one sender doing
// one action against one target.
//
// Sender.Endpoint is set only when the cap comes from a rule that names that
// endpoint. Endpoint identifiers are chosen by the sending node and this node
// cannot verify them, so for every other cap a node would escape its limit by
// minting endpoint identifiers; those caps are counted per sending node and
// Sender.Endpoint is zero.
type RateKey struct {
	Sender Sender
	Target Target
	Action Action
}

// RateObligation is a duty the caller takes on with an allow: admit at most
// Limit requests under Key in any span of Window. This package does not count;
// a limiter elsewhere does.
type RateObligation struct {
	Key    RateKey
	Limit  uint32
	Window time.Duration
}

// Verdict is the result of Evaluate.
type Verdict struct {
	Outcome Outcome
	Reason  Reason
	// Obligations is non-empty only for an allow granted by a rule that carries
	// a rate cap, and never for a verified reply. The caller honours them at
	// the intake check only: at the recv check it ignores them, because the
	// message was already counted at intake.
	Obligations []RateObligation
}

func verdictFor(r Reason) Verdict { return Verdict{Outcome: r.Outcome(), Reason: r} }
