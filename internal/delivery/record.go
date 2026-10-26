package delivery

import (
	"errors"
	"fmt"
	"time"
)

// Errors returned for input that no correct caller produces. Facts that are
// merely late, repeated or not yet due are not errors: they come back as an
// Outcome that says why they were ignored.
var (
	// ErrIllegalTransition: the fact contradicts the delivery's state, for
	// example a rejection reported for a delivered message.
	ErrIllegalTransition = errors.New("illegal delivery transition")
	// ErrWrongSide: the operation belongs to the other side of the delivery, or
	// to the other kind of target record (a control record is never handed out,
	// notified or blocked, and a message record never has its reply committed).
	ErrWrongSide = errors.New("operation does not apply to this side of the delivery")
	// ErrInvalidEvent: the event is malformed.
	ErrInvalidEvent = errors.New("invalid event")
	// ErrInvalidInput: an argument is out of its domain.
	ErrInvalidInput = errors.New("invalid input")
)

// Side says which node's view a Record holds.
type Side uint8

const (
	// SideSource is the sender's node. It starts at queued, judges expiry,
	// collects the target's status reports and produces the feedback.
	SideSource Side = iota + 1
	// SideTarget is the recipient's node. It starts at accepted, owns the queue
	// and the leases, and guarantees that every delivery is settled before
	// SettleBy. It produces no feedback: the sender's node does, from the
	// status reports.
	SideTarget
)

func (s Side) known() bool { return s == SideSource || s == SideTarget }

// Record is one delivery as one side sees it. Build it with NewSourceRecord,
// NewTargetRecord, NewTargetControlRecord or, for the sender's own record lost to
// storage corruption, NewLostRecord, and change it only through its methods. The
// zero value is not usable.
type Record struct {
	Side    Side
	State   State
	Sub     SubState
	Reject  RejectReason // set while State is StateRejected
	Unknown UnknownCause // set while State is StateDeliveryUnknown

	// StateAt is the moment this node's record entered State: the time the
	// constructor was given, or the now of the transition that last changed the
	// state. It is the one home of that time, written only there and never by a
	// caller. A fact that leaves the state alone (a block, a prompt, a hand-out, a
	// lease that runs out and goes back to the queue, a duplicate) leaves it
	// alone, so a report sent again for an unchanged state repeats it. A target
	// puts it in a signed status report as the moment it recorded the state. It is
	// set on every record; the zero value is not a valid one.
	StateAt time.Time

	// Lost marks the sender's record that replaces one lost to storage
	// corruption. Only NewLostRecord sets it and no transition clears it, so it
	// still says what the record is after the record has closed, when Unknown no
	// longer does: a record restored from storage is recognised as a replacement
	// by it, and not judged by the shape of a delivery that was accepted normally.
	// A target never holds one: a target that lost its record keeps nothing.
	Lost bool

	// Control marks the target's record of a control signal; only
	// NewTargetControlRecord sets it, so it is never set on the source side. A
	// control signal is executed by the adapter when it is accepted and answered
	// with a reply; it is never queued for recv, so it has no lease, no chunks
	// and no backlog deadline, and it is delivered by EventReplyCommitted, not
	// by Ack. False for every other record.
	Control bool

	// Exempt marks the target's record of traffic that endpoint policy leaves
	// alone (authz.TrafficKind.PolicyExempt): a notice of the system endpoint.
	// Only NewTargetNoticeRecord sets it, so it is decided once, where the
	// traffic is taken in, and no transition changes it. Such a record has no
	// verdict of the authorization check: Handout makes no second check for it,
	// and it takes no slot of the endpoint's backlog. False for every other
	// record, and always for a control record or a sender's record.
	Exempt bool

	// AcceptBefore is the delivery deadline from the envelope.
	AcceptBefore time.Time
	// ReportBy is when the sender stops asking and the delivery closes.
	ReportBy time.Time
	// BacklogDeadline is when a queued message becomes rejected (target only).
	BacklogDeadline time.Time

	// Reported lists the feedback already produced (source only).
	Reported KindSet
	// Blocked is set while delivery is blocked, with the cause of the last block.
	Blocked      bool
	BlockedCause Cause

	// Lease is the pull-lease bookkeeping (target only).
	Lease Lease
}

// NewSourceRecord returns the sender's record of a delivery that its node
// accepted from the caller at queuedAt. ReportBy follows from the deadline.
func NewSourceRecord(queuedAt, acceptBefore time.Time) Record {
	return Record{
		Side:         SideSource,
		State:        StateQueued,
		StateAt:      queuedAt,
		AcceptBefore: acceptBefore,
		ReportBy:     ReportBy(acceptBefore),
	}
}

// NewTargetRecord returns the target's record of a delivery it has just
// accepted. acceptBefore and reportBy are the envelope's values, already
// validated by the caller; endpointMaxQueue is the recipient endpoint's longest
// queue time. A zero acceptedAt is ErrInvalidInput, as a transition at the zero
// time is: the record would enter its state at no time at all.
func NewTargetRecord(acceptedAt, acceptBefore, reportBy time.Time, endpointMaxQueue time.Duration) (Record, error) {
	if acceptedAt.IsZero() {
		return Record{}, fmt.Errorf("%w: a target record needs the time it was accepted", ErrInvalidInput)
	}
	if err := ValidateEndpointMaxQueue(endpointMaxQueue); err != nil {
		return Record{}, err
	}
	return Record{
		Side:            SideTarget,
		State:           StateAccepted,
		StateAt:         acceptedAt,
		AcceptBefore:    acceptBefore,
		ReportBy:        reportBy,
		BacklogDeadline: BacklogDeadline(acceptedAt, reportBy, endpointMaxQueue),
	}, nil
}

// NewTargetNoticeRecord returns the target's record of a notice from the system
// endpoint it has just accepted, for the endpoint to receive like a message:
// system feedback, which is exempt traffic (authz.TrafficSystemFeedback).
// The arguments are those of NewTargetRecord. The record is marked Exempt, so
// the authorization check that decides every other message is neither made
// for it at intake (see Intake) nor repeated at recv (see Record.Handout), and
// it takes no slot of the endpoint's backlog (see OccupiesBacklogSlot).
func NewTargetNoticeRecord(acceptedAt, acceptBefore, reportBy time.Time, endpointMaxQueue time.Duration) (Record, error) {
	r, err := NewTargetRecord(acceptedAt, acceptBefore, reportBy, endpointMaxQueue)
	if err != nil {
		return Record{}, err
	}
	r.Exempt = true
	return r, nil
}

// NewTargetControlRecord returns the target's record of a control signal it
// accepted at acceptedAt. acceptBefore and reportBy are the envelope's values,
// already validated by the caller. The record is never queued for recv and has no
// backlog deadline: the signal is executed, not handed to an agent, and the
// delivery ends when the control reply is committed (EventReplyCommitted) or,
// failing that, is settled by Tick.
func NewTargetControlRecord(acceptedAt, acceptBefore, reportBy time.Time) Record {
	return Record{
		Side:         SideTarget,
		State:        StateAccepted,
		StateAt:      acceptedAt,
		Control:      true,
		AcceptBefore: acceptBefore,
		ReportBy:     reportBy,
	}
}

// NewLostRecord returns the record that replaces the sender's delivery record
// lost to storage corruption, discovered at discoveredAt. Only what the lost
// file's first line tells survives: the delivery's identity, which the caller
// keeps as it does for every record. The record is delivery_unknown with the
// cause lost_at_sender: it is neither delivered nor rejected, because nobody can
// tell whether the delivery was handed over. It also returns the one feedback the
// sender must get (KindLostAtSender), already marked as reported.
//
// Only the sender has a replacement. A target that loses its record keeps
// nothing: the sender learns of it when the target answers a pull that it holds
// no record (EventNoRecordAtTarget).
//
// The record enters its state at discoveredAt and carries the Lost marker for the
// rest of its life. ReportBy is LostReportBy(discoveredAt), an upper bound of the
// lost record's own report_by. AcceptBefore and BacklogDeadline stay zero because
// they are unknown, so Tick closes the record at ReportBy and at no other time.
//
// This is the only way into a storage loss: it is a way to start a record, like
// the other constructors, and no event reaches it, so a healthy record can never
// be turned into a lost one. The caller must pass a discoveredAt that is not
// earlier than the lost record's acceptance, which makes the bound hold. A zero
// discoveredAt is ErrInvalidInput.
func NewLostRecord(discoveredAt time.Time) (Record, Outcome, error) {
	if discoveredAt.IsZero() {
		return Record{}, Outcome{}, fmt.Errorf("%w: a lost record needs the time the loss was discovered", ErrInvalidInput)
	}
	r := Record{Side: SideSource, State: StateDeliveryUnknown, StateAt: discoveredAt, Unknown: UnknownLostAtSender, Lost: true, ReportBy: LostReportBy(discoveredAt)}
	n, feedback := r.withFeedback(StateInvalid, StateDeliveryUnknown, RejectNone)
	return n, Outcome{Result: ResultApplied, From: StateInvalid, To: StateDeliveryUnknown, Feedback: feedback}, nil
}

// EventType names a fact or occurrence that Apply understands.
type EventType uint8

const (
	// EventReceipt: the target confirms it has accepted the message.
	EventReceipt EventType = iota + 1
	// EventDelivered: the target reports the body delivered (source only; a
	// target record is delivered by Ack).
	EventDelivered
	// EventRejected: the target reports a rejection; Event.Reject says why.
	EventRejected
	// EventDeliveryUnknown: the target reports the delivery result unknown,
	// because it handed the delivery over (UnknownHandedOver).
	EventDeliveryUnknown
	// EventExpiryQueried: the sender actively queried the target at
	// Event.QueriedAt and got no receipt.
	EventExpiryQueried
	// EventReportByPassed: the clock may have passed ReportBy.
	EventReportByPassed
	// EventNotified: a prompt was handed to the session (target only).
	EventNotified
	// EventContactFailing: contact with the target has been failing since
	// Event.Since, for Event.Cause (source only).
	EventContactFailing
	// EventBlocked: delivery is blocked for Event.Cause. Whether and when it
	// counts as blocked is decided by the caller. Never for a control record.
	EventBlocked
	// EventUnblocked: the cause of the block is gone.
	EventUnblocked
	// EventReplyCommitted: the control reply was committed together with the
	// execution result (target control records only). It is the only fact that
	// delivers a control record; the sender applies the reply with
	// EventDelivered.
	EventReplyCommitted
	// EventNoRecordAtTarget: the target node answered a pull by saying it holds
	// no record of the delivery (source only).
	EventNoRecordAtTarget
)

// Event is one input to Record.Apply.
type Event struct {
	Type      EventType
	Reject    RejectReason // EventRejected
	Cause     Cause        // EventContactFailing, EventBlocked
	Since     time.Time    // EventContactFailing
	QueriedAt time.Time    // EventExpiryQueried
}

// Result says whether an event changed the record.
type Result uint8

const (
	// ResultApplied: the record changed, or the event was a state-preserving
	// occurrence (such as a block) that was recorded.
	ResultApplied Result = iota + 1
	// ResultIgnored: the record is unchanged; Outcome.Ignored says why.
	ResultIgnored
)

// IgnoreReason says why an event left the record unchanged.
type IgnoreReason uint8

const (
	IgnoreNone IgnoreReason = iota
	// IgnoreDuplicate: the fact is already recorded.
	IgnoreDuplicate
	// IgnoreStale: the fact describes an earlier stage than the record shows.
	IgnoreStale
	// IgnoreAlreadyFinal: the record is final.
	IgnoreAlreadyFinal
	// IgnoreNotDueYet: the time condition of the event is not met.
	IgnoreNotDueYet
	// IgnoreNotApplicable: the event does not apply in the record's state.
	IgnoreNotApplicable
	// IgnoreAlreadyReported: the feedback this event would cause was given.
	IgnoreAlreadyReported
	// IgnoreRetryEnded: the sender no longer retries, so the hint is moot.
	IgnoreRetryEnded
	// IgnoreRecordLost: the record replaces one lost to storage corruption on
	// the sender's own node. It was rebuilt without the history that facts are
	// judged against, and the sender was already told the outcome is unknown.
	IgnoreRecordLost
)

// Outcome is the machine-readable result of one change to a Record.
type Outcome struct {
	Result  Result
	Ignored IgnoreReason
	From    State
	To      State
	// Requeued is set when a lease expired and the message went back to the
	// queue flagged as a possible duplicate.
	Requeued bool
	// Feedback lists the notices the sender must receive. Only source records
	// produce any.
	Feedback []Feedback
}

func ignored(from State, why IgnoreReason) Outcome {
	return Outcome{Result: ResultIgnored, Ignored: why, From: from, To: from}
}

// Apply applies one event at time now and returns the updated record.
//
// Apply, Tick, Handout and Ack are the ways a record changes; every state
// change among them goes through change. A target message record is delivered
// only by Ack and a target control record only by EventReplyCommitted, and a
// rejection reported to a target record is a forced closure (see
// closeForcibly). No event puts a record into a storage loss: NewLostRecord
// alone makes one.
func (r Record) Apply(now time.Time, ev Event) (Record, Outcome, error) {
	switch ev.Type {
	case EventReceipt:
		return r.moveTo(now, StateAccepted, RejectNone)
	case EventDelivered:
		if r.Side == SideTarget {
			return r, Outcome{}, fmt.Errorf("%w: on the target a delivery becomes delivered only through Ack, or through EventReplyCommitted for a control signal", ErrWrongSide)
		}
		return r.moveTo(now, StateDelivered, RejectNone)
	case EventReplyCommitted:
		if r.Side != SideTarget || !r.Control {
			return r, Outcome{}, fmt.Errorf("%w: a reply is committed only for a control signal on the target", ErrWrongSide)
		}
		return r.moveTo(now, StateDelivered, RejectNone)
	case EventRejected:
		if ev.Reject == RejectNone || !ev.Reject.known() {
			return r, Outcome{}, fmt.Errorf("%w: rejection without a known reason", ErrInvalidEvent)
		}
		if r.Side == SideTarget {
			return r.closeForcibly(now, ev.Reject)
		}
		return r.moveTo(now, StateRejected, ev.Reject)
	case EventDeliveryUnknown:
		return r.moveToUnknown(now, UnknownHandedOver)
	case EventExpiryQueried:
		return r.judgeExpiry(now, ev)
	case EventReportByPassed:
		return r.closeIfDue(now)
	case EventNotified:
		return r.markNotified()
	case EventContactFailing:
		return r.noteUnreachable(now, ev)
	case EventBlocked:
		return r.noteBlocked(ev)
	case EventUnblocked:
		return r.noteUnblocked()
	case EventNoRecordAtTarget:
		return r.noRecordAtTarget(now)
	}
	return r, Outcome{}, fmt.Errorf("%w: unknown event type %d", ErrInvalidEvent, ev.Type)
}

// noRecordAtTarget applies the target's answer that it holds no record of the
// delivery. Only a delivery the target accepted, with a receipt in hand, was lost
// there; every other state ignores it, in this order:
//
//   - A target's record is refused: only the sender reads pull answers.
//   - The replacement of the sender's own lost record ignores every later fact.
//   - A final delivery has its conclusion, and is no longer pulled.
//   - After ReportBy the sender no longer pulls, and the target's retention
//     (RecordRetainUntil) covers only the window, so the answer is moot.
//   - Without a receipt (queued, expired_unconfirmed) the target may never have
//     taken the message in, for example while authorization is suspended, which
//     stores nothing; the expiry rule judges that case.
//   - A delivery already delivery_unknown stays so: the sender has been told
//     "unknown", and the target forgetting the record later does not change that.
func (r Record) noRecordAtTarget(now time.Time) (Record, Outcome, error) {
	switch {
	case r.Side != SideSource:
		return r, Outcome{}, fmt.Errorf("%w: only the sender reads the answer of a pull", ErrWrongSide)
	case r.Lost:
		return r, ignored(r.State, IgnoreRecordLost), nil
	case r.State.IsTerminal():
		return r, ignored(r.State, IgnoreAlreadyFinal), nil
	case now.After(r.ReportBy):
		return r, ignored(r.State, IgnoreRetryEnded), nil
	case r.State != StateAccepted:
		return r, ignored(r.State, IgnoreNotApplicable), nil
	}
	return r.moveToUnknown(now, UnknownLostAtTarget)
}

// moveTo changes the state to any state but delivery_unknown, which moveToUnknown
// reaches.
func (r Record) moveTo(now time.Time, to State, reason RejectReason) (Record, Outcome, error) {
	return r.change(now, to, reason, UnknownCauseNone)
}

// moveToUnknown changes the state to delivery_unknown for the given cause.
func (r Record) moveToUnknown(now time.Time, cause UnknownCause) (Record, Outcome, error) {
	return r.change(now, StateDeliveryUnknown, RejectNone, cause)
}

// change is the only place that changes State, and so the only place that
// writes StateAt: the state is entered at now. The reason and the cause are set
// with the states they belong to and cleared when the state moves on. The
// callers pass a cause exactly when to is delivery_unknown, and one of the three
// causes then; anything else is a programming error, refused before it can leave
// a record whose cause and state disagree.
func (r Record) change(now time.Time, to State, reason RejectReason, unknown UnknownCause) (Record, Outcome, error) {
	from := r.State
	switch {
	case !causeFitsState(to, unknown):
		return r, Outcome{}, fmt.Errorf("%w: entering %s with unknown cause %s", ErrInvalidInput, to, unknown)
	case r.Unknown == UnknownLostAtSender && to != StateClosedUnconfirmed:
		return r, ignored(from, IgnoreRecordLost), nil
	case from == to:
		return r, ignored(from, IgnoreDuplicate), nil
	case from == StateClosedUnconfirmed:
		return r, ignored(from, IgnoreAlreadyFinal), nil
	case to.rank() < from.rank():
		return r, ignored(from, IgnoreStale), nil
	case !CanTransition(from, to):
		return r, Outcome{}, fmt.Errorf("%w: %s to %s", ErrIllegalTransition, from, to)
	case to == StateDeliveryUnknown && r.Side == SideTarget && !r.handedOver():
		return r, Outcome{}, fmt.Errorf("%w: delivery_unknown needs a body that was handed out", ErrIllegalTransition)
	}
	if now.IsZero() {
		return r, Outcome{}, fmt.Errorf("%w: entering %s at the zero time", ErrInvalidInput, to)
	}
	n := r
	n.State = to
	n.StateAt = now
	n.Sub = SubNone
	n.Unknown = unknown
	if to == StateRejected {
		n.Reject = reason
	}
	if to != StateAccepted {
		n.Blocked = false
		n.Lease.Active = false
	}
	n, feedback := n.withFeedback(from, to, reason)
	return n, Outcome{Result: ResultApplied, From: from, To: to, Feedback: feedback}, nil
}

// withFeedback returns the record, which has just entered state to from state
// from, with the feedback that change calls for marked as reported, and that
// feedback. Only a source record produces any. Reject is the reason of a
// rejection; the cause of the entered delivery_unknown is the record's own.
func (r Record) withFeedback(from, to State, reject RejectReason) (Record, []Feedback) {
	if r.Side != SideSource {
		return r, nil
	}
	var feedback []Feedback
	for _, k := range TransitionFeedback(from, to, r.Unknown, r.Reported) {
		fb := Feedback{Kind: k, State: to}
		switch k {
		case KindRejected:
			fb.Reject = reject
		case KindBlockedRecovered:
			fb.Cause = r.BlockedCause
		}
		r.Reported = r.Reported.With(k)
		feedback = append(feedback, fb)
	}
	return r, feedback
}

// handedOver reports whether the target has passed the delivery on to where
// it can act on it unseen, so that its result may be unknown: a message body
// was handed out at least once, or a control signal was accepted, which hands
// it to the adapter to execute.
func (r Record) handedOver() bool {
	return r.Control || r.Lease.Handouts > 0
}

// hasQueue reports whether the record waits in the target's recv queue, with
// leases and a backlog deadline. A control record never does.
func (r Record) hasQueue() bool {
	return r.Side == SideTarget && !r.Control
}

// causeFitsState reports whether an unknown cause goes with a state: the cause
// is set exactly while the state is delivery_unknown. It is the one statement of
// that pairing, for the transitions, the status report and Valid.
func causeFitsState(state State, cause UnknownCause) bool {
	return (state == StateDeliveryUnknown) == (cause != UnknownCauseNone)
}

// checkUnknownCause states, in one place, which unknown cause a record can hold,
// for Valid to call:
//
//   - The cause is set exactly while the state is delivery_unknown (see
//     causeFitsState) and is one of the causes of the record's side. A target
//     holds handed_over alone: it never holds a loss, because a target that lost
//     its record keeps nothing. A source holds handed_over; lost_at_target, which
//     the target's answer that it holds no record gave it; or, for the record
//     that replaces its own loss, lost_at_sender.
//   - The Lost marker and lost_at_sender go together, and the marker is never on
//     a record handed over or lost at the target.
//
// That a target's handed_over needs a hand-out or a control signal is
// checkOutcome's rule, which every target delivery_unknown meets.
func (r Record) checkUnknownCause() Violation {
	if !causeFitsState(r.State, r.Unknown) {
		return ViolationUnknownCause
	}
	var ok bool
	switch r.Unknown {
	case UnknownCauseNone:
		ok = true
	case UnknownHandedOver:
		ok = !r.Lost
	case UnknownLostAtSender:
		ok = r.Side == SideSource && r.Lost
	case UnknownLostAtTarget:
		ok = r.Side == SideSource && !r.Lost
	}
	if !ok {
		return ViolationUnknownCause
	}
	return ViolationNone
}

// checkLostShape checks a record that carries the Lost marker against what
// NewLostRecord makes and the transitions can leave of it. It is the one place
// that says what a replacement looks like, for Valid to call; the other checks
// admit that shape (the notice of its own loss is a legal notice) and a marker on
// any other shape is refused here:
//
//   - Only the sender has a replacement: a target never holds one.
//   - The state is delivery_unknown, or closed_unconfirmed after ReportBy.
//     Nothing else reaches a replacement.
//   - There is a ReportBy, from LostReportBy, and neither an AcceptBefore nor a
//     BacklogDeadline, which are lost with the record, so only Tick at ReportBy
//     closes it.
//   - There is no history: no lease or block cause.
//   - It has reported exactly its own notice; the sender learns of it from the
//     outcome of NewLostRecord and from nowhere else.
func (r Record) checkLostShape() Violation {
	if !r.Lost {
		return ViolationNone
	}
	switch {
	case r.Side != SideSource:
		return ViolationLostShape
	case r.State != StateDeliveryUnknown && r.State != StateClosedUnconfirmed:
		return ViolationLostShape
	case r.ReportBy.IsZero() || !r.AcceptBefore.IsZero() || !r.BacklogDeadline.IsZero():
		return ViolationLostShape
	case r.BlockedCause != CauseNone || !r.Lease.isZero():
		return ViolationLostShape
	case r.Reported != KindSet(0).With(KindLostAtSender):
		return ViolationLostShape
	}
	return ViolationNone
}

// NeedsBody reports whether the delivery still needs the bytes of its body: a
// sender that has no receipt yet may have to send it again, and a target
// message waiting in the queue or under a lease has still to hand it out. A
// control record never does: its own content is in the record, and the body it
// references is the initial message, which the delivery derived from it hands
// out. A source record whose body turns out to be corrupt and which needs it is
// replaced by NewLostRecord, a target record has no replacement (a target keeps no
// loss), and any other record keeps its state and merely drops its reference to
// the body.
func (r Record) NeedsBody() bool {
	switch r.Side {
	case SideSource:
		return r.State == StateQueued
	case SideTarget:
		return !r.Control && r.State == StateAccepted
	}
	return false
}

// closeForcibly ends a target delivery that cannot go on for a reason other
// than the agent's own handling: its backlog deadline passed, the second
// authorization check denied it, or its endpoint was closed. The reason is
// recorded only when the delivery was never handed over (see handedOver). Once
// it was, the agent may have seen the body or the adapter may have executed
// the signal, and "rejected" would tell the sender it was not delivered, so the
// delivery becomes delivery_unknown, which a late acknowledgement or committed
// reply can still correct. A delivery whose result is already unknown stays so,
// whatever the cause: "rejected" would be as untrue for it.
func (r Record) closeForcibly(now time.Time, reason RejectReason) (Record, Outcome, error) {
	if r.State == StateDeliveryUnknown {
		return r, ignored(r.State, IgnoreDuplicate), nil
	}
	if r.handedOver() {
		return r.moveToUnknown(now, UnknownHandedOver)
	}
	return r.moveTo(now, StateRejected, reason)
}

// judgeExpiry moves a queued delivery to expired_unconfirmed once the deadline
// plus the clock tolerance has passed and a query made after that found no
// receipt. The tolerance keeps a target whose clock runs behind from being
// judged expired while it can still legitimately accept the message.
func (r Record) judgeExpiry(now time.Time, ev Event) (Record, Outcome, error) {
	if r.Side != SideSource {
		return r, Outcome{}, fmt.Errorf("%w: expiry is judged by the sender", ErrWrongSide)
	}
	if r.State != StateQueued {
		return r, ignored(r.State, IgnoreNotApplicable), nil
	}
	if !ev.QueriedAt.After(ExpiryJudgeableAfter(r.AcceptBefore)) {
		return r, ignored(r.State, IgnoreNotDueYet), nil
	}
	return r.moveTo(now, StateExpiredUnconfirmed, RejectNone)
}

// closeIfDue closes a delivery that is still not final after ReportBy.
func (r Record) closeIfDue(now time.Time) (Record, Outcome, error) {
	if r.State.IsTerminal() {
		return r, ignored(r.State, IgnoreAlreadyFinal), nil
	}
	if !r.closeDeadline().reachedBy(now) {
		return r, ignored(r.State, IgnoreNotDueYet), nil
	}
	return r.moveTo(now, StateClosedUnconfirmed, RejectNone)
}

func (r Record) markNotified() (Record, Outcome, error) {
	if r.Side != SideTarget {
		return r, Outcome{}, fmt.Errorf("%w: prompts are handed out by the target", ErrWrongSide)
	}
	if r.Control {
		return r, Outcome{}, fmt.Errorf("%w: a control signal is executed, never put in front of an agent as a prompt", ErrWrongSide)
	}
	switch {
	case r.State != StateAccepted:
		return r, ignored(r.State, IgnoreNotApplicable), nil
	case r.Sub == SubNotified:
		return r, ignored(r.State, IgnoreDuplicate), nil
	}
	r.Sub = SubNotified
	return r, Outcome{Result: ResultApplied, From: r.State, To: r.State}, nil
}

// noteUnreachable produces the unreachable hint once contact has been failing
// for UnreachableHintAfter while the sender is still retrying. A failed pull of
// the status of a delivery the target already accepted is not unreachability of
// the message, so only a queued delivery qualifies.
func (r Record) noteUnreachable(now time.Time, ev Event) (Record, Outcome, error) {
	if r.Side != SideSource {
		return r, Outcome{}, fmt.Errorf("%w: contact failures are seen by the sender", ErrWrongSide)
	}
	if !ev.Cause.isUnreachable() || ev.Since.IsZero() {
		return r, Outcome{}, fmt.Errorf("%w: unreachable needs a network or trust cause and a start time", ErrInvalidEvent)
	}
	switch {
	case r.State != StateQueued:
		return r, ignored(r.State, IgnoreNotApplicable), nil
	case !now.Before(r.AcceptBefore):
		return r, ignored(r.State, IgnoreRetryEnded), nil
	case now.Sub(ev.Since) < UnreachableHintAfter:
		return r, ignored(r.State, IgnoreNotDueYet), nil
	case r.Reported.Has(KindUnreachable):
		return r, ignored(r.State, IgnoreAlreadyReported), nil
	}
	r.Reported = r.Reported.With(KindUnreachable)
	return r, Outcome{
		Result:   ResultApplied,
		From:     r.State,
		To:       r.State,
		Feedback: []Feedback{{Kind: KindUnreachable, Cause: ev.Cause, State: r.State}},
	}, nil
}

// noteBlocked records that delivery is blocked. Blocking is not a state: the
// message stays accepted and queued. The sender hears of it once per delivery,
// even if the delivery is blocked again later. A control record is never
// blocked: blocked means prompts persistently fail to reach a session, and a
// control signal has neither prompts nor a session. An adapter that cannot be
// reached is told to the sender by the control reply.
func (r Record) noteBlocked(ev Event) (Record, Outcome, error) {
	if r.Control {
		return r, Outcome{}, fmt.Errorf("%w: a control signal has no prompts or session to be blocked", ErrWrongSide)
	}
	if !ev.Cause.isBlocked() {
		return r, Outcome{}, fmt.Errorf("%w: blocked needs an adapter, runtime or native cause", ErrInvalidEvent)
	}
	switch {
	case r.State != StateAccepted:
		return r, ignored(r.State, IgnoreNotApplicable), nil
	case r.Blocked:
		return r, ignored(r.State, IgnoreDuplicate), nil
	}
	r.Blocked = true
	r.BlockedCause = ev.Cause
	out := Outcome{Result: ResultApplied, From: r.State, To: r.State}
	if r.Side == SideSource && !r.Reported.Has(KindDeliveryBlocked) {
		r.Reported = r.Reported.With(KindDeliveryBlocked)
		out.Feedback = []Feedback{{Kind: KindDeliveryBlocked, Cause: ev.Cause, State: r.State}}
	}
	return r, out, nil
}

// noteUnblocked records that the cause of a block is gone and, if the sender
// was told about the block, produces the one recovery correction.
func (r Record) noteUnblocked() (Record, Outcome, error) {
	if !r.Blocked {
		return r, ignored(r.State, IgnoreNotApplicable), nil
	}
	r.Blocked = false
	out := Outcome{Result: ResultApplied, From: r.State, To: r.State}
	if r.Side == SideSource && r.Reported.Has(KindDeliveryBlocked) && !r.Reported.Has(KindBlockedRecovered) {
		r.Reported = r.Reported.With(KindBlockedRecovered)
		out.Feedback = []Feedback{{Kind: KindBlockedRecovered, Cause: r.BlockedCause, State: r.State}}
	}
	return r, out, nil
}

// DeliveredAt returns when this node recorded the delivery: StateAt of a
// delivered record, and the zero time for a record in any other state. Delivered
// is final, so the delivery time is the moment the state was entered and no
// second time is kept. On the target it is the delivery time of a signed status
// report, which carries it exactly when the state is delivered.
func (r Record) DeliveredAt() time.Time {
	if r.State != StateDelivered {
		return time.Time{}
	}
	return r.StateAt
}

// StatusReport is what a target tells the sender's node about one delivery
// when the sender pulls it: the target's state and, while the message waits in
// the queue, whether delivery is blocked.
type StatusReport struct {
	State  State
	Reject RejectReason // set when State is StateRejected
	// Blocked and BlockedCause describe a delivery that is accepted but cannot
	// be put in front of the agent. They are set only while State is
	// StateAccepted.
	Blocked      bool
	BlockedCause Cause
}

// StatusReport returns the report a target gives for this record.
func (r Record) StatusReport() StatusReport {
	rep := StatusReport{State: r.State, Reject: r.Reject}
	if r.Blocked {
		rep.Blocked, rep.BlockedCause = true, r.BlockedCause
	}
	return rep
}

// ApplyStatusReport applies a status report pulled from the target to a source
// record and returns the outcome of each fact in the order applied: first the
// state, then, if the record is accepted afterwards, that the delivery is
// blocked or no longer blocked. A report of closed_unconfirmed carries no fact
// the sender can use (it closes by its own clock), so it changes nothing. On an
// error the record is unchanged.
func (r Record) ApplyStatusReport(now time.Time, rep StatusReport) (Record, []Outcome, error) {
	if r.Side != SideSource {
		return r, nil, fmt.Errorf("%w: only the sender applies status reports", ErrWrongSide)
	}
	var state Event
	switch rep.State {
	case StateAccepted:
		state = Event{Type: EventReceipt}
	case StateDelivered:
		state = Event{Type: EventDelivered}
	case StateRejected:
		state = Event{Type: EventRejected, Reject: rep.Reject}
	case StateDeliveryUnknown:
		state = Event{Type: EventDeliveryUnknown}
	case StateClosedUnconfirmed:
		return r, nil, nil
	default:
		return r, nil, fmt.Errorf("%w: a target does not report state %s", ErrInvalidInput, rep.State)
	}
	n, out, err := r.Apply(now, state)
	if err != nil {
		return r, nil, err
	}
	outs := []Outcome{out}
	if n.State != StateAccepted {
		return n, outs, nil
	}
	block := Event{Type: EventUnblocked}
	if rep.Blocked {
		block = Event{Type: EventBlocked, Cause: rep.BlockedCause}
	}
	n, out, err = n.Apply(now, block)
	if err != nil {
		return r, nil, err
	}
	return n, append(outs, out), nil
}

// Tick applies everything that is due by time alone and returns the outcomes
// in the order they happened. Run it whenever the clock may have passed a
// lease expiry, BacklogDeadline, SettleBy or ReportBy of a record, or at
// NextTick.
//
// A target record is settled in this order: a lease that is still open at
// SettleBy becomes delivery_unknown; otherwise a lease past its expiry goes
// back to the queue as a possible duplicate, or becomes delivery_unknown when
// the last allowed lease expired; a message in the queue at BacklogDeadline
// becomes rejected with RejectBacklogExpired, or delivery_unknown when its body
// was handed out before; and any record that is not final after ReportBy
// closes. A control record has no lease and no backlog: accepted at SettleBy it
// becomes delivery_unknown, because the signal was handed to the adapter and
// its reply was never committed, and then closes after ReportBy like any other.
//
// Every timed rule of Tick is a deadline method of Record, listed in
// tickDeadlines, which NextTick reads. Adding a rule means adding its deadline
// method and listing it there, and extending the property-test generator and
// requiredNextTickPaths with it.
func (r Record) Tick(now time.Time) (Record, []Outcome, error) {
	var outs []Outcome
	step := func(n Record, o Outcome, err error) error {
		if err != nil {
			return err
		}
		r = n
		outs = append(outs, o)
		return nil
	}
	if r.controlSettleDeadline().reachedBy(now) {
		if err := step(r.moveToUnknown(now, UnknownHandedOver)); err != nil {
			return r, outs, err
		}
	}
	switch {
	case r.leaseSettleDeadline().reachedBy(now):
		if err := step(r.moveToUnknown(now, UnknownHandedOver)); err != nil {
			return r, outs, err
		}
	case r.leaseExpiryDeadline().reachedBy(now):
		if r.Lease.Handouts >= MaxLeases {
			if err := step(r.moveToUnknown(now, UnknownHandedOver)); err != nil {
				return r, outs, err
			}
		} else {
			r.Lease.Active = false
			outs = append(outs, Outcome{Result: ResultApplied, From: StateAccepted, To: StateAccepted, Requeued: true})
		}
	}
	if r.backlogDeadline().reachedBy(now) {
		if err := step(r.closeForcibly(now, RejectBacklogExpired)); err != nil {
			return r, outs, err
		}
	}
	if r.closeDeadline().reachedBy(now) {
		if err := step(r.moveTo(now, StateClosedUnconfirmed, RejectNone)); err != nil {
			return r, outs, err
		}
	}
	return r, outs, nil
}
