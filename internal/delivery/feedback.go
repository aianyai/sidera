package delivery

// Kind is a category of feedback the sender's node gives its agent. The
// channel reports only its own anomalies: normal progress never produces
// feedback, and neither does a close that follows a notice the sender already
// has, so silence after the delivery deadline means the message arrived. A
// delivery that closes while it is still queued or accepted is not silent: the
// sender has never been told an outcome, and silence would read as delivered.
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
	// KindLostAtTarget: the target's node answered a pull of a delivery it had
	// accepted by saying it holds no record of it, with the same consequence as a
	// loss at the sender. The sender knows only that the record is gone, not why.
	KindLostAtTarget
	// KindAcceptedUnconfirmed: the delivery closed at report_by while it was
	// accepted: the target gave a receipt, and no report that settled the delivery
	// reached the sender before the deadline for learning it. Whether it was
	// delivered is unknown, and it is not known to have failed either.
	KindAcceptedUnconfirmed
	// KindLostUnknownMessage: the sender's node lost what it had stored about one
	// of the messages an endpoint sent and cannot tell which one, because the
	// record that said so is unreadable. It has no delivery behind it: no
	// transition produces it, no Record reports it, and its item names no message
	// and no recipient (NewLostUnknownMessageItem).
	KindLostUnknownMessage
)

// LastRecordKind is the highest kind a Record's transitions produce, and so the
// highest a Record.Reported can hold. KindLostUnknownMessage is after it: it
// belongs to no delivery.
const LastRecordKind = KindAcceptedUnconfirmed

var kindNames = [...]string{
	KindUnreachable:         "unreachable",
	KindDeliveryBlocked:     "delivery_blocked",
	KindBlockedRecovered:    "blocked_recovered",
	KindExpiredUnconfirmed:  "expired_unconfirmed",
	KindExpiredCorrected:    "expired_corrected",
	KindRejected:            "rejected",
	KindDeliveryUnknown:     "delivery_unknown",
	KindUnknownCorrected:    "unknown_corrected",
	KindLostAtSender:        "lost_at_sender",
	KindLostAtTarget:        "lost_at_target",
	KindAcceptedUnconfirmed: "accepted_unconfirmed",
	KindLostUnknownMessage:  "lost_unknown_message",
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
		return "the target node no longer holds any record of the message it had accepted, so whether it was delivered, or for a control signal executed, is unknown; do not assume it was not delivered"
	case KindAcceptedUnconfirmed:
		return "the target accepted the message, but its result could not be learned before the report deadline; whether it was delivered, or for a control signal executed, is unknown; do not assume it was not delivered"
	case KindLostUnknownMessage:
		return "this node lost what it had stored about one of the messages this endpoint sent, to storage corruption, and cannot tell which one; whether it was delivered is unknown; do not assume it was not delivered"
	}
	return ""
}

func (k Kind) valid() bool { return k >= KindUnreachable && k <= KindLostUnknownMessage }

// KindSet is a set of Kind values. A Record uses it to remember which
// feedback it has already produced.
type KindSet uint16

// Has reports whether k is in the set.
func (s KindSet) Has(k Kind) bool { return s&(1<<k) != 0 }

// known reports whether the set holds only kinds a record can have produced
// (up to LastRecordKind).
func (s KindSet) known() bool {
	for k := KindUnreachable; k <= LastRecordKind; k++ {
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
// the cause of the delivery_unknown being entered and is read only then; it must
// be one of the three causes when to is delivery_unknown, and a cause that is not
// one gives no notice.
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
//   - Closing at report_by while queued reports the expiry, which the sender has
//     not been told, and closing while accepted reports that the result could not
//     be learned. Closing from expired_unconfirmed or delivery_unknown is silent:
//     the sender already holds the notice that says the outcome is unknown.
//   - Progress (queued, accepted, delivered) is silent.
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
	case StateClosedUnconfirmed:
		switch from {
		case StateQueued:
			kinds = append(kinds, KindExpiredUnconfirmed)
		case StateAccepted:
			kinds = append(kinds, KindAcceptedUnconfirmed)
		}
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
