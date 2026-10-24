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
	// to the other kind of target record (a control record is never handed out
	// and a message record never has its reply committed).
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

// Record is one delivery as one side sees it. Build it with NewSourceRecord,
// NewTargetRecord or NewTargetControlRecord and change it only through its
// methods. The zero value is not usable.
type Record struct {
	Side   Side
	State  State
	Sub    SubState
	Reject RejectReason // set while State is StateRejected

	// Control marks the target's record of a control signal (only
	// NewTargetControlRecord sets it, so it is never set on the source side). A control signal
	// is executed by the adapter when it is accepted and answered with a reply;
	// it is never queued for recv, so it has no lease, no chunks and no backlog
	// deadline, and it is delivered by EventReplyCommitted, not by Ack. False
	// for every other record.
	Control bool

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

// NewSourceRecord returns the sender's record of a delivery that its node has
// just accepted from the caller. ReportBy follows from the deadline.
func NewSourceRecord(acceptBefore time.Time) Record {
	return Record{
		Side:         SideSource,
		State:        StateQueued,
		AcceptBefore: acceptBefore,
		ReportBy:     ReportBy(acceptBefore),
	}
}

// NewTargetRecord returns the target's record of a delivery it has just
// accepted. acceptBefore and reportBy are the envelope's values, already
// validated by the caller; endpointMaxQueue is the recipient endpoint's longest
// queue time.
func NewTargetRecord(acceptedAt, acceptBefore, reportBy time.Time, endpointMaxQueue time.Duration) (Record, error) {
	if err := ValidateEndpointMaxQueue(endpointMaxQueue); err != nil {
		return Record{}, err
	}
	return Record{
		Side:            SideTarget,
		State:           StateAccepted,
		AcceptBefore:    acceptBefore,
		ReportBy:        reportBy,
		BacklogDeadline: BacklogDeadline(acceptedAt, reportBy, endpointMaxQueue),
	}, nil
}

// NewTargetControlRecord returns the target's record of a control signal it has
// just accepted. acceptBefore and reportBy are the envelope's values, already
// validated by the caller. The record is never queued for recv and has no
// backlog deadline: the signal is executed, not handed to an agent, and the
// delivery ends when the control reply is committed (EventReplyCommitted) or,
// failing that, is settled by Tick.
func NewTargetControlRecord(acceptBefore, reportBy time.Time) Record {
	return Record{
		Side:         SideTarget,
		State:        StateAccepted,
		Control:      true,
		AcceptBefore: acceptBefore,
		ReportBy:     reportBy,
	}
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
	// EventDeliveryUnknown: the target reports the delivery result unknown.
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
	// counts as blocked is decided by the caller.
	EventBlocked
	// EventUnblocked: the cause of the block is gone.
	EventUnblocked
	// EventReplyCommitted: the control reply was committed together with the
	// execution result (target control records only). It is the only fact that
	// delivers a control record; the sender applies the reply with
	// EventDelivered.
	EventReplyCommitted
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
// change among them goes through moveTo. A target message record is delivered
// only by Ack and a target control record only by EventReplyCommitted, and a
// rejection reported to a target record is a forced closure (see
// closeForcibly).
func (r Record) Apply(now time.Time, ev Event) (Record, Outcome, error) {
	switch ev.Type {
	case EventReceipt:
		return r.moveTo(StateAccepted, RejectNone)
	case EventDelivered:
		if r.Side == SideTarget {
			return r, Outcome{}, fmt.Errorf("%w: on the target a delivery becomes delivered only through Ack, or through EventReplyCommitted for a control signal", ErrWrongSide)
		}
		return r.moveTo(StateDelivered, RejectNone)
	case EventReplyCommitted:
		if !r.Control {
			return r, Outcome{}, fmt.Errorf("%w: a reply is committed only for a control signal on the target", ErrWrongSide)
		}
		return r.moveTo(StateDelivered, RejectNone)
	case EventRejected:
		if ev.Reject == RejectNone {
			return r, Outcome{}, fmt.Errorf("%w: rejection without a reason", ErrInvalidEvent)
		}
		if r.Side == SideTarget {
			return r.closeForcibly(ev.Reject)
		}
		return r.moveTo(StateRejected, ev.Reject)
	case EventDeliveryUnknown:
		return r.moveTo(StateDeliveryUnknown, RejectNone)
	case EventExpiryQueried:
		return r.judgeExpiry(ev)
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
	}
	return r, Outcome{}, fmt.Errorf("%w: unknown event type %d", ErrInvalidEvent, ev.Type)
}

// moveTo is the only place that changes State.
func (r Record) moveTo(to State, reason RejectReason) (Record, Outcome, error) {
	from := r.State
	switch {
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
	n := r
	n.State = to
	n.Sub = SubNone
	if to == StateRejected {
		n.Reject = reason
	}
	if to != StateAccepted {
		n.Blocked = false
		n.Lease.Active = false
	}
	out := Outcome{Result: ResultApplied, From: from, To: to}
	if r.Side == SideSource {
		for _, k := range TransitionFeedback(from, to, r.Reported) {
			fb := Feedback{Kind: k, State: to}
			switch k {
			case KindRejected:
				fb.Reject = reason
			case KindBlockedRecovered:
				fb.Cause = r.BlockedCause
			}
			n.Reported = n.Reported.With(k)
			out.Feedback = append(out.Feedback, fb)
		}
	}
	return n, out, nil
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

// closeForcibly ends a target delivery that cannot go on for a reason other
// than the agent's own handling: its backlog deadline passed, the second
// authorization check denied it, or its endpoint was closed. The reason is
// recorded only when the delivery was never handed over (see handedOver). Once
// it was, the agent may have seen the body or the adapter may have executed
// the signal, and "rejected" would tell the sender it was not delivered, so the
// delivery becomes delivery_unknown, which a late acknowledgement or committed
// reply can still correct.
func (r Record) closeForcibly(reason RejectReason) (Record, Outcome, error) {
	if r.handedOver() {
		return r.moveTo(StateDeliveryUnknown, RejectNone)
	}
	return r.moveTo(StateRejected, reason)
}

// judgeExpiry moves a queued delivery to expired_unconfirmed once the deadline
// plus the clock tolerance has passed and a query made after that found no
// receipt. The tolerance keeps a target whose clock runs behind from being
// judged expired while it can still legitimately accept the message.
func (r Record) judgeExpiry(ev Event) (Record, Outcome, error) {
	if r.Side != SideSource {
		return r, Outcome{}, fmt.Errorf("%w: expiry is judged by the sender", ErrWrongSide)
	}
	if r.State != StateQueued {
		return r, ignored(r.State, IgnoreNotApplicable), nil
	}
	if !ev.QueriedAt.After(ExpiryJudgeableAfter(r.AcceptBefore)) {
		return r, ignored(r.State, IgnoreNotDueYet), nil
	}
	return r.moveTo(StateExpiredUnconfirmed, RejectNone)
}

// closeIfDue closes a delivery that is still not final after ReportBy.
func (r Record) closeIfDue(now time.Time) (Record, Outcome, error) {
	if r.State.IsTerminal() {
		return r, ignored(r.State, IgnoreAlreadyFinal), nil
	}
	if !now.After(r.ReportBy) {
		return r, ignored(r.State, IgnoreNotDueYet), nil
	}
	return r.moveTo(StateClosedUnconfirmed, RejectNone)
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
// even if the delivery is blocked again later.
func (r Record) noteBlocked(ev Event) (Record, Outcome, error) {
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
// lease expiry, BacklogDeadline, SettleBy or ReportBy of a record.
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
	if r.Side == SideTarget && r.Control && r.State == StateAccepted && !now.Before(SettleBy(r.ReportBy)) {
		if err := step(r.moveTo(StateDeliveryUnknown, RejectNone)); err != nil {
			return r, outs, err
		}
	}
	if r.hasQueue() && r.State == StateAccepted && r.Lease.Active {
		switch {
		case !now.Before(SettleBy(r.ReportBy)):
			if err := step(r.moveTo(StateDeliveryUnknown, RejectNone)); err != nil {
				return r, outs, err
			}
		case !now.Before(r.Lease.ExpiresAt):
			if r.Lease.Handouts >= MaxLeases {
				if err := step(r.moveTo(StateDeliveryUnknown, RejectNone)); err != nil {
					return r, outs, err
				}
			} else {
				r.Lease.Active = false
				outs = append(outs, Outcome{Result: ResultApplied, From: StateAccepted, To: StateAccepted, Requeued: true})
			}
		}
	}
	if r.hasQueue() && r.State == StateAccepted && !r.Lease.Active && !now.Before(r.BacklogDeadline) {
		if err := step(r.closeForcibly(RejectBacklogExpired)); err != nil {
			return r, outs, err
		}
	}
	if !r.State.IsTerminal() && now.After(r.ReportBy) {
		if err := step(r.moveTo(StateClosedUnconfirmed, RejectNone)); err != nil {
			return r, outs, err
		}
	}
	return r, outs, nil
}
