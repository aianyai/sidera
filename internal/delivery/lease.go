package delivery

import (
	"fmt"
	"io"
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
	// IDs holds the identifier of every lease issued so far, oldest first; the
	// last one is the outstanding lease or, when none is outstanding, the most
	// recent. An acknowledgement must name one of them.
	IDs []LeaseID
	// Active is set while a lease is outstanding.
	Active bool
	// IssuedAt is when the outstanding (or last) lease was issued.
	IssuedAt time.Time
	// ExpiresAt is when the outstanding lease runs out unless renewed.
	ExpiresAt time.Time
	// PossibleDuplicate is set when the message may already have been seen:
	// its previous lease ran out without an acknowledgement.
	PossibleDuplicate bool
	// Chunks is the number of chunks the body is handed out in; 0 until the
	// first hand-out. HandedOut[i] says whether chunk i+1 was ever handed out
	// and Acked[i] whether it was acknowledged; only a chunk that was handed
	// out can be acknowledged. Both keep their values across leases.
	Chunks    int
	HandedOut []bool
	Acked     []bool
}

// current returns the identifier of the outstanding lease or, when none is
// outstanding, of the most recent one; the zero ID before the first lease.
func (l Lease) current() LeaseID {
	if len(l.IDs) == 0 {
		return LeaseID{}
	}
	return l.IDs[len(l.IDs)-1]
}

// issued reports whether id is the identifier of a lease issued for this
// message. The unset ID never is.
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
	// PossibleDuplicate tells the agent the message may have been seen before.
	PossibleDuplicate bool
	// Lease is the identifier of the lease the body is handed out under
	// (granted and resent); the page recv prints carries it.
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
// closes the delivery as closeForcibly does.
//
// rand supplies the randomness of a new lease's identifier (see NewLeaseID) and
// is read only when a new lease is issued; a failing source returns an error
// and leaves the record as it was.
func (r Record) Handout(now time.Time, auth AuthorizationKind, chunks, part int, rand io.Reader) (Record, HandoutResult, error) {
	if r.Side != SideTarget {
		return r, HandoutResult{}, fmt.Errorf("%w: only the target hands out bodies", ErrWrongSide)
	}
	if !auth.valid() || chunks < 1 || part < 1 || part > chunks {
		return r, HandoutResult{}, fmt.Errorf("%w: authorization %d, part %d of %d chunks", ErrInvalidInput, auth, part, chunks)
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
		n, out, err := r.closeForcibly(RejectSecondCheckFailed)
		return n, HandoutResult{Action: HandoutDenied, Outcome: out}, err
	}
	live := r.HoldsLiveLease(now)
	var id LeaseID
	if !live {
		var err error
		if id, err = NewLeaseID(rand); err != nil {
			return r, HandoutResult{}, err
		}
	}
	if r.Lease.Chunks == 0 {
		r.Lease.Chunks = chunks
		r.Lease.HandedOut = make([]bool, chunks)
		r.Lease.Acked = make([]bool, chunks)
	}
	handedOut := append([]bool(nil), r.Lease.HandedOut...)
	handedOut[part-1] = true
	r.Lease.HandedOut = handedOut
	if live {
		return r, HandoutResult{Action: HandoutResent, PossibleDuplicate: true, Lease: r.Lease.current(), ExpiresAt: r.Lease.ExpiresAt}, nil
	}
	r.Lease.IDs = append(append([]LeaseID(nil), r.Lease.IDs...), id)
	r.Lease.Handouts++
	r.Lease.Active = true
	r.Lease.IssuedAt = now
	r.Lease.ExpiresAt = now.Add(LeaseDuration)
	return r, HandoutResult{
		Action:            HandoutGranted,
		PossibleDuplicate: r.Lease.PossibleDuplicate,
		Lease:             id,
		ExpiresAt:         r.Lease.ExpiresAt,
	}, nil
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
func (r Record) handoutDue(now time.Time) bool {
	if r.Lease.Active {
		return !now.Before(r.Lease.ExpiresAt) || !now.Before(SettleBy(r.ReportBy))
	}
	return !now.Before(r.BacklogDeadline)
}

// SortForRecv orders records for one recv call: those on which the endpoint
// holds a live lease come first, so a caller that was cut off mid-call gets
// them back at once, flagged as possible duplicates. Order within each group is
// kept.
func SortForRecv(records []Record, now time.Time) {
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].HoldsLiveLease(now) && !records[j].HoldsLiveLease(now)
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
	limit := r.Lease.IssuedAt.Add(LeaseRenewalCap)
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
	// Lease is the identifier of the lease the chunk was handed out under, as
	// the page printed it. It must be one of the leases issued for this
	// message: an acknowledgement that names none of them is not about this
	// message's hand-outs.
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
	// to acknowledge.
	AckRefusedNoHandout
	// AckRefusedLease: the lease ID is unset or was never issued for this
	// message.
	AckRefusedLease
	// AckTooLate: the delivery already ended as rejected or closed_unconfirmed.
	AckTooLate
)

// Ack records the acknowledgement of one chunk. An acknowledgement stays valid
// after its lease has run out and even after the delivery was declared
// delivery_unknown: the agent did see the chunk, and the late acknowledgement
// is the fact that corrects the unknown. The acknowledgement names the lease
// the chunk was handed out under; any lease issued for the message counts, the
// outstanding one or an earlier one, because the chunk was seen under whichever
// lease carried it and a redelivery in between does not unsee it. Only a chunk
// that was handed out can be acknowledged: an acknowledgement for anything else
// would deliver a body the agent never received. The delivery becomes
// delivered when every chunk is acknowledged, not before.
func (r Record) Ack(now time.Time, a Ack) (Record, AckVerdict, Outcome, error) {
	if r.Side != SideTarget {
		return r, 0, Outcome{}, fmt.Errorf("%w: only the target settles leases", ErrWrongSide)
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
	if a.Part < 1 || a.Part > r.Lease.Chunks {
		return r, 0, Outcome{}, fmt.Errorf("%w: part %d of %d", ErrInvalidInput, a.Part, r.Lease.Chunks)
	}
	if !r.Lease.HandedOut[a.Part-1] {
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
	n, out, err := r.moveTo(StateDelivered, RejectNone)
	return n, AckCompleted, out, err
}
