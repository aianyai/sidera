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
	// ReasonEndpointDraining: the endpoint was closed and the deliveries queued
	// for it are not all closed yet. A retry after they are succeeds.
	ReasonEndpointDraining
	// ReasonInvalidArgument: a request value is not acceptable; the Field of
	// the Refusal names it.
	ReasonInvalidArgument
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

	reasonEnd
)

var reasonNames = [reasonEnd]string{
	ReasonUnauthenticated:    "unauthenticated",
	ReasonEndpointUnknown:    "endpoint_unknown",
	ReasonInstanceUnknown:    "instance_unknown",
	ReasonWrongEndpointKind:  "wrong_endpoint_kind",
	ReasonEndpointLimit:      "endpoint_limit",
	ReasonEndpointDraining:   "endpoint_draining",
	ReasonInvalidArgument:    "invalid_argument",
	ReasonNodeQueueFull:      "node_queue_full",
	ReasonStorageFull:        "storage_full",
	ReasonStorageUnavailable: "storage_unavailable",
	ReasonClockRollback:      "clock_rollback",
	ReasonLoopBusy:           "loop_busy",
	ReasonRecovering:         "recovering",
	ReasonShuttingDown:       "shutting_down",
	ReasonStorageFatal:       "storage_fatal",
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
	// Err is the cause, when there is one beyond the reason (a storage error).
	Err error
}

func (r *Refusal) Error() string {
	s := "engine: refused: " + r.Reason.String()
	switch {
	case r.Limit != 0:
		s += " (" + r.Limit.String() + ")"
	case r.Field != "":
		s += " (" + r.Field + ")"
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
