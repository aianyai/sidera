package envelope

import "errors"

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
	// ErrKind: the kind is not one of the five defined.
	ErrKind = errors.New("invalid kind")
	// ErrSeq: the per-recipient sequence numbers are malformed or do not cover
	// the recipients.
	ErrSeq = errors.New("invalid sequence numbers")
	// ErrReportBy: report_by is not delivery.ReportBy(accept_before).
	ErrReportBy = errors.New("report_by does not follow from accept_before")
	// ErrBody: the content type and body are inconsistent, too large, or not
	// allowed for the kind.
	ErrBody = errors.New("invalid content type or body")
	// ErrParams: the params are missing, present where the kind has none, or
	// invalid.
	ErrParams = errors.New("invalid params")
	// ErrEvidence: the evidence list is malformed, out of order, or without a
	// reply_to to refer to.
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
	// ErrMismatch: a report or reply does not refer to the message, recipient
	// or signal the sender's record has.
	ErrMismatch = errors.New("report or reply does not match the outbound record")
	// ErrDerivedConflict: a control reply names a session endpoint that
	// contradicts the one already bound for the message.
	ErrDerivedConflict = errors.New("control reply contradicts the bound session endpoint")
	// ErrFromFuture: a report claims a time later than now plus the clock
	// tolerance.
	ErrFromFuture = errors.New("report is dated in the future")
)
