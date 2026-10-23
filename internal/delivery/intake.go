package delivery

import (
	"time"

	"github.com/aianyai/sidera/internal/timing"
)

// IntakeOutcome is the target's answer to a first-time request.
type IntakeOutcome uint8

const (
	// IntakeAccept: store the message and answer with a receipt.
	IntakeAccept IntakeOutcome = iota + 1
	// IntakeReject: refuse with the verdict's reason; the delivery is rejected.
	IntakeReject
	// IntakeSuspended: answer with the non-final "suspended" result and store
	// nothing. The sender keeps retrying until accept_before; the suspension
	// produces no rejection.
	IntakeSuspended
)

// IntakeVerdict is the result of Intake.
type IntakeVerdict struct {
	Outcome IntakeOutcome
	// Reject is the reason when Outcome is IntakeReject.
	Reject RejectReason
}

// Intake validates the delivery deadline of a request that deduplication found
// to be new, then applies the authorization verdict. It does not run for a
// retry of a known message: that gets the original record back, even after the
// deadline.
//
// A deadline that has passed is refused so a stale request cannot start a
// delivery nobody waits for. A deadline further away than now plus
// MaxAcceptBefore plus the clock tolerance is refused so a sender cannot make
// the target keep records for longer than the protocol allows. The checks on the
// deadline come before authorization: a suspension can only be retried until
// the deadline, and an expired request has none left. A refusal is reported
// with the reason the authorization carries.
func Intake(now, acceptBefore time.Time, auth Authorization) (IntakeVerdict, error) {
	if err := auth.validate(); err != nil {
		return IntakeVerdict{}, err
	}
	switch {
	case acceptBefore.IsZero():
		return IntakeVerdict{Outcome: IntakeReject, Reject: RejectMalformed}, nil
	case now.After(acceptBefore):
		return IntakeVerdict{Outcome: IntakeReject, Reject: RejectExpired}, nil
	case acceptBefore.After(now.Add(MaxAcceptBefore + timing.ClockTolerance)):
		return IntakeVerdict{Outcome: IntakeReject, Reject: RejectDeadlineOutOfRange}, nil
	}
	switch auth.Kind {
	case AuthorizationDeny:
		return IntakeVerdict{Outcome: IntakeReject, Reject: auth.Reject}, nil
	case AuthorizationSuspend:
		return IntakeVerdict{Outcome: IntakeSuspended}, nil
	}
	return IntakeVerdict{Outcome: IntakeAccept}, nil
}
