package delivery

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/timing"
)

// violationOf returns the invariant Valid reports, ViolationNone for a valid
// record, and fails the test when the error is not the typed error of Valid.
func violationOf(t *testing.T, err error) Violation {
	t.Helper()
	if err == nil {
		return ViolationNone
	}
	var inv *InvalidRecordError
	if !errors.As(err, &inv) || !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("Valid returned %T %v, want *InvalidRecordError matching ErrInvalidRecord", err, err)
	}
	return inv.Violation
}

// cloneRecord copies a record together with the slices of its lease, so a
// mutation of the copy never reaches the original.
func cloneRecord(r Record) Record {
	r.Lease.IDs = append([]LeaseID(nil), r.Lease.IDs...)
	r.Lease.HandedOut = append([]bool(nil), r.Lease.HandedOut...)
	r.Lease.Acked = append([]bool(nil), r.Lease.Acked...)
	return r
}

// lastKind is the highest Kind that exists, found by walking the kinds so no
// test names it; kindSetBits is the width of a KindSet.
var lastKind = func() Kind {
	k := KindUnreachable
	for (k + 1).valid() {
		k++
	}
	return k
}()

const kindSetBits = 16

func kindSetOf(kinds ...Kind) KindSet {
	var s KindSet
	for _, k := range kinds {
		s = s.With(k)
	}
	return s
}

// validBase is a record reached through the constructors and the transitions,
// the starting point of a row of the table of invalid records.
type validBase struct {
	name  string
	build func(t *testing.T) Record
}

func applyAll(t *testing.T, r Record, now time.Time, evs ...Event) Record {
	t.Helper()
	for _, ev := range evs {
		r, _ = mustApply(t, r, now, ev)
	}
	return r
}

var (
	validAcceptBefore = at(10 * time.Minute)
	farFuture         = at(100 * time.Hour)
)

var (
	baseQueued      = validBase{"source queued", func(t *testing.T) Record { return NewSourceRecord(validAcceptBefore) }}
	baseSrcAccepted = validBase{"source accepted", func(t *testing.T) Record {
		return applyAll(t, NewSourceRecord(validAcceptBefore), at(time.Minute), Event{Type: EventReceipt})
	}}
	baseSrcRejected = validBase{"source rejected", func(t *testing.T) Record {
		return applyAll(t, NewSourceRecord(validAcceptBefore), at(time.Minute), Event{Type: EventRejected, Reject: RejectTooLarge})
	}}
	baseSrcDelivered = validBase{"source delivered", func(t *testing.T) Record {
		return applyAll(t, NewSourceRecord(validAcceptBefore), at(time.Minute), Event{Type: EventDelivered})
	}}
	baseSrcUnknownDelivered = validBase{"source delivered after delivery_unknown", func(t *testing.T) Record {
		return applyAll(t, NewSourceRecord(validAcceptBefore), at(time.Minute), Event{Type: EventDeliveryUnknown, Unknown: UnknownHandedOver}, Event{Type: EventDelivered})
	}}
	baseSrcExpired = validBase{"source expired_unconfirmed", func(t *testing.T) Record {
		return applyAll(t, NewSourceRecord(validAcceptBefore), farFuture, Event{Type: EventExpiryQueried, QueriedAt: farFuture})
	}}
	baseSrcBlocked = validBase{"source accepted and blocked", func(t *testing.T) Record {
		return applyAll(t, NewSourceRecord(validAcceptBefore), at(time.Minute), Event{Type: EventReceipt}, Event{Type: EventBlocked, Cause: CauseAdapterOffline})
	}}
	baseSrcUnblocked = validBase{"source accepted, blocked and unblocked", func(t *testing.T) Record {
		return applyAll(t, NewSourceRecord(validAcceptBefore), at(time.Minute), Event{Type: EventReceipt}, Event{Type: EventBlocked, Cause: CauseAdapterOffline}, Event{Type: EventUnblocked})
	}}
	baseTgt       = validBase{"target accepted", func(t *testing.T) Record { return targetRecord(t, t0, 30*time.Minute) }}
	baseTgtLeased = validBase{"target leased, 2 chunks, one handed out", func(t *testing.T) Record {
		return handOut(t, targetRecord(t, t0, 30*time.Minute), at(time.Minute), 2, 1)
	}}
	baseTgtDelivered = validBase{"target delivered", func(t *testing.T) Record {
		return deliverBody(t, targetRecord(t, t0, 30*time.Minute), at(time.Minute))
	}}
	baseTgtUnknown = validBase{"target delivery_unknown", func(t *testing.T) Record {
		r := handOut(t, targetRecord(t, t0, 30*time.Minute), at(time.Minute), 2, 1)
		r, _, err := r.Tick(SettleBy(r.ReportBy))
		if err != nil || r.State != StateDeliveryUnknown {
			t.Fatalf("Tick at the settle deadline: state %s, %v", r.State, err)
		}
		return r
	}}
	baseTgtRejected = validBase{"target rejected", func(t *testing.T) Record {
		return applyAll(t, targetRecord(t, t0, 30*time.Minute), at(time.Minute), Event{Type: EventRejected, Reject: RejectEndpointClosed})
	}}
	baseControlRecord = validBase{"control accepted", func(t *testing.T) Record { return controlRecord() }}
	baseLostSource    = validBase{"lost source record", func(t *testing.T) Record { return newLostRecord(t, SideSource, false) }}
	baseLostTarget    = validBase{"lost target record", func(t *testing.T) Record { return newLostRecord(t, SideTarget, false) }}
)

func newLostRecord(t *testing.T, side Side, control bool) Record {
	t.Helper()
	r, _ := newLost(t, side, control)
	return r
}

// TestRecordValidRefusesImpossibleRecords is the table of invalid records: one
// row per invariant, each named after it. Every row starts from a record the
// package really produces, so the row fails for the one change it makes.
func TestRecordValidRefusesImpossibleRecords(t *testing.T) {
	someID := freshLease()
	rows := []struct {
		name   string
		base   validBase
		mutate func(r *Record)
		want   Violation
	}{
		// side
		{"side is unset", baseQueued, func(r *Record) { r.Side = 0 }, ViolationSide},
		{"side is unknown", baseQueued, func(r *Record) { r.Side = 3 }, ViolationSide},
		// control
		{"control is set on a source record", baseQueued, func(r *Record) { r.Control = true }, ViolationControlSide},
		// state fits the kind of record
		{"a target is never queued", baseTgt, func(r *Record) { r.State = StateQueued }, ViolationState},
		{"a target is never expired_unconfirmed", baseTgt, func(r *Record) { r.State = StateExpiredUnconfirmed }, ViolationState},
		{"a control record is never rejected", baseControlRecord, func(r *Record) { r.State, r.Reject = StateRejected, RejectTooLarge }, ViolationState},
		{"the state is unset", baseQueued, func(r *Record) { r.State = StateInvalid }, ViolationState},
		{"the state is unknown", baseQueued, func(r *Record) { r.State = 99 }, ViolationState},
		// rejection reason
		{"a rejected record has a reason", baseSrcRejected, func(r *Record) { r.Reject = RejectNone }, ViolationRejectReason},
		{"only a rejected record has a reason", baseQueued, func(r *Record) { r.Reject = RejectTooLarge }, ViolationRejectReason},
		{"the rejection reason is a known one", baseSrcRejected, func(r *Record) { r.Reject = 200 }, ViolationRejectReason},
		// sub-state
		{"notified is not a source sub-state", baseQueued, func(r *Record) { r.Sub = SubNotified }, ViolationSubState},
		{"notified only while accepted", baseTgtDelivered, func(r *Record) { r.Sub = SubNotified }, ViolationSubState},
		{"a control record is never notified", baseControlRecord, func(r *Record) { r.Sub = SubNotified }, ViolationSubState},
		{"the sub-state is a known one", baseTgt, func(r *Record) { r.Sub = 7 }, ViolationSubState},
		// unknown cause and the replacement of a lost record
		{"a cause is set only with delivery_unknown", baseQueued, func(r *Record) { r.Unknown = UnknownHandedOver }, ViolationUnknownCause},
		{"delivery_unknown has a cause", baseSrcUnknownDelivered, func(r *Record) { r.State, r.Unknown = StateDeliveryUnknown, UnknownCauseNone }, ViolationUnknownCause},
		{"a lost record keeps its replacement marker's cause", baseLostTarget, func(r *Record) { r.Unknown = UnknownHandedOver }, ViolationUnknownCause},
		{"lost_at_target on a target is the replacement's", baseTgtUnknown, func(r *Record) { r.Unknown = UnknownLostAtTarget }, ViolationUnknownCause},
		{"the marker goes only on a replacement's states", baseQueued, func(r *Record) { r.Lost = true }, ViolationLostShape},
		{"a replacement has a report_by and no envelope deadlines", baseLostTarget, func(r *Record) { r.AcceptBefore = validAcceptBefore }, ViolationLostShape},
		{"a replacement has no lease", baseLostTarget, func(r *Record) { r.Lease.Handouts = 1 }, ViolationLostShape},
		{"a source replacement has reported exactly its own notice", baseLostSource, func(r *Record) { r.Reported = 0 }, ViolationLostShape},
		// deadlines
		{"accept_before is set", baseQueued, func(r *Record) { r.AcceptBefore = time.Time{} }, ViolationDeadlinesUnset},
		{"report_by is set", baseQueued, func(r *Record) { r.ReportBy = time.Time{} }, ViolationDeadlinesUnset},
		{"a source report_by follows from accept_before", baseQueued, func(r *Record) { r.ReportBy = r.ReportBy.Add(time.Nanosecond) }, ViolationReportBy},
		{"a target report_by follows from accept_before", baseTgt, func(r *Record) { r.ReportBy = r.AcceptBefore }, ViolationReportBy},
		{"a queued message has a backlog deadline", baseTgt, func(r *Record) { r.BacklogDeadline = time.Time{} }, ViolationBacklogDeadline},
		{"the backlog deadline is not past the settle deadline", baseTgt, func(r *Record) { r.BacklogDeadline = SettleBy(r.ReportBy).Add(time.Nanosecond) }, ViolationBacklogDeadline},
		{"a source record has no backlog deadline", baseQueued, func(r *Record) { r.BacklogDeadline = t0 }, ViolationBacklogDeadline},
		{"a control record has no backlog deadline", baseControlRecord, func(r *Record) { r.BacklogDeadline = t0 }, ViolationBacklogDeadline},
		// produced feedback
		{"a target record produces no feedback", baseTgt, func(r *Record) { r.Reported = kindSetOf(KindUnreachable) }, ViolationReportedOnTarget},
		{"the produced feedback holds only existing kinds (beyond the last)", baseQueued, func(r *Record) { r.Reported = KindSet(1 << 12) }, ViolationReportedUnknownKind},
		{"the produced feedback holds only existing kinds (the zero kind)", baseQueued, func(r *Record) { r.Reported = KindSet(1) }, ViolationReportedUnknownKind},
		{"the rejection notice exists only for a rejected record", baseQueued, func(r *Record) { r.Reported = kindSetOf(KindRejected) }, ViolationReportedRejected},
		{"a rejected source record reported its rejection", baseSrcRejected, func(r *Record) { r.Reported = 0 }, ViolationReportedRejected},
		{"a delivery_unknown source record reported it", baseSrcUnknownDelivered, func(r *Record) { r.State, r.Unknown, r.Reported = StateDeliveryUnknown, UnknownHandedOver, 0 }, ViolationReportedDeliveryUnknown},
		{"the delivery_unknown notice is not produced while queued", baseQueued, func(r *Record) { r.Reported = kindSetOf(KindDeliveryUnknown) }, ViolationReportedDeliveryUnknown},
		{"the delivery_unknown notice is not produced while accepted", baseSrcAccepted, func(r *Record) { r.Reported = kindSetOf(KindDeliveryUnknown) }, ViolationReportedDeliveryUnknown},
		{"the correction of delivery_unknown needs delivered", baseSrcAccepted, func(r *Record) { r.Reported = kindSetOf(KindDeliveryUnknown, KindUnknownCorrected) }, ViolationReportedDeliveryUnknown},
		{"delivered after delivery_unknown reported the correction", baseSrcUnknownDelivered, func(r *Record) { r.Reported = kindSetOf(KindDeliveryUnknown) }, ViolationReportedDeliveryUnknown},
		{"an expired_unconfirmed source record reported it", baseSrcExpired, func(r *Record) { r.Reported = 0 }, ViolationReportedExpiry},
		{"the expiry notice is not produced while queued", baseQueued, func(r *Record) { r.Reported = kindSetOf(KindExpiredUnconfirmed) }, ViolationReportedExpiry},
		{"the expiry correction needs the expiry notice", baseSrcAccepted, func(r *Record) { r.Reported = kindSetOf(KindExpiredCorrected) }, ViolationReportedExpiry},
		{"the expiry correction is not produced while expired", baseSrcExpired, func(r *Record) { r.Reported = kindSetOf(KindExpiredUnconfirmed, KindExpiredCorrected) }, ViolationReportedExpiry},
		{"an accepted record after expiry reported the correction", baseSrcAccepted, func(r *Record) { r.Reported = kindSetOf(KindExpiredUnconfirmed) }, ViolationReportedExpiry},
		{"a delivered record after expiry reported the correction", baseSrcDelivered, func(r *Record) { r.Reported = kindSetOf(KindExpiredUnconfirmed) }, ViolationReportedExpiry},
		{"the recovery needs the blocked notice", baseSrcAccepted, func(r *Record) { r.Reported = kindSetOf(KindBlockedRecovered) }, ViolationReportedBlocked},
		{"the blocked notice is not produced while queued", baseQueued, func(r *Record) {
			r.Reported, r.BlockedCause = kindSetOf(KindDeliveryBlocked), CauseAdapterOffline
		}, ViolationReportedBlocked},
		{"a delivered record reported the end of a reported block", baseSrcDelivered, func(r *Record) {
			r.Reported, r.BlockedCause = kindSetOf(KindDeliveryBlocked), CauseAdapterOffline
		}, ViolationReportedBlocked},
		{"an accepted record that is not blocked reported the end of a reported block", baseSrcUnblocked, func(r *Record) { r.Reported = kindSetOf(KindDeliveryBlocked) }, ViolationReportedBlocked},
		{"a cause of a block is recorded only with a blocked notice", baseSrcAccepted, func(r *Record) { r.BlockedCause = CauseAdapterOffline }, ViolationReportedBlocked},
		{"a blocked notice has the cause of the block", baseSrcUnblocked, func(r *Record) { r.BlockedCause = CauseNone }, ViolationReportedBlocked},
		// blocked
		{"a queued source record is not blocked", baseQueued, func(r *Record) { r.Blocked = true }, ViolationBlockedState},
		{"a delivered record is not blocked", baseTgtDelivered, func(r *Record) { r.Blocked = true }, ViolationBlockedState},
		{"a control record is never blocked", baseControlRecord, func(r *Record) { r.Blocked = true }, ViolationBlockedState},
		{"a blocked record has a cause", baseTgt, func(r *Record) { r.Blocked = true }, ViolationBlockedCause},
		{"the cause of a block is a blocking cause", baseTgt, func(r *Record) { r.Blocked, r.BlockedCause = true, CauseNetwork }, ViolationBlockedCause},
		{"the cause of a block is a known cause", baseTgt, func(r *Record) { r.BlockedCause = 99 }, ViolationBlockedCause},
		{"a control record has no cause of a block", baseControlRecord, func(r *Record) { r.BlockedCause = CauseAdapterOffline }, ViolationBlockedCause},
		// lease: which records have one
		{"a source record has no lease", baseQueued, func(r *Record) { r.Lease.Handouts = 1 }, ViolationLeaseUnused},
		{"a control record has no lease", baseControlRecord, func(r *Record) { r.Lease.Chunks = 1 }, ViolationLeaseUnused},
		// lease: chunks
		{"nothing is leased before the first hand-out", baseTgt, func(r *Record) { r.Lease.Active = true }, ViolationLeaseChunks},
		{"the chunk flags cover every chunk (handed out)", baseTgtLeased, func(r *Record) { r.Lease.Chunks = 3 }, ViolationLeaseChunks},
		{"the chunk count is not negative", baseTgtLeased, func(r *Record) { r.Lease.Chunks = -1 }, ViolationLeaseChunks},
		{"the chunk flags cover every chunk (acknowledged)", baseTgtLeased, func(r *Record) { r.Lease.Acked = r.Lease.Acked[:1] }, ViolationLeaseChunks},
		{"only a chunk handed out is acknowledged", baseTgtLeased, func(r *Record) { r.Lease.Acked[1] = true }, ViolationLeaseAckedUnseen},
		{"counted chunks mean a hand-out happened (no flag set)", baseTgtLeased, func(r *Record) { r.Lease.HandedOut[0] = false }, ViolationLeaseNoHandout},
		{"counted chunks mean a hand-out happened (no lease issued)", baseTgtLeased, func(r *Record) { r.Lease.Handouts = 0 }, ViolationLeaseNoHandout},
		// lease: identifiers
		{"a handed-out message has a page identifier", baseTgtLeased, func(r *Record) { r.Lease.IDs = nil }, ViolationLeaseIDs},
		{"a page identifier is set", baseTgtLeased, func(r *Record) { r.Lease.IDs = []LeaseID{{}} }, ViolationLeaseIDs},
		{"a page identifier is recorded once", baseTgtLeased, func(r *Record) { r.Lease.IDs = []LeaseID{someID, someID} }, ViolationLeaseIDs},
		// lease: count, state, times
		{"a message has at most MaxLeases leases", baseTgtLeased, func(r *Record) { r.Lease.Handouts = MaxLeases + 1 }, ViolationLeaseCount},
		{"the last lease is not over while the message is queued", baseTgtLeased, func(r *Record) { r.Lease.Handouts, r.Lease.Active = MaxLeases, false }, ViolationLeaseCount},
		{"a lease is outstanding only while accepted", baseTgtUnknown, func(r *Record) { r.Lease.Active = true }, ViolationLeaseActive},
		{"a lease has an issue time", baseTgtLeased, func(r *Record) { r.Lease.IssuedAt = time.Time{} }, ViolationLeaseTimes},
		{"a lease has an expiry time", baseTgtLeased, func(r *Record) { r.Lease.ExpiresAt = time.Time{} }, ViolationLeaseTimes},
		{"a lease lives at most LeaseRenewalCap", baseTgtLeased, func(r *Record) { r.Lease.ExpiresAt = r.Lease.IssuedAt.Add(LeaseRenewalCap + time.Nanosecond) }, ViolationLeaseTimes},
		{"a lease is issued before the backlog deadline", baseTgtLeased, func(r *Record) {
			r.Lease.IssuedAt = r.BacklogDeadline
			r.Lease.ExpiresAt = r.BacklogDeadline.Add(LeaseDuration)
		}, ViolationLeaseAfterBacklog},
		// outcome against lease
		{"a delivered message has every chunk acknowledged", baseTgtDelivered, func(r *Record) { r.Lease.Acked[0] = false }, ViolationDelivered},
		{"a delivered message was handed out", baseTgt, func(r *Record) { r.State = StateDelivered }, ViolationDelivered},
		{"a message with every chunk acknowledged is delivered", baseTgtLeased, func(r *Record) {
			r.Lease.HandedOut[1] = true
			r.Lease.Acked[0], r.Lease.Acked[1] = true, true
		}, ViolationDelivered},
		{"a target delivery_unknown was handed over", baseTgt, func(r *Record) { r.State, r.Unknown = StateDeliveryUnknown, UnknownHandedOver }, ViolationDeliveryUnknownUnhandled},
		{"a handed-over delivery is not rejected", baseTgtLeased, func(r *Record) {
			r.State, r.Reject, r.Lease.Active = StateRejected, RejectEndpointClosed, false
		}, ViolationRejectedHandedOver},
	}

	covered := map[Violation]int{}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			base := row.base.build(t)
			if err := base.Valid(); err != nil {
				t.Fatalf("the base %q is not valid: %v (%+v)", row.base.name, err, base)
			}
			r := cloneRecord(base)
			row.mutate(&r)
			if reflect.DeepEqual(r, base) {
				t.Fatal("the row does not change the record")
			}
			if got := violationOf(t, r.Valid()); got != row.want {
				t.Errorf("violation %s, want %s (record %+v)", got, row.want, r)
			}
		})
		covered[row.want]++
	}
	// Every invariant has a row.
	for v := ViolationNone + 1; v.String() != "invalid"; v++ {
		if covered[v] == 0 {
			t.Errorf("invariant %s has no row", v)
		}
	}
}

func TestRecordValidAcceptsFreshRecords(t *testing.T) {
	for _, b := range []validBase{baseQueued, baseTgt, baseControlRecord} {
		if err := b.build(t).Valid(); err != nil {
			t.Errorf("%s: %v", b.name, err)
		}
	}
	if got := violationOf(t, Record{}.Valid()); got != ViolationSide {
		t.Errorf("the zero Record: violation %s, want %s", got, ViolationSide)
	}
}

// TestRecordValidAcceptsALeaseRenewedAfterTheClockSteppedBack: the wall clock
// may step back by up to timing.ClockTolerance in normal operation, so a lease
// renewed at an instant before its issue time expires earlier than LeaseDuration
// after the issue time, and the record is still one the transitions produced.
func TestRecordValidAcceptsALeaseRenewedAfterTheClockSteppedBack(t *testing.T) {
	issuedAt := at(time.Minute)
	r := handOut(t, targetRecord(t, t0, 30*time.Minute), issuedAt, 2, 1)
	renewedAt := issuedAt.Add(-timing.ClockTolerance / 2)
	r, res := r.Renew(renewedAt, true)
	if res != Renewed {
		t.Fatalf("Renew at %v before the issue time: %v, want Renewed", issuedAt.Sub(renewedAt), res)
	}
	if !r.Lease.ExpiresAt.Before(r.Lease.IssuedAt.Add(LeaseDuration)) {
		t.Fatalf("the lease expires at %v after its issue, want less than LeaseDuration: the scenario no longer steps the clock back", r.Lease.ExpiresAt.Sub(r.Lease.IssuedAt))
	}
	if err := r.Valid(); err != nil {
		t.Errorf("a lease renewed after a clock step back is refused: %v", err)
	}
}

// TestRecordValidReportsTheFirstViolation: a record that fails two invariants
// reports the one checked first, so the answer does not depend on map order or
// on the number of faults.
func TestRecordValidReportsTheFirstViolation(t *testing.T) {
	r := baseQueued.build(t)
	r.Side = 0
	r.State = 99
	r.Reject = 200
	if got := violationOf(t, r.Valid()); got != ViolationSide {
		t.Errorf("violation %s, want %s", got, ViolationSide)
	}
}

// reachedSource is the part of a source record that the feedback and the block
// flags can tell apart.
type reachedSource struct {
	State        State
	Reject       RejectReason
	Unknown      UnknownCause
	Reported     KindSet
	Blocked      bool
	BlockedCause Cause
}

func keyOfSource(r Record) reachedSource {
	return reachedSource{r.State, r.Reject, r.Unknown, r.Reported, r.Blocked, r.BlockedCause}
}

// TestRecordValidSourceFeedbackBookkeepingIsExact enumerates every source
// record the transitions reach by applying every event in every order, and
// every source record that differs only in its state, produced feedback and
// block fields, and requires Valid to accept exactly the reached ones. The
// expected set comes from running the transitions here, not from the rules in
// Valid.
func TestRecordValidSourceFeedbackBookkeepingIsExact(t *testing.T) {
	start := NewSourceRecord(validAcceptBefore)
	var events []Event
	for _, ty := range []EventType{EventReceipt, EventDelivered, EventReportByPassed, EventUnblocked} {
		events = append(events, Event{Type: ty})
	}
	for _, cause := range []UnknownCause{UnknownHandedOver, UnknownLostAtTarget} {
		events = append(events, Event{Type: EventDeliveryUnknown, Unknown: cause})
	}
	events = append(events,
		Event{Type: EventRejected, Reject: RejectTooLarge},
		Event{Type: EventExpiryQueried, QueriedAt: farFuture},
	)
	for _, c := range []Cause{CauseNetwork, CauseTrustSuspended} {
		events = append(events, Event{Type: EventContactFailing, Cause: c, Since: t0})
	}
	for _, c := range []Cause{CauseAdapterOffline, CauseRuntimeAuth, CauseNativeUnavailable} {
		events = append(events, Event{Type: EventBlocked, Cause: c})
	}
	// Before the delivery deadline the unreachable hint can be produced; long
	// after it, only the rest.
	clocks := []time.Time{at(5 * time.Minute), farFuture}

	reached := map[reachedSource]bool{keyOfSource(start): true}
	frontier := []Record{start}
	for len(frontier) > 0 {
		r := frontier[0]
		frontier = frontier[1:]
		for _, now := range clocks {
			for _, ev := range events {
				n, _, err := r.Apply(now, ev)
				if err != nil {
					continue
				}
				if k := keyOfSource(n); !reached[k] {
					reached[k] = true
					frontier = append(frontier, n)
				}
			}
		}
	}
	if len(reached) < 150 {
		t.Fatalf("only %d source records reached: the enumeration no longer explores the transitions", len(reached))
	}
	for k := range reached {
		r := start
		r.State, r.Reject, r.Unknown, r.Reported, r.Blocked, r.BlockedCause = k.State, k.Reject, k.Unknown, k.Reported, k.Blocked, k.BlockedCause
		if err := r.Valid(); err != nil {
			t.Errorf("reached record refused: %+v: %v", k, err)
		}
	}

	candidates, accepted := 0, 0
	states := []State{StateQueued, StateAccepted, StateDelivered, StateRejected, StateExpiredUnconfirmed, StateDeliveryUnknown, StateClosedUnconfirmed}
	causes := []Cause{CauseNone, CauseNetwork, CauseTrustSuspended, CauseAdapterOffline, CauseRuntimeAuth, CauseNativeUnavailable}
	// lost_at_sender is the cause of a replacement alone, which the Lost marker
	// recognises; this walk starts from an ordinary source record.
	unknowns := []UnknownCause{UnknownCauseNone, UnknownHandedOver, UnknownLostAtTarget}
	for _, s := range states {
		for mask := 0; mask < 1<<lastKind; mask++ {
			for _, blocked := range []bool{false, true} {
				for _, cause := range causes {
					for _, unknown := range unknowns {
						k := reachedSource{State: s, Unknown: unknown, Reported: KindSet(mask) << 1, Blocked: blocked, BlockedCause: cause}
						if s == StateRejected {
							k.Reject = RejectTooLarge
						}
						r := start
						r.State, r.Reject, r.Unknown, r.Reported, r.Blocked, r.BlockedCause = k.State, k.Reject, k.Unknown, k.Reported, k.Blocked, k.BlockedCause
						err := r.Valid()
						candidates++
						if err == nil {
							accepted++
						}
						if (err == nil) != reached[k] {
							t.Errorf("%+v: Valid = %v, reachable = %v", k, err, reached[k])
						}
					}
				}
			}
		}
	}
	t.Logf("%d source records reached, %d candidates, %d accepted", len(reached), candidates, accepted)
}

// recordObserver checks with Valid every record that the random walks produce
// and counts which kinds of record that covered, so a generator that stops
// reaching one of them fails the test instead of passing on fewer records.
type recordObserver struct {
	seen    map[string]int
	samples []Record
	rng     *rand.Rand // picks the samples kept for the mutation test; nil keeps none
}

func newRecordObserver(sampleSeed int64, keep bool) *recordObserver {
	o := &recordObserver{seen: map[string]int{}}
	if keep {
		o.rng = rand.New(rand.NewSource(sampleSeed))
	}
	return o
}

func (o *recordObserver) observe(t *testing.T, r Record) {
	t.Helper()
	if err := r.Valid(); err != nil {
		t.Fatalf("a record produced by the constructors and transitions is refused: %v\n%+v", err, r)
	}
	for _, tag := range recordTags(r) {
		o.seen[tag]++
	}
	if o.rng != nil && o.rng.Intn(8) == 0 {
		o.samples = append(o.samples, cloneRecord(r))
	}
}

// recordTags names the kinds of record, by the fields Valid reads, that an
// observed record belongs to.
func recordTags(r Record) []string {
	side := "source"
	switch {
	case r.Side == SideTarget && r.Control:
		side = "control"
	case r.Side == SideTarget:
		side = "target"
	}
	tags := []string{side + " " + r.State.String()}
	add := func(cond bool, s string) {
		if cond {
			tags = append(tags, side+" "+s)
		}
	}
	add(r.Blocked, "blocked")
	add(r.Sub == SubNotified, "notified")
	add(r.Lease.Active, "lease active")
	add(r.Lease.Handouts > 0 && !r.Lease.Active && r.State == StateAccepted, "requeued")
	add(r.Lease.Handouts >= 2, "redelivered")
	add(r.Lease.Handouts == MaxLeases, "last lease")
	add(r.Lease.Active && r.Lease.ExpiresAt.After(r.Lease.IssuedAt.Add(LeaseDuration)), "lease renewed")
	add(r.Lease.Active && r.Lease.ExpiresAt.Equal(r.Lease.renewalLimit()), "lease at cap")
	add(r.Lease.Chunks > 1, "several chunks")
	add(r.Lease.Chunks > 0 && !r.Lease.Active && r.Lease.Handouts > 0 && r.State == StateDeliveryUnknown, "unknown after hand-out")
	add(len(r.Lease.IDs) > 1, "several pages")
	add(r.Reject != RejectNone, "reject "+r.Reject.String())
	add(r.Lost, "lost "+r.State.String())
	for k := KindUnreachable; k.valid(); k++ {
		add(r.Reported.Has(k), "reported "+k.String())
	}
	return tags
}

// requiredRecordTags are the kinds of record the random walks must produce, each
// at least minPathCount times.
var requiredRecordTags = []string{
	"source queued", "source accepted", "source delivered", "source rejected", "source expired_unconfirmed",
	"source delivery_unknown", "source closed_unconfirmed", "source blocked", "source reported unreachable",
	"source reported delivery_blocked", "source reported blocked_recovered", "source reported expired_corrected",
	"source reported unknown_corrected", "source reported rejected",
	"target accepted", "target delivered", "target rejected", "target delivery_unknown", "target closed_unconfirmed",
	"target blocked", "target notified", "target lease active", "target requeued", "target redelivered",
	"target last lease", "target lease renewed", "target lease at cap", "target several chunks", "target unknown after hand-out", "target several pages",
	"control accepted", "control delivered", "control delivery_unknown", "control closed_unconfirmed",
	"source lost delivery_unknown", "source lost closed_unconfirmed", "source reported lost_at_sender", "source reported lost_at_target",
	"target lost delivery_unknown", "target lost closed_unconfirmed",
	"control lost delivery_unknown", "control lost delivered", "control lost closed_unconfirmed",
}

func (o *recordObserver) requireCoverage(t *testing.T) {
	t.Helper()
	for _, tag := range requiredRecordTags {
		if o.seen[tag] < minPathCount {
			t.Errorf("records of kind %q seen %d times, want at least %d: the generators no longer produce them", tag, o.seen[tag], minPathCount)
		}
	}
	names := make([]string, 0, len(o.seen))
	for n := range o.seen {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Logf("%-45s %d", n, o.seen[n])
	}
}

// runSourceLearnsLoss drives source records that hear from the target that it
// lost its record: the loss is reported at a random stage and then corrected by
// a delivery, or left until the record closes. observe sees every record.
func runSourceLearnsLoss(t *testing.T, runs int, observe func(*testing.T, Record)) {
	t.Helper()
	rng := rand.New(rand.NewSource(5))
	lost := StatusReport{State: StateDeliveryUnknown, Unknown: UnknownLostAtTarget}
	for i := 0; i < runs; i++ {
		r := NewSourceRecord(validAcceptBefore)
		observe(t, r)
		switch rng.Intn(3) {
		case 1:
			r, _ = mustApply(t, r, at(time.Minute), Event{Type: EventReceipt})
		case 2:
			r, _ = mustApply(t, r, farFuture, Event{Type: EventExpiryQueried, QueriedAt: farFuture})
		}
		observe(t, r)
		r, _, err := r.ApplyStatusReport(at(2*time.Minute), lost)
		if err != nil {
			t.Fatal(err)
		}
		observe(t, r)
		switch rng.Intn(3) {
		case 0:
			r, _ = mustApply(t, r, at(3*time.Minute), Event{Type: EventDelivered})
		case 1:
			r, _ = mustApply(t, r, r.ReportBy.Add(time.Nanosecond), Event{Type: EventReportByPassed})
		}
		observe(t, r)
	}
}

// TestPropertyEveryProducedRecordIsValid runs the random deliveries and random
// control signals of the other property tests, with the same fixed seeds, the
// records that replace a loss from their creation to their closure, and the
// source records that learn of a loss, and requires every record they produce,
// on both sides, after every step, to be Valid.
func TestPropertyEveryProducedRecordIsValid(t *testing.T) {
	obs := newRecordObserver(0, false)
	paths := map[string]int{}
	for seed := int64(1); seed <= propertySeeds; seed++ {
		t.Run(fmt.Sprintf("delivery seed %d", seed), func(t *testing.T) { runRandomDelivery(t, seed, paths, obs.observe) })
		t.Run(fmt.Sprintf("control seed %d", seed), func(t *testing.T) { runRandomControl(t, seed, paths, obs.observe) })
	}
	t.Run("lost records", func(t *testing.T) { runLostLifecycles(t, lostLifecycleRuns, obs.observe) })
	t.Run("source learns a loss", func(t *testing.T) { runSourceLearnsLoss(t, 300, obs.observe) })
	if t.Failed() {
		return
	}
	obs.requireCoverage(t)
}

// recordMutator changes one field, or a few that belong together, of a copy of
// a valid record. exact says that every change it makes breaks an invariant,
// so every effective application must be refused; for the others a change can
// land on another valid record, and the test only requires a measured rate.
type recordMutator struct {
	name    string
	exact   bool
	mutate  func(rng *rand.Rand, r *Record)
	minRate float64 // of effective applications that Valid refuses, when not exact
}

func otherInt(rng *rand.Rand, cur, lo, hi int) int {
	for {
		if v := lo + rng.Intn(hi-lo+1); v != cur {
			return v
		}
	}
}

func randomShift(rng *rand.Rand) time.Duration {
	d := time.Duration(1+rng.Intn(20*60)) * time.Second
	if rng.Intn(2) == 0 {
		d = -d
	}
	return d
}

func recordMutators() []recordMutator {
	return []recordMutator{
		{"side to an unknown value", true, func(rng *rand.Rand, r *Record) { r.Side = []Side{0, 3, 255}[rng.Intn(3)] }, 0},
		{"side swapped", true, func(rng *rand.Rand, r *Record) { r.Side = SideSource + SideTarget - r.Side }, 0},
		{"control flipped", true, func(rng *rand.Rand, r *Record) {
			// What the lost file's first line says is all a replacement keeps, so a
			// lost message record and a lost control record look alike until the
			// state tells them apart: only a delivered one is a control record.
			if r.Lost && r.Side == SideTarget && r.State != StateDelivered {
				return
			}
			r.Control = !r.Control
		}, 0},
		{"lost marker flipped", true, func(rng *rand.Rand, r *Record) { r.Lost = !r.Lost }, 0},
		{"unknown cause to another", true, func(rng *rand.Rand, r *Record) { r.Unknown = UnknownCause(otherInt(rng, int(r.Unknown), 0, 3)) }, 0},
		{"state to another", false, func(rng *rand.Rand, r *Record) { r.State = State(otherInt(rng, int(r.State), 0, 8)) }, 0.6},
		{"state to another with the matching reason", false, func(rng *rand.Rand, r *Record) {
			r.State = State(otherInt(rng, int(r.State), 1, 7))
			r.Reject = RejectNone
			if r.State == StateRejected {
				r.Reject = RejectTooLarge
			}
		}, 0.5},
		{"state to delivery_unknown with its cause", false, func(rng *rand.Rand, r *Record) {
			r.State, r.Unknown, r.Reject = StateDeliveryUnknown, UnknownHandedOver, RejectNone
		}, 0.7},
		{"reject reason to an unknown value", true, func(rng *rand.Rand, r *Record) { r.Reject = RejectReason(100 + rng.Intn(100)) }, 0},
		{"reject reason to another known one", false, func(rng *rand.Rand, r *Record) { r.Reject = RejectReason(otherInt(rng, int(r.Reject), 0, 11)) }, 0.85},
		{"sub-state flipped", false, func(rng *rand.Rand, r *Record) { r.Sub = SubNotified - r.Sub }, 0.9},
		{"accept_before shifted", true, func(rng *rand.Rand, r *Record) { r.AcceptBefore = r.AcceptBefore.Add(randomShift(rng)) }, 0},
		{"report_by shifted", true, func(rng *rand.Rand, r *Record) {
			// A replacement's ReportBy is any instant: LostReportBy of a discovery
			// time the record does not keep.
			if r.Lost {
				return
			}
			r.ReportBy = r.ReportBy.Add(randomShift(rng))
		}, 0},
		{"a deadline unset", true, func(rng *rand.Rand, r *Record) {
			if rng.Intn(2) == 0 {
				r.AcceptBefore = time.Time{}
			} else {
				r.ReportBy = time.Time{}
			}
		}, 0},
		{"backlog deadline unset", true, func(rng *rand.Rand, r *Record) { r.BacklogDeadline = time.Time{} }, 0},
		{"backlog deadline shifted", false, func(rng *rand.Rand, r *Record) { r.BacklogDeadline = r.BacklogDeadline.Add(randomShift(rng)) }, 0.8},
		{"backlog deadline set past the settle deadline", false, func(rng *rand.Rand, r *Record) {
			r.BacklogDeadline = SettleBy(r.ReportBy).Add(time.Duration(1+rng.Intn(3600)) * time.Second)
		}, 0.5},
		{"produced feedback: one kind flipped", false, func(rng *rand.Rand, r *Record) { r.Reported ^= 1 << (1 + rng.Intn(int(lastKind))) }, 0.8},
		{"produced feedback: a bit outside the kinds", true, func(rng *rand.Rand, r *Record) {
			r.Reported |= 1 << (int(lastKind) + 1 + rng.Intn(kindSetBits-int(lastKind)-1))
		}, 0},
		{"blocked flipped", false, func(rng *rand.Rand, r *Record) { r.Blocked = !r.Blocked }, 0.9},
		{"block cause to another", false, func(rng *rand.Rand, r *Record) { r.BlockedCause = Cause(otherInt(rng, int(r.BlockedCause), 0, 6)) }, 0.85},
		{"lease: hand-out count to another", false, func(rng *rand.Rand, r *Record) { r.Lease.Handouts = otherInt(rng, r.Lease.Handouts, 0, MaxLeases+2) }, 0.9},
		{"lease: chunk count to another", true, func(rng *rand.Rand, r *Record) { r.Lease.Chunks = otherInt(rng, r.Lease.Chunks, -1, 4) }, 0},
		{"lease: active flipped", false, func(rng *rand.Rand, r *Record) { r.Lease.Active = !r.Lease.Active }, 0.9},
		{"lease: page identifier dropped", false, func(rng *rand.Rand, r *Record) {
			if n := len(r.Lease.IDs); n > 0 {
				r.Lease.IDs = r.Lease.IDs[:n-1]
			}
		}, 0.4},
		{"lease: page identifier unset", true, func(rng *rand.Rand, r *Record) {
			if n := len(r.Lease.IDs); n > 0 {
				r.Lease.IDs[rng.Intn(n)] = LeaseID{}
			}
		}, 0},
		{"lease: page identifier repeated", true, func(rng *rand.Rand, r *Record) {
			if n := len(r.Lease.IDs); n > 0 {
				r.Lease.IDs = append(r.Lease.IDs, r.Lease.IDs[rng.Intn(n)])
			}
		}, 0},
		{"lease: issue time shifted", false, func(rng *rand.Rand, r *Record) {
			if !r.Lease.IssuedAt.IsZero() {
				r.Lease.IssuedAt = r.Lease.IssuedAt.Add(randomShift(rng))
			}
		}, 0.33},
		{"lease: expiry time shifted", false, func(rng *rand.Rand, r *Record) {
			if !r.Lease.ExpiresAt.IsZero() {
				r.Lease.ExpiresAt = r.Lease.ExpiresAt.Add(randomShift(rng))
			}
		}, 0.24},
		{"lease: a lease time unset", true, func(rng *rand.Rand, r *Record) {
			if rng.Intn(2) == 0 {
				r.Lease.IssuedAt = time.Time{}
			} else {
				r.Lease.ExpiresAt = time.Time{}
			}
		}, 0},
		{"lease: hand-out flag flipped", false, func(rng *rand.Rand, r *Record) {
			if n := len(r.Lease.HandedOut); n > 0 {
				i := rng.Intn(n)
				r.Lease.HandedOut[i] = !r.Lease.HandedOut[i]
			}
		}, 0.5},
		{"lease: acknowledgement flag flipped", false, func(rng *rand.Rand, r *Record) {
			if n := len(r.Lease.Acked); n > 0 {
				i := rng.Intn(n)
				r.Lease.Acked[i] = !r.Lease.Acked[i]
			}
		}, 0.55},
		{"lease: flag slice cut short", true, func(rng *rand.Rand, r *Record) {
			if n := len(r.Lease.Acked); n > 0 {
				r.Lease.Acked = r.Lease.Acked[:n-1]
			}
		}, 0},
	}
}

// TestPropertyMutatedRecordsAreCaught applies one random mutation to records
// sampled from the random walks, with a fixed seed, and measures how many of
// the effective mutations Valid refuses. A mutator that cannot land on a valid
// record must be refused every time; the others have a measured floor. Every
// invariant must be the one that caught at least minCaughtPerInvariant
// mutations, so a rule that stopped firing, or that a mutation can no longer
// reach, fails the test.
func TestPropertyMutatedRecordsAreCaught(t *testing.T) {
	const minCaughtPerInvariant = 20
	obs := newRecordObserver(7, true)
	paths := map[string]int{}
	for seed := int64(1); seed <= 1500; seed++ {
		t.Run(fmt.Sprintf("delivery seed %d", seed), func(t *testing.T) { runRandomDelivery(t, seed, paths, obs.observe) })
		t.Run(fmt.Sprintf("control seed %d", seed), func(t *testing.T) { runRandomControl(t, seed, paths, obs.observe) })
	}
	t.Run("lost records", func(t *testing.T) { runLostLifecycles(t, lostLifecycleRuns/3, obs.observe) })
	t.Run("source learns a loss", func(t *testing.T) { runSourceLearnsLoss(t, 300, obs.observe) })
	if t.Failed() {
		return
	}
	if len(obs.samples) < 5000 {
		t.Fatalf("only %d sampled records", len(obs.samples))
	}

	rng := rand.New(rand.NewSource(11))
	type tally struct{ effective, caught int }
	perMutator := map[string]*tally{}
	caughtBy := map[Violation]int{}
	mutators := recordMutators()
	total := tally{}
	for _, base := range obs.samples {
		for _, m := range mutators {
			r := cloneRecord(base)
			m.mutate(rng, &r)
			if reflect.DeepEqual(r, base) {
				continue
			}
			tl := perMutator[m.name]
			if tl == nil {
				tl = &tally{}
				perMutator[m.name] = tl
			}
			tl.effective++
			total.effective++
			v := violationOf(t, r.Valid())
			if v != ViolationNone {
				tl.caught++
				total.caught++
				caughtBy[v]++
			} else if m.exact {
				t.Errorf("mutator %q left a record valid: %+v (from %+v)", m.name, r, base)
			}
		}
	}
	for _, m := range mutators {
		tl := perMutator[m.name]
		if tl == nil || tl.effective < 50 {
			t.Errorf("mutator %q had %v effective applications, want at least 50", m.name, tl)
			continue
		}
		rate := float64(tl.caught) / float64(tl.effective)
		t.Logf("%-52s %6d effective, %6.1f%% caught", m.name, tl.effective, 100*rate)
		if !m.exact && rate < m.minRate {
			t.Errorf("mutator %q: %.1f%% caught, want at least %.1f%%", m.name, 100*rate, 100*m.minRate)
		}
	}
	t.Logf("%d of %d effective mutations caught (%.1f%%)", total.caught, total.effective, 100*float64(total.caught)/float64(total.effective))
	for v := ViolationNone + 1; v.String() != "invalid"; v++ {
		t.Logf("%-40s %d", v, caughtBy[v])
		if caughtBy[v] < minCaughtPerInvariant {
			t.Errorf("invariant %s caught %d mutations, want at least %d", v, caughtBy[v], minCaughtPerInvariant)
		}
	}
}
