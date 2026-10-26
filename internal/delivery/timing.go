package delivery

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/timing"
)

// Decided timing values. They are protocol constants, not node settings: the
// sender computes ReportBy from the caps below and every target must reach the
// same value from the same envelope.
const (
	// DefaultAcceptBefore is the delivery deadline a sender uses when the
	// caller names none.
	DefaultAcceptBefore = 10 * time.Minute
	// MaxAcceptBefore is how far in the future a delivery deadline may lie.
	MaxAcceptBefore = time.Hour

	// UnreachableHintAfter is how long contact with the target must have been
	// failing before the sender hears about it.
	UnreachableHintAfter = 2 * time.Minute

	// DefaultEndpointMaxQueue is the longest an endpoint keeps a message
	// queued unless its owner chooses otherwise.
	DefaultEndpointMaxQueue = 30 * time.Minute
	// MaxQueueCap is the protocol cap on any endpoint's longest queue time.
	MaxQueueCap = 2 * time.Hour

	// LeaseDuration is the initial life of a lease handed out by recv.
	LeaseDuration = 5 * time.Minute
	// LeaseRenewalCap caps the total life of one lease, measured from the
	// moment it was issued, however often it is renewed.
	LeaseRenewalCap = 15 * time.Minute
	// MaxRedeliveries is how many times a message whose lease expired
	// unacknowledged is handed out again before its outcome is declared
	// unknown.
	MaxRedeliveries = 3

	// ReportBySafetyMargin is how long before ReportBy the target must already
	// have settled every delivery, so the sender can still pull the result.
	ReportBySafetyMargin = 5 * time.Minute
)

// MaxLeases is how many leases one message can receive: the first hand-out and
// its redeliveries.
const MaxLeases = 1 + MaxRedeliveries

// MaxRedeliveryPeriod is the longest time a message can spend under leases. It
// follows only from the lease life cap and the lease count: every one of the
// MaxLeases leases may live for LeaseRenewalCap.
const MaxRedeliveryPeriod = MaxLeases * LeaseRenewalCap

// ReportByOffset is the distance from accept_before to report_by: the longest
// queue time, then the longest time under leases, then the clock tolerance.
const ReportByOffset = MaxQueueCap + MaxRedeliveryPeriod + timing.ClockTolerance

// ReportBy returns the time until which the sender keeps pulling the state of a
// delivery whose delivery deadline is acceptBefore. After it every delivery
// that is not final becomes closed_unconfirmed.
func ReportBy(acceptBefore time.Time) time.Time {
	return acceptBefore.Add(ReportByOffset)
}

// LostReportBy returns the report_by a record that replaces one lost to storage
// corruption is kept and reported until: an upper bound of the report_by of any
// delivery accepted no later than discoveredAt. The bound is that no accepted
// deadline lies later than the acceptance time plus MaxAcceptBefore plus the
// clock tolerance, and the lost record was accepted no later than discoveredAt.
// Nothing in this package enforces it for the sender's own record: the caller
// must not accept from its own caller a deadline the target's Intake would
// refuse (see the README).
func LostReportBy(discoveredAt time.Time) time.Time {
	return ReportBy(discoveredAt.Add(MaxAcceptBefore + timing.ClockTolerance))
}

// SettleBy returns the instant by which the target must have given every
// delivery with this report_by a final state or a reported non-final one.
func SettleBy(reportBy time.Time) time.Time {
	return reportBy.Add(-ReportBySafetyMargin)
}

// DedupRetainUntil returns how long the target keeps a deduplication record.
// It must cover every retry the sender can still make: a retry after report_by
// cannot arrive within its delivery deadline, so forgetting the record then is
// safe. It does not bound the stored reply of a control signal: that reply
// outlives the record and is kept until reply.RetainFactsUntil, because
// reply permission still reads it, so storage must not purge it with the
// deduplication record.
func DedupRetainUntil(reportBy time.Time) time.Time {
	return reportBy
}

// RecordRetainUntil is the earliest instant a target may forget its record of
// a delivery with this report_by. A source pulls until report_by on its own
// clock and reads a missing record as a loss, so the target keeps the record,
// and answers pulls from it, for the clock tolerance after report_by.
func RecordRetainUntil(reportBy time.Time) time.Time {
	return reportBy.Add(timing.ClockTolerance)
}

// IdempotencyKeyRetainUntil returns how long the sender's node remembers a
// caller-supplied idempotency key. It is never shorter than the target's
// deduplication record, so a caller's retry is recognized for as long as the
// target would recognize it.
func IdempotencyKeyRetainUntil(reportBy time.Time) time.Time {
	return DedupRetainUntil(reportBy)
}

// StatusReportRetainUntil returns how long the target keeps a status report
// created at createdAt. The report must stay pullable for at least the whole
// report_by window counted from its creation, and in any case until report_by,
// because a report created early in a long window is still pulled until the
// end of it.
func StatusReportRetainUntil(createdAt, reportBy time.Time) time.Time {
	byWindow := createdAt.Add(ReportByOffset)
	if byWindow.After(reportBy) {
		return byWindow
	}
	return reportBy
}

// ErrMaxQueue reports an endpoint queue time outside (0, MaxQueueCap].
var ErrMaxQueue = errors.New("endpoint max queue time out of range")

// ValidateEndpointMaxQueue checks an endpoint owner's longest queue time
// against the protocol cap.
func ValidateEndpointMaxQueue(d time.Duration) error {
	if d <= 0 || d > MaxQueueCap {
		return fmt.Errorf("%w: %v (cap %v)", ErrMaxQueue, d, MaxQueueCap)
	}
	return nil
}

// BacklogDeadline returns when a message accepted at acceptedAt and still
// queued becomes rejected with RejectBacklogExpired: the endpoint's longest
// queue time, but never later than SettleBy(reportBy). The second bound keeps
// the guarantee that the sender hears about every delivery before it stops
// asking, even when the endpoint's own limit would run past it.
func BacklogDeadline(acceptedAt, reportBy time.Time, endpointMaxQueue time.Duration) time.Time {
	d := acceptedAt.Add(endpointMaxQueue)
	if settle := SettleBy(reportBy); d.After(settle) {
		return settle
	}
	return d
}

// ExpiryJudgeableAfter returns the instant after which the sender may judge a
// delivery expired_unconfirmed, provided an active query after that instant
// found no receipt. The tolerance covers a target clock that runs behind.
func ExpiryJudgeableAfter(acceptBefore time.Time) time.Time {
	return acceptBefore.Add(timing.ClockTolerance)
}
