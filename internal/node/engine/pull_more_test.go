package engine_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/node/adapterhost"
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

// A long message is cut for every front end its endpoint can be read through, whichever
// one asks: the adapter of the fixture declares a budget in characters for the command
// line and one in tokens for MCP, and a message read through both is cut in the same
// parts, so that the parts handed out through one are the ones acknowledged through the
// other and the message ends delivered.
func TestALongMessageIsCutTheSameThroughEveryFrontEnd(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	s, addr := newSession(t, h, "a")
	body := bigText("a message read through two front ends", 60*1024)
	msg := sendToAddr(t, h, "a", addr, body)
	quiet(t, h)

	through := func(front adapterproto.Frontend, in fakeagent.RecvInput) adapterproto.PageMessage {
		t.Helper()
		in.Frontend = front
		res, err := s.Recv(h.Ctx(), in)
		if err != nil {
			t.Fatalf("recv through %s: %v", front, err)
		}
		return wantPart(t, res, msg, max(in.Part, 1))
	}
	byCLI := through(adapterproto.FrontendCLI, fakeagent.RecvInput{Msg: msg, Part: 1})
	byMCP := through(adapterproto.FrontendMCP, fakeagent.RecvInput{Msg: msg, Part: 1})
	if byCLI.Part.Total < 2 || byCLI.Part.Total != byMCP.Part.Total || byCLI.Body != byMCP.Body {
		t.Fatalf("part 1 of %d through the command line and of %d through MCP, bodies %d and %d bytes, want the same cut",
			byCLI.Part.Total, byMCP.Part.Total, len(byCLI.Body), len(byMCP.Body))
	}

	// The message is read to its end, the front end changing with each page, and every
	// part is acknowledged normally.
	var got strings.Builder
	fronts := []adapterproto.Frontend{adapterproto.FrontendMCP, adapterproto.FrontendCLI}
	for i := 0; i < byCLI.Part.Total; i++ {
		res := recvAs(t, h, s, fakeagent.RecvInput{Frontend: fronts[i%2]})
		m := wantPart(t, res, msg, i+1)
		got.WriteString(m.Body)
		want := adapterproto.ItemRecorded
		if i == byCLI.Part.Total-1 {
			want = adapterproto.ItemCompleted
		}
		if acks := ackAs(t, h, s, res.Page.Lease); len(acks) != 1 || acks[0].Ack != want {
			t.Fatalf("the acknowledgement of part %d is %+v, want %s", i+1, acks, want)
		}
	}
	quiet(t, h)
	if got.String() != body {
		t.Errorf("the parts read add up to %d bytes, want the %d of the message", got.Len(), len(body))
	}
	if in := inOf(t, h, msg); in.Record.State != delivery.StateDelivered {
		t.Errorf("the message is %s, want delivered", in.Record.State)
	}
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

// An acknowledgement of a whole page, with no parts named, is what the agent sends
// when it has read the page. The node remembers what a page held for as long as a
// lease lives; after a restart or past that, it knows only that a message that fits
// whole handed out its one part. A part of a message that was cut cannot be told from
// the others there, and acknowledging one that was never shown would deliver a body
// nobody read, so it is left for the agent to name.
func TestAWholePageAcknowledgementAfterThePageIsForgottenAcknowledgesOnlyWhatFitsWhole(t *testing.T) {
	restart := func(t *testing.T, h *harness.H, _ fakeagent.Agent) { h.Restart(); quiet(t, h) }
	pastMemory := func(t *testing.T, h *harness.H, b fakeagent.Agent) {
		h.Advance(delivery.LeaseRenewalCap + time.Minute)
		quiet(t, h)
		// The next page the node remembers makes it forget the older ones.
		recvAs(t, h, b, fakeagent.RecvInput{})
	}
	for _, tc := range []struct {
		name   string
		bodies []string
		// forget makes the node forget the page the agent holds the lease of.
		forget func(t *testing.T, h *harness.H, b fakeagent.Agent)
		// completed is how many messages the acknowledgement completes: the ones that fit whole.
		completed int
	}{
		{"whole messages, after a restart", []string{"a short one", "another short one"}, restart, 2},
		{"whole messages, past the time a page is remembered", []string{"a short one", "another short one"}, pastMemory, 2},
		{"a message cut in parts, after a restart", []string{bigText("a message that is cut", 60*1024)}, restart, 0},
		{"a message cut in parts, past the time a page is remembered", []string{bigText("a message that is cut", 60*1024)}, pastMemory, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := nodeWith(t, harness.Options{}, "a", "b")
			b := h.Agent("b")
			var sent []adapterproto.MessageID
			for _, body := range tc.bodies {
				sent = append(sent, send(t, h, "a", "b", body))
				h.Advance(time.Second) // the queue is in the order of arrival
			}
			quiet(t, h)
			page := recvAs(t, h, b, fakeagent.RecvInput{})
			if got := pageIDs(page); !slices.Equal(got, sent) {
				t.Fatalf("the page holds %v, want %v", got, sent)
			}

			tc.forget(t, h, b)
			acks := ackAs(t, h, b, page.Page.Lease)
			quiet(t, h)

			if len(acks) != tc.completed {
				t.Fatalf("the acknowledgement is %+v, want %d parts acknowledged", acks, tc.completed)
			}
			for i, a := range acks {
				if a.MessageID != sent[i] || a.Part != 1 || a.Ack != adapterproto.ItemCompleted {
					t.Errorf("the acknowledgement %d is %+v, want part 1 of %s completed", i, a, sent[i])
				}
			}
			for _, id := range sent[tc.completed:] {
				if in := inOf(t, h, id); in.Record.State != delivery.StateAccepted || slices.Contains(in.Record.Lease.Acked, true) {
					t.Errorf("message %s is %s with parts acknowledged %v, want it accepted and no part acknowledged", id, in.Record.State, in.Record.Lease.Acked)
				}
			}
		})
	}
}

// A message that was handed out before in another number of parts than it is cut into now
// (the budgets the adapter declares changed) can never be handed out again, and it was
// handed out: the recv that finds it ends it as unknown at once. Left queued it would be
// prompted for at every turn end for as long as it lasted, and read by no one.
func TestAMessageCutDifferentlyThanItWasHandedOutIsClosedUnknownAndNotPromptedFor(t *testing.T) {
	host := &promptRecorder{hostDouble: newHostDouble()}
	session := mustEndpoint(t, 80)
	token := bytes.Repeat([]byte{0x5a}, store.TokenLen)
	r := newRig(t, 10, withAdapters(host))
	r.seedRecord(store.EndpointRecord{ID: session, Kind: sessionKind, Instance: "chat", Status: store.EndpointOpen,
		Token: token, Session: &store.SessionInfo{Preset: "default", NativeRef: "native-1"}})
	in := queuedIn(t, 1, session)
	in.Key.SenderNode = r.node
	in.Dedup = delivery.NewDedupRecord(in.Key.Dedup(), digestOf(1), in.Record.ReportBy)
	lease, err := delivery.NewLeaseID(bytes.NewReader(bytes.Repeat([]byte{7}, delivery.LeaseIDLen)))
	if err != nil {
		t.Fatal(err)
	}
	// The body is short, so it is one part now; it was handed out as the first of three.
	rec, _, err := in.Record.Handout(t0, delivery.AuthorizationAllow, 3, 1, lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.st.Deliver(bodyOf(1), []store.InRecord{in}); err != nil {
		t.Fatal(err)
	}
	in.Record = rec
	r.seedRecord(in)
	r.clk.Advance(delivery.LeaseDuration + time.Minute)

	e := r.start()
	if err := admitOn(ctx, e, "chat", runtimeAdmission(t, 0, adapterproto.FrontendCLI)); err != nil {
		t.Fatal(err)
	}
	if err := e.AdapterStarted(ctx, "chat", adapterproto.StartResult{Sessions: []adapterproto.SessionState{}}); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(engine.DefaultParams().RetryBackoff) // the probe of the adapter has had time to answer
	rigQuiet(t, e)
	waitUntil(t, func() bool { return host.prompts.Load() == 1 })

	page, err := e.Recv(ctx, engine.ByToken(adapterproto.Token(tokenText(token))), engine.RecvRequest{Frontend: adapterproto.FrontendCLI})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(page), "body of message 1") {
		t.Errorf("the page holds the message: %s", page)
	}
	rigQuiet(t, e)
	var got []delivery.State
	for _, rec := range r.scan().In {
		got = append(got, rec.Record.State)
	}
	if !slices.Equal(got, []delivery.State{delivery.StateDeliveryUnknown}) {
		t.Fatalf("the target records are %v, want the one message closed as delivery_unknown", got)
	}
	// The turn the prompt started ends with the message unread: an agent that left a
	// message waiting is woken once more, and one that has none is not.
	end := adapterhost.Notification{Method: adapterproto.MethodTurnEnded, Params: adapterproto.TurnEndedParams{Endpoint: adapterproto.EndpointRef(session.String())}}
	if err := e.AdapterNotified(ctx, "chat", end); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(handoff.DefaultMinInterval)
	rigQuiet(t, e)
	if n := host.prompts.Load(); n != 1 {
		t.Errorf("%d prompts reached the adapter, want only the first: nothing waits for the agent", n)
	}
}
