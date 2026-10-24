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
	// ViolationUnknownCause: the cause of an unknown result is set outside
	// delivery_unknown, is missing or unknown there, or is not one the record's
	// side and kind can hold.
	ViolationUnknownCause
	// ViolationLostShape: a record that replaces one lost to storage corruption
	// does not have the shape of one, or the marker is on a record that cannot be
	// one.
	ViolationLostShape
	// ViolationDeadlinesUnset: AcceptBefore or ReportBy is not set.
	ViolationDeadlinesUnset
	// ViolationReportBy: ReportBy is not the one that follows from AcceptBefore.
	ViolationReportBy
	// ViolationBacklogDeadline: a queued message has no backlog deadline or one
	// past the settle deadline, or a record that is not queued has one.
	ViolationBacklogDeadline
	// ViolationBlockedState: Blocked is set outside an accepted record that can
	// be blocked (a control record never can).
	ViolationBlockedState
	// ViolationBlockedCause: the cause of the last block is unknown, is not a
	// blocking cause, or is missing while Blocked is set.
	ViolationBlockedCause
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
	// ViolationLeaseTimes: the lease instants are unset, or the expiry is later
	// than LeaseRenewalCap after the issue time.
	ViolationLeaseTimes
	// ViolationLeaseAfterBacklog: a lease was issued at or after the backlog
	// deadline, when the message could no longer be handed out.
	ViolationLeaseAfterBacklog
	// ViolationDelivered: a target message is delivered exactly when every chunk
	// is acknowledged.
	ViolationDelivered
	// ViolationDeliveryUnknownUnhandled: a target is delivery_unknown for a
	// delivery that was never handed over, unless it replaces one lost to storage
	// corruption, which is never handed out.
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
	ViolationUnknownCause:             "unknown_cause",
	ViolationLostShape:                "lost_shape",
	ViolationDeadlinesUnset:           "deadlines_unset",
	ViolationReportBy:                 "report_by",
	ViolationBacklogDeadline:          "backlog_deadline",
	ViolationBlockedState:             "blocked_state",
	ViolationBlockedCause:             "blocked_cause",
	ViolationReportedOnTarget:         "reported_on_target",
	ViolationReportedUnknownKind:      "reported_unknown_kind",
	ViolationReportedRejected:         "reported_rejected",
	ViolationReportedDeliveryUnknown:  "reported_delivery_unknown",
	ViolationReportedExpiry:           "reported_expiry",
	ViolationReportedBlocked:          "reported_blocked",
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

// Valid reports nil for a record that has every invariant listed in
// Violation, and otherwise an *InvalidRecordError that names the first one it
// fails. Every record the constructors and the transitions produce is valid,
// under the caller guarantees of the package README, and every listed
// invariant is refused; the accepted set is not claimed to equal the reachable
// set, except for the feedback bookkeeping of a source record. A caller that
// restores a record from storage calls it after decoding and before any other
// method of the record, so a well-formed but impossible record is refused
// instead of being trusted by Apply, Tick, Handout and Ack: a lease whose chunk
// flags disagree with its chunk count would make Ack and Handout index out of
// range.
//
// The invariants are those of the transitions: what change clears when the
// state changes, what only the target or only the sender sets, and what a lease
// can look like after Handout, Renew and Tick. A record that replaces one lost
// to storage corruption is recognised by its Lost marker, which it keeps in every
// state it can reach, and is held to the shape NewLostRecord gives it instead of
// that of a delivery accepted normally; without the marker such a record would
// be judged corrupt again after it closed, and replaced and reported once more. The checks run in the order of
// the Violation constants, so the answer for a record with several faults does
// not depend on how many there are.
func (r Record) Valid() error {
	for _, check := range []func() Violation{
		r.checkKind, r.checkUnknownCause, r.checkLostShape, r.checkDeadlines, r.checkBlocked, r.checkReported, r.checkLease, r.checkOutcome,
	} {
		if v := check(); v != ViolationNone {
			return &InvalidRecordError{Violation: v}
		}
	}
	return nil
}

// stateAllowed reports whether a record of this side and kind can be in its
// state: a target starts at accepted and is never queued or expired_unconfirmed,
// and a control record is delivered or left unknown but never rejected, because
// it counts as handed over from acceptance (see closeForcibly).
func (r Record) stateAllowed() bool {
	switch r.State {
	case StateAccepted, StateDelivered, StateDeliveryUnknown, StateClosedUnconfirmed:
		return true
	case StateRejected:
		return !r.Control
	case StateQueued, StateExpiredUnconfirmed:
		return r.Side == SideSource
	}
	return false
}

// checkKind covers the fields that say what kind of record this is and in
// which state: side, control, state, rejection reason and sub-state.
func (r Record) checkKind() Violation {
	if !r.Side.known() {
		return ViolationSide
	}
	if r.Control && r.Side != SideTarget {
		return ViolationControlSide
	}
	if !r.stateAllowed() {
		return ViolationState
	}
	if r.State == StateRejected && r.Reject == RejectNone {
		return ViolationRejectReason
	}
	if r.State != StateRejected && r.Reject != RejectNone {
		return ViolationRejectReason
	}
	if !r.Reject.known() {
		return ViolationRejectReason
	}
	if !r.Sub.known() {
		return ViolationSubState
	}
	if r.Sub == SubNotified && !(r.hasQueue() && r.State == StateAccepted) {
		return ViolationSubState
	}
	return ViolationNone
}

// checkDeadlines covers AcceptBefore, ReportBy and BacklogDeadline. Only a
// message in the target's queue has a backlog deadline, and it never lies past
// SettleBy (see BacklogDeadline). A record that replaces a loss has none of the
// envelope's deadlines to compare, and checkLostShape holds it to its own.
func (r Record) checkDeadlines() Violation {
	if r.Lost {
		return ViolationNone
	}
	if r.AcceptBefore.IsZero() || r.ReportBy.IsZero() {
		return ViolationDeadlinesUnset
	}
	if !r.ReportBy.Equal(ReportBy(r.AcceptBefore)) {
		return ViolationReportBy
	}
	if r.hasQueue() && (r.BacklogDeadline.IsZero() || r.BacklogDeadline.After(SettleBy(r.ReportBy))) {
		return ViolationBacklogDeadline
	}
	if !r.hasQueue() && !r.BacklogDeadline.IsZero() {
		return ViolationBacklogDeadline
	}
	return ViolationNone
}

// checkBlocked covers the block flag and the cause of the last block. moveTo
// clears the flag when the state leaves accepted and keeps the cause, which the
// recovery notice names; a control record is never blocked.
func (r Record) checkBlocked() Violation {
	if r.Blocked && (r.State != StateAccepted || r.Control) {
		return ViolationBlockedState
	}
	if r.BlockedCause != CauseNone && !r.BlockedCause.isBlocked() {
		return ViolationBlockedCause
	}
	if r.Blocked && r.BlockedCause == CauseNone {
		return ViolationBlockedCause
	}
	if r.Control && r.BlockedCause != CauseNone {
		return ViolationBlockedCause
	}
	return ViolationNone
}

// checkReported covers the feedback a sender's record remembers having
// produced. The sets of kinds that can go with a state follow from
// TransitionFeedback and from the note functions, which add the unreachable,
// blocked and recovered kinds: a notice is produced on entering the state it
// reports and stays in the set afterwards, and a state is only left for a state
// of a higher rank.
func (r Record) checkReported() Violation {
	if r.Side == SideTarget && r.Reported != 0 {
		return ViolationReportedOnTarget
	}
	if !r.Reported.known() {
		return ViolationReportedUnknownKind
	}
	if r.Side != SideSource {
		return ViolationNone
	}
	// The one notice of a source replacement is checkLostShape's.
	if r.Lost {
		return ViolationNone
	}
	has, s := r.Reported.Has, r.State
	if has(KindRejected) != (s == StateRejected) {
		return ViolationReportedRejected
	}
	// delivery_unknown is reported on entering it, as the notice of its cause: the
	// handed-over notice, or the loss at the target that a status report told. The
	// notice of the sender's own loss belongs to a replacement alone, which
	// returned above. One delivery gets at most one of these notices, because
	// delivery_unknown is entered once. The states after it are delivered (which
	// reports the correction) and closed_unconfirmed.
	if has(KindLostAtSender) || (has(KindDeliveryUnknown) && has(KindLostAtTarget)) {
		return ViolationReportedDeliveryUnknown
	}
	unknownReported := has(KindDeliveryUnknown) || has(KindLostAtTarget)
	if s == StateDeliveryUnknown && (has(KindDeliveryUnknown) != (r.Unknown == UnknownHandedOver) || has(KindLostAtTarget) != (r.Unknown == UnknownLostAtTarget)) {
		return ViolationReportedDeliveryUnknown
	}
	if unknownReported && s != StateDeliveryUnknown && s != StateDelivered && s != StateClosedUnconfirmed {
		return ViolationReportedDeliveryUnknown
	}
	if has(KindUnknownCorrected) != (s == StateDelivered && unknownReported) {
		return ViolationReportedDeliveryUnknown
	}
	// expired_unconfirmed is reported on entering it from queued and corrected
	// on the way to accepted or delivered; a rejection or delivery_unknown that
	// follows replaces it, so those leave it uncorrected.
	if s == StateExpiredUnconfirmed && !has(KindExpiredUnconfirmed) {
		return ViolationReportedExpiry
	}
	if has(KindExpiredUnconfirmed) && s == StateQueued {
		return ViolationReportedExpiry
	}
	if has(KindExpiredCorrected) && !has(KindExpiredUnconfirmed) {
		return ViolationReportedExpiry
	}
	if has(KindExpiredCorrected) && (s == StateQueued || s == StateExpiredUnconfirmed) {
		return ViolationReportedExpiry
	}
	if s == StateAccepted && has(KindExpiredUnconfirmed) && !has(KindExpiredCorrected) {
		return ViolationReportedExpiry
	}
	if s == StateDelivered && has(KindExpiredUnconfirmed) && !has(KindExpiredCorrected) && !unknownReported {
		return ViolationReportedExpiry
	}
	// A block is noticed only in accepted, so the notice comes after queued and
	// expired_unconfirmed, and a record that was expired went through accepted,
	// which corrected the expiry. The cause of the last block is recorded with
	// the notice. Delivered reports the recovery if there was a block, and so
	// does unblocking while accepted.
	if has(KindDeliveryBlocked) && has(KindExpiredUnconfirmed) && !has(KindExpiredCorrected) {
		return ViolationReportedExpiry
	}
	if has(KindBlockedRecovered) && !has(KindDeliveryBlocked) {
		return ViolationReportedBlocked
	}
	if has(KindDeliveryBlocked) && (s == StateQueued || s == StateExpiredUnconfirmed) {
		return ViolationReportedBlocked
	}
	if has(KindDeliveryBlocked) != r.BlockedCause.isBlocked() {
		return ViolationReportedBlocked
	}
	if s == StateDelivered && has(KindDeliveryBlocked) && !has(KindBlockedRecovered) {
		return ViolationReportedBlocked
	}
	if s == StateAccepted && !r.Blocked && has(KindDeliveryBlocked) && !has(KindBlockedRecovered) {
		return ViolationReportedBlocked
	}
	return ViolationNone
}

// checkLease covers the pull-lease bookkeeping, which only a message in the
// target's queue has. The shapes are those Handout produces (chunk flags
// allocated together at the first hand-out, an identifier recorded once, times
// set when a lease is issued) as Renew and Tick leave them.
func (r Record) checkLease() Violation {
	l := r.Lease
	if !r.hasQueue() {
		if !l.isZero() {
			return ViolationLeaseUnused
		}
		return ViolationNone
	}
	if l.Chunks < 0 || len(l.HandedOut) != l.Chunks || len(l.Acked) != l.Chunks {
		return ViolationLeaseChunks
	}
	if l.Chunks == 0 {
		if !l.isZero() {
			return ViolationLeaseChunks
		}
		return ViolationNone
	}
	handedOut := false
	for i := range l.HandedOut {
		if l.Acked[i] && !l.HandedOut[i] {
			return ViolationLeaseAckedUnseen
		}
		handedOut = handedOut || l.HandedOut[i]
	}
	if l.Handouts < 1 || !handedOut {
		return ViolationLeaseNoHandout
	}
	if len(l.IDs) == 0 {
		return ViolationLeaseIDs
	}
	for i, id := range l.IDs {
		if id.IsZero() {
			return ViolationLeaseIDs
		}
		for _, earlier := range l.IDs[:i] {
			if earlier == id {
				return ViolationLeaseIDs
			}
		}
	}
	// The last allowed lease cannot end with the message still queued: Tick
	// makes it delivery_unknown, and Handout never issues another.
	if l.Handouts > MaxLeases || (r.State == StateAccepted && l.Handouts == MaxLeases && !l.Active) {
		return ViolationLeaseCount
	}
	if l.Active && r.State != StateAccepted {
		return ViolationLeaseActive
	}
	if l.IssuedAt.IsZero() || l.ExpiresAt.IsZero() || l.ExpiresAt.After(l.renewalLimit()) {
		return ViolationLeaseTimes
	}
	if !l.IssuedAt.Before(r.BacklogDeadline) {
		return ViolationLeaseAfterBacklog
	}
	return ViolationNone
}

// checkOutcome ties the state of a target to what was handed out and
// acknowledged: a message is delivered by its last acknowledgement only, and a
// delivery that was handed over is never rejected (see closeForcibly) while one
// that was not never becomes delivery_unknown.
func (r Record) checkOutcome() Violation {
	if r.Side != SideTarget {
		return ViolationNone
	}
	if r.hasQueue() {
		allAcked := r.Lease.Chunks > 0
		for _, acked := range r.Lease.Acked {
			allAcked = allAcked && acked
		}
		if (r.State == StateDelivered) != allAcked {
			return ViolationDelivered
		}
	}
	if r.State == StateDeliveryUnknown && !r.Lost && !r.handedOver() {
		return ViolationDeliveryUnknownUnhandled
	}
	if r.State == StateRejected && r.handedOver() {
		return ViolationRejectedHandedOver
	}
	return ViolationNone
}
