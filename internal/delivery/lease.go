package delivery

import (
	"fmt"
	"sort"
	"time"
)

// Lease is the pull-lease bookkeeping of one target delivery. A message is
// leased to its recipient endpoint when recv hands the body out; the lease
// lasts until the body is acknowledged or the lease runs out.
type Lease struct {
	// Handouts counts the leases issued so far: the first hand-out and its
	// redeliveries.
	Handouts int
	// IDs holds every page identifier under which some part of this message was
	// handed out, oldest first. An acknowledgement must name one of them.
	IDs []LeaseID
	// Active is set while a lease is outstanding.
	Active bool
	// IssuedAt is when the outstanding (or last) lease was issued.
	IssuedAt time.Time
	// ExpiresAt is when the outstanding lease runs out unless renewed.
	ExpiresAt time.Time
	// Chunks is the number of chunks the body is handed out in; 0 until the
	// first hand-out. HandedOut[i] says whether chunk i+1 was ever handed out
	// and Acked[i] whether it was acknowledged; only a chunk that was handed
	// out can be acknowledged. Both keep their values across leases.
	Chunks    int
	HandedOut []bool
	Acked     []bool
}

// isZero reports whether no lease was ever issued: the state of a message
// before its first hand-out, and the only state of a record that is never
// queued for recv.
func (l Lease) isZero() bool {
	return l.Handouts == 0 && len(l.IDs) == 0 && !l.Active && l.IssuedAt.IsZero() && l.ExpiresAt.IsZero() &&
		l.Chunks == 0 && len(l.HandedOut) == 0 && len(l.Acked) == 0
}

// renewalLimit returns the instant past which the lease cannot be renewed, however
// often: LeaseRenewalCap after it was issued.
func (l Lease) renewalLimit() time.Time { return l.IssuedAt.Add(LeaseRenewalCap) }

// issued reports whether id is an identifier under which some part of this
// message was handed out. The unset ID never is.
func (l Lease) issued(id LeaseID) bool {
	for _, have := range l.IDs {
		if have == id {
			return !id.IsZero()
		}
	}
	return false
}

// HandoutAction says what recv does with one message.
type HandoutAction uint8

const (
	// HandoutGranted: a new lease was issued; hand the body out.
	HandoutGranted HandoutAction = iota + 1
	// HandoutResent: the endpoint already holds a live lease on the message;
	// hand the body out again under it, flagged as a possible duplicate.
	HandoutResent
	// HandoutSkipped: authorization is suspended; leave the message queued,
	// without a lease and without a prompt, and tell nobody.
	HandoutSkipped
	// HandoutDenied: authorization is denied; the delivery is closed: rejected
	// if the body was never handed out, delivery_unknown if it was (see
	// Outcome.To).
	HandoutDenied
	// HandoutUnavailable: the message cannot be handed out now; Reason says why.
	HandoutUnavailable
)

// UnavailableReason says why a message cannot be handed out.
type UnavailableReason uint8

const (
	UnavailableNone UnavailableReason = iota
	// UnavailableNotQueued: the delivery is not in the accepted state.
	UnavailableNotQueued
	// UnavailableDue: a deadline has passed; run Tick first so the delivery
	// takes its due state.
	UnavailableDue
)

// HandoutResult is what recv must do for one message.
type HandoutResult struct {
	Action HandoutAction
	// PossibleDuplicate tells the agent this part of the message may have been
	// seen before (granted and resent); it is Record.PossibleDuplicate as it
	// stood before this hand-out.
	PossibleDuplicate bool
	// Lease is the identifier the caller passed (granted and resent): the one
	// the page recv prints carries.
	Lease LeaseID
	// ExpiresAt is when the lease runs out (granted and resent).
	ExpiresAt time.Time
	Reason    UnavailableReason
	// Outcome is the transition made by HandoutDenied.
	Outcome Outcome
}

// HoldsLiveLease reports whether the record has a lease that has not run out.
func (r Record) HoldsLiveLease(now time.Time) bool {
	return r.Lease.Active && now.Before(r.Lease.ExpiresAt)
}

// Handout decides what recv does with this message and, when it hands the body
// out, records that chunk part (counted from 1, see NextPart) was handed out;
// recording it in the same step keeps "a lease was issued" and "a chunk left
// the target" from ever disagreeing. chunks is the number of chunks the caller
// splits the body into (1 when it fits); it is fixed at the first hand-out.
// Run Tick before Handout: a message past its deadlines is refused here rather
// than handed out and settled behind the agent's back.
//
// The second authorization check is made here, at the moment the body would be
// handed out. A suspended check leaves the message untouched. A denied check
// closes the delivery as closeForcibly does. An exempt record (a notice of the
// system endpoint, see Record.Exempt) is not judged by the policy and has no
// second check: auth must be the zero AuthorizationKind, and any verdict for it
// is invalid input. Every other record needs a valid auth.
//
// lease is the identifier of the page this hand-out belongs to. The caller
// generates it once per recv, because one recv hands out a whole page, possibly
// several messages, under one lease and the page prints one identifier. Handout
// records it in Lease.IDs, so an acknowledgement that names it is accepted for
// every part handed out on that page; a zero lease is ErrInvalidInput. A new
// lease or a re-send under a live lease records it alike; the lease's times
// and the hand-out count change only when a new lease is issued.
func (r Record) Handout(now time.Time, auth AuthorizationKind, chunks, part int, lease LeaseID) (Record, HandoutResult, error) {
	if r.Side != SideTarget {
		return r, HandoutResult{}, fmt.Errorf("%w: only the target hands out bodies", ErrWrongSide)
	}
	if r.Control {
		return r, HandoutResult{}, fmt.Errorf("%w: a control signal is never handed out by recv", ErrWrongSide)
	}
	authFits := auth.valid()
	if r.Exempt {
		authFits = auth == 0
	}
	if !authFits || chunks < 1 || part < 1 || part > chunks || lease.IsZero() {
		return r, HandoutResult{}, fmt.Errorf("%w: authorization %d, part %d of %d chunks, lease %s", ErrInvalidInput, auth, part, chunks, lease)
	}
	if r.Lease.Chunks != 0 && r.Lease.Chunks != chunks {
		return r, HandoutResult{}, fmt.Errorf("%w: chunk count changed from %d to %d", ErrInvalidInput, r.Lease.Chunks, chunks)
	}
	if r.State != StateAccepted {
		return r, HandoutResult{Action: HandoutUnavailable, Reason: UnavailableNotQueued}, nil
	}
	if r.handoutDue(now) {
		return r, HandoutResult{Action: HandoutUnavailable, Reason: UnavailableDue}, nil
	}
	switch auth {
	case AuthorizationSuspend:
		return r, HandoutResult{Action: HandoutSkipped}, nil
	case AuthorizationDeny:
		n, out, err := r.closeForcibly(now, RejectSecondCheckFailed)
		return n, HandoutResult{Action: HandoutDenied, Outcome: out}, err
	}
	live := r.HoldsLiveLease(now)
	if r.Lease.Chunks == 0 {
		r.Lease.Chunks = chunks
		r.Lease.HandedOut = make([]bool, chunks)
		r.Lease.Acked = make([]bool, chunks)
	}
	dup := r.PossibleDuplicate(part)
	handedOut := append([]bool(nil), r.Lease.HandedOut...)
	handedOut[part-1] = true
	r.Lease.HandedOut = handedOut
	if !r.Lease.issued(lease) {
		r.Lease.IDs = append(append([]LeaseID(nil), r.Lease.IDs...), lease)
	}
	if live {
		return r, HandoutResult{Action: HandoutResent, PossibleDuplicate: dup, Lease: lease, ExpiresAt: r.Lease.ExpiresAt}, nil
	}
	r.Lease.Handouts++
	r.Lease.Active = true
	r.Lease.IssuedAt = now
	r.Lease.ExpiresAt = now.Add(LeaseDuration)
	return r, HandoutResult{
		Action:            HandoutGranted,
		PossibleDuplicate: dup,
		Lease:             lease,
		ExpiresAt:         r.Lease.ExpiresAt,
	}, nil
}

// PossibleDuplicate reports whether chunk part, counted from 1, may already
// have been seen: it was handed out before. A chunk never handed out is not a
// possible duplicate, whatever happened to the other chunks of the message.
// Handout returns the value as it stood before the hand-out it records.
func (r Record) PossibleDuplicate(part int) bool {
	return part >= 1 && part <= len(r.Lease.HandedOut) && r.Lease.HandedOut[part-1]
}

// NextPart returns the first chunk, counted from 1, that was not acknowledged
// yet: the chunk to hand out next. It is 1 before the first hand-out and 0
// when every chunk is acknowledged.
func (r Record) NextPart() int {
	if r.Lease.Chunks == 0 {
		return 1
	}
	for i, done := range r.Lease.Acked {
		if !done {
			return i + 1
		}
	}
	return 0
}

// handoutDue reports whether a deadline that Tick settles has already passed.
// Only an accepted message of the target's queue is handed out, so the lease
// and backlog deadlines apply as they do in Tick.
func (r Record) handoutDue(now time.Time) bool {
	return r.leaseSettleDeadline().reachedBy(now) || r.leaseExpiryDeadline().reachedBy(now) || r.backlogDeadline().reachedBy(now)
}

// SortForRecv orders the items that hold the records of one recv call: those on
// which the endpoint holds a live lease come first, so a caller that was cut off
// mid-call gets them back at once, flagged as possible duplicates. Order within
// each group is kept. record gives the record an item holds, so the caller sorts
// the stored records it works with and not copies of what is inside them.
func SortForRecv[T any](items []T, record func(T) Record, now time.Time) {
	sort.SliceStable(items, func(i, j int) bool {
		return record(items[i]).HoldsLiveLease(now) && !record(items[j]).HoldsLiveLease(now)
	})
}

// RenewResult says what happened to a renewal request.
type RenewResult uint8

const (
	// Renewed: the lease was extended.
	Renewed RenewResult = iota + 1
	// RenewNoLease: the message has no outstanding lease.
	RenewNoLease
	// RenewLeaseExpired: the lease already ran out; Tick requeues the message.
	RenewLeaseExpired
	// RenewNoActiveTurn: the endpoint has no active turn. A lease is renewed
	// only while the agent is working, otherwise a forgotten message would hold
	// its lease for the whole cap.
	RenewNoActiveTurn
	// RenewCapReached: the lease already lives as long as LeaseRenewalCap allows.
	RenewCapReached
)

// Renew extends the lease by LeaseDuration from now, never beyond
// LeaseRenewalCap after it was issued. The cap bounds MaxRedeliveryPeriod and
// with it ReportBy, which is why it is protocol-level and not configurable.
func (r Record) Renew(now time.Time, activeTurn bool) (Record, RenewResult) {
	switch {
	case !r.Lease.Active:
		return r, RenewNoLease
	case !now.Before(r.Lease.ExpiresAt):
		return r, RenewLeaseExpired
	case !activeTurn:
		return r, RenewNoActiveTurn
	}
	limit := r.Lease.renewalLimit()
	if !r.Lease.ExpiresAt.Before(limit) {
		return r, RenewCapReached
	}
	next := now.Add(LeaseDuration)
	if next.After(limit) {
		next = limit
	}
	r.Lease.ExpiresAt = next
	return r, Renewed
}

// Ack is an acknowledgement of one chunk of a message.
type Ack struct {
	// Part is the chunk, counted from 1.
	Part int
	// Lease is the identifier of the page the chunk was handed out on, as the
	// page printed it. It must be one of Lease.IDs: an acknowledgement that
	// names none of them is not about this message's hand-outs.
	Lease LeaseID
	// TokenMatches states that the acknowledgement carries the token of the
	// endpoint the message is for. The caller verifies the token; nobody else's
	// acknowledgement counts.
	TokenMatches bool
}

// AckVerdict says what an acknowledgement did.
type AckVerdict uint8

const (
	// AckRecorded: the chunk is acknowledged; others are still outstanding.
	AckRecorded AckVerdict = iota + 1
	// AckCompleted: the last chunk was acknowledged and the delivery is
	// delivered (Outcome describes the transition).
	AckCompleted
	// AckDuplicate: the chunk was already acknowledged, or the delivery was
	// already delivered.
	AckDuplicate
	// AckRefusedToken: the token is not the owning endpoint's.
	AckRefusedToken
	// AckRefusedNoHandout: that chunk was never handed out, so there is nothing
	// to acknowledge. A chunk number outside 1..Chunks is such a chunk too: the
	// message has no such part, and the acknowledgement is about something the
	// agent was never given.
	AckRefusedNoHandout
	// AckRefusedLease: the lease ID is unset or names no page this message was
	// handed out on.
	AckRefusedLease
	// AckTooLate: the delivery already ended as rejected or closed_unconfirmed.
	AckTooLate
)

// Ack records the acknowledgement of one chunk. An acknowledgement stays valid
// after its lease has run out and even after the delivery was declared
// delivery_unknown: the agent did see the chunk, and the late acknowledgement
// is the fact that corrects the unknown. The acknowledgement names the page
// the chunk was handed out on; any page the message was handed out on counts,
// the latest one or an earlier one, because the chunk was seen on whichever
// page carried it and a redelivery in between does not unsee it. Only a chunk
// that was handed out can be acknowledged: an acknowledgement for anything else,
// a chunk number outside the message included, would deliver a body the agent
// never received, and is refused as AckRefusedNoHandout. The delivery becomes
// delivered when every chunk is acknowledged, not before.
func (r Record) Ack(now time.Time, a Ack) (Record, AckVerdict, Outcome, error) {
	if r.Side != SideTarget {
		return r, 0, Outcome{}, fmt.Errorf("%w: only the target settles leases", ErrWrongSide)
	}
	if r.Control {
		return r, 0, Outcome{}, fmt.Errorf("%w: a control signal has no lease to acknowledge", ErrWrongSide)
	}
	if !a.TokenMatches {
		return r, AckRefusedToken, Outcome{}, nil
	}
	switch r.State {
	case StateDelivered:
		return r, AckDuplicate, Outcome{}, nil
	case StateRejected, StateClosedUnconfirmed:
		return r, AckTooLate, Outcome{}, nil
	case StateAccepted, StateDeliveryUnknown:
	default:
		return r, 0, Outcome{}, fmt.Errorf("%w: acknowledgement in state %s", ErrIllegalTransition, r.State)
	}
	if r.Lease.Chunks == 0 {
		return r, AckRefusedNoHandout, Outcome{}, nil
	}
	if !r.Lease.issued(a.Lease) {
		return r, AckRefusedLease, Outcome{}, nil
	}
	if a.Part < 1 || a.Part > r.Lease.Chunks || !r.Lease.HandedOut[a.Part-1] {
		return r, AckRefusedNoHandout, Outcome{}, nil
	}
	if r.Lease.Acked[a.Part-1] {
		return r, AckDuplicate, Outcome{}, nil
	}
	acked := append([]bool(nil), r.Lease.Acked...)
	acked[a.Part-1] = true
	r.Lease.Acked = acked
	for _, done := range acked {
		if !done {
			return r, AckRecorded, Outcome{}, nil
		}
	}
	n, out, err := r.moveTo(now, StateDelivered, RejectNone)
	return n, AckCompleted, out, err
}
