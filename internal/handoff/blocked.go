package handoff

import "time"

// DefaultBlockedThreshold is how long prompts must have been failing to reach
// a session before delivery counts as blocked. Messages move between agents
// within minutes, so a wait of an hour or a day would hide a dead adapter for
// longer than most conversations last.
const DefaultBlockedThreshold = 5 * time.Minute

// BlockedParams holds the values of the blocked-delivery rule.
type BlockedParams struct {
	// Threshold is how long prompts must have been failing before delivery
	// counts as blocked. The boundary itself is not yet blocked.
	Threshold time.Duration
}

// DefaultBlockedParams returns the decided values.
func DefaultBlockedParams() BlockedParams {
	return BlockedParams{Threshold: DefaultBlockedThreshold}
}

// Cause is why prompts cannot reach a session.
type Cause int

const (
	// CauseAdapterOffline: the adapter that controls the session is not connected.
	CauseAdapterOffline Cause = iota + 1
	// CauseAuthInvalid: the runtime refuses the session's credentials.
	CauseAuthInvalid
	// CauseServiceUnavailable: the runtime's native service does not answer.
	CauseServiceUnavailable
)

// Signals are the causes the adapter and the daemon currently observe.
type Signals struct {
	AdapterOffline     bool
	AuthInvalid        bool
	ServiceUnavailable bool
	// Since is when the active cause started being continuously true. Zero
	// means the start is unknown: AssessBlocked then measures from the arrival
	// of the oldest waiting message, which can only overstate how long the
	// cause has lasted. Setting it makes the measure exact when the cause began
	// after that message arrived.
	Since time.Time
}

// cause names the active signal. When several are active the first of
// AdapterOffline, AuthInvalid, ServiceUnavailable wins: an offline adapter
// cannot report on credentials or on the service behind it, so it is the
// signal that is always trustworthy.
func (s Signals) cause() (Cause, bool) {
	switch {
	case s.AdapterOffline:
		return CauseAdapterOffline, true
	case s.AuthInvalid:
		return CauseAuthInvalid, true
	case s.ServiceUnavailable:
		return CauseServiceUnavailable, true
	}
	return 0, false
}

// BlockedReport is the blocked state the caller has already reported for an
// endpoint. The zero value means nothing is reported.
type BlockedReport struct {
	Cause Cause
	Since time.Time
}

func (r BlockedReport) reported() bool { return r.Cause != 0 }

// BlockedInput is everything AssessBlocked reads about one endpoint.
type BlockedInput struct {
	// Attempts is the endpoint's prompt attempts in ascending time order.
	Attempts []Attempt
	// OldestPendingAt is when the oldest message still waiting for the
	// endpoint arrived; zero if none waits. Attempts before it belong to
	// earlier messages and say nothing about the ones waiting now.
	OldestPendingAt time.Time
	Signals         Signals
	// Reported is what the caller last reported; the caller stores the
	// Cause and Since of each BlockedNew verdict here and clears it on
	// BlockedRecovered.
	Reported BlockedReport
}

// BlockedStatus is the verdict of AssessBlocked.
type BlockedStatus int

const (
	// NotBlocked: delivery is not blocked and nothing was reported.
	NotBlocked BlockedStatus = iota + 1
	// BlockedNew: delivery has just become blocked. Report it, once.
	BlockedNew
	// BlockedOngoing: delivery is blocked and that was already reported.
	BlockedOngoing
	// BlockedRecovered: delivery was reported blocked and is no longer. Report
	// the correction, once.
	BlockedRecovered
)

// BlockedReason says why AssessBlocked reached its status.
type BlockedReason int

const (
	// ReasonPersistentFailure: prompts have failed for longer than the
	// threshold and an active cause explains it.
	ReasonPersistentFailure BlockedReason = iota + 1
	// ReasonNothingPending: no message waits, so nothing is stuck.
	ReasonNothingPending
	// ReasonNoFailures: no prompt for the waiting messages has failed since
	// the last delivered one, and no active cause has kept prompts from being
	// delivered. A busy agent whose adapter is online, or one that ignores a
	// delivered prompt, ends here: its prompts reach the session.
	ReasonNoFailures
	// ReasonBelowThreshold: prompts are failing but not for long enough yet.
	ReasonBelowThreshold
	// ReasonNoKnownCause: prompts are failing but no active signal explains it.
	// The failure may be passing, and without a cause there is nothing useful
	// to tell the sender.
	ReasonNoKnownCause
)

// BlockedVerdict is the result of AssessBlocked. Cause and Since are set for
// BlockedNew and BlockedOngoing, and for BlockedRecovered they are the ones
// that were reported.
type BlockedVerdict struct {
	Status BlockedStatus
	Reason BlockedReason
	Cause  Cause
	Since  time.Time
}

// AssessBlocked decides whether delivery to an endpoint is blocked: whether
// prompts have been failing to reach its session for longer than the
// threshold because of a cause that is still active.
//
// Only a failure of the prompt path counts. An agent that is busy or that
// reads nothing after a delivered prompt is behaving as an agent and has no
// active cause, so it is never reported. Attempts with an unknown outcome
// neither prove a failure nor break a run of them; a delivered attempt ends
// the run.
//
// The failing run does not need failed attempts to be recorded. While a cause
// is active, prompts cannot reach the session, whether or not one was tried:
// a busy agent is not prompted at all, and the adapter can go offline during
// its turn. So the run also starts when the cause did, or when the oldest
// waiting message arrived if that was later, provided no prompt was delivered
// since; a failed attempt earlier than that starts it earlier.
//
// The status is edge-triggered through input.Reported, so a blocked period is
// reported once and its recovery once, however often this is evaluated.
func AssessBlocked(in BlockedInput, p BlockedParams, now time.Time) BlockedVerdict {
	since, reason := failingSince(in, p, now)
	cause, hasCause := in.Signals.cause()
	if reason == 0 && !hasCause {
		reason = ReasonNoKnownCause
	}

	if reason == 0 {
		if in.Reported.reported() {
			return BlockedVerdict{Status: BlockedOngoing, Reason: ReasonPersistentFailure,
				Cause: in.Reported.Cause, Since: in.Reported.Since}
		}
		return BlockedVerdict{Status: BlockedNew, Reason: ReasonPersistentFailure, Cause: cause, Since: since}
	}
	if in.Reported.reported() {
		return BlockedVerdict{Status: BlockedRecovered, Reason: reason,
			Cause: in.Reported.Cause, Since: in.Reported.Since}
	}
	return BlockedVerdict{Status: NotBlocked, Reason: reason}
}

// failingSince returns when the current run of failed prompts began. A
// non-zero reason means there is no failing run longer than the threshold.
func failingSince(in BlockedInput, p BlockedParams, now time.Time) (time.Time, BlockedReason) {
	if in.OldestPendingAt.IsZero() {
		return time.Time{}, ReasonNothingPending
	}
	var since, deliveredAt time.Time
	for i := len(in.Attempts) - 1; i >= 0; i-- {
		a := in.Attempts[i]
		if a.At.Before(in.OldestPendingAt) {
			break
		}
		if a.Outcome == AttemptDelivered {
			deliveredAt = a.At
			break
		}
		if a.Outcome == AttemptFailed {
			since = a.At
		}
	}
	if _, active := in.Signals.cause(); active {
		from := in.OldestPendingAt
		if in.Signals.Since.After(from) {
			from = in.Signals.Since
		}
		if !deliveredAt.After(from) && (since.IsZero() || from.Before(since)) {
			since = from
		}
	}
	if since.IsZero() {
		return time.Time{}, ReasonNoFailures
	}
	if now.Sub(since) <= p.Threshold {
		return time.Time{}, ReasonBelowThreshold
	}
	return since, 0
}
