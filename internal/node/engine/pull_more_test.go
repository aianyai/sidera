package engine_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// cliRecvWait is the longest a recv through the command line may wait on the
// adapter of the fixture that declares a runtime.
const cliRecvWait = 20 * time.Second

// The rules around recv and ack that the named scenarios do not reach: the second
// check at the moment of the hand-out, a body that is no longer what its record
// says, the waits, the lease across a restart, and the parts of a long message.

// An endpoint may tighten its declaration after a message was taken in. The check is
// made again when the body would be handed out: a message the declaration no longer
// lets in is not handed out but closed, as rejected when its body was never given to
// the agent and as unknown when it was, and the sender is told once.
func TestADeclarationTightenedAfterIntakeRejectsAtRecv(t *testing.T) {
	t.Run("a body never handed out is rejected", func(t *testing.T) {
		h := nodeWith(t, harness.Options{}, "a", "b", "c")
		b := h.Agent("b")
		fromA := send(t, h, "a", "b", "from a, queued before the declaration changed")
		quiet(t, h)
		h.Advance(time.Second) // the queue is in the order of arrival
		fromC := send(t, h, "c", "b", "from c")
		quiet(t, h)
		accept := []string{h.Addr("c")}
		if err := b.Declare(h.Ctx(), fakeagent.DeclareInput{RestrictSenders: boolp(true), AcceptFrom: &accept}); err != nil {
			t.Fatal(err)
		}
		res := recvAs(t, h, b, fakeagent.RecvInput{})
		wantOnly(t, res, fromC)
		quiet(t, h)
		in := inOf(t, h, fromA)
		if in.Record.State != delivery.StateRejected || in.Record.Reject != delivery.RejectSecondCheckFailed || in.Body != nil {
			t.Fatalf("the record is %s (%s) with body %v, want rejected, second_check_failed, no body", in.Record.State, in.Record.Reject, in.Body)
		}
		h.Advance(pullInterval)
		quiet(t, h)
		if d := sourceDelivery(t, h, fromA); d.State != delivery.StateRejected || d.Reject != delivery.RejectSecondCheckFailed {
			t.Fatalf("the source record is %s (%s), want rejected, second_check_failed", d.State, d.Reject)
		}
		settledOnce(t, h, []delivery.Kind{delivery.KindRejected}, "no_such_alert", 0)
	})
	t.Run("a body handed out before is unknown", func(t *testing.T) {
		h := nodeWith(t, harness.Options{}, "a", "b")
		b := h.Agent("b")
		msg := send(t, h, "a", "b", "handed out, not acknowledged")
		quiet(t, h)
		wantOnly(t, recvAs(t, h, b, fakeagent.RecvInput{}), msg)
		h.Advance(delivery.LeaseDuration)
		quiet(t, h)
		declineAll(t, h, "b")
		wantEmpty(t, recvAs(t, h, b, fakeagent.RecvInput{}))
		quiet(t, h)
		if in := inOf(t, h, msg); in.Record.State != delivery.StateDeliveryUnknown {
			t.Fatalf("the record is %s, want delivery_unknown: the body was handed out", in.Record.State)
		}
		h.Advance(pullInterval)
		quiet(t, h)
		settledOnce(t, h, []delivery.Kind{delivery.KindDeliveryUnknown}, "no_such_alert", 0)
	})
}

// A message the page does not reach is not judged: the declaration may be loosened
// again before the message would be handed out, and a message closed in the
// meantime would have been lost for a rule that was never applied to it.
func TestAMessageBeyondThePageIsNotJudgedByTheSecondCheck(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b", "c")
	b := h.Agent("b")
	big := send(t, h, "c", "b", bigText("a message that fills the page", 60*1024))
	quiet(t, h)
	h.Advance(time.Second) // the queue is in the order of arrival
	beyond := send(t, h, "a", "b", "queued behind it, from an endpoint the declaration will refuse")
	quiet(t, h)
	accept := []string{h.Addr("c")}
	if err := b.Declare(h.Ctx(), fakeagent.DeclareInput{RestrictSenders: boolp(true), AcceptFrom: &accept}); err != nil {
		t.Fatal(err)
	}
	res := recvAs(t, h, b, fakeagent.RecvInput{})
	if m := res.Page.Messages; len(m) != 1 || m[0].ID != big || m[0].Part == nil {
		t.Fatalf("the page holds %v, want one part of the long message", pageIDs(res))
	}
	quiet(t, h)
	if in := inOf(t, h, beyond); in.Record.State != delivery.StateAccepted {
		t.Fatalf("the message behind the page is %s, want accepted: no hand-out reached it", in.Record.State)
	}
	both := []string{h.Addr("a"), h.Addr("c")}
	if err := b.Declare(h.Ctx(), fakeagent.DeclareInput{AcceptFrom: &both}); err != nil {
		t.Fatal(err)
	}
	ackAs(t, h, b, res.Page.Lease)
	for i := 0; i < 20; i++ {
		res = recvAs(t, h, b, fakeagent.RecvInput{})
		if slices.Contains(pageIDs(res), beyond) {
			return
		}
		ackAs(t, h, b, res.Page.Lease)
	}
	t.Fatal("the message behind the page was never handed out once the declaration allowed it")
}

// recv reads the body of a record through the one reader that settles a body that is
// not what its record says. Whether the file is changed or gone, nothing is handed
// out for that record, the owner is alerted once, the other messages of the page are
// not held back, and the recipient's ledger shows no sighting of it. What the sender
// is told is the settlement's, proved with the quarantine of a body.
func TestRecvOfABodyThatIsWrongOrGoneHandsNothingOutForIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, h *harness.H, in store.InRecord)
	}{
		{"a body that is not what its record says", func(t *testing.T, h *harness.H, in store.InRecord) { flipBodyFile(t, h, in) }},
		{"a body file that is gone", func(t *testing.T, h *harness.H, in store.InRecord) { removeBody(t, h, in) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, bad := settledMessage(t)
			good := send(t, h, "a", "b", "its body is intact")
			quiet(t, h)
			tc.damage(t, h, inOf(t, h, bad))
			// The damage is made on a stopped node and read by a running one.
			h.Stop()
			h.Start()
			quiet(t, h)

			res := recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{})
			wantOnly(t, res, good)
			if ins := inRecords(t, h, bad); len(ins) != 0 {
				t.Fatalf("the target holds %d records of the message whose body was lost, want none", len(ins))
			}
			again := recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{})
			if slices.Contains(pageIDs(again), bad) {
				t.Errorf("the message whose body was lost is handed out by the next recv")
			}
			log, err := h.Ledger()
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range log.Kind("model.saw") {
				if e.Attrs.MessageID == string(bad) {
					t.Errorf("the recipient's ledger shows a sighting of the message whose body was lost")
				}
			}
			if n := len(alertsOfKind(t, h, "corrupt_body")); n != 1 {
				t.Errorf("the owner has %d alerts of kind corrupt_body, want 1", n)
			}
		})
	}
}

// A lease is part of the record on the disk. One that is live when the node starts is
// still live, and the message comes back under it as a possible duplicate; one that
// ran out while the node was down is released at the start and comes back as a
// possible duplicate under a new lease.
func TestALeaseAcrossARestart(t *testing.T) {
	t.Run("still live", func(t *testing.T) {
		h := nodeWith(t, harness.Options{}, "a", "b")
		msg := send(t, h, "a", "b", "leased when the node stopped")
		quiet(t, h)
		wantOnly(t, recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{}), msg)
		h.Restart()
		quiet(t, h)
		m := wantOnly(t, recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{}), msg)
		if !m.PossiblyDuplicate {
			t.Error("the message handed out again is not marked as a possible duplicate")
		}
		if in := inOf(t, h, msg); in.Record.Lease.Handouts != 1 {
			t.Errorf("the record counts %d hand-outs, want 1: a live lease is not a new one", in.Record.Lease.Handouts)
		}
	})
	t.Run("ran out while the node was down", func(t *testing.T) {
		h := nodeWith(t, harness.Options{}, "a", "b")
		msg := send(t, h, "a", "b", "leased when the node stopped")
		quiet(t, h)
		first := recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{})
		wantOnly(t, first, msg)
		h.Stop()
		h.Advance(delivery.LeaseDuration + time.Minute)
		h.Start()
		quiet(t, h)
		if in := inOf(t, h, msg); in.Record.Lease.Active {
			t.Fatal("the lease that ran out is still active after the start")
		}
		res := recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{})
		m := wantOnly(t, res, msg)
		if !m.PossiblyDuplicate || res.Page.Lease == first.Page.Lease {
			t.Errorf("the message is handed out again with possibly_duplicate %v under the lease %s, want a possible duplicate under a new lease", m.PossiblyDuplicate, res.Page.Lease)
		}
	})
}

// A part of a message can be asked for by its number, whether or not the parts before
// it were seen, and a plain recv goes on at the first part that was not acknowledged.
func TestAPartOfAMessageCanBeFetchedByNumber(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	b := h.Agent("b")
	body := bigText("a long message", 40*1024)
	msg := send(t, h, "a", "b", body)
	quiet(t, h)

	third := recvAs(t, h, b, fakeagent.RecvInput{Msg: msg, Part: 3})
	m := wantPart(t, third, msg, 3)
	if m.PossiblyDuplicate {
		t.Error("a part never handed out is marked as a possible duplicate")
	}
	if acks := ackAs(t, h, b, third.Page.Lease); len(acks) != 1 || acks[0].Part != 3 || acks[0].Ack != adapterproto.ItemRecorded {
		t.Fatalf("the acknowledgement is %+v, want part 3 recorded", acks)
	}
	// Part 1 is the first not acknowledged.
	wantPart(t, recvAs(t, h, b, fakeagent.RecvInput{}), msg, 1)
	again := recvAs(t, h, b, fakeagent.RecvInput{Msg: msg, Part: 3})
	if m := wantPart(t, again, msg, 3); !m.PossiblyDuplicate {
		t.Error("part 3 handed out a second time is not marked as a possible duplicate")
	}
	for _, bad := range []fakeagent.RecvInput{{Msg: msg, Part: 99}, {Msg: "no-such-message", Part: 1}, {Msg: msg, Part: 1, WaitS: 5}} {
		if _, err := b.Recv(h.Ctx(), withCLI(bad)); err == nil {
			t.Errorf("recv %+v was accepted, want a refusal", bad)
		}
	}
}

func withCLI(in fakeagent.RecvInput) fakeagent.RecvInput {
	in.Frontend = adapterproto.FrontendCLI
	return in
}

// wantPart fails the test unless the page holds exactly one part, the given one, of
// the message, and returns it.
func wantPart(t testing.TB, res fakeagent.RecvResult, msg adapterproto.MessageID, part int) adapterproto.PageMessage {
	t.Helper()
	if len(res.Page.Messages) != 1 || res.Page.Messages[0].ID != msg || res.Page.Messages[0].Part == nil || res.Page.Messages[0].Part.Index != part {
		t.Fatalf("the page holds %+v, want part %d of %s", res.Page.Messages, part, msg)
	}
	return res.Page.Messages[0]
}

// recv with ack_lease acknowledges the page before it and hands out the next; a lease
// the endpoint does not hold only costs the acknowledgement.
func TestRecvAcknowledgesTheEarlierPageFirst(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	b := h.Agent("b")
	m1 := send(t, h, "a", "b", "first")
	quiet(t, h)
	p1 := recvAs(t, h, b, fakeagent.RecvInput{})
	wantOnly(t, p1, m1)
	m2 := send(t, h, "a", "b", "second")
	quiet(t, h)
	p2 := recvAs(t, h, b, fakeagent.RecvInput{AckLease: p1.Page.Lease})
	wantOnly(t, p2, m2)
	quiet(t, h)
	if in := inOf(t, h, m1); in.Record.State != delivery.StateDelivered {
		t.Errorf("the first message is %s, want delivered by the acknowledgement that came with the next recv", in.Record.State)
	}
	p3 := recvAs(t, h, b, fakeagent.RecvInput{AckLease: "00000000000000000000000000000001"})
	wantOnly(t, p3, m2) // still leased, handed out again
	if !p3.Page.Messages[0].PossiblyDuplicate {
		t.Error("the message under a live lease is handed out again without the mark")
	}
}

// What an agent that waits is handed, and when the wait ends. The wait is the
// engine's, on the injected clock: it ends with an empty page at its deadline, is
// cut by a newer recv of the endpoint, is dropped when its caller gives up, and is
// served by the arrival of a message in the command that took the message in.
func TestARecvThatWaits(t *testing.T) {
	waiting := func(t *testing.T, h *harness.H, n int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for engine.Waiting(h.Engine()) != n {
			if time.Now().After(deadline) {
				t.Fatalf("%d recv calls wait, want %d", engine.Waiting(h.Engine()), n)
			}
			time.Sleep(time.Millisecond)
		}
	}
	type outcome struct {
		res fakeagent.RecvResult
		err error
	}
	start := func(h *harness.H, a fakeagent.Agent, in fakeagent.RecvInput) <-chan outcome {
		ch := make(chan outcome, 1)
		go func() {
			res, err := a.Recv(h.Ctx(), withCLI(in))
			ch <- outcome{res, err}
		}()
		return ch
	}

	t.Run("a message that arrives is handed to the waiter", func(t *testing.T) {
		h := nodeWith(t, harness.Options{}, "a", "b")
		got := start(h, h.Agent("b"), fakeagent.RecvInput{WaitS: 30})
		waiting(t, h, 1)
		msg := send(t, h, "a", "b", "arrives while b waits")
		o := <-got
		if o.err != nil {
			t.Fatal(o.err)
		}
		wantOnly(t, o.res, msg)
		waiting(t, h, 0)
	})
	t.Run("the deadline ends the wait with an empty page", func(t *testing.T) {
		h := nodeWith(t, harness.Options{}, "a", "b")
		got := start(h, h.Agent("b"), fakeagent.RecvInput{WaitS: 10})
		waiting(t, h, 1)
		h.Advance(9 * time.Second)
		select {
		case <-got:
			t.Fatal("the wait ended before its deadline")
		case <-time.After(20 * time.Millisecond):
		}
		h.Advance(time.Second)
		o := <-got
		if o.err != nil || len(o.res.Page.Messages) != 0 || o.res.Page.Lease == "" {
			t.Fatalf("the wait ended with %+v, %v, want an empty page with a lease", o.res.Page, o.err)
		}
	})
	t.Run("a newer recv of the endpoint ends the older", func(t *testing.T) {
		h := nodeWith(t, harness.Options{}, "a", "b")
		first := start(h, h.Agent("b"), fakeagent.RecvInput{WaitS: 30})
		waiting(t, h, 1)
		second := start(h, h.Agent("b"), fakeagent.RecvInput{WaitS: 30})
		if o := <-first; o.err != nil || len(o.res.Page.Messages) != 0 {
			t.Fatalf("the older recv got %+v, %v, want an empty page", o.res.Page, o.err)
		}
		waiting(t, h, 1)
		msg := send(t, h, "a", "b", "for the newer")
		if o := <-second; o.err != nil {
			t.Fatal(o.err)
		} else {
			wantOnly(t, o.res, msg)
		}
	})
	t.Run("a waiter whose caller gave up is not handed a message", func(t *testing.T) {
		h := nodeWith(t, harness.Options{}, "a", "b")
		ctx, cancel := context.WithCancel(h.Ctx())
		ch := make(chan error, 1)
		go func() {
			_, err := h.Agent("b").Recv(ctx, withCLI(fakeagent.RecvInput{WaitS: 30}))
			ch <- err
		}()
		waiting(t, h, 1)
		cancel()
		if err := <-ch; err == nil {
			t.Fatal("a recv whose context ended returned a page")
		}
		waiting(t, h, 0)
		msg := send(t, h, "a", "b", "arrives after the caller left")
		quiet(t, h)
		wantOnly(t, recvAs(t, h, h.Agent("b"), fakeagent.RecvInput{}), msg)
	})
	t.Run("the wait is cut to what the front end of the adapter allows", func(t *testing.T) {
		h := sessionNode(t, harness.Options{}, "a")
		s, _ := newSession(t, h, "a")
		limit := cliRecvWait
		got := start(h, s, fakeagent.RecvInput{WaitS: 3600})
		waiting(t, h, 1)
		h.Advance(limit - time.Second)
		select {
		case <-got:
			t.Fatal("the wait ended before the limit of the front end")
		case <-time.After(20 * time.Millisecond):
		}
		h.Advance(time.Second)
		if o := <-got; o.err != nil || len(o.res.Page.Messages) != 0 {
			t.Fatalf("the wait ended with %+v, %v, want an empty page at the limit", o.res.Page, o.err)
		}
	})
}
