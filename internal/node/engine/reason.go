package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/node/transport"
)

// Reason is why an engine method refused to do what it was asked: a closed
// enumeration, so that a caller and a test tell the causes apart without
// reading text. The engine says what happened; the interface packages alone
// map a Reason to the category and code their wire form uses, so the words of
// the wire never enter the engine.
type Reason uint8

// The reasons. The zero value is not a reason.
const (
	// ReasonUnauthenticated: the caller is not a known endpoint: no caller was
	// given, the token is unknown or belongs to a closed endpoint.
	ReasonUnauthenticated Reason = iota + 1
	// ReasonEndpointUnknown: the endpoint does not exist, or is not the one the
	// adapter instance owns.
	ReasonEndpointUnknown
	// ReasonInstanceUnknown: no adapter instance of that name has been admitted.
	ReasonInstanceUnknown
	// ReasonWrongEndpointKind: the endpoint's kind cannot be used for this.
	ReasonWrongEndpointKind
	// ReasonEndpointLimit: registering the endpoint would exceed a limit; the
	// Limit of the Refusal says which.
	ReasonEndpointLimit
	// ReasonInvalidArgument: a request value is not acceptable; the Field of
	// the Refusal names it.
	ReasonInvalidArgument
	// ReasonOperationIDReused: the identifier of an administrative operation was
	// used before for a different request, so nothing is executed and the stored
	// result is not shown.
	ReasonOperationIDReused
	// ReasonNodeQueueFull: the node holds as many unfinished deliveries as it
	// may. Backpressure: nothing was recorded.
	ReasonNodeQueueFull
	// ReasonStorageFull: the free space of the volume is below the reserve, or
	// a write failed for lack of space. Backpressure: nothing was acknowledged.
	ReasonStorageFull
	// ReasonStorageUnavailable: a write failed in a way that a retry may cure.
	// Backpressure: nothing was acknowledged.
	ReasonStorageUnavailable
	// ReasonClockRollback: the wall clock was set back beyond the tolerance and
	// the owner has not cleared it, so nothing that depends on a deadline is
	// accepted.
	ReasonClockRollback
	// ReasonLoopBusy: the loop did not take the command in time.
	ReasonLoopBusy
	// ReasonRecovering: the engine is still starting.
	ReasonRecovering
	// ReasonShuttingDown: the engine is stopping or has stopped.
	ReasonShuttingDown
	// ReasonStorageFatal: the command met a fatal storage error; whether it took
	// effect is not known, and the engine has stopped.
	ReasonStorageFatal
	// ReasonIdempotencyConflict: the idempotency key was used before for other
	// content. Nothing was accepted.
	ReasonIdempotencyConflict
	// ReasonMessageConflict: a message with this identifier, from this sender, to
	// this recipient, was taken in before with other content. The record the node
	// holds is as it was; the envelope is refused.
	ReasonMessageConflict
	// ReasonMessageUnknown: the endpoint has no message with that identifier to
	// answer.
	ReasonMessageUnknown
	// ReasonTooLarge: the body is longer than the protocol allows.
	ReasonTooLarge
	// ReasonAlertUnknown: the node holds no alert with that identifier.
	ReasonAlertUnknown
	// ReasonIdentityRetired: the owner retired the identity of this node, so
	// nothing is signed in its name any more while the node goes down.
	ReasonIdentityRetired
	// ReasonInstallInProgress: another adapter package is being copied; one is
	// copied at a time, and nothing of this request was done.
	ReasonInstallInProgress
	// ReasonPlatformUnsupported: the package does not list the platform of this
	// machine, so it would be installed and then fail to start.
	ReasonPlatformUnsupported
	// ReasonProbeTokenUsed: the probe token was presented before; it is good once.
	ReasonProbeTokenUsed
	// ReasonProbeTokenExpired: the probe token was issued more than
	// ProbeTokenLifetime ago.
	ReasonProbeTokenExpired
	// ReasonInstallRepeated: the installation this request names is the one being
	// copied now, so the request is a resend of it that arrived too early; nothing
	// of it was done again. The interface says it as ReasonInstallInProgress, with
	// the words of a request that is still running.
	ReasonInstallRepeated
	// ReasonLeaseUnknown: the lease is not one that was handed out to this endpoint,
	// whoever else it was handed to.
	ReasonLeaseUnknown
	// ReasonAttemptUnknown: the attempt is not in flight for a session of the
	// adapter instance. It may belong to another instance or have settled.
	ReasonAttemptUnknown
	// ReasonDirectoryUnavailable: a directory query got no answer within its
	// budget or could not reach its target. A later query may succeed.
	ReasonDirectoryUnavailable
	// ReasonPairingPaused: the node stopped pairing after code mismatches were
	// reported, so it takes no pairing command now; the RetryAfter of the Refusal
	// says when it may again.
	ReasonPairingPaused
	// ReasonPairingInProgress: a pairing with that node is under way, or the same
	// request for a pairing is still running; nothing of this request was done.
	ReasonPairingInProgress
	// ReasonExpectationsFull: the list of expectations holds as many as it may, so
	// a new one is not entered.
	ReasonExpectationsFull
	// ReasonNodeUnreachable: the node the owner asked to pair with could not be
	// reached, or the pairing ended without a request saved and without the node's
	// refusal. Nothing is stored, so the owner can ask again.
	ReasonNodeUnreachable
	// ReasonNotPermitted: the actor may make the request but not have it do what
	// it would do here, which only the operation can tell once it runs (an
	// installation that replaces the package of an enabled instance is for the
	// local owner). Nothing of the request was done.
	ReasonNotPermitted
	// ReasonAttachUnsupported: the adapter of the instance did not declare that it
	// takes attached sessions, so a session a person opened cannot be attached to it.
	ReasonAttachUnsupported
	// ReasonNativeSessionCreated: the native session is one this node created and
	// controls, an open session endpoint or one whose release is still owed, so it is
	// not one a person opened and cannot be attached.
	ReasonNativeSessionCreated
	// ReasonSessionNotAttached: a request that only finds the endpoint of a session a
	// person opened (AttachRequest.FindOnly) met no open attached endpoint for it, and
	// made none.
	ReasonSessionNotAttached

	reasonEnd
)

var reasonNames = [reasonEnd]string{
	ReasonUnauthenticated:      "unauthenticated",
	ReasonEndpointUnknown:      "endpoint_unknown",
	ReasonInstanceUnknown:      "instance_unknown",
	ReasonWrongEndpointKind:    "wrong_endpoint_kind",
	ReasonEndpointLimit:        "endpoint_limit",
	ReasonInvalidArgument:      "invalid_argument",
	ReasonOperationIDReused:    "operation_id_reused",
	ReasonNodeQueueFull:        "node_queue_full",
	ReasonStorageFull:          "storage_full",
	ReasonStorageUnavailable:   "storage_unavailable",
	ReasonClockRollback:        "clock_rollback",
	ReasonLoopBusy:             "loop_busy",
	ReasonRecovering:           "recovering",
	ReasonShuttingDown:         "shutting_down",
	ReasonStorageFatal:         "storage_fatal",
	ReasonIdempotencyConflict:  "idempotency_conflict",
	ReasonMessageConflict:      "message_conflict",
	ReasonMessageUnknown:       "message_unknown",
	ReasonTooLarge:             "too_large",
	ReasonAlertUnknown:         "alert_unknown",
	ReasonIdentityRetired:      "identity_retired",
	ReasonInstallInProgress:    "install_in_progress",
	ReasonPlatformUnsupported:  "platform_unsupported",
	ReasonProbeTokenUsed:       "probe_token_used",
	ReasonProbeTokenExpired:    "probe_token_expired",
	ReasonInstallRepeated:      "install_repeated",
	ReasonLeaseUnknown:         "lease_unknown",
	ReasonAttemptUnknown:       "attempt_unknown",
	ReasonDirectoryUnavailable: "directory_unavailable",
	ReasonPairingPaused:        "pairing_paused",
	ReasonPairingInProgress:    "pairing_in_progress",
	ReasonExpectationsFull:     "expectations_full",
	ReasonNodeUnreachable:      "node_unreachable",
	ReasonNotPermitted:         "not_permitted",
	ReasonAttachUnsupported:    "attach_unsupported",
	ReasonNativeSessionCreated: "native_session_created",
	ReasonSessionNotAttached:   "session_not_attached",
}

// String returns the name of the reason; a value that is not a reason has none.
func (r Reason) String() string {
	if r > 0 && r < reasonEnd {
		return reasonNames[r]
	}
	return fmt.Sprintf("invalid(%d)", uint8(r))
}

// Reasons lists every reason, in order.
func Reasons() []Reason {
	out := make([]Reason, 0, reasonEnd-1)
	for r := Reason(1); r < reasonEnd; r++ {
		out = append(out, r)
	}
	return out
}

// Limit says which limit a ReasonEndpointLimit refusal hit.
type Limit uint8

// The limits of registering an endpoint. The zero value is no limit.
const (
	// LimitInstance: the adapter instance already owns as many direct endpoints
	// as its capabilities declare.
	LimitInstance Limit = iota + 1
	// LimitNode: the node already has as many endpoints as a node may have
	// (publication.MaxEndpointsPerNode), whatever their kind.
	LimitNode
)

// String returns the name of the limit.
func (l Limit) String() string {
	switch l {
	case LimitInstance:
		return "instance"
	case LimitNode:
		return "node"
	}
	return fmt.Sprintf("invalid(%d)", uint8(l))
}

// Detail says in which way a request value is not acceptable, for the refusals
// of ReasonInvalidArgument that a caller tells apart without reading the Field.
// The zero value says nothing beyond the reason: the Field names the value.
type Detail uint8

// The details of ReasonInvalidArgument.
const (
	// DetailEmptyPatch: a patch that changes nothing.
	DetailEmptyPatch Detail = iota + 1
	// DetailNodeMismatch: the node the owner named is not this node.
	DetailNodeMismatch
	// DetailDigestMismatch: the digest the owner named is not the digest of this
	// node's statement.
	DetailDigestMismatch
	// DetailProbeFrontendMismatch: the front end that presented a probe token is
	// not the one the token was issued for.
	DetailProbeFrontendMismatch
	// DetailSourceUnreadable: the directory the owner named as a package, or a
	// directory or file inside it, cannot be read.
	DetailSourceUnreadable
	// DetailCursorInvalid: a directory continuation is malformed, belongs to
	// another source endpoint, or was issued by another process instance.
	DetailCursorInvalid
	// DetailInstanceRequired: the request names no adapter instance and more than
	// one instance of the adapter is enabled, so the node cannot choose.
	DetailInstanceRequired

	detailEnd
)

var detailNames = [detailEnd]string{
	DetailEmptyPatch:            "empty_patch",
	DetailNodeMismatch:          "node_mismatch",
	DetailDigestMismatch:        "digest_mismatch",
	DetailProbeFrontendMismatch: "probe_frontend_mismatch",
	DetailSourceUnreadable:      "source_unreadable",
	DetailCursorInvalid:         "cursor_invalid",
	DetailInstanceRequired:      "instance_required",
}

// String returns the name of the detail; the zero value has none.
func (d Detail) String() string {
	if d > 0 && d < detailEnd {
		return detailNames[d]
	}
	if d == 0 {
		return ""
	}
	return fmt.Sprintf("invalid(%d)", uint8(d))
}

// Details lists every detail, in order.
func Details() []Detail {
	out := make([]Detail, 0, detailEnd-1)
	for d := Detail(1); d < detailEnd; d++ {
		out = append(out, d)
	}
	return out
}

// Refusal is the error of an engine method that did not do what it was asked
// for an expected reason. Anything that is not a Refusal is an unexpected
// failure of the engine itself.
type Refusal struct {
	Reason Reason
	// Limit says which limit was hit; set for ReasonEndpointLimit only.
	Limit Limit
	// Field names the request value that was not acceptable; set for
	// ReasonInvalidArgument only.
	Field string
	// Detail says in which way it was not acceptable, when one of the details
	// applies; set for ReasonInvalidArgument only.
	Detail Detail
	// Path is where in what the request named the fault is, when the request names
	// a place that holds more than one value: the file or directory inside the
	// package that cannot be read, set for DetailSourceUnreadable only, and empty
	// when the package itself is the fault.
	Path string
	// RetryAfter says how long until the refused request may be made again; set for
	// ReasonPairingPaused only.
	RetryAfter time.Duration
	// Err is the cause, when there is one beyond the reason (a storage error).
	Err error
}

func (r *Refusal) Error() string {
	s := "engine: refused: " + r.Reason.String()
	switch {
	case r.Limit != 0:
		s += " (" + r.Limit.String() + ")"
	case r.Field != "" && r.Detail != 0:
		s += " (" + r.Field + ": " + r.Detail.String() + ")"
	case r.Field != "":
		s += " (" + r.Field + ")"
	case r.Detail != 0:
		s += " (" + r.Detail.String() + ")"
	}
	if r.Err != nil {
		s += ": " + r.Err.Error()
	}
	return s
}

// Unwrap returns the cause.
func (r *Refusal) Unwrap() error { return r.Err }

// ReasonOf returns the reason of a refusal found in err's chain.
func ReasonOf(err error) (Reason, bool) {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Reason, true
	}
	return 0, false
}

// reasonByName returns the reason that has the name, the inverse of String.
func reasonByName(name string) (Reason, bool) {
	for r := Reason(1); r < reasonEnd; r++ {
		if reasonNames[r] == name {
			return r, true
		}
	}
	return 0, false
}

// callReasonOf returns the reason a transport call was refused for: that of a
// refusal the target's engine returned in this process, or the reason a remote
// target named, mapped back by name. A name this engine does not know has no
// reason, so the call is read like any other error of the target.
//
// It is for the error of a call to another node only. A name that came over a
// wire says what the target did, not what this node did, so it must never stand
// where ReasonOf is read for a failure of this node's own work, a fatal storage
// error above all.
func callReasonOf(err error) (Reason, bool) {
	if r, ok := ReasonOf(err); ok {
		return r, true
	}
	var remote transport.Refused
	if errors.As(err, &remote) {
		return reasonByName(remote.Reason)
	}
	return 0, false
}

func refuse(r Reason) *Refusal { return &Refusal{Reason: r} }
