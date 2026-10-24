package delivery

// Kind is a category of feedback the sender's node gives its agent. The
// channel reports only its own anomalies: normal progress and closed_unconfirmed
// never produce feedback, so silence after the delivery deadline means the
// message arrived.
//
// Every correction has its own kind, so a delivery that is corrected twice
// (expired_unconfirmed to accepted, later delivery_unknown to delivered) is
// still told about each correction once.
type Kind uint8

const (
	// KindUnreachable: contact with the target has been failing; the sender
	// keeps retrying until the delivery deadline. The cause says whether the
	// network or a suspended trust chain is to blame.
	KindUnreachable Kind = iota + 1
	// KindDeliveryBlocked: the message is at the target but cannot be put in
	// front of the agent. Not final; the message stays queued. The cause says
	// why.
	KindDeliveryBlocked
	// KindBlockedRecovered: the cause of an earlier KindDeliveryBlocked is gone.
	KindBlockedRecovered
	// KindExpiredUnconfirmed: the deadline passed without a receipt. The target
	// may have received the message.
	KindExpiredUnconfirmed
	// KindExpiredCorrected: the target did receive a message reported as
	// KindExpiredUnconfirmed.
	KindExpiredCorrected
	// KindRejected: the target refused the message, for the reason given.
	KindRejected
	// KindDeliveryUnknown: the target handed the delivery over and the result is
	// unknown. A message was handed to the agent; a control signal was handed to
	// the adapter that executes it. The feedback cannot tell the two apart, so
	// its wording holds for both.
	KindDeliveryUnknown
	// KindUnknownCorrected: a message reported as KindDeliveryUnknown was
	// delivered after all.
	KindUnknownCorrected
	// KindLostAtSender: the sender's node lost what it had stored about the
	// delivery to storage corruption, so whether the message was delivered, or a
	// control signal executed, is unknown. It says nothing of the target.
	KindLostAtSender
	// KindLostAtTarget: the target's node lost what it had stored about the
	// delivery to storage corruption, with the same consequence.
	KindLostAtTarget
)

var kindNames = [...]string{
	KindUnreachable:        "unreachable",
	KindDeliveryBlocked:    "delivery_blocked",
	KindBlockedRecovered:   "blocked_recovered",
	KindExpiredUnconfirmed: "expired_unconfirmed",
	KindExpiredCorrected:   "expired_corrected",
	KindRejected:           "rejected",
	KindDeliveryUnknown:    "delivery_unknown",
	KindUnknownCorrected:   "unknown_corrected",
	KindLostAtSender:       "lost_at_sender",
	KindLostAtTarget:       "lost_at_target",
}

func (k Kind) String() string {
	if int(k) < len(kindNames) && kindNames[k] != "" {
		return kindNames[k]
	}
	return "invalid"
}

// Summary is the fixed English statement of what the sender may conclude from
// feedback of this kind. Its wording is part of the contract: the expiry
// notice must not read as "not delivered", or agents resend and duplicate.
func (k Kind) Summary() string {
	switch k {
	case KindUnreachable:
		return "the target is not reachable; delivery is still being retried until the delivery deadline"
	case KindDeliveryBlocked:
		return "the message is at the target but cannot be put in front of the agent; it stays queued"
	case KindBlockedRecovered:
		return "the problem that blocked delivery is gone; the message is no longer blocked"
	case KindExpiredUnconfirmed:
		return "the delivery deadline passed without a confirmation; the target may have received the message, so do not assume it was not delivered"
	case KindExpiredCorrected:
		return "correction: the target did receive the message that was reported as unconfirmed"
	case KindRejected:
		return "the target rejected the message; it was not delivered"
	case KindDeliveryUnknown:
		return "the target handed it over, to the agent or, for a control signal, to the adapter that executes it, but whether it was seen or executed is unknown; do not assume it was not delivered"
	case KindUnknownCorrected:
		return "correction: the message whose result was unknown was delivered"
	case KindLostAtSender:
		return "this node lost what it had stored about the message to storage corruption, so whether it was delivered, or for a control signal executed, is unknown; do not assume it was not delivered"
	case KindLostAtTarget:
		return "the target node lost what it had stored about the message to storage corruption, so whether it was delivered, or for a control signal executed, is unknown; do not assume it was not delivered"
	}
	return ""
}

func (k Kind) valid() bool { return k >= KindUnreachable && k <= KindLostAtTarget }

// KindSet is a set of Kind values. A Record uses it to remember which
// feedback it has already produced.
type KindSet uint16

// Has reports whether k is in the set.
func (s KindSet) Has(k Kind) bool { return s&(1<<k) != 0 }

// known reports whether the set holds only kinds that exist.
func (s KindSet) known() bool {
	for k := KindUnreachable; k.valid(); k++ {
		s &^= 1 << k
	}
	return s == 0
}

// With returns the set with k added.
func (s KindSet) With(k Kind) KindSet { return s | 1<<k }

// Feedback is one decision to tell the sender something.
type Feedback struct {
	Kind Kind
	// Cause is set for KindUnreachable, KindDeliveryBlocked and
	// KindBlockedRecovered (the cause that has ended).
	Cause Cause
	// Reject is set for KindRejected.
	Reject RejectReason
	// State is the state the delivery has now; it is the news of a correction.
	State State
}

// TransitionFeedback returns the feedback a change of state calls for, given
// what has already been reported for the delivery. It never returns a kind that
// is in reported, so repeating a transition cannot repeat a notice. unknown is
// the cause of the delivery_unknown being entered and is read only then.
//
//   - Entering rejected, expired_unconfirmed or delivery_unknown reports that
//     anomaly; the kind of a delivery_unknown follows its cause. Entering
//     rejected or delivery_unknown from expired_unconfirmed replaces the earlier
//     report instead of correcting it, because the new notice already
//     contradicts it.
//   - Leaving expired_unconfirmed for accepted or delivered, and leaving
//     delivery_unknown for delivered, reports a correction.
//   - Delivered after a reported block that was never reported recovered also
//     reports the recovery, since delivered itself is silent.
//   - Progress (queued, accepted, delivered) and closed_unconfirmed are silent.
func TransitionFeedback(from, to State, unknown UnknownCause, reported KindSet) []Kind {
	var kinds []Kind
	switch to {
	case StateRejected:
		kinds = append(kinds, KindRejected)
	case StateDeliveryUnknown:
		switch unknown {
		case UnknownHandedOver:
			kinds = append(kinds, KindDeliveryUnknown)
		case UnknownLostAtTarget:
			kinds = append(kinds, KindLostAtTarget)
		case UnknownLostAtSender:
			kinds = append(kinds, KindLostAtSender)
		}
	case StateExpiredUnconfirmed:
		kinds = append(kinds, KindExpiredUnconfirmed)
	case StateAccepted:
		if from == StateExpiredUnconfirmed {
			kinds = append(kinds, KindExpiredCorrected)
		}
	case StateDelivered:
		switch from {
		case StateExpiredUnconfirmed:
			kinds = append(kinds, KindExpiredCorrected)
		case StateDeliveryUnknown:
			kinds = append(kinds, KindUnknownCorrected)
		}
		if reported.Has(KindDeliveryBlocked) {
			kinds = append(kinds, KindBlockedRecovered)
		}
	}
	out := kinds[:0]
	for _, k := range kinds {
		if !reported.Has(k) {
			out = append(out, k)
		}
	}
	return out
}
