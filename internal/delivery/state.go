package delivery

// State is the externally visible state of one delivery (one message to one
// recipient endpoint).
type State uint8

const (
	// StateInvalid is the zero value: no state has been assigned.
	StateInvalid State = iota
	// StateQueued: the sender's node has recorded the message. Sender view only.
	StateQueued
	// StateAccepted: the target has authorized, deduplicated and stored the
	// message. On the target it covers both "waiting in the queue" and "leased
	// to the agent". Not final.
	StateAccepted
	// StateDelivered: the whole body reached the agent and was acknowledged.
	// Final.
	StateDelivered
	// StateRejected: the target refused the message, for the recorded
	// RejectReason. Final.
	StateRejected
	// StateExpiredUnconfirmed: the delivery deadline passed and an active query
	// found no receipt. The target may nevertheless have the message. Not final:
	// later facts correct it.
	StateExpiredUnconfirmed
	// StateDeliveryUnknown: the body was handed out but whether the agent saw
	// it cannot be told. The target produces it only for a delivery whose body
	// was handed out at least once: when the leases run out, when the settle
	// deadline passes under a lease, and when a delivery that was handed out is
	// closed forcibly (backlog deadline, a denied second check, a closed
	// endpoint), because "rejected" would tell the sender the message was not
	// delivered. Not final: a late acknowledgement corrects it.
	StateDeliveryUnknown
	// StateClosedUnconfirmed: report_by passed while the delivery was not
	// final. Final and silent.
	StateClosedUnconfirmed
)

var stateNames = [...]string{
	StateInvalid:            "invalid",
	StateQueued:             "queued",
	StateAccepted:           "accepted",
	StateDelivered:          "delivered",
	StateRejected:           "rejected",
	StateExpiredUnconfirmed: "expired_unconfirmed",
	StateDeliveryUnknown:    "delivery_unknown",
	StateClosedUnconfirmed:  "closed_unconfirmed",
}

func (s State) String() string {
	if int(s) < len(stateNames) {
		return stateNames[s]
	}
	return "invalid"
}

// known reports whether s is one of the states, StateInvalid excluded.
func (s State) known() bool { return s >= StateQueued && s <= StateClosedUnconfirmed }

// IsTerminal reports whether no further fact can change the state.
func (s State) IsTerminal() bool {
	return s == StateDelivered || s == StateRejected || s == StateClosedUnconfirmed
}

// IsCorrectable reports whether the state is non-final and can still be
// replaced by a later fact that contradicts it. A correction is reported to the
// sender once (see TransitionFeedback).
func (s State) IsCorrectable() bool {
	return s == StateExpiredUnconfirmed || s == StateDeliveryUnknown
}

// rank orders states by how much of the delivery they show. A fact whose state
// ranks below the current one is a stale report, not a conflict.
func (s State) rank() int {
	switch s {
	case StateQueued:
		return 0
	case StateExpiredUnconfirmed:
		return 1
	case StateAccepted:
		return 2
	case StateDeliveryUnknown:
		return 3
	case StateDelivered, StateRejected:
		return 4
	case StateClosedUnconfirmed:
		return 5
	}
	return -1
}

// legalTransitions lists, for each state, the states it may move to. A
// delivered or rejected message never changes. accepted does not lead to
// expired_unconfirmed because expiry is only judged when no receipt is known.
// delivery_unknown leads only to delivered: reconciliation can show that the
// agent did get the body, but nothing can show that a handed-out body was not
// delivered.
var legalTransitions = map[State][]State{
	StateQueued:             {StateAccepted, StateDelivered, StateRejected, StateDeliveryUnknown, StateExpiredUnconfirmed, StateClosedUnconfirmed},
	StateExpiredUnconfirmed: {StateAccepted, StateDelivered, StateRejected, StateDeliveryUnknown, StateClosedUnconfirmed},
	StateAccepted:           {StateDelivered, StateRejected, StateDeliveryUnknown, StateClosedUnconfirmed},
	StateDeliveryUnknown:    {StateDelivered, StateClosedUnconfirmed},
}

// CanTransition reports whether a delivery may move from one state to another.
func CanTransition(from, to State) bool {
	for _, s := range legalTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// SubState is the target-internal refinement of StateAccepted. It never
// changes the externally visible state and never produces feedback.
type SubState uint8

const (
	// SubNone: no prompt has been handed to the session for this message.
	SubNone SubState = iota
	// SubNotified: a prompt was handed to the session; the body is still to be
	// delivered.
	SubNotified
)

func (s SubState) known() bool { return s == SubNone || s == SubNotified }

// RejectReason says why a delivery is StateRejected.
type RejectReason uint8

const (
	// RejectNone is the zero value; it is the reason of no rejection.
	RejectNone RejectReason = iota
	// RejectUnauthorized: the authorization check at intake denied the message.
	RejectUnauthorized
	// RejectEndpointMissing: the recipient endpoint does not exist.
	RejectEndpointMissing
	// RejectEndpointClosed: the recipient endpoint was closed or deleted,
	// possibly while the message was queued. After a hand-out the target
	// records delivery_unknown instead.
	RejectEndpointClosed
	// RejectEndpointDeclined: the endpoint declared that it does not accept
	// this message, for example one with several recipients.
	RejectEndpointDeclined
	// RejectTooLarge: the body exceeds the size limit.
	RejectTooLarge
	// RejectExpired: the delivery deadline had already passed at first intake.
	RejectExpired
	// RejectBacklogExpired: the message stayed queued, never handed out, past
	// the endpoint's longest queue time or past the settle deadline. A message
	// that was handed out and then requeued becomes delivery_unknown instead.
	RejectBacklogExpired
	// RejectSecondCheckFailed: the authorization check made when the body was
	// to be handed out for the first time denied it, after revocation, a local
	// revoke or a policy tightening. Once a body was handed out, a denial
	// makes the delivery delivery_unknown instead.
	RejectSecondCheckFailed
	// RejectMalformed: the envelope is not valid.
	RejectMalformed
	// RejectDeadlineOutOfRange: the delivery deadline lies further in the future
	// than the protocol allows.
	RejectDeadlineOutOfRange
	// RejectBacklogFull: the recipient endpoint already holds as many queued
	// messages as its backlog limit allows. Final at intake, so the sender
	// learns at once that the endpoint is not reading and does not wait for a
	// deadline.
	RejectBacklogFull
)

var rejectNames = [...]string{
	RejectNone:               "none",
	RejectUnauthorized:       "unauthorized",
	RejectEndpointMissing:    "endpoint_missing",
	RejectEndpointClosed:     "endpoint_closed",
	RejectEndpointDeclined:   "endpoint_declined",
	RejectTooLarge:           "too_large",
	RejectExpired:            "expired",
	RejectBacklogExpired:     "backlog_expired",
	RejectSecondCheckFailed:  "second_check_failed",
	RejectMalformed:          "malformed",
	RejectDeadlineOutOfRange: "deadline_out_of_range",
	RejectBacklogFull:        "backlog_full",
}

// known reports whether r is a reason or RejectNone.
func (r RejectReason) known() bool { return r <= RejectBacklogFull }

func (r RejectReason) String() string {
	if int(r) < len(rejectNames) {
		return rejectNames[r]
	}
	return "invalid"
}

// Cause says why contact or delivery is failing. The first two explain an
// unreachable target, the rest a blocked delivery.
type Cause uint8

const (
	// CauseNone is the zero value.
	CauseNone Cause = iota
	// CauseNetwork: the target node cannot be reached.
	CauseNetwork
	// CauseTrustSuspended: an intermediate node of the trust chain is offline
	// and the target answers with the non-final "suspended" result.
	CauseTrustSuspended
	// CauseAdapterOffline: the runtime adapter is not connected.
	CauseAdapterOffline
	// CauseRuntimeAuth: the runtime's credentials are no longer valid.
	CauseRuntimeAuth
	// CauseNativeUnavailable: the runtime's native service is unavailable.
	CauseNativeUnavailable
)

var causeNames = [...]string{
	CauseNone:              "none",
	CauseNetwork:           "network",
	CauseTrustSuspended:    "trust_suspended",
	CauseAdapterOffline:    "adapter_offline",
	CauseRuntimeAuth:       "runtime_auth",
	CauseNativeUnavailable: "native_unavailable",
}

func (c Cause) String() string {
	if int(c) < len(causeNames) {
		return causeNames[c]
	}
	return "invalid"
}

func (c Cause) isUnreachable() bool { return c == CauseNetwork || c == CauseTrustSuspended }

func (c Cause) isBlocked() bool {
	return c == CauseAdapterOffline || c == CauseRuntimeAuth || c == CauseNativeUnavailable
}
