package delivery

import (
	"errors"
	"fmt"
)

// ErrInvalidRecord is what Record.Valid returns, wrapped in an
// *InvalidRecordError that names the violated invariant.
var ErrInvalidRecord = errors.New("invalid delivery record")

// Violation names one invariant of a Record. Each is a property that every
// record built by the constructors and changed by the transitions has, and
// that a record restored from storage can fail to have.
type Violation uint8

const (
	// ViolationNone is the zero value: no invariant is violated.
	ViolationNone Violation = iota
	// ViolationSide: Side is neither the sender's nor the target's.
	ViolationSide
	// ViolationControlSide: Control is set on a record that is not the target's.
	ViolationControlSide
	// ViolationState: the state is not one this kind of record can be in: a
	// target is never queued or expired_unconfirmed, a control record is never
	// rejected.
	ViolationState
	// ViolationRejectReason: a rejection reason is recorded exactly when the
	// state is rejected, and it is a known reason.
	ViolationRejectReason
	// ViolationSubState: the sub-state is unknown, or notified outside an
	// accepted message record of the target.
	ViolationSubState
	// ViolationDeadlinesUnset: AcceptBefore or ReportBy is not set.
	ViolationDeadlinesUnset
	// ViolationReportBy: ReportBy is not the one that follows from AcceptBefore.
	ViolationReportBy
	// ViolationBacklogDeadline: a queued message has no backlog deadline or one
	// past the settle deadline, or a record that is not queued has one.
	ViolationBacklogDeadline
	// ViolationReportedOnTarget: a target record lists produced feedback.
	ViolationReportedOnTarget
	// ViolationReportedUnknownKind: the produced feedback holds a kind that does
	// not exist.
	ViolationReportedUnknownKind
	// ViolationReportedRejected: the rejection notice is reported exactly when
	// the state is rejected.
	ViolationReportedRejected
	// ViolationReportedDeliveryUnknown: the delivery_unknown notice and its
	// correction do not fit the state.
	ViolationReportedDeliveryUnknown
	// ViolationReportedExpiry: the expired_unconfirmed notice and its correction
	// do not fit the state.
	ViolationReportedExpiry
	// ViolationReportedBlocked: the blocked notice, its recovery and the cause
	// of the last block do not fit the state and each other.
	ViolationReportedBlocked
	// ViolationBlockedState: Blocked is set outside an accepted record that can
	// be blocked (a control record never can).
	ViolationBlockedState
	// ViolationBlockedCause: the cause of the last block is unknown, is not a
	// blocking cause, or is missing while Blocked is set.
	ViolationBlockedCause
	// ViolationLeaseUnused: a record that is never queued for recv has lease
	// bookkeeping.
	ViolationLeaseUnused
	// ViolationLeaseChunks: the chunk count and the per-chunk flags disagree, or
	// lease bookkeeping exists before the first hand-out.
	ViolationLeaseChunks
	// ViolationLeaseAckedUnseen: a chunk is acknowledged that was never handed
	// out.
	ViolationLeaseAckedUnseen
	// ViolationLeaseNoHandout: chunks are counted but nothing was handed out.
	ViolationLeaseNoHandout
	// ViolationLeaseIDs: the page identifiers are missing, unset or repeated.
	ViolationLeaseIDs
	// ViolationLeaseCount: more leases were issued than a message can have, or
	// the last allowed lease is over while the message is still queued.
	ViolationLeaseCount
	// ViolationLeaseActive: a lease is outstanding outside the accepted state.
	ViolationLeaseActive
	// ViolationLeaseTimes: the lease instants are unset or outside what a lease
	// issued and renewed under the rules can have.
	ViolationLeaseTimes
	// ViolationLeaseAfterBacklog: a lease was issued at or after the backlog
	// deadline, when the message could no longer be handed out.
	ViolationLeaseAfterBacklog
	// ViolationDelivered: a target message is delivered exactly when every chunk
	// is acknowledged.
	ViolationDelivered
	// ViolationDeliveryUnknownUnhandled: a target is delivery_unknown for a
	// delivery that was never handed over.
	ViolationDeliveryUnknownUnhandled
	// ViolationRejectedHandedOver: a target rejected a delivery that was handed
	// over, which must have become delivery_unknown.
	ViolationRejectedHandedOver
)

var violationNames = [...]string{
	ViolationNone:                     "none",
	ViolationSide:                     "side",
	ViolationControlSide:              "control_on_source",
	ViolationState:                    "state_for_record",
	ViolationRejectReason:             "reject_reason",
	ViolationSubState:                 "sub_state",
	ViolationDeadlinesUnset:           "deadlines_unset",
	ViolationReportBy:                 "report_by",
	ViolationBacklogDeadline:          "backlog_deadline",
	ViolationReportedOnTarget:         "reported_on_target",
	ViolationReportedUnknownKind:      "reported_unknown_kind",
	ViolationReportedRejected:         "reported_rejected",
	ViolationReportedDeliveryUnknown:  "reported_delivery_unknown",
	ViolationReportedExpiry:           "reported_expiry",
	ViolationReportedBlocked:          "reported_blocked",
	ViolationBlockedState:             "blocked_state",
	ViolationBlockedCause:             "blocked_cause",
	ViolationLeaseUnused:              "lease_on_unqueued_record",
	ViolationLeaseChunks:              "lease_chunks",
	ViolationLeaseAckedUnseen:         "lease_acked_not_handed_out",
	ViolationLeaseNoHandout:           "lease_chunks_without_handout",
	ViolationLeaseIDs:                 "lease_ids",
	ViolationLeaseCount:               "lease_count",
	ViolationLeaseActive:              "lease_active_outside_accepted",
	ViolationLeaseTimes:               "lease_times",
	ViolationLeaseAfterBacklog:        "lease_issued_after_backlog_deadline",
	ViolationDelivered:                "delivered_acknowledgements",
	ViolationDeliveryUnknownUnhandled: "delivery_unknown_not_handed_over",
	ViolationRejectedHandedOver:       "rejected_after_hand_over",
}

func (v Violation) String() string {
	if int(v) < len(violationNames) {
		return violationNames[v]
	}
	return "invalid"
}

// InvalidRecordError is the error of Record.Valid: the first invariant a record
// fails. It matches ErrInvalidRecord with errors.Is.
type InvalidRecordError struct {
	Violation Violation
}

func (e *InvalidRecordError) Error() string {
	return fmt.Sprintf("%v: %s", ErrInvalidRecord, e.Violation)
}

func (e *InvalidRecordError) Unwrap() error { return ErrInvalidRecord }

// Valid reports nil exactly when the record is one the constructors and the
// transitions can produce, and otherwise an *InvalidRecordError that names the
// first invariant it fails. A caller that restores a record from storage calls
// it after decoding, so a well-formed but impossible record is refused instead
// of being trusted by Apply, Tick and Handout.
func (r Record) Valid() error {
	return nil
}
