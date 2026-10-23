package delivery

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestHandout(t *testing.T) {
	rows := []struct {
		rule      string
		prepare   func(*testing.T) Record
		now       time.Duration
		auth      AuthorizationKind
		chunks    int
		action    HandoutAction
		duplicate bool
		reason    UnavailableReason
		wantState State
		handouts  int
	}{
		{"recv leases a queued message", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) },
			time.Minute, AuthorizationAllow, 1, HandoutGranted, false, UnavailableNone, StateAccepted, 1},
		{"suspended authorization skips the message: no lease, no state change", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) },
			time.Minute, AuthorizationSuspend, 1, HandoutSkipped, false, UnavailableNone, StateAccepted, 0},
		{"denied authorization at recv rejects a delivery whose body was never handed out", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) },
			time.Minute, AuthorizationDeny, 1, HandoutDenied, false, UnavailableNone, StateRejected, 0},
		{"a second recv by the same endpoint returns its leased message flagged as duplicate", func(t *testing.T) Record {
			r := targetRecord(t, t0, time.Hour)
			r, _, _ = r.Handout(at(time.Minute), AuthorizationAllow, 1, 1, testRand)
			return r
		}, 2 * time.Minute, AuthorizationAllow, 1, HandoutResent, true, UnavailableNone, StateAccepted, 1},
		{"suspension also skips a message the endpoint already holds", func(t *testing.T) Record {
			r := targetRecord(t, t0, time.Hour)
			r, _, _ = r.Handout(at(time.Minute), AuthorizationAllow, 1, 1, testRand)
			return r
		}, 2 * time.Minute, AuthorizationSuspend, 1, HandoutSkipped, false, UnavailableNone, StateAccepted, 1},
		{"a message past its backlog deadline is not handed out", func(t *testing.T) Record { return targetRecord(t, t0, 30*time.Minute) },
			30 * time.Minute, AuthorizationAllow, 1, HandoutUnavailable, false, UnavailableDue, StateAccepted, 0},
		{"a lease past its expiry is not resent", func(t *testing.T) Record {
			r := targetRecord(t, t0, time.Hour)
			r, _, _ = r.Handout(at(time.Minute), AuthorizationAllow, 1, 1, testRand)
			return r
		}, 6 * time.Minute, AuthorizationAllow, 1, HandoutUnavailable, false, UnavailableDue, StateAccepted, 1},
		{"a delivered message is not handed out", func(t *testing.T) Record {
			return deliverBody(t, targetRecord(t, t0, time.Hour), at(time.Minute))
		}, 2 * time.Minute, AuthorizationAllow, 1, HandoutUnavailable, false, UnavailableNotQueued, StateDelivered, 1},
		{"a delivery_unknown message is not handed out again", func(t *testing.T) Record {
			r := handOut(t, targetRecord(t, t0, time.Hour), at(time.Minute), 1, 1)
			r, _ = mustApply(t, r, at(time.Minute), Event{Type: EventDeliveryUnknown})
			return r
		}, 2 * time.Minute, AuthorizationAllow, 1, HandoutUnavailable, false, UnavailableNotQueued, StateDeliveryUnknown, 1},
		{"denied authorization at recv closes a delivery with a live lease as delivery_unknown", func(t *testing.T) Record {
			return handOut(t, targetRecord(t, t0, time.Hour), at(time.Minute), 1, 1)
		}, 2 * time.Minute, AuthorizationDeny, 1, HandoutDenied, false, UnavailableNone, StateDeliveryUnknown, 1},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			rec, res, err := r.prepare(t).Handout(t0.Add(r.now), r.auth, r.chunks, 1, testRand)
			if err != nil {
				t.Fatal(err)
			}
			if res.Action != r.action || res.PossibleDuplicate != r.duplicate || res.Reason != r.reason {
				t.Errorf("result %+v", res)
			}
			if rec.State != r.wantState || rec.Lease.Handouts != r.handouts {
				t.Errorf("state %s, handouts %d", rec.State, rec.Lease.Handouts)
			}
		})
	}
}

func TestHandoutLeaseTimes(t *testing.T) {
	t.Run("a lease lasts five minutes", func(t *testing.T) {
		r := targetRecord(t, t0, time.Hour)
		_, res, _ := r.Handout(at(time.Minute), AuthorizationAllow, 1, 1, testRand)
		if want := at(6 * time.Minute); !res.ExpiresAt.Equal(want) {
			t.Errorf("expires %v, want %v", res.ExpiresAt, want)
		}
	})
	t.Run("a resend keeps the original lease", func(t *testing.T) {
		r := targetRecord(t, t0, time.Hour)
		r, first, _ := r.Handout(at(time.Minute), AuthorizationAllow, 1, 1, testRand)
		_, again, _ := r.Handout(at(3*time.Minute), AuthorizationAllow, 1, 1, testRand)
		if !again.ExpiresAt.Equal(first.ExpiresAt) {
			t.Errorf("expiry changed from %v to %v", first.ExpiresAt, again.ExpiresAt)
		}
	})
}

func TestHandoutInputs(t *testing.T) {
	rows := []struct {
		rule string
		rec  func(*testing.T) Record
		auth AuthorizationKind
		n    int
		part int
	}{
		{"the source cannot hand out bodies", func(t *testing.T) Record { return NewSourceRecord(at(10 * time.Minute)) }, AuthorizationAllow, 1, 1},
		{"zero chunks is invalid", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) }, AuthorizationAllow, 0, 1},
		{"an unset authorization is invalid", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) }, 0, 1, 1},
		{"part zero is invalid", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) }, AuthorizationAllow, 2, 0},
		{"a part beyond the chunk count is invalid", func(t *testing.T) Record { return targetRecord(t, t0, time.Hour) }, AuthorizationAllow, 2, 3},
		{"the chunk count cannot change between hand-outs", func(t *testing.T) Record {
			return handOut(t, targetRecord(t, t0, time.Hour), at(time.Minute), 2, 1)
		}, AuthorizationAllow, 3, 1},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if _, _, err := r.rec(t).Handout(at(2*time.Minute), r.auth, r.n, r.part, testRand); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestRenew(t *testing.T) {
	leased := func(t *testing.T) Record {
		r := targetRecord(t, t0, 2*time.Hour)
		r, _, err := r.Handout(t0, AuthorizationAllow, 1, 1, testRand)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	type renewal struct {
		at     time.Duration
		active bool
		want   RenewResult
		expiry time.Duration // expiry after the call, from t0
	}
	rows := []struct {
		rule  string
		calls []renewal
	}{
		{"renewal extends the lease by five minutes from now", []renewal{{4 * time.Minute, true, Renewed, 9 * time.Minute}}},
		{"no renewal without an active turn", []renewal{{4 * time.Minute, false, RenewNoActiveTurn, 5 * time.Minute}}},
		{"no renewal of a lease that has run out", []renewal{{5 * time.Minute, true, RenewLeaseExpired, 5 * time.Minute}}},
		{"the cumulative life is capped at fifteen minutes", []renewal{
			{4 * time.Minute, true, Renewed, 9 * time.Minute},
			{8 * time.Minute, true, Renewed, 13 * time.Minute},
			{12 * time.Minute, true, Renewed, 15 * time.Minute},
			{14 * time.Minute, true, RenewCapReached, 15 * time.Minute},
		}},
		{"renewing again right after hitting the cap changes nothing", []renewal{
			{4 * time.Minute, true, Renewed, 9 * time.Minute},
			{8 * time.Minute, true, Renewed, 13 * time.Minute},
			{9 * time.Minute, true, Renewed, 14 * time.Minute},
			{10 * time.Minute, true, Renewed, 15 * time.Minute},
			{11 * time.Minute, true, RenewCapReached, 15 * time.Minute},
		}},
		{"a lease cannot be renewed after the cap", []renewal{
			{4 * time.Minute, true, Renewed, 9 * time.Minute},
			{8 * time.Minute, true, Renewed, 13 * time.Minute},
			{12 * time.Minute, true, Renewed, 15 * time.Minute},
			{15 * time.Minute, true, RenewLeaseExpired, 15 * time.Minute},
		}},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			rec := leased(t)
			for i, c := range r.calls {
				var got RenewResult
				rec, got = rec.Renew(t0.Add(c.at), c.active)
				if got != c.want || !rec.Lease.ExpiresAt.Equal(t0.Add(c.expiry)) {
					t.Fatalf("call %d: result %v expiry %v, want %v %v", i, got, rec.Lease.ExpiresAt, c.want, t0.Add(c.expiry))
				}
			}
		})
	}
	t.Run("a message with no lease cannot be renewed", func(t *testing.T) {
		_, got := targetRecord(t, t0, time.Hour).Renew(at(time.Minute), true)
		if got != RenewNoLease {
			t.Errorf("got %v", got)
		}
	})
}

func TestLeaseExpiry(t *testing.T) {
	t.Run("an expired lease returns the message to the queue flagged as possible duplicate", func(t *testing.T) {
		r := targetRecord(t, t0, 2*time.Hour)
		r, _, _ = r.Handout(t0, AuthorizationAllow, 1, 1, testRand)
		r, outs, err := r.Tick(at(5 * time.Minute))
		if err != nil || len(outs) != 1 || !outs[0].Requeued {
			t.Fatalf("outs %+v err %v", outs, err)
		}
		if r.State != StateAccepted || r.Lease.Active || !r.Lease.PossibleDuplicate {
			t.Errorf("record %+v", r)
		}
		r, res, _ := r.Handout(at(6*time.Minute), AuthorizationAllow, 1, 1, testRand)
		if res.Action != HandoutGranted || !res.PossibleDuplicate || r.Lease.Handouts != 2 {
			t.Errorf("redelivery %+v handouts %d", res, r.Lease.Handouts)
		}
	})
	t.Run("a lease still running is left alone", func(t *testing.T) {
		r := targetRecord(t, t0, 2*time.Hour)
		r, _, _ = r.Handout(t0, AuthorizationAllow, 1, 1, testRand)
		r2, outs, _ := r.Tick(at(5*time.Minute - time.Second))
		if len(outs) != 0 || !r2.Lease.Active {
			t.Errorf("outs %+v", outs)
		}
	})
	t.Run("after the third redelivery expires the delivery is delivery_unknown", func(t *testing.T) {
		r := targetRecord(t, t0, 2*time.Hour)
		now := t0
		for lease := 1; lease <= MaxLeases; lease++ {
			var res HandoutResult
			var err error
			r, res, err = r.Handout(now, AuthorizationAllow, 1, 1, testRand)
			if err != nil || res.Action != HandoutGranted {
				t.Fatalf("lease %d: %+v %v", lease, res, err)
			}
			now = now.Add(LeaseDuration)
			var outs []Outcome
			r, outs, err = r.Tick(now)
			if err != nil {
				t.Fatal(err)
			}
			wantUnknown := lease == MaxLeases
			if got := r.State == StateDeliveryUnknown; got != wantUnknown {
				t.Fatalf("lease %d: state %s, outcomes %+v", lease, r.State, outs)
			}
		}
		if r.Lease.Handouts != 4 {
			t.Errorf("handouts = %d", r.Lease.Handouts)
		}
	})
	t.Run("renewal up to the cap on every lease still ends in delivery_unknown after four leases", func(t *testing.T) {
		r := targetRecord(t, t0, 2*time.Hour)
		now := t0
		for lease := 1; lease <= MaxLeases; lease++ {
			r, _, _ = r.Handout(now, AuthorizationAllow, 1, 1, testRand)
			for i := 0; i < 5; i++ {
				now = now.Add(4 * time.Minute)
				r, _ = r.Renew(now, true)
			}
			now = r.Lease.ExpiresAt
			r, _, _ = r.Tick(now)
		}
		if r.State != StateDeliveryUnknown || r.Lease.Handouts != MaxLeases {
			t.Errorf("state %s handouts %d", r.State, r.Lease.Handouts)
		}
		if elapsed := now.Sub(t0); elapsed > MaxRedeliveryPeriod {
			t.Errorf("leases took %v, more than the derived period %v", elapsed, MaxRedeliveryPeriod)
		}
	})
	t.Run("a lease still open at the settle deadline becomes delivery_unknown", func(t *testing.T) {
		ab := at(10 * time.Minute)
		r, err := NewTargetRecord(t0, ab, ab.Add(time.Hour), 2*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		settle := SettleBy(r.ReportBy)
		r, _, _ = r.Handout(settle.Add(-time.Minute), AuthorizationAllow, 1, 1, testRand)
		r, outs, _ := r.Tick(settle)
		if r.State != StateDeliveryUnknown || len(outs) != 1 {
			t.Errorf("state %s outs %+v", r.State, outs)
		}
	})
}

func TestAck(t *testing.T) {
	leasedN := func(t *testing.T, n int) Record {
		return handOutAll(t, targetRecord(t, t0, 2*time.Hour), t0, n)
	}
	good := func(part int) Ack { return Ack{Part: part, TokenMatches: true} }

	t.Run("an acknowledgement of a single-chunk message delivers it", func(t *testing.T) {
		r, v, out, err := ackUnder(leasedN(t, 1), at(time.Minute), good(1))
		if err != nil || v != AckCompleted || r.State != StateDelivered || out.To != StateDelivered || r.Lease.Active {
			t.Errorf("verdict %v state %s out %+v err %v", v, r.State, out, err)
		}
	})
	t.Run("an acknowledgement with another endpoint's token is refused", func(t *testing.T) {
		r, v, _, err := ackUnder(leasedN(t, 1), at(time.Minute), Ack{Part: 1})
		if err != nil || v != AckRefusedToken || r.State != StateAccepted {
			t.Errorf("verdict %v state %s err %v", v, r.State, err)
		}
	})
	t.Run("a late acknowledgement after the lease expired still delivers", func(t *testing.T) {
		r := leasedN(t, 1)
		r, _, _ = r.Tick(at(5 * time.Minute))
		r, v, _, _ := ackUnder(r, at(7*time.Minute), good(1))
		if v != AckCompleted || r.State != StateDelivered {
			t.Errorf("verdict %v state %s", v, r.State)
		}
	})
	t.Run("a late acknowledgement corrects delivery_unknown", func(t *testing.T) {
		r := leasedN(t, 1)
		now := t0
		for i := 0; i < MaxLeases; i++ {
			now = now.Add(LeaseDuration)
			r, _, _ = r.Tick(now)
			if i < MaxLeases-1 {
				r, _, _ = r.Handout(now, AuthorizationAllow, 1, 1, testRand)
			}
		}
		if r.State != StateDeliveryUnknown {
			t.Fatalf("state %s", r.State)
		}
		r, v, out, _ := ackUnder(r, now.Add(time.Hour), good(1))
		if v != AckCompleted || r.State != StateDelivered || out.From != StateDeliveryUnknown {
			t.Errorf("verdict %v state %s out %+v", v, r.State, out)
		}
	})
	t.Run("an acknowledgement before any hand-out is refused", func(t *testing.T) {
		r, v, _, err := ackUnder(targetRecord(t, t0, time.Hour), at(time.Minute), good(1))
		if err != nil || v != AckRefusedNoHandout || r.State != StateAccepted {
			t.Errorf("verdict %v err %v", v, err)
		}
	})
	t.Run("a repeated acknowledgement after delivery is a duplicate", func(t *testing.T) {
		r, _, _, _ := ackUnder(leasedN(t, 1), at(time.Minute), good(1))
		_, v, _, _ := ackUnder(r, at(2*time.Minute), good(1))
		if v != AckDuplicate {
			t.Errorf("verdict %v", v)
		}
	})
	t.Run("an acknowledgement after rejection is too late", func(t *testing.T) {
		r, _ := mustApply(t, targetRecord(t, t0, time.Hour), at(time.Minute), Event{Type: EventRejected, Reject: RejectEndpointClosed})
		if r.State != StateRejected {
			t.Fatalf("state %s", r.State)
		}
		_, v, _, _ := ackUnder(r, at(2*time.Minute), good(1))
		if v != AckTooLate {
			t.Errorf("verdict %v", v)
		}
	})
	t.Run("endpoint closed after handout, a late acknowledgement corrects delivery_unknown", func(t *testing.T) {
		r, _ := mustApply(t, leasedN(t, 1), at(time.Minute), Event{Type: EventRejected, Reject: RejectEndpointClosed})
		if r.State != StateDeliveryUnknown {
			t.Fatalf("state after the closure %s", r.State)
		}
		r, v, out := ack(t, r, at(2*time.Minute), 1)
		if v != AckCompleted || r.State != StateDelivered || out.From != StateDeliveryUnknown {
			t.Errorf("verdict %v state %s out %+v", v, r.State, out)
		}
	})
	t.Run("an acknowledgement of a chunk that was never handed out is refused", func(t *testing.T) {
		r := handOut(t, targetRecord(t, t0, 2*time.Hour), t0, 2, 1)
		r2, v, _, err := ackUnder(r, at(time.Minute), good(2))
		if err != nil || v != AckRefusedNoHandout || r2.Lease.Acked[1] || r2.State != StateAccepted {
			t.Errorf("verdict %v acked %v state %s err %v", v, r2.Lease.Acked, r2.State, err)
		}
	})
	t.Run("an acknowledgement after closing is too late", func(t *testing.T) {
		r, _, _ := leasedN(t, 1).Tick(at(10*time.Minute + ReportByOffset + time.Minute))
		_, v, _, _ := ackUnder(r, at(10*time.Minute+ReportByOffset+2*time.Minute), good(1))
		if v != AckTooLate {
			t.Errorf("verdict %v", v)
		}
	})
	t.Run("the source cannot settle leases", func(t *testing.T) {
		_, _, _, err := ackUnder(NewSourceRecord(at(10*time.Minute)), at(time.Minute), good(1))
		if !errors.Is(err, ErrWrongSide) {
			t.Errorf("err %v", err)
		}
	})
}

func TestChunkedAck(t *testing.T) {
	good := func(part int) Ack { return Ack{Part: part, TokenMatches: true} }
	start := func(t *testing.T) Record {
		return handOutAll(t, targetRecord(t, t0, 2*time.Hour), t0, 3)
	}
	t.Run("delivery needs every chunk acknowledged, in any order", func(t *testing.T) {
		r := start(t)
		want := []AckVerdict{AckRecorded, AckRecorded, AckCompleted}
		for i, part := range []int{3, 1, 2} {
			var v AckVerdict
			var err error
			r, v, _, err = ackUnder(r, at(time.Minute), good(part))
			if err != nil || v != want[i] {
				t.Fatalf("part %d: verdict %v err %v", part, v, err)
			}
			if (r.State == StateDelivered) != (i == 2) {
				t.Fatalf("after part %d state is %s", part, r.State)
			}
		}
	})
	t.Run("a repeated chunk acknowledgement does not count twice", func(t *testing.T) {
		r := start(t)
		r, _, _, _ = ackUnder(r, at(time.Minute), good(1))
		r, v, _, _ := ackUnder(r, at(time.Minute), good(1))
		if v != AckDuplicate || r.State != StateAccepted {
			t.Errorf("verdict %v state %s", v, r.State)
		}
	})
	t.Run("a chunk acknowledged before it was handed out is refused, even when the others were handed out", func(t *testing.T) {
		r := handOut(t, targetRecord(t, t0, 2*time.Hour), t0, 3, 1)
		r = handOut(t, r, t0, 3, 3)
		for part, want := range map[int]AckVerdict{1: AckRecorded, 2: AckRefusedNoHandout, 3: AckRecorded} {
			var v AckVerdict
			r, v, _ = ack(t, r, at(time.Minute), part)
			if v != want {
				t.Errorf("part %d: verdict %v, want %v", part, v, want)
			}
		}
		if r.State != StateAccepted || r.NextPart() != 2 {
			t.Errorf("state %s, next part %d", r.State, r.NextPart())
		}
	})
	t.Run("a chunk outside 1..n is invalid", func(t *testing.T) {
		for _, part := range []int{0, 4, -1} {
			if _, _, _, err := ackUnder(start(t), at(time.Minute), good(part)); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("part %d: err %v", part, err)
			}
		}
	})
	t.Run("a refused token does not record the chunk", func(t *testing.T) {
		r, v, _, _ := ackUnder(start(t), at(time.Minute), Ack{Part: 1})
		if v != AckRefusedToken || r.Lease.Acked[0] {
			t.Errorf("verdict %v acked %v", v, r.Lease.Acked)
		}
	})
	t.Run("chunks acknowledged under an expired lease stay acknowledged after redelivery", func(t *testing.T) {
		r := start(t)
		r, _, _, _ = ackUnder(r, at(time.Minute), good(1))
		r, _, _ = r.Tick(at(5 * time.Minute))
		r, _, _ = r.Handout(at(6*time.Minute), AuthorizationAllow, 3, 1, testRand)
		if !r.Lease.Acked[0] {
			t.Error("chunk 1 lost its acknowledgement")
		}
		if h := r.Lease.HandedOut; !h[0] || !h[1] || !h[2] {
			t.Errorf("hand-outs of the first lease were forgotten: %v", h)
		}
	})
	t.Run("the next part is the first one not acknowledged", func(t *testing.T) {
		rows := []struct {
			rule  string
			acked []int
			next  int
		}{
			{"before any acknowledgement the next part is 1", nil, 1},
			{"an acknowledged first part is skipped", []int{1}, 2},
			{"acknowledgements out of order leave the gap as the next part", []int{3}, 1},
			{"acknowledged parts around a gap are skipped to the gap", []int{1, 3}, 2},
			{"parts acknowledged in the order 3, 1 leave 2", []int{3, 1}, 2},
			{"with every part acknowledged there is no next part", []int{2, 3, 1}, 0},
		}
		for _, row := range rows {
			t.Run(row.rule, func(t *testing.T) {
				r := start(t)
				for _, part := range row.acked {
					r, _, _ = ack(t, r, at(time.Minute), part)
				}
				if got := r.NextPart(); got != row.next {
					t.Errorf("next part %d, want %d", got, row.next)
				}
			})
		}
		t.Run("before the first hand-out the next part is 1", func(t *testing.T) {
			if got := targetRecord(t, t0, time.Hour).NextPart(); got != 1 {
				t.Errorf("next part %d", got)
			}
		})
	})
	t.Run("late chunk acknowledgements correct delivery_unknown only when complete", func(t *testing.T) {
		r := start(t)
		r, _ = mustApply(t, r, at(time.Minute), Event{Type: EventDeliveryUnknown})
		r, v, _, _ := ackUnder(r, at(2*time.Minute), good(1))
		if v != AckRecorded || r.State != StateDeliveryUnknown {
			t.Fatalf("verdict %v state %s", v, r.State)
		}
		r, _, _, _ = ackUnder(r, at(3*time.Minute), good(2))
		r, v, _, _ = ackUnder(r, at(4*time.Minute), good(3))
		if v != AckCompleted || r.State != StateDelivered {
			t.Errorf("verdict %v state %s", v, r.State)
		}
	})
	t.Run("acknowledging does not alias the stored chunk set of the earlier record", func(t *testing.T) {
		r := start(t)
		r2, _, _, _ := ackUnder(r, at(time.Minute), good(1))
		if r.Lease.Acked[0] || !r2.Lease.Acked[0] {
			t.Errorf("before %v after %v", r.Lease.Acked, r2.Lease.Acked)
		}
	})
}

func TestSortForRecv(t *testing.T) {
	mk := func(leased bool) Record {
		r := targetRecord(t, t0, time.Hour)
		if leased {
			r, _, _ = r.Handout(t0, AuthorizationAllow, 1, 1, testRand)
		}
		return r
	}
	rs := []Record{mk(false), mk(true), mk(false), mk(true)}
	rs[0].Reject = 1 // marks identity: queued-first
	rs[2].Reject = 2 // queued-second
	rs[1].Reject = 3 // leased-first
	rs[3].Reject = 4 // leased-second
	SortForRecv(rs, at(time.Minute))
	var order []RejectReason
	for _, r := range rs {
		order = append(order, r.Reject)
	}
	want := []RejectReason{3, 4, 1, 2}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order %v, want %v", order, want)
		}
	}
	t.Run("a lease that has run out does not come first", func(t *testing.T) {
		rs := []Record{mk(false), mk(true)}
		SortForRecv(rs, at(10*time.Minute))
		if rs[0].Lease.Active {
			t.Error("expired lease sorted first")
		}
	})
}

func TestLeaseIDs(t *testing.T) {
	t.Run("a new lease carries a fresh identifier that the result reports", func(t *testing.T) {
		r := targetRecord(t, t0, 2*time.Hour)
		r, first, err := r.Handout(t0, AuthorizationAllow, 1, 1, testRand)
		if err != nil || first.Lease.IsZero() || first.Lease != r.Lease.current() {
			t.Fatalf("result %+v current %v err %v", first, r.Lease.current(), err)
		}
		r, _, _ = r.Tick(at(LeaseDuration))
		_, second, err := r.Handout(at(LeaseDuration), AuthorizationAllow, 1, 1, testRand)
		if err != nil || second.Action != HandoutGranted || second.Lease.IsZero() || second.Lease == first.Lease {
			t.Errorf("redelivery lease %v, first %v, err %v", second.Lease, first.Lease, err)
		}
	})
	t.Run("a resend reports the outstanding lease and reads no randomness", func(t *testing.T) {
		r := targetRecord(t, t0, 2*time.Hour)
		r, first, _ := r.Handout(t0, AuthorizationAllow, 1, 1, testRand)
		r, again, err := r.Handout(at(time.Minute), AuthorizationAllow, 1, 1, nil)
		if err != nil || again.Action != HandoutResent || again.Lease != first.Lease || len(r.Lease.IDs) != 1 {
			t.Errorf("result %+v ids %v err %v", again, r.Lease.IDs, err)
		}
	})
	t.Run("randomness that fails leaves the record as it was", func(t *testing.T) {
		r := targetRecord(t, t0, 2*time.Hour)
		after, res, err := r.Handout(t0, AuthorizationAllow, 2, 1, bytes.NewReader(nil))
		if err == nil || res.Action != 0 || after.Lease.Handouts != 0 || after.Lease.Chunks != 0 || len(after.Lease.IDs) != 0 || after.Lease.Active {
			t.Errorf("result %+v lease %+v err %v", res, after.Lease, err)
		}
	})
	t.Run("an all-zero source is refused", func(t *testing.T) {
		_, _, err := targetRecord(t, t0, 2*time.Hour).Handout(t0, AuthorizationAllow, 1, 1, bytes.NewReader(make([]byte, LeaseIDLen)))
		if !errors.Is(err, ErrLeaseIDZero) {
			t.Errorf("err %v", err)
		}
	})
	t.Run("a suspended or denied hand-out reads no randomness", func(t *testing.T) {
		for _, auth := range []AuthorizationKind{AuthorizationSuspend, AuthorizationDeny} {
			if _, _, err := targetRecord(t, t0, 2*time.Hour).Handout(t0, auth, 1, 1, nil); err != nil {
				t.Errorf("authorization %d: %v", auth, err)
			}
		}
	})
}

func TestAckLease(t *testing.T) {
	leased := func(t *testing.T) (Record, LeaseID) {
		r, res, err := targetRecord(t, t0, 2*time.Hour).Handout(t0, AuthorizationAllow, 2, 1, testRand)
		if err != nil {
			t.Fatal(err)
		}
		return r, res.Lease
	}
	other, err := NewLeaseID(testRand)
	if err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		rule  string
		lease func(l LeaseID) LeaseID
		want  AckVerdict
	}{
		{"the outstanding lease is accepted", func(l LeaseID) LeaseID { return l }, AckCompleted},
		{"an identifier never issued for the message is refused", func(LeaseID) LeaseID { return other }, AckRefusedLease},
		{"an unset identifier is refused", func(LeaseID) LeaseID { return LeaseID{} }, AckRefusedLease},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			r, l := leased(t)
			r = handOut(t, r, t0, 2, 2)
			r, _, _, _ = r.Ack(at(time.Minute), Ack{Part: 1, Lease: l, TokenMatches: true})
			got, v, _, err := r.Ack(at(time.Minute), Ack{Part: 2, Lease: row.lease(l), TokenMatches: true})
			if err != nil || v != row.want {
				t.Fatalf("verdict %v err %v", v, err)
			}
			if v == AckRefusedLease && (got.State != StateAccepted || got.Lease.Acked[1]) {
				t.Errorf("a refused acknowledgement changed the record: state %s acked %v", got.State, got.Lease.Acked)
			}
		})
	}
	t.Run("the lease of an expired lease is accepted after the message was redelivered under a new one", func(t *testing.T) {
		r, first := leased(t)
		r, _, _ = r.Tick(at(LeaseDuration))
		r, res, _ := r.Handout(at(LeaseDuration), AuthorizationAllow, 2, 1, testRand)
		if res.Action != HandoutGranted || res.Lease == first {
			t.Fatalf("redelivery %+v", res)
		}
		_, v, _, err := r.Ack(at(LeaseDuration+time.Second), Ack{Part: 1, Lease: first, TokenMatches: true})
		if err != nil || v != AckRecorded {
			t.Errorf("verdict %v err %v", v, err)
		}
	})
	t.Run("a lease named before any hand-out is reported as no hand-out, not as a bad lease", func(t *testing.T) {
		_, v, _, _ := targetRecord(t, t0, time.Hour).Ack(at(time.Minute), Ack{Part: 1, Lease: other, TokenMatches: true})
		if v != AckRefusedNoHandout {
			t.Errorf("verdict %v", v)
		}
	})
	t.Run("a wrong token is reported before a wrong lease", func(t *testing.T) {
		r, _ := leased(t)
		_, v, _, _ := r.Ack(at(time.Minute), Ack{Part: 1, Lease: other})
		if v != AckRefusedToken {
			t.Errorf("verdict %v", v)
		}
	})
	t.Run("a late acknowledgement under a known lease still corrects delivery_unknown", func(t *testing.T) {
		r, l := leased(t)
		r, _ = mustApply(t, r, at(time.Minute), Event{Type: EventDeliveryUnknown})
		r, v, _, _ := r.Ack(at(2*time.Minute), Ack{Part: 1, Lease: l, TokenMatches: true})
		if v != AckRecorded || r.State != StateDeliveryUnknown {
			t.Errorf("verdict %v state %s", v, r.State)
		}
	})
}
