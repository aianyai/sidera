package engine_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/reply"
	"github.com/aianyai/sidera/internal/timing"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// What a source delivery that comes to report_by without the sender having been told
// an outcome does. The target keeps its records, and answers a pull from them, for
// about a day after report_by, so the node pulls the target once more before it
// closes the delivery, and applies what it learns with its true meaning. What it
// cannot learn (the call fails, the target holds no record, the target's retention
// is over) it closes with one notice that says so: silence after a deadline reads as
// delivered, and the target may have rejected the message meanwhile.
//
// The tests run on one node whose source and target sides are the same process. A
// node that sleeps is stopped, the clock moved and the node started again; what the
// sender could not learn while it was awake is kept from it by making the target
// unreachable, or by holding its reports.

// sleepUntilAfter stops the node, moves the clock to the given time after the
// instant, and starts the node again, which is where the node finds every delivery
// that came to its end while it slept.
func sleepUntilAfter(t *testing.T, h *harness.H, instant time.Time, after time.Duration) {
	t.Helper()
	h.Stop()
	h.Advance(instant.Sub(h.Clock().Now()) + after)
	h.Start()
	quiet(t, h)
}

// toldOnce says what the sender a was told after the merge window, and that neither
// time nor a restart tells it again.
func toldOnce(t *testing.T, h *harness.H, want []delivery.Kind) {
	t.Helper()
	h.Advance(mergeWindow)
	quiet(t, h)
	check := func(when string) {
		t.Helper()
		if got := toldKinds(t, h, "a"); !slices.Equal(got, want) {
			t.Errorf("%s: a was told %v, want %v", when, got, want)
		}
		if got := pending(t, h, "a"); got != 0 {
			t.Errorf("%s: %d items are still pending", when, got)
		}
	}
	check("at the start")
	h.Restart()
	quiet(t, h)
	h.Advance(time.Hour)
	quiet(t, h)
	check("after a restart and an hour")
}

// finalPullStates make the message a sends to b in the two states the sender can be
// in when it holds no outcome: it has the receipt (accepted), or the target holds the
// message and the receipt never came (queued).
var finalPullStates = []struct {
	name  string
	setup func(t *testing.T, h *harness.H) adapterproto.MessageID
	state delivery.State
	// closes is what the sender is told when the delivery closes without an answer.
	closes delivery.Kind
}{
	{"accepted", func(t *testing.T, h *harness.H) adapterproto.MessageID {
		msg := send(t, h, "a", "b", "accepted and never answered")
		quiet(t, h)
		return msg
	}, delivery.StateAccepted, delivery.KindAcceptedUnconfirmed},
	{"queued", func(t *testing.T, h *harness.H) adapterproto.MessageID {
		h.Unreachable(true)
		msg := send(t, h, "a", "b", "its receipt is held")
		quiet(t, h)
		h.HoldReports(string(msg))
		h.Unreachable(false)
		h.Advance(resendBackoff)
		quiet(t, h)
		return msg
	}, delivery.StateQueued, delivery.KindExpiredUnconfirmed},
}

// A sender asleep past report_by wakes to a delivery the target rejected while it
// slept. The one pull before the close brings the signed rejection, which is told as
// a rejection: not "unknown", and not silence. It carries a time before report_by,
// because the target settled it while it was awake.
func TestASourceAsleepPastReportByIsToldTheRejectionTheTargetSigned(t *testing.T) {
	for _, st := range finalPullStates {
		t.Run(st.name, func(t *testing.T) {
			h := nodeWith(t, harness.Options{}, "a", "b")
			msg := st.setup(t, h)
			if d := sourceDelivery(t, h, msg); d.State != st.state {
				t.Fatalf("the source record is %s, want %s", d.State, st.state)
			}
			reportBy := sourceDelivery(t, h, msg).ReportBy
			// The sender cannot learn anything while the target rejects the message
			// at the end of the endpoint's queue time.
			h.Unreachable(true)
			h.Advance(delivery.DefaultEndpointMaxQueue + time.Minute)
			quiet(t, h)
			if in := inOf(t, h, msg); in.Record.State != delivery.StateRejected || !in.Record.StateAt.Before(reportBy) {
				t.Fatalf("the target record is %s at %v, want rejected before report_by %v", in.Record.State, in.Record.StateAt, reportBy)
			}
			if d := sourceDelivery(t, h, msg); d.State != st.state {
				t.Fatalf("the source learned %s while the target was unreachable", d.State)
			}

			h.Stop()
			h.Advance(reportBy.Sub(h.Clock().Now()) + time.Minute)
			h.Unreachable(false)
			h.Release(string(msg))
			h.Start()
			quiet(t, h)

			if d := sourceDelivery(t, h, msg); d.State != delivery.StateRejected || d.Reject != delivery.RejectBacklogExpired {
				t.Fatalf("the source record is %s (%s) at the start after report_by, want rejected (backlog_expired): the pull before the close learned it", d.State, d.Reject)
			}
			toldOnce(t, h, []delivery.Kind{delivery.KindRejected})
		})
	}
}

// A sender asleep past report_by that cannot reach the target when it wakes closes the
// delivery and tells the sender once that its result could not be learned (for a
// queued delivery, the expiry), and neither a restart nor time tells it again.
func TestASourceAsleepPastReportByTellsOnceWhenTheTargetCannotBeAsked(t *testing.T) {
	for _, st := range finalPullStates {
		t.Run(st.name, func(t *testing.T) {
			h := nodeWith(t, harness.Options{}, "a", "b")
			msg := st.setup(t, h)
			reportBy := sourceDelivery(t, h, msg).ReportBy
			h.Unreachable(true)
			sleepUntilAfter(t, h, reportBy, time.Minute)

			if d := sourceDelivery(t, h, msg); d.State != delivery.StateClosedUnconfirmed {
				t.Fatalf("the source record is %s at the start after report_by, want closed_unconfirmed", d.State)
			}
			toldOnce(t, h, []delivery.Kind{st.closes})
		})
	}
}

// The window the target keeps its records for is over when the node wakes: it makes no
// pull, and tells the sender that the result could not be learned, though the target
// still holds a rejection it would have told.
func TestASourceAsleepPastTheTargetsRetentionAsksNoOne(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	msg := send(t, h, "a", "b", "its target forgets it")
	quiet(t, h)
	reportBy := sourceDelivery(t, h, msg).ReportBy
	h.Unreachable(true)
	h.Advance(delivery.DefaultEndpointMaxQueue + time.Minute)
	quiet(t, h)
	if in := inOf(t, h, msg); in.Record.State != delivery.StateRejected {
		t.Fatalf("the target record is %s, want rejected", in.Record.State)
	}

	const delay = time.Second
	h.Stop()
	h.Advance(reply.RetainFactsUntil(reportBy).Sub(h.Clock().Now()) + time.Minute)
	h.Unreachable(false)
	h.Delay(delay) // a call that is made is counted, and waits for the clock
	h.Start()
	quiet(t, h)

	if pulls := callsOf(t, h, "pull"); pulls != 0 {
		t.Errorf("the node made %d pulls after the target's retention was over, want none", pulls)
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateClosedUnconfirmed {
		t.Fatalf("the source record is %s, want closed_unconfirmed", d.State)
	}
	toldOnce(t, h, []delivery.Kind{delivery.KindAcceptedUnconfirmed})
}

// The window is the one the target's store keeps its records for: a pull made at its
// last instant still finds the record the store has not swept, and the engine does
// not stop asking sooner than the store keeps.
func TestTheEnginesLastPullIsNeverLaterThanTheStoreKeepsARecord(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	msg := send(t, h, "a", "b", "kept as long as the window")
	quiet(t, h)
	in := inOf(t, h, msg)
	until, _ := store.RetainUntil(in)
	if limit := engine.FinalPullUntil(in.Record.ReportBy); limit.After(until) {
		t.Errorf("the engine asks until %v, the store keeps the record until %v", limit, until)
	}
	if limit := engine.FinalPullUntil(in.Record.ReportBy); !limit.After(in.Record.ReportBy.Add(timing.ClockTolerance)) {
		t.Errorf("the engine asks until %v, no longer than the pull window after report_by %v", limit, in.Record.ReportBy)
	}
}

// A delivery the target delivered while the sender slept is learned by the pull before
// the close, and stays silent.
func TestADeliveryLearnedByThePullBeforeTheCloseIsSilent(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	msg := send(t, h, "a", "b", "read while the sender slept")
	quiet(t, h)
	reportBy := sourceDelivery(t, h, msg).ReportBy
	h.Unreachable(true)
	ins := inRecords(t, h, msg)
	h.Stop()
	deliverOnDisk(t, h, ins)
	h.Advance(reportBy.Sub(h.Clock().Now()) + time.Minute)
	h.Unreachable(false)
	h.Start()
	quiet(t, h)

	if d := sourceDelivery(t, h, msg); d.State != delivery.StateDelivered {
		t.Fatalf("the source record is %s, want delivered: the pull before the close learned it", d.State)
	}
	toldOnce(t, h, nil)
}

// A delivery whose expiry the sender was told closes silently: it has been told the
// target may have the message, and the close adds nothing. No pull is made for it
// either: it was never accepted, as far as the sender knows.
func TestADeliveryTheSenderWasToldExpiredClosesWithoutAWord(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	h.Unreachable(true)
	msg := send(t, h, "a", "b", "never reached the target")
	quiet(t, h)
	reportBy := sourceDelivery(t, h, msg).ReportBy
	h.Advance(delivery.DefaultAcceptBefore + timing.ClockTolerance + time.Minute)
	quiet(t, h)
	h.Unreachable(false)
	h.Advance(pullInterval)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateExpiredUnconfirmed {
		t.Fatalf("the source record is %s, want expired_unconfirmed", d.State)
	}
	h.Advance(mergeWindow)
	quiet(t, h)
	if got := toldKinds(t, h, "a"); !slices.Equal(got, []delivery.Kind{delivery.KindExpiredUnconfirmed}) {
		t.Fatalf("a was told %v, want the expiry", got)
	}

	h.Unreachable(true)
	sleepUntilAfter(t, h, reportBy, time.Minute)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateClosedUnconfirmed {
		t.Fatalf("the source record is %s, want closed_unconfirmed", d.State)
	}
	toldOnce(t, h, []delivery.Kind{delivery.KindExpiredUnconfirmed})
}

// The target says it holds no record when the node pulls it before the close: after
// report_by the target may have forgotten the record legitimately, so it is not read
// as a loss. The delivery closes and the sender is told that its result could not be
// learned.
func TestNoRecordInThePullBeforeTheCloseIsNotALoss(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	msg := send(t, h, "a", "b", "its record will be lost")
	quiet(t, h)
	reportBy := sourceDelivery(t, h, msg).ReportBy
	removeTargetRecords(t, h, msg, "b")
	h.Advance(reportBy.Sub(h.Clock().Now()) + time.Minute)
	h.Start()
	quiet(t, h)

	d := sourceDelivery(t, h, msg)
	if d.State != delivery.StateClosedUnconfirmed || d.Unknown != delivery.UnknownCauseNone {
		t.Fatalf("the source record is %s (%s), want closed_unconfirmed and no loss", d.State, d.Unknown)
	}
	toldOnce(t, h, []delivery.Kind{delivery.KindAcceptedUnconfirmed})
}

// unreachablePulls counts the pulls the node made while the target was unreachable,
// from the ledger: each call to an unreachable node is a hit that names its kind.
func unreachablePulls(t testing.TB, h *harness.H) int {
	t.Helper()
	log, err := h.Ledger()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	n := 0
	for _, e := range log.Kind(ledger.KindKnobHit) {
		if e.Attrs.Knob == "unreachable" && strings.Contains(e.Attrs.Detail, "call=pull ") {
			n++
		}
	}
	return n
}

// inFlightSend puts the source record of the message in the state it is in while its
// send is under way, and refuses the send as a conflict when the returned function
// is called, as the target does that holds another request under the identifier.
// The delivery is no longer in the outbox then, and nothing in the schedule asks for
// it.
func inFlightSend(t *testing.T, h *harness.H, msg adapterproto.MessageID) (refuse func()) {
	t.Helper()
	name := outOf(t, h, msg).Name()
	inLoop := func(fn func(engine.LoopSide)) {
		t.Helper()
		if err := engine.InLoop(h.Ctx(), h.Engine(), fn); err != nil {
			t.Fatal(err)
		}
	}
	inLoop(func(l engine.LoopSide) { l.SendInFlight(name) })
	return func() {
		t.Helper()
		inLoop(func(l engine.LoopSide) { l.SendRefusedAsConflict(name, harness.NodeID(), delivery.MessageID(msg)) })
	}
}

// A delivery that stays queued because the target answered its send with a conflict
// is no longer sent, but it is still a delivery the sender was told nothing of: when
// the target cannot be asked at report_by, it closes with the notice of that, and not
// in silence. (What the pull brings when the target holds the conflicting request is
// that request's report, which the delivery follows; no test here covers it.)
func TestADeliveryRemovedFromTheOutboxByAConflictIsClosedWithANoticeWhenTheTargetCannotBeAsked(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	h.Unreachable(true)
	msg := send(t, h, "a", "b", "its identifier is held by another request")
	quiet(t, h)
	reportBy := sourceDelivery(t, h, msg).ReportBy
	inFlightSend(t, h, msg)()
	sleepUntilAfter(t, h, reportBy, time.Minute)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateClosedUnconfirmed {
		t.Fatalf("the source record is %s, want closed_unconfirmed", d.State)
	}
	toldOnce(t, h, []delivery.Kind{delivery.KindExpiredUnconfirmed})
}

// The same while the node keeps running: nothing of the outbound schedule is left for
// a delivery a conflict removed from the outbox, so the tick at report_by is what asks
// for the one pull before the close. The target is unreachable, so the pull fails and
// the delivery closes with the notice, once.
func TestAConflictedDeliveryIsPulledOnceAndClosedWithANoticeWhileTheNodeRuns(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	h.Unreachable(true)
	msg := send(t, h, "a", "b", "its identifier is held by another request")
	quiet(t, h)
	reportBy := sourceDelivery(t, h, msg).ReportBy
	inFlightSend(t, h, msg)()
	before := unreachablePulls(t, h)

	h.Advance(reportBy.Sub(h.Clock().Now()) + time.Minute)
	quiet(t, h)

	if pulls := unreachablePulls(t, h) - before; pulls != 1 {
		t.Errorf("the node made %d pulls after report_by, want the one before the close", pulls)
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateClosedUnconfirmed {
		t.Fatalf("the source record is %s, want closed_unconfirmed without a restart", d.State)
	}
	toldOnce(t, h, []delivery.Kind{delivery.KindExpiredUnconfirmed})
}

// A delivery whose pull before the close never comes still closes with its notice
// when the window the target keeps its records for is over, with no restart needed:
// here the send that was under way when report_by passed is refused as a conflict, so
// the pull that was asked for found the worker busy and nothing asked again.
func TestADeliveryWhoseLastPullNeverComesClosesWhenTheTargetsRetentionIsOver(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	h.Unreachable(true)
	msg := send(t, h, "a", "b", "its send is under way across report_by")
	quiet(t, h)
	reportBy := sourceDelivery(t, h, msg).ReportBy
	refuse := inFlightSend(t, h, msg)
	before := unreachablePulls(t, h)

	h.Advance(reportBy.Sub(h.Clock().Now()) + time.Minute)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateQueued {
		t.Fatalf("the source record is %s after report_by with a send under way, want it waiting for the pull before the close", d.State)
	}
	refuse()
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateQueued {
		t.Fatalf("the source record is %s, want queued: the worker has nothing to ask the target", d.State)
	}

	h.Advance(reply.RetainFactsUntil(reportBy).Sub(h.Clock().Now()) + time.Minute)
	quiet(t, h)
	if pulls := unreachablePulls(t, h) - before; pulls != 0 {
		t.Errorf("the node made %d pulls, want none: the target's retention is over", pulls)
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateClosedUnconfirmed {
		t.Fatalf("the source record is %s once the target's retention is over, want closed_unconfirmed without a restart", d.State)
	}
	toldOnce(t, h, []delivery.Kind{delivery.KindExpiredUnconfirmed})
}

// While the wall clock is in doubt the node judges nothing by a deadline, and the pull
// before a close is a step towards judging one: it is not made. When the owner has
// cleared the pause the pull is made once and the delivery closes with its notice.
func TestNoPullBeforeTheCloseIsMadeWhileTheClockIsInDoubt(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	msg := send(t, h, "a", "b", "its report_by comes while the clock is in doubt")
	quiet(t, h)
	reportBy := sourceDelivery(t, h, msg).ReportBy
	h.Unreachable(true)
	h.Advance(10 * time.Minute)
	pending(t, h, "a") // a command reads the clock and raises the mark
	quiet(t, h)
	mark := h.Clock().Now()

	h.Set(mark.Add(-30 * time.Minute))
	if _, err := trySend(h, "a", "b", "finds the clock set back", ""); fakeagent.CodeOf(err) != "clock_rollback" {
		t.Fatalf("send while the clock is behind the mark: %v; want the refusal for the clock", err)
	}
	before := unreachablePulls(t, h)

	h.Set(reportBy.Add(time.Minute)) // ahead of the mark again; the owner has cleared nothing
	quiet(t, h)
	h.Advance(5 * time.Minute)
	quiet(t, h)
	if pulls := unreachablePulls(t, h) - before; pulls != 0 {
		t.Errorf("the node made %d pulls after report_by while the clock was in doubt, want none", pulls)
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateAccepted {
		t.Fatalf("the source record is %s while the clock is in doubt, want it untouched (accepted)", d.State)
	}

	if _, err := h.Engine().Execute(context.Background(), clearCommand(74, "clear")); err != nil {
		t.Fatal(err)
	}
	h.Advance(2 * time.Minute)
	quiet(t, h)
	if pulls := unreachablePulls(t, h) - before; pulls != 1 {
		t.Errorf("the node made %d pulls once the pause was cleared, want the one before the close", pulls)
	}
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateClosedUnconfirmed {
		t.Fatalf("the source record is %s once the pause was cleared, want closed_unconfirmed", d.State)
	}
	toldOnce(t, h, []delivery.Kind{delivery.KindAcceptedUnconfirmed})
}

// deliverOnDisk makes the target record of the message delivered, as the agent's
// acknowledgement of its body would, on the disk of a node that is down.
func deliverOnDisk(t *testing.T, h *harness.H, ins []store.InRecord) {
	t.Helper()
	for _, in := range ins {
		now := h.Clock().Now()
		lease, err := delivery.NewLeaseID(bytes.NewReader([]byte("0123456789abcdef")))
		if err != nil {
			t.Fatal(err)
		}
		rec, res, err := in.Record.Handout(now, delivery.AuthorizationAllow, 1, 1, lease)
		if err != nil || res.Action != delivery.HandoutGranted {
			t.Fatalf("handing out the body: %+v %v", res, err)
		}
		rec, _, _, err = rec.Ack(now, delivery.Ack{Part: 1, Lease: lease, TokenMatches: true})
		if err != nil || rec.State != delivery.StateDelivered {
			t.Fatalf("acknowledging the body: state %s, %v", rec.State, err)
		}
		in.Record, in.Body = rec, nil
		data, err := store.EncodeRecord(in)
		if err != nil {
			t.Fatal(err)
		}
		writeOnDisk(t, h, store.InDir, in.Name(), data)
	}
}
