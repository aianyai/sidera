package engine_test

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// What a lease does while the agent works. A page that recv hands out is leased for
// LeaseDuration; the lease is extended while the endpoint has a turn running, never
// beyond LeaseRenewalCap from its issue, and put back in the queue when it runs out.
// The clock moves a minute at a time, which is the scale the mechanism works at: the
// lease is extended well before it runs out.

// leaseStep is how far the tests of leases move the clock between two looks at the records.
const leaseStep = time.Minute

// leaseOf is the lease the target record of the message holds.
func leaseOf(t testing.TB, h *harness.H, msg adapterproto.MessageID) delivery.Lease {
	t.Helper()
	return inOf(t, h, msg).Record.Lease
}

// minute moves the clock a leaseStep and waits for the node to settle.
func minute(t testing.TB, h *harness.H) {
	t.Helper()
	h.Advance(leaseStep)
	quiet(t, h)
}

// leasedFor follows a lease, a minute at a time, until it is no longer outstanding or
// the delivery has left the queue, and returns how long after its issue that was and
// the longest the lease was ever extended to, measured from its issue. While the lease
// is outstanding it must be live: it never runs out and waits to be put back.
func leasedFor(t testing.TB, h *harness.H, msg adapterproto.MessageID) (ended, longest time.Duration) {
	t.Helper()
	issued := leaseOf(t, h, msg).IssuedAt
	for {
		minute(t, h)
		in := inOf(t, h, msg)
		now := h.Clock().Now()
		if !in.Record.Lease.Active || in.Record.State != delivery.StateAccepted {
			return now.Sub(issued), longest
		}
		if !in.Record.Lease.ExpiresAt.After(now) {
			t.Fatalf("the lease ran out at %v and is still outstanding at %v", in.Record.Lease.ExpiresAt, now)
		}
		longest = max(longest, in.Record.Lease.ExpiresAt.Sub(issued))
		if now.Sub(issued) > 2*delivery.LeaseRenewalCap {
			t.Fatalf("the lease is still outstanding %v after its issue", now.Sub(issued))
		}
	}
}

// A lease is extended only while the endpoint has a turn running, and only as far as
// the cap allows. The endpoint of a session has turns, and the adapter reports them; a
// direct endpoint has none, so a lease on it is never extended.
func TestLeaseIsRenewedOnlyWhileTheEndpointHasAnActiveTurn(t *testing.T) {
	t.Run("a running turn extends the lease to the cap and not beyond", func(t *testing.T) {
		h := sessionNode(t, harness.Options{}, "a")
		_, addr := newSession(t, h, "a") // the first turn of a session is running
		s := h.Session(endpointOfAddr(t, addr).String())
		msg := sendToAddr(t, h, "a", addr, "read, and not acknowledged")
		quiet(t, h)
		recvAs(t, h, s, fakeagent.RecvInput{})
		ended, longest := leasedFor(t, h, msg)
		if longest != delivery.LeaseRenewalCap {
			t.Errorf("the lease was extended to %v after its issue, want the cap %v", longest, delivery.LeaseRenewalCap)
		}
		if ended < delivery.LeaseRenewalCap || ended >= delivery.LeaseRenewalCap+2*leaseStep {
			t.Errorf("the lease ended %v after its issue, want the cap %v", ended, delivery.LeaseRenewalCap)
		}
		if in := inOf(t, h, msg); in.Record.State != delivery.StateAccepted || !in.Record.PossibleDuplicate(1) {
			t.Errorf("after the lease the record is %s, possible duplicate %v, want accepted and possibly seen", in.Record.State, in.Record.PossibleDuplicate(1))
		}
	})
	t.Run("a turn that has ended leaves the lease to run out", func(t *testing.T) {
		h := sessionNode(t, harness.Options{}, "a")
		_, addr := newSession(t, h, "a")
		s := h.Session(endpointOfAddr(t, addr).String())
		msg := sendToAddr(t, h, "a", addr, "read after the turn is over")
		quiet(t, h)
		recvAs(t, h, s, fakeagent.RecvInput{})
		endTurn(t, h, addr)
		ended, longest := leasedFor(t, h, msg)
		if longest > delivery.LeaseDuration || ended > delivery.LeaseDuration+leaseStep {
			t.Errorf("the lease lived %v and was extended to %v with no turn running, want %v", ended, longest, delivery.LeaseDuration)
		}
	})
	t.Run("a direct endpoint has no turn and its lease is not extended", func(t *testing.T) {
		h := nodeWith(t, harness.Options{}, "a", "b")
		msg := send(t, h, "a", "b", "read by a direct endpoint")
		quiet(t, h)
		recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{})
		ended, longest := leasedFor(t, h, msg)
		if longest > delivery.LeaseDuration || ended > delivery.LeaseDuration+leaseStep {
			t.Errorf("the lease lived %v and was extended to %v, want %v", ended, longest, delivery.LeaseDuration)
		}
	})
}

// A message that is never acknowledged is handed out under four leases, each of which
// lives as long as the cap while the agent works; the delivery whose last lease runs out
// is unknown, and the sender is told once.
func TestLeaseRenewalCapEndsInDeliveryUnknownOnce(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	_, addr := newSession(t, h, "a")
	s := h.Session(endpointOfAddr(t, addr).String())
	msg := sendToAddr(t, h, "a", addr, "never acknowledged")
	quiet(t, h)
	for i := 1; i <= delivery.MaxLeases; i++ {
		res := recvAs(t, h, s, fakeagent.RecvInput{})
		if m := wantOnly(t, res, msg); m.PossiblyDuplicate != (i > 1) {
			t.Errorf("hand-out %d: possibly_duplicate is %v, want %v", i, m.PossiblyDuplicate, i > 1)
		}
		ended, longest := leasedFor(t, h, msg)
		if longest > delivery.LeaseRenewalCap || ended < delivery.LeaseRenewalCap {
			t.Fatalf("lease %d lived %v and was extended to %v, want the cap %v and no more", i, ended, longest, delivery.LeaseRenewalCap)
		}
	}
	in := inOf(t, h, msg)
	if in.Record.State != delivery.StateDeliveryUnknown || in.Record.Lease.Handouts != delivery.MaxLeases {
		t.Fatalf("the target record is %s after %d hand-outs, want delivery_unknown after %d", in.Record.State, in.Record.Lease.Handouts, delivery.MaxLeases)
	}
	h.Advance(pullInterval)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateDeliveryUnknown {
		t.Fatalf("the source record is %s, want delivery_unknown", d.State)
	}
	settledOnce(t, h, []delivery.Kind{delivery.KindDeliveryUnknown}, "no_such_alert", 0)
}

// The longest the node can hold a message: the endpoint queues it for the protocol
// maximum, it is leased just before its backlog deadline, and every one of its four
// leases is extended to the cap. The delivery still has its outcome before report_by,
// and the sender has it before it stops pulling.
func TestLongestQueueThenRedeliveriesSettleBeforeReportBy(t *testing.T) {
	h := sessionNode(t, harness.Options{NodeSettings: `{"endpoint_defaults":{"max_queue_s":7200}}`}, "a")
	_, addr := newSession(t, h, "a")
	s := h.Session(endpointOfAddr(t, addr).String())
	sent := h.Clock().Now()
	msg := sendToAddr(t, h, "a", addr, "queued as long as a queue may be")
	quiet(t, h)
	reportBy := inOf(t, h, msg).Record.ReportBy

	// Nobody reads it until the last minutes of its queue time.
	readAt := sent.Add(delivery.MaxQueueCap - 2*time.Minute)
	for h.Clock().Now().Before(readAt) {
		h.Advance(min(10*time.Minute, readAt.Sub(h.Clock().Now())))
		quiet(t, h)
	}
	if in := inOf(t, h, msg); in.Record.State != delivery.StateAccepted || in.Record.Lease.Handouts != 0 {
		t.Fatalf("after the queue time but two minutes the record is %s with %d hand-outs, want it still queued", in.Record.State, in.Record.Lease.Handouts)
	}
	for i := 1; i <= delivery.MaxLeases; i++ {
		wantOnly(t, recvAs(t, h, s, fakeagent.RecvInput{}), msg)
		if _, longest := leasedFor(t, h, msg); longest != delivery.LeaseRenewalCap {
			t.Fatalf("lease %d was extended to %v, want the cap %v", i, longest, delivery.LeaseRenewalCap)
		}
	}
	in := inOf(t, h, msg)
	if in.Record.State != delivery.StateDeliveryUnknown {
		t.Fatalf("the target record is %s after four leases of the cap, want delivery_unknown", in.Record.State)
	}
	if now := h.Clock().Now(); !now.Before(delivery.SettleBy(reportBy)) {
		t.Errorf("the delivery was settled at %v, not before %v, the instant by which the target must have settled", now, delivery.SettleBy(reportBy))
	}
	// The sender learns it by its pull, before it stops pulling at report_by.
	for h.Clock().Now().Before(reportBy) {
		if d := sourceDelivery(t, h, msg); d.State == delivery.StateDeliveryUnknown {
			break
		}
		minute(t, h)
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateDeliveryUnknown {
		t.Fatalf("the source record is %s at report_by %v, want delivery_unknown", d.State, reportBy)
	}
	if now := h.Clock().Now(); now.After(reportBy) {
		t.Errorf("the sender had the outcome at %v, after its pull deadline %v", now, reportBy)
	}
}

// A result the runtime cut or replaced is not what the agent was handed, so the node
// does not acknowledge the page on the agent's behalf for it: the message is left
// leased until it runs out and is handed out again. A result that is whole is
// acknowledged. The adapter reports what it read in the runtime's record; the node
// judges it.
func TestTruncatedOrReplacedResultIsNotAcknowledged(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      fakeadapter.ObservedMode
		delivered bool
	}{
		{"a whole result is acknowledged", fakeadapter.ObservedFaithful, true},
		{"a result the runtime cut is not acknowledged", fakeadapter.ObservedTruncated, false},
		{"a result the runtime replaced with a notice is not acknowledged", fakeadapter.ObservedReplaced, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := sessionNode(t, harness.Options{}, "a")
			_, addr := idleSession(t, h, "a")
			ctl, err := h.Adapter(harness.Instance)
			if err != nil {
				t.Fatal(err)
			}
			if err := ctl.SetObserved(h.Ctx(), tc.mode); err != nil {
				t.Fatal(err)
			}
			// The model reads the page of the next turn and the turn ends.
			plan := fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionRecv}}, EndAfter: true}
			if err := runtimeOfAddr(t, h, addr).SetPlan(nativeRef(t, h, addr), plan); err != nil {
				t.Fatal(err)
			}
			msg := sendToAddr(t, h, "a", addr, "the model reads this")
			quiet(t, h)
			if got := observedVerdicts(t, h); len(got) != 1 || (got[0] == "allowed") != tc.delivered {
				t.Fatalf("the node answered recv.observed with %v, want one answer, allowed %v", got, tc.delivered)
			}
			in := inOf(t, h, msg)
			if tc.delivered {
				if in.Record.State != delivery.StateDelivered {
					t.Fatalf("the record is %s, want delivered: the page was seen whole", in.Record.State)
				}
				return
			}
			if in.Record.State != delivery.StateAccepted || !in.Record.Lease.Active || anyTrue(in.Record.Lease.Acked) {
				t.Fatalf("the record is %s, lease active %v, acknowledged %v: nothing may be acknowledged for a page the model was not shown", in.Record.State, in.Record.Lease.Active, in.Record.Lease.Acked)
			}
			// Nobody acknowledges: the lease runs out and the message is handed out again,
			// as one the agent may have seen.
			leasedFor(t, h, msg)
			s := h.Session(endpointOfAddr(t, addr).String())
			m := wantOnly(t, recvAs(t, h, s, fakeagent.RecvInput{}), msg)
			if !m.PossiblyDuplicate {
				t.Error("the message handed out again is not marked as possibly seen")
			}
		})
	}
}

func anyTrue(bs []bool) bool {
	for _, b := range bs {
		if b {
			return true
		}
	}
	return false
}

// observedVerdicts are the verdicts the node gave to the recv.observed requests of the
// adapter, read from the adapter's own ledger.
func observedVerdicts(t testing.TB, h *harness.H) []string {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range log.Select(func(e ledger.Entry) bool { return e.Kind == ledger.KindObservedSent }) {
		out = append(out, e.Attrs.Verdict)
	}
	return out
}
