package envelope

import (
	"errors"
	"fmt"
)

// Errors returned by this package. They are wrapped with context; match them
// with errors.Is.
var (
	// ErrLimits: the Limits given are unset or not positive.
	ErrLimits = errors.New("envelope limits are not set")
	// ErrType: the envelope's type string is not the one this package reads.
	ErrType = errors.New("wrong envelope type")
	// ErrID: a message id or thread is not 1 to MaxIDLen bytes of visible ASCII.
	ErrID = errors.New("invalid message id")
	// ErrRecipients: the recipient list is empty, too long, repeats an address
	// or an endpoint, or does not fit the kind, the multi-recipient flag or the
	// node it is read for.
	ErrRecipients = errors.New("invalid recipients")
	// ErrSystemRecipient: a recipient is the system endpoint, which only sends
	// (a node's own channel feedback) and is never addressed. It wraps
	// ErrRecipients, so a caller that must tell it from other recipient faults
	// matches it first.
	ErrSystemRecipient = fmt.Errorf("%w: the system endpoint is not a recipient", ErrRecipients)
	// ErrKind: the kind is not one of the five defined.
	ErrKind = errors.New("invalid kind")
	// ErrSeq: the per-recipient sequence numbers are malformed or do not cover
	// the recipients.
	ErrSeq = errors.New("invalid sequence numbers")
	// ErrReportBy: report_by is not delivery.ReportBy(accept_before).
	ErrReportBy = errors.New("report_by does not follow from accept_before")
	// ErrBody: the content type and body are inconsistent or not allowed for the
	// kind, or the body is not valid UTF-8. A body that is too large is the wrapped
	// case ErrBodyTooLarge.
	ErrBody = errors.New("invalid content type or body")
	// ErrBodyTooLarge: the body is longer than Limits.MaxBody. It wraps ErrBody,
	// so a caller that must tell too large from malformed (one is reported to the
	// sender as too_large, the other as malformed) matches it first.
	ErrBodyTooLarge = fmt.Errorf("%w: body is larger than the limit", ErrBody)
	// ErrParams: the params are missing, present where the kind has none, or
	// invalid.
	ErrParams = errors.New("invalid params")
	// ErrReplyTo: a control signal carries a reply_to; only a message answers
	// another message, and the result of a control signal returns as its
	// control reply.
	ErrReplyTo = errors.New("invalid reply_to")
	// ErrEvidence: the evidence list is malformed, out of order, without a
	// reply_to to refer to, or on a control signal.
	ErrEvidence = errors.New("invalid evidence")

	// ErrReport: the payload of a status report is invalid.
	ErrReport = errors.New("invalid status report")
	// ErrControlReply: the payload of a control reply is invalid.
	ErrControlReply = errors.New("invalid control reply")

	// ErrNotVerified: a record that is not the result of record.Verify was passed
	// where a verified one is required.
	ErrNotVerified = errors.New("record is not verified")
	// ErrOutbound: the sender's record of a message is inconsistent.
	ErrOutbound = errors.New("invalid outbound record")
	// ErrMismatch: a report or reply does not refer to the message, recipient,
	// signal or sending node the sender's record has.
	ErrMismatch = errors.New("report or reply does not match the outbound record")
	// ErrPeerContradiction: a signed statement of the peer contradicts what the
	// sender holds: a control reply that differs from the one already accepted
	// for the signal, or a reply or report whose state cannot follow the
	// delivery's. The peer broke the contract: the sender stops pulling from it
	// for the message and logs it. The error wraps the specific cause.
	ErrPeerContradiction = errors.New("peer contradicts its own earlier statement or the sender's record")
	// ErrFromFuture: a report claims a time later than now plus the clock
	// tolerance.
	ErrFromFuture = errors.New("report is dated in the future")
	// ErrObservation: the event offered to Outbound.ApplyObservation is not
	// something a sender observes itself. A fact the target reports reaches the
	// record through ApplyReport or ApplyNoRecord, never through here.
	ErrObservation = errors.New("event is not an observation of the sender")
)
