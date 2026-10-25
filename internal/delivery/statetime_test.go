package delivery

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// The time a record entered its current state is what a target puts in a signed
// status report as the moment it recorded the state, and the time of delivery is
// the same moment for a delivered record. These tests state, per transition, the
// instant the record must show afterwards, taken from the clock the test passes
// and never from the record.

func TestConstructorsStartTheStateAtTheGivenTime(t *testing.T) {
	ab := at(10 * time.Minute)
	acceptedAt := at(3 * time.Minute)
	queued := NewSourceRecord(acceptedAt, ab)
	target, err := NewTargetRecord(acceptedAt, ab, ReportBy(ab), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	notice, err := NewTargetNoticeRecord(acceptedAt, ab, ReportBy(ab), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	control := NewTargetControlRecord(acceptedAt, ab, ReportBy(ab))
	lostSource, _, err := NewLostRecord(SideSource, false, acceptedAt)
	if err != nil {
		t.Fatal(err)
	}
	lostTarget, _, err := NewLostRecord(SideTarget, true, acceptedAt)
	if err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		rule string
		rec  Record
	}{
		{"a source record starts queued at the time it was taken from the caller", queued},
		{"a target message record starts accepted at the time it was accepted", target},
		{"a target notice record starts accepted at the time it was accepted", notice},
		{"a target control record starts accepted at the time it was accepted", control},
		{"a lost source record starts at the time the loss was discovered", lostSource},
		{"a lost target record starts at the time the loss was discovered", lostTarget},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if !r.rec.StateAt.Equal(acceptedAt) {
				t.Errorf("StateAt = %v, want %v", r.rec.StateAt, acceptedAt)
			}
			if err := r.rec.Valid(); err != nil {
				t.Errorf("Valid: %v", err)
			}
			if d := r.rec.DeliveredAt(); !d.IsZero() {
				t.Errorf("DeliveredAt = %v for a record in %s, want zero", d, r.rec.State)
			}
		})
	}
}

// transitionRow builds a record entered at start, makes one transition at the
// instant `later` and says which state the record must be in afterwards.
type transitionRow struct {
	rule  string
	build func(t *testing.T) Record
	// do makes the transition at now.
	do        func(t *testing.T, r Record, now time.Time) Record
	wantState State
}

func TestEveryTransitionIntoAStateSetsTheStateAtToItsClock(t *testing.T) {
	const start = time.Minute // every record is built at t0+start
	later := at(37 * time.Minute)
	apply := func(ev Event) func(*testing.T, Record, time.Time) Record {
		return func(t *testing.T, r Record, now time.Time) Record {
			n, _ := mustApply(t, r, now, ev)
			return n
		}
	}
	tick := func(t *testing.T, r Record, now time.Time) Record {
		n, _, err := r.Tick(now)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	source := func(t *testing.T) Record { return NewSourceRecord(at(start), at(10*time.Minute)) }
	sourceAccepted := func(t *testing.T) Record {
		n, _ := mustApply(t, source(t), at(start), Event{Type: EventReceipt})
		return n
	}
	target := func(t *testing.T) Record { return targetRecord(t, at(start), time.Hour) }
	handedOut := func(t *testing.T) Record { return handOut(t, target(t), at(start), 1, 1) }
	control := func(t *testing.T) Record {
		return NewTargetControlRecord(at(start), at(10*time.Minute), ReportBy(at(10*time.Minute)))
	}
	unknownTarget := func(t *testing.T) Record {
		n, _, err := handedOut(t).Tick(SettleBy(ReportBy(at(10 * time.Minute))))
		if err != nil || n.State != StateDeliveryUnknown {
			t.Fatalf("setup: state %s, %v", n.State, err)
		}
		return n
	}
	afterReportBy := ReportBy(at(10 * time.Minute)).Add(time.Hour)
	rows := []transitionRow{
		{"a source record that learns of a receipt", source, apply(Event{Type: EventReceipt}), StateAccepted},
		{"a source record that learns of a delivery", source, apply(Event{Type: EventDelivered}), StateDelivered},
		{"a source record that learns of a rejection", source, apply(Event{Type: EventRejected, Reject: RejectUnauthorized}), StateRejected},
		{"a source record that learns the result is unknown", sourceAccepted, apply(Event{Type: EventDeliveryUnknown, Unknown: UnknownHandedOver}), StateDeliveryUnknown},
		{"a source record that learns the target lost its record", sourceAccepted, apply(Event{Type: EventDeliveryUnknown, Unknown: UnknownLostAtTarget}), StateDeliveryUnknown},
		{"a source record judged expired by a query", source, func(t *testing.T, r Record, now time.Time) Record {
			n, _ := mustApply(t, r, now, Event{Type: EventExpiryQueried, QueriedAt: now})
			return n
		}, StateExpiredUnconfirmed},
		{"a source record closed by report_by", source, apply(Event{Type: EventReportByPassed}), StateClosedUnconfirmed},
		{"a source record closed by Tick", sourceAccepted, tick, StateClosedUnconfirmed},
		{"a source record that applies a delivered status report", sourceAccepted, func(t *testing.T, r Record, now time.Time) Record {
			n, _, err := r.ApplyStatusReport(now, StatusReport{State: StateDelivered})
			if err != nil {
				t.Fatal(err)
			}
			return n
		}, StateDelivered},
		{"a target record rejected by an event", target, apply(Event{Type: EventRejected, Reject: RejectEndpointClosed}), StateRejected},
		{"a target record closed forcibly after a hand-out", handedOut, apply(Event{Type: EventRejected, Reject: RejectEndpointClosed}), StateDeliveryUnknown},
		{"a target record whose backlog deadline passed unhanded", target, func(t *testing.T, r Record, now time.Time) Record {
			n, _, err := r.Tick(r.BacklogDeadline.Add(time.Minute))
			if err != nil || n.State != StateRejected {
				t.Fatalf("setup: state %s, %v", n.State, err)
			}
			return n
		}, StateRejected},
		{"a target record whose hand-out is denied by the second check", target, func(t *testing.T, r Record, now time.Time) Record {
			n, res, err := r.Handout(now, AuthorizationDeny, 1, 1, freshLease())
			if err != nil || res.Action != HandoutDenied {
				t.Fatalf("Handout: %+v %v", res, err)
			}
			return n
		}, StateRejected},
		{"a target record whose hand-out is denied after an earlier hand-out", func(t *testing.T) Record {
			return handOut(t, target(t), at(36*time.Minute), 1, 1)
		}, func(t *testing.T, r Record, now time.Time) Record {
			n, res, err := r.Handout(now, AuthorizationDeny, 1, 1, freshLease())
			if err != nil || res.Action != HandoutDenied {
				t.Fatalf("Handout: %+v %v", res, err)
			}
			return n
		}, StateDeliveryUnknown},
		{"a target record whose settle deadline passed under a lease", handedOut, func(t *testing.T, r Record, now time.Time) Record {
			n, _, err := r.Tick(SettleBy(r.ReportBy))
			if err != nil {
				t.Fatal(err)
			}
			return n
		}, StateDeliveryUnknown},
		{"a target record acknowledged", handedOut, func(t *testing.T, r Record, now time.Time) Record {
			n, v, _ := ack(t, r, now, 1)
			if v != AckCompleted {
				t.Fatalf("verdict %v", v)
			}
			return n
		}, StateDelivered},
		{"a target record acknowledged late, after delivery_unknown", unknownTarget, func(t *testing.T, r Record, now time.Time) Record {
			n, v, _ := ack(t, r, now, 1)
			if v != AckCompleted {
				t.Fatalf("verdict %v", v)
			}
			return n
		}, StateDelivered},
		{"a target record in delivery_unknown closed by report_by", unknownTarget, tick, StateClosedUnconfirmed},
		{"a control record whose reply was committed", control, apply(Event{Type: EventReplyCommitted}), StateDelivered},
		{"a control record settled at the settle deadline", control, func(t *testing.T, r Record, now time.Time) Record {
			n, _, err := r.Tick(SettleBy(r.ReportBy))
			if err != nil || n.State != StateDeliveryUnknown {
				t.Fatalf("setup: state %s, %v", n.State, err)
			}
			return n
		}, StateDeliveryUnknown},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			rec := r.build(t)
			// The clock of a transition that waits for a deadline is the one the
			// row's own Tick call names; the others are made at later. After
			// report_by every deadline has passed.
			now := later
			if r.wantState == StateClosedUnconfirmed {
				now = afterReportBy
			}
			got := r.do(t, rec, now)
			if got.State != r.wantState {
				t.Fatalf("state = %s, want %s", got.State, r.wantState)
			}
			// A row whose transition runs at a deadline of its own states which.
			want := now
			switch r.rule {
			case "a target record whose backlog deadline passed unhanded":
				want = rec.BacklogDeadline.Add(time.Minute)
			case "a target record whose settle deadline passed under a lease",
				"a control record settled at the settle deadline":
				want = SettleBy(rec.ReportBy)
			}
			if !got.StateAt.Equal(want) {
				t.Errorf("StateAt = %v after the transition, want %v", got.StateAt.Sub(t0), want.Sub(t0))
			}
			if err := got.Valid(); err != nil {
				t.Errorf("Valid: %v", err)
			}
			wantDelivered := time.Time{}
			if got.State == StateDelivered {
				wantDelivered = want
			}
			if !got.DeliveredAt().Equal(wantDelivered) {
				t.Errorf("DeliveredAt = %v, want %v", got.DeliveredAt(), wantDelivered)
			}
		})
	}
}

// TestTickStampsEveryStepOfOneCallWithItsClock: a target that slept past
// report_by settles the delivery when it wakes, in several steps of one Tick, and
// records every one of them at the moment it woke, not at the deadline each step
// was due.
func TestTickStampsEveryStepOfOneCallWithItsClock(t *testing.T) {
	r := handOut(t, targetRecord(t, at(time.Minute), time.Hour), at(time.Minute), 1, 1)
	wake := r.ReportBy.Add(24 * time.Hour)
	n, outs, err := r.Tick(wake)
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 2 || n.State != StateClosedUnconfirmed {
		t.Fatalf("outcomes %+v, state %s: want delivery_unknown then closed_unconfirmed", outs, n.State)
	}
	if !n.StateAt.Equal(wake) {
		t.Errorf("StateAt = %v, want the moment of waking %v", n.StateAt.Sub(t0), wake.Sub(t0))
	}
}

// TestFactsThatLeaveTheStateAloneLeaveTheStateAtAlone: the time is the time the
// state was entered, so a report sent again for an unchanged state repeats it.
func TestFactsThatLeaveTheStateAloneLeaveTheStateAtAlone(t *testing.T) {
	enteredAt := at(time.Minute)
	later := at(20 * time.Minute)
	target := targetRecord(t, enteredAt, time.Hour)
	delivered := deliverBody(t, targetRecord(t, enteredAt, time.Hour), enteredAt)
	sourceAccepted, _ := mustApply(t, NewSourceRecord(enteredAt, at(10*time.Minute)), enteredAt, Event{Type: EventReceipt})
	sourceDelivered, _ := mustApply(t, NewSourceRecord(enteredAt, at(10*time.Minute)), enteredAt, Event{Type: EventDelivered})
	rows := []struct {
		rule string
		rec  Record
		do   func(t *testing.T, r Record) Record
	}{
		{"a prompt handed to the session", target, func(t *testing.T, r Record) Record {
			n, _ := mustApply(t, r, later, Event{Type: EventNotified})
			return n
		}},
		{"a block", target, func(t *testing.T, r Record) Record {
			n, _ := mustApply(t, r, later, Event{Type: EventBlocked, Cause: CauseAdapterOffline})
			return n
		}},
		{"a block that is lifted", target, func(t *testing.T, r Record) Record {
			n, _ := mustApply(t, r, later, Event{Type: EventBlocked, Cause: CauseAdapterOffline})
			n, _ = mustApply(t, n, later.Add(time.Minute), Event{Type: EventUnblocked})
			return n
		}},
		{"a hand-out", target, func(t *testing.T, r Record) Record { return handOut(t, r, later, 2, 1) }},
		{"a renewed lease", target, func(t *testing.T, r Record) Record {
			n := handOut(t, r, later, 1, 1)
			n, res := n.Renew(later.Add(time.Minute), true)
			if res != Renewed {
				t.Fatalf("Renew: %v", res)
			}
			return n
		}},
		{"a lease that ran out and went back to the queue", target, func(t *testing.T, r Record) Record {
			n := handOut(t, r, later, 1, 1)
			n, outs, err := n.Tick(n.Lease.ExpiresAt.Add(time.Second))
			if err != nil || len(outs) != 1 || !outs[0].Requeued {
				t.Fatalf("Tick: %+v %v", outs, err)
			}
			return n
		}},
		{"a chunk acknowledged before the last one", target, func(t *testing.T, r Record) Record {
			n := handOut(t, r, later, 2, 1)
			n, v, _ := ack(t, n, later, 1)
			if v != AckRecorded {
				t.Fatalf("verdict %v", v)
			}
			return n
		}},
		{"a Tick before anything is due", target, func(t *testing.T, r Record) Record {
			n, outs, err := r.Tick(later)
			if err != nil || len(outs) != 0 {
				t.Fatalf("Tick: %+v %v", outs, err)
			}
			return n
		}},
		{"a repeated receipt", sourceAccepted, func(t *testing.T, r Record) Record {
			n, out := mustApply(t, r, later, Event{Type: EventReceipt})
			if out.Ignored != IgnoreDuplicate {
				t.Fatalf("outcome %+v", out)
			}
			return n
		}},
		{"a repeated acknowledgement of a delivered record", delivered, func(t *testing.T, r Record) Record {
			n, v, _ := ack(t, r, later, 1)
			if v != AckDuplicate {
				t.Fatalf("verdict %v", v)
			}
			return n
		}},
		{"a stale receipt after delivery", sourceDelivered, func(t *testing.T, r Record) Record {
			n, out := mustApply(t, r, later, Event{Type: EventReceipt})
			if out.Ignored != IgnoreStale {
				t.Fatalf("outcome %+v", out)
			}
			return n
		}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			got := r.do(t, r.rec)
			if !got.StateAt.Equal(enteredAt) {
				t.Errorf("StateAt = %v, want the unchanged %v", got.StateAt, enteredAt)
			}
		})
	}
}

// TestDeliveredAtIsTheStateAtOfADeliveredRecord: a delivered record is final, so
// the time it was delivered is the time it entered its state; no other state has
// a delivery time.
func TestDeliveredAtIsTheStateAtOfADeliveredRecord(t *testing.T) {
	ackedAt := at(12 * time.Minute)
	delivered := deliverBody(t, targetRecord(t, t0, time.Hour), ackedAt)
	if !delivered.DeliveredAt().Equal(ackedAt) || !delivered.StateAt.Equal(ackedAt) {
		t.Errorf("DeliveredAt = %v, StateAt = %v, want both %v", delivered.DeliveredAt(), delivered.StateAt, ackedAt)
	}
	for _, s := range []State{StateQueued, StateAccepted, StateRejected, StateExpiredUnconfirmed, StateDeliveryUnknown, StateClosedUnconfirmed} {
		r := Record{State: s, StateAt: ackedAt}
		if !r.DeliveredAt().IsZero() {
			t.Errorf("DeliveredAt of a record in %s = %v, want zero", s, r.DeliveredAt())
		}
	}
}

// TestPropertyStateAtOnlyMovesWithTheState checks every record of the random
// deliveries against an oracle written here: within one side's walk, the time
// never goes back (the walks are time-ordered), it stays while the state stays,
// and it is set on every record.
func TestPropertyStateAtOnlyMovesWithTheState(t *testing.T) {
	type last struct {
		state State
		at    time.Time
		seen  bool
	}
	changed, kept := 0, 0
	for seed := int64(1); seed <= 3000; seed++ {
		var prev [3]last
		t.Run("", func(t *testing.T) {
			runRandomDelivery(t, seed, map[string]int{}, func(t *testing.T, r Record) {
				if r.StateAt.IsZero() {
					t.Fatalf("seed %d: a record in %s has no StateAt", seed, r.State)
				}
				p := &prev[r.Side]
				if p.seen {
					switch {
					case r.State == p.state && !r.StateAt.Equal(p.at):
						t.Fatalf("seed %d: StateAt moved from %v to %v while the state stayed %s", seed, p.at.Sub(t0), r.StateAt.Sub(t0), r.State)
					case r.StateAt.Before(p.at):
						t.Fatalf("seed %d: StateAt went back from %v to %v", seed, p.at.Sub(t0), r.StateAt.Sub(t0))
					}
					if r.State != p.state {
						changed++
					} else {
						kept++
					}
				}
				*p = last{r.State, r.StateAt, true}
			})
		})
	}
	if changed < 5000 || kept < 5000 {
		t.Errorf("the walks changed the state %d times and kept it %d times, want at least 5000 each", changed, kept)
	}
}

// TestTransitionsRefuseAZeroClock: a transition that enters a state at the zero
// time would leave a record without a StateAt, which is no record the package
// produces, so it is an error and the record is unchanged.
func TestTransitionsRefuseAZeroClock(t *testing.T) {
	r := NewSourceRecord(at(time.Minute), at(10*time.Minute))
	got, _, err := r.Apply(time.Time{}, Event{Type: EventReceipt})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
	if !reflect.DeepEqual(got, r) {
		t.Errorf("the record changed on error: %+v", got)
	}
	if err := NewSourceRecord(time.Time{}, at(10*time.Minute)).Valid(); violationOf(t, err) != ViolationStateAt {
		t.Errorf("a record started at the zero time: %v, want %s", err, ViolationStateAt)
	}
}

// A constructor that can refuse refuses a zero time on the spot, as a transition at
// the zero time is refused: the record would otherwise be made and fail only when it
// is written.
func TestConstructorsThatCanRefuseRefuseTheZeroTime(t *testing.T) {
	ab := at(10 * time.Minute)
	_, targetErr := NewTargetRecord(time.Time{}, ab, ReportBy(ab), time.Hour)
	_, noticeErr := NewTargetNoticeRecord(time.Time{}, ab, ReportBy(ab), time.Hour)
	_, _, sourceLostErr := NewLostRecord(SideSource, false, time.Time{})
	_, _, targetLostErr := NewLostRecord(SideTarget, false, time.Time{})
	for rule, err := range map[string]error{
		"a target message record at the zero time":    targetErr,
		"a target notice record at the zero time":     noticeErr,
		"a lost source record found at the zero time": sourceLostErr,
		"a lost target record found at the zero time": targetLostErr,
	} {
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", rule, err)
		}
	}
}
