package engine

import (
	"errors"
	"fmt"
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

	reasonEnd
)

var reasonNames = [reasonEnd]string{
	ReasonUnauthenticated:     "unauthenticated",
	ReasonEndpointUnknown:     "endpoint_unknown",
	ReasonInstanceUnknown:     "instance_unknown",
	ReasonWrongEndpointKind:   "wrong_endpoint_kind",
	ReasonEndpointLimit:       "endpoint_limit",
	ReasonInvalidArgument:     "invalid_argument",
	ReasonOperationIDReused:   "operation_id_reused",
	ReasonNodeQueueFull:       "node_queue_full",
	ReasonStorageFull:         "storage_full",
	ReasonStorageUnavailable:  "storage_unavailable",
	ReasonClockRollback:       "clock_rollback",
	ReasonLoopBusy:            "loop_busy",
	ReasonRecovering:          "recovering",
	ReasonShuttingDown:        "shutting_down",
	ReasonStorageFatal:        "storage_fatal",
	ReasonIdempotencyConflict: "idempotency_conflict",
	ReasonMessageConflict:     "message_conflict",
	ReasonMessageUnknown:      "message_unknown",
	ReasonTooLarge:            "too_large",
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

	detailEnd
)

var detailNames = [detailEnd]string{DetailEmptyPatch: "empty_patch"}

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

func refuse(r Reason) *Refusal { return &Refusal{Reason: r} }
