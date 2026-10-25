package delivery

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/timing"
)

// DefaultEndpointBacklogLimit is how many messages an endpoint may hold in its
// queue before further ones are refused. It is the node's queue limit divided by
// the node's endpoint scale, so one endpoint that is flooded can take at most
// its share of the node, and it is well above what ordinary traffic queues in
// the default queue time of 30 minutes. An endpoint's owner may set another limit.
const DefaultEndpointBacklogLimit = 200

// ErrBacklogLimit reports an endpoint backlog limit below one.
var ErrBacklogLimit = errors.New("endpoint backlog limit out of range")

// ValidateEndpointBacklogLimit checks an endpoint owner's backlog limit. Zero
// is no limit set, not "no limit": a limit that refuses every message or none
// is never what an unset value should mean.
func ValidateEndpointBacklogLimit(limit int) error {
	if limit < 1 {
		return fmt.Errorf("%w: %d (at least 1)", ErrBacklogLimit, limit)
	}
	return nil
}

// Backlog is the state of the recipient endpoint's queue at intake.
type Backlog struct {
	// Queued is the number of the endpoint's records that occupy a slot now,
	// counted with Record.OccupiesBacklogSlot.
	Queued int
	// Limit is the endpoint's backlog limit (ValidateEndpointBacklogLimit).
	Limit int
}

// backlogGoverned reports whether the backlog limit applies to traffic of this
// kind. It governs ordinary messages only: they are what a sender can pile up in
// an endpoint's queue. A control signal is executed, never queued, and refusing
// it would keep the owner from closing or interrupting an endpoint that is
// flooded. System feedback is exempt too: refusing a notice because the endpoint
// is behind would silence the anomaly it reports.
func backlogGoverned(traffic authz.TrafficKind) bool {
	return traffic == authz.TrafficMessage
}

// validIntakeTraffic reports whether Intake judges traffic of this kind: a
// message, a control signal or system feedback. A status report is taken by
// ApplyReport and a control reply by HandleControlReply, never by Intake, so the
// backlog limit never refuses them; trust maintenance is not delivered to an
// endpoint at all.
func validIntakeTraffic(traffic authz.TrafficKind) bool {
	switch traffic {
	case authz.TrafficMessage, authz.TrafficControlSignal, authz.TrafficSystemFeedback:
		return true
	default:
		return false
	}
}

// OccupiesBacklogSlot reports whether a record counts toward its endpoint's
// backlog. Only a target record that still waits to be handed out or
// acknowledged does: a control record is never queued, a delivered, rejected or
// handed-over record is no longer queued, and an exempt record (a notice of the
// system endpoint) does not count, so that notices the node itself sends can
// never take room from the messages other senders may queue. It reads the
// record alone: what the traffic was is settled when the record is made.
func (r Record) OccupiesBacklogSlot() bool {
	return r.hasQueue() && r.State == StateAccepted && !r.Exempt
}

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
//
// Exempt traffic (authz.TrafficKind.PolicyExempt) is not judged by the policy,
// so it carries no authorization verdict: auth must be the zero Authorization,
// and any verdict for it is invalid input, because a caller that made one has
// applied the policy to what the policy does not govern. Its deadline is still
// checked. For every other traffic a valid verdict is required.
//
// A message that arrives when the endpoint already holds Backlog.Limit queued
// messages is rejected as RejectBacklogFull. That is judged last: a sender that
// is not allowed to write to the endpoint is told so and learns nothing of its
// queue, and a suspension stores nothing, so there is nothing yet for the limit
// to bound. Other traffic is never refused by the limit (backlogGoverned), and
// for it the Backlog is not read.
func Intake(now, acceptBefore time.Time, traffic authz.TrafficKind, auth Authorization, backlog Backlog) (IntakeVerdict, error) {
	if !validIntakeTraffic(traffic) {
		return IntakeVerdict{}, fmt.Errorf("%w: traffic kind %d is not judged by Intake", ErrInvalidInput, traffic)
	}
	if traffic.PolicyExempt() {
		if auth != (Authorization{}) {
			return IntakeVerdict{}, fmt.Errorf("%w: traffic kind %d is exempt from the policy and carries no authorization verdict", ErrInvalidInput, traffic)
		}
	} else if err := auth.validate(); err != nil {
		return IntakeVerdict{}, err
	}
	if backlogGoverned(traffic) {
		if err := ValidateEndpointBacklogLimit(backlog.Limit); err != nil {
			return IntakeVerdict{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		if backlog.Queued < 0 {
			return IntakeVerdict{}, fmt.Errorf("%w: backlog count %d", ErrInvalidInput, backlog.Queued)
		}
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
	if backlogGoverned(traffic) && backlog.Queued >= backlog.Limit {
		return IntakeVerdict{Outcome: IntakeReject, Reject: RejectBacklogFull}, nil
	}
	return IntakeVerdict{Outcome: IntakeAccept}, nil
}
