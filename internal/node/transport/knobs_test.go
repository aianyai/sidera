package transport_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/record"
)

// waitFor polls until cond holds. It exists for the one thing a test cannot
// order by itself: a call that has been started in another goroutine reaching
// the point where it waits.
func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestKnobs has one row per fault knob and one row for no knob at all. Every
// row asserts the effect of the knob and, through wantHits, that the knob was
// hit as often as the row says and that no other knob was.
func TestKnobs(t *testing.T) {
	cases := []struct {
		rule string
		run  func(t *testing.T, r *rig)
		hits map[transport.Knob]int
	}{
		{
			rule: "with no knob armed a send reaches the target once, from a local origin, and nothing is hit",
			run: func(t *testing.T, r *rig) {
				rc, err := r.send(r.message(t, "m1", "hello"))
				if err != nil {
					t.Fatalf("send: %v", err)
				}
				if got := reportIDs(t, rc.Reports); !sameIDs(got, []string{"m1"}) {
					t.Errorf("receipt reports %v, want [m1]", got)
				}
				calls := r.target.inbound()
				if len(calls) != 1 {
					t.Fatalf("target saw %d envelopes, want 1", len(calls))
				}
				if !calls[0].from.Local() || calls[0].from.Node() != r.self {
					t.Errorf("origin local=%v node=%v, want local origin of this node", calls[0].from.Local(), calls[0].from.Node())
				}
				if calls[0].sender != ep(9) || string(calls[0].env.Body) != "hello" {
					t.Errorf("target saw sender %v body %q", calls[0].sender, calls[0].env.Body)
				}
				ans, err := r.pull("m1")
				if err != nil || !sameIDs(reportIDs(t, ans.Reports), []string{"m1"}) {
					t.Errorf("pull: reports %v, err %v", ans.Reports, err)
				}
			},
			hits: nil,
		},
		{
			rule: "DropReceipt: the target has processed the envelope, the sender hears a timeout, and the knob spends one receipt per hit",
			run: func(t *testing.T, r *rig) {
				r.knobs.DropReceipt(2)
				for i := 1; i <= 2; i++ {
					rc, err := r.send(r.message(t, "m1", "hello"))
					if !errors.Is(err, transport.ErrTimeout) {
						t.Fatalf("send %d: error %v, want ErrTimeout", i, err)
					}
					if len(rc.Reports) != 0 || rc.ControlReply != nil {
						t.Errorf("send %d returned a receipt with its error", i)
					}
					if n := len(r.target.inbound()); n != i {
						t.Errorf("after send %d the target has processed %d envelopes, want %d", i, n, i)
					}
				}
				rc, err := r.send(r.message(t, "m1", "hello"))
				if err != nil || !sameIDs(reportIDs(t, rc.Reports), []string{"m1"}) {
					t.Errorf("third send: reports %v, err %v, want the receipt once the two are spent", rc.Reports, err)
				}
				drop := transport.Hit{Knob: transport.KnobDropReceipt, Call: transport.CallSend, Node: r.self}
				r.wantObserved(t, drop, drop)
			},
			hits: map[transport.Knob]int{transport.KnobDropReceipt: 2},
		},
		{
			rule: "DropReceipt: a target that refuses to answer has no receipt to drop, so its error passes and the knob keeps its count",
			run: func(t *testing.T, r *rig) {
				r.knobs.DropReceipt(1)
				refused := errors.New("target cannot sign now")
				r.target.failNext(refused)
				if _, err := r.send(r.message(t, "m1", "hello")); !errors.Is(err, refused) {
					t.Fatalf("send: error %v, want the target's own", err)
				}
				if got := r.knobs.Hits(transport.KnobDropReceipt); got != 0 {
					t.Fatalf("a refused envelope counted %d hits of DropReceipt", got)
				}
				if _, err := r.send(r.message(t, "m1", "hello")); !errors.Is(err, transport.ErrTimeout) {
					t.Fatalf("second send: error %v, want ErrTimeout from the knob that was kept", err)
				}
				r.wantObserved(t, transport.Hit{Knob: transport.KnobDropReceipt, Call: transport.CallSend, Node: r.self})
			},
			hits: map[transport.Knob]int{transport.KnobDropReceipt: 1},
		},
		{
			rule: "Unreachable: neither a send nor a pull reaches the target while it is on, and both do once it is off",
			run: func(t *testing.T, r *rig) {
				r.knobs.Unreachable(r.self, true)
				if _, err := r.send(r.message(t, "m1", "hello")); !errors.Is(err, transport.ErrUnreachable) {
					t.Fatalf("send: error %v, want ErrUnreachable", err)
				}
				if _, err := r.pull("m1"); !errors.Is(err, transport.ErrUnreachable) {
					t.Fatalf("pull: error %v, want ErrUnreachable", err)
				}
				if n, p := len(r.target.inbound()), r.target.pulled(); n != 0 || p != 0 {
					t.Fatalf("the target was called %d times for envelopes and %d for pulls while unreachable", n, p)
				}
				r.knobs.Unreachable(r.self, false)
				if _, err := r.send(r.message(t, "m1", "hello")); err != nil {
					t.Fatalf("send after the knob was turned off: %v", err)
				}
				if _, err := r.pull("m1"); err != nil {
					t.Fatalf("pull after the knob was turned off: %v", err)
				}
				r.wantObserved(t,
					transport.Hit{Knob: transport.KnobUnreachable, Call: transport.CallSend, Node: r.self},
					transport.Hit{Knob: transport.KnobUnreachable, Call: transport.CallPull, Node: r.self})
			},
			hits: map[transport.Knob]int{transport.KnobUnreachable: 2},
		},
		{
			rule: "a loopback reaches only its own node: another node is unreachable without any knob",
			run: func(t *testing.T, r *rig) {
				if _, err := r.loop.Send(context.Background(), r.other, ep(9), r.message(t, "m1", "hello")); !errors.Is(err, transport.ErrUnreachable) {
					t.Fatalf("send to another node: error %v, want ErrUnreachable", err)
				}
				if _, err := r.loop.Pull(context.Background(), r.other, transport.PullRequest{Message: "m1", Recipients: pulled}); !errors.Is(err, transport.ErrUnreachable) {
					t.Fatalf("pull from another node: error %v, want ErrUnreachable", err)
				}
				if n, p := len(r.target.inbound()), r.target.pulled(); n != 0 || p != 0 {
					t.Fatalf("the target was called %d and %d times for another node", n, p)
				}
			},
			hits: nil,
		},
		{
			rule: "Unreachable names a node: turning it on for another node leaves this node reachable",
			run: func(t *testing.T, r *rig) {
				r.knobs.Unreachable(r.other, true)
				if _, err := r.send(r.message(t, "m1", "hello")); err != nil {
					t.Fatalf("send to this node: %v", err)
				}
			},
			hits: nil,
		},
		{
			rule: "HoldReports: the held message is processed, but neither the receipt of its send nor a pull for it gives a report; others are unaffected; Release gives it back",
			run: func(t *testing.T, r *rig) {
				r.knobs.HoldReports("m1")
				rc, err := r.send(r.message(t, "m1", "hello"))
				if err != nil {
					t.Fatalf("send of the held message: %v", err)
				}
				if len(rc.Reports) != 0 || rc.ControlReply != nil {
					t.Errorf("the receipt of the held message carries %d reports", len(rc.Reports))
				}
				if n := len(r.target.inbound()); n != 1 {
					t.Fatalf("the target processed %d envelopes, want the held one", n)
				}
				ans, err := r.pull("m1")
				if err != nil || len(ans.Reports) != 0 || ans.ControlReply != nil {
					t.Errorf("pull of the held message: reports %d, err %v, want an empty answer", len(ans.Reports), err)
				}
				if p := r.target.pulled(); p != 0 {
					t.Errorf("the held pull reached the target %d times", p)
				}
				rc, err = r.send(r.message(t, "m2", "other"))
				if err != nil || !sameIDs(reportIDs(t, rc.Reports), []string{"m2"}) {
					t.Errorf("another message: reports %v, err %v, want its own report", rc.Reports, err)
				}
				ans, err = r.pull("m2")
				if err != nil || !sameIDs(reportIDs(t, ans.Reports), []string{"m2"}) {
					t.Errorf("pull of another message: reports %v, err %v", ans.Reports, err)
				}
				r.knobs.Release("m1")
				ans, err = r.pull("m1")
				if err != nil || !sameIDs(reportIDs(t, ans.Reports), []string{"m1"}) {
					t.Errorf("pull after Release: reports %v, err %v, want the report back", ans.Reports, err)
				}
				r.wantObserved(t,
					transport.Hit{Knob: transport.KnobHoldReports, Call: transport.CallSend, Node: r.self, Message: "m1"},
					transport.Hit{Knob: transport.KnobHoldReports, Call: transport.CallPull, Node: r.self, Message: "m1"})
			},
			hits: map[transport.Knob]int{transport.KnobHoldReports: 2},
		},
		{
			rule: "HoldReports: the control reply is held with the reports, in the receipt of the send and in every pull, and comes back with Release",
			run: func(t *testing.T, r *rig) {
				r.target.replyWith(record.Record{Type: "test/control-reply", Payload: []byte("c1")})
				r.knobs.HoldReports("c1")
				rc, err := r.send(r.status(t, "c1"))
				if err != nil || len(rc.Reports) != 0 || rc.ControlReply != nil {
					t.Errorf("send of the held control signal: %d reports, reply %v, err %v, want an empty receipt", len(rc.Reports), rc.ControlReply, err)
				}
				ans, err := r.pull("c1")
				if err != nil || len(ans.Reports) != 0 || ans.ControlReply != nil {
					t.Errorf("pull of the held control signal: %d reports, reply %v, err %v, want an empty answer", len(ans.Reports), ans.ControlReply, err)
				}
				r.knobs.Release("c1")
				ans, err = r.pull("c1")
				if err != nil || ans.ControlReply == nil || ans.ControlReply.Type != "test/control-reply" {
					t.Errorf("pull after Release: reply %v, err %v, want the control reply back", ans.ControlReply, err)
				}
			},
			hits: map[transport.Knob]int{transport.KnobHoldReports: 2},
		},
		{
			rule: "Delay: the call waits on the injected clock without reaching the target, is counted as waiting only until its time is due, and then goes through",
			run: func(t *testing.T, r *rig) {
				const d = 5 * time.Minute
				r.knobs.Delay(d)
				type result struct {
					rc  transport.Receipt
					err error
				}
				done := make(chan result, 1)
				go func() {
					rc, err := r.send(r.message(t, "m1", "hello"))
					done <- result{rc, err}
				}()
				waitFor(t, "the call to wait out its delay", func() bool { return r.knobs.Waiting() == 1 })
				r.clk.Advance(d - time.Second)
				if n := r.knobs.Waiting(); n != 1 {
					t.Errorf("Waiting() = %d one second before the delay is due, want 1", n)
				}
				select {
				case res := <-done:
					t.Fatalf("the call returned before its delay was due: %v", res.err)
				default:
				}
				if n := len(r.target.inbound()); n != 0 {
					t.Fatalf("the target saw %d envelopes before the delay was due", n)
				}
				r.clk.Advance(time.Second)
				if n := r.knobs.Waiting(); n != 0 {
					t.Errorf("Waiting() = %d once the delay is due, want 0: the call is work again, not a wait", n)
				}
				select {
				case res := <-done:
					if res.err != nil || !sameIDs(reportIDs(t, res.rc.Reports), []string{"m1"}) {
						t.Errorf("delayed send: reports %v, err %v", res.rc.Reports, res.err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("the call did not return after its delay was due")
				}
				if n := len(r.target.inbound()); n != 1 {
					t.Errorf("the target saw %d envelopes, want 1", n)
				}
				r.wantObserved(t, transport.Hit{Knob: transport.KnobDelay, Call: transport.CallSend, Node: r.self})
			},
			hits: map[transport.Knob]int{transport.KnobDelay: 1},
		},
		{
			rule: "Delay: a call whose context ends while it waits stops waiting, leaves no timer, and never reaches the target",
			run: func(t *testing.T, r *rig) {
				r.knobs.Delay(time.Hour)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					_, err := r.loop.Pull(ctx, r.self, transport.PullRequest{Message: "m1", Recipients: pulled})
					done <- err
				}()
				waitFor(t, "the pull to wait out its delay", func() bool { return r.knobs.Waiting() == 1 })
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Errorf("error %v, want context.Canceled", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("the call did not stop waiting when its context ended")
				}
				if n, w, p := r.clk.Timers(), r.knobs.Waiting(), r.target.pulled(); n != 0 || w != 0 || p != 0 {
					t.Errorf("after the cancel: %d timers, %d waiting, %d pulls reached the target; want none", n, w, p)
				}
				r.wantObserved(t, transport.Hit{Knob: transport.KnobDelay, Call: transport.CallPull, Node: r.self})
			},
			hits: map[transport.Knob]int{transport.KnobDelay: 1},
		},
		{
			rule: "Mutate: once, after the send of the message, the target is handed the same id and sender with another body; the sender's own receipt is the original's",
			run: func(t *testing.T, r *rig) {
				r.knobs.Mutate("m1")
				rc, err := r.send(r.message(t, "m1", "hello"))
				if err != nil || !sameIDs(reportIDs(t, rc.Reports), []string{"m1"}) {
					t.Fatalf("send: reports %v, err %v, want the original's receipt", rc.Reports, err)
				}
				calls := r.target.inbound()
				if len(calls) != 2 {
					t.Fatalf("target saw %d envelopes, want the original and one replay", len(calls))
				}
				orig, replay := calls[0], calls[1]
				if replay.env.ID != orig.env.ID || replay.sender != orig.sender || !replay.from.Local() {
					t.Errorf("replay is id %q from %v local=%v, want the original's id and sender from a local origin", replay.env.ID, replay.sender, replay.from.Local())
				}
				if string(replay.env.Body) == string(orig.env.Body) || replay.env.Digest() == orig.env.Digest() {
					t.Errorf("replay body %q has the original's content", replay.env.Body)
				}
				if replay.env.Kind != orig.env.Kind || len(replay.env.To) != len(orig.env.To) || !replay.env.AcceptBefore.Equal(orig.env.AcceptBefore) {
					t.Errorf("replay differs from the original in more than its content: %+v", replay.env)
				}
				got, ok := r.knobs.Mutated("m1")
				if !ok || !errors.Is(got.Err, errConflict) {
					t.Errorf("Mutated: ok=%v err=%v, want the target's conflict kept for the test", ok, got.Err)
				}
				// The knob is spent: a send of the same message again replays nothing.
				if _, err := r.send(r.message(t, "m1", "hello")); err != nil {
					t.Fatalf("second send: %v", err)
				}
				if n := len(r.target.inbound()); n != 3 {
					t.Errorf("target saw %d envelopes after a second send, want 3 (no second replay)", n)
				}
				r.wantObserved(t, transport.Hit{Knob: transport.KnobMutate, Call: transport.CallSend, Node: r.self, Message: "m1"})
			},
			hits: map[transport.Knob]int{transport.KnobMutate: 1},
		},
		{
			rule: "Mutate: an envelope without a body is changed in its thread, so the replay still differs in content",
			run: func(t *testing.T, r *rig) {
				r.knobs.Mutate("s1")
				if _, err := r.send(r.status(t, "s1")); err != nil {
					t.Fatalf("send: %v", err)
				}
				calls := r.target.inbound()
				if len(calls) != 2 {
					t.Fatalf("target saw %d envelopes, want 2", len(calls))
				}
				if calls[1].env.Digest() == calls[0].env.Digest() {
					t.Errorf("the replay of a bodyless envelope has the original's content")
				}
				r.wantObserved(t, transport.Hit{Knob: transport.KnobMutate, Call: transport.CallSend, Node: r.self, Message: "s1"})
			},
			hits: map[transport.Knob]int{transport.KnobMutate: 1},
		},
		{
			rule: "Mutate waits for a failed send: it is not spent on an envelope the target did not take",
			run: func(t *testing.T, r *rig) {
				r.knobs.Mutate("m1")
				r.target.failNext(errors.New("target cannot sign now"))
				if _, err := r.send(r.message(t, "m1", "hello")); err == nil {
					t.Fatal("send of a refused envelope succeeded")
				}
				if n := len(r.target.inbound()); n != 1 {
					t.Fatalf("target saw %d envelopes after a refusal, want 1: nothing is replayed", n)
				}
				if _, err := r.send(r.message(t, "m1", "hello")); err != nil {
					t.Fatalf("second send: %v", err)
				}
				if n := len(r.target.inbound()); n != 3 {
					t.Fatalf("target saw %d envelopes, want the refused one, the retry and the replay", n)
				}
				r.wantObserved(t, transport.Hit{Knob: transport.KnobMutate, Call: transport.CallSend, Node: r.self, Message: "m1"})
			},
			hits: map[transport.Knob]int{transport.KnobMutate: 1},
		},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			r := newRig(t)
			c.run(t, r)
			r.wantHits(t, c.hits)
		})
	}
}

// TestKnobsAreNotSharedBetweenLoopbacks pins that a knob belongs to the Knobs
// value it was set on: two nodes in one process do not fault each other.
func TestKnobsAreNotSharedBetweenLoopbacks(t *testing.T) {
	a, b := newRig(t), newRig(t)
	a.knobs.Unreachable(a.self, true)
	if _, err := b.send(b.message(t, "m1", "hello")); err != nil {
		t.Fatalf("send on the other loopback: %v", err)
	}
	if got := b.knobs.Hits(transport.KnobUnreachable); got != 0 {
		t.Errorf("the other loopback counted %d hits", got)
	}
}

// TestNilKnobsMeansNoFaults is the constructor's contract: no Knobs, no fault.
func TestNilKnobsMeansNoFaults(t *testing.T) {
	g := newTarget()
	self := nodeOf(t, 1)
	l := transport.NewLoopback(self, g, nil)
	rg := &rig{self: self, target: g}
	rc, err := l.Send(context.Background(), self, ep(9), rg.message(t, "m1", "hello"))
	if err != nil || !sameIDs(reportIDs(t, rc.Reports), []string{"m1"}) {
		t.Fatalf("send: reports %v, err %v", rc.Reports, err)
	}
}

// TestKnobsAreSafeToDriveWhileCallsRun runs sends and pulls while another
// goroutine turns every knob on and off. The race detector is the assertion;
// the results are not judged because the knobs change under them.
func TestKnobsAreSafeToDriveWhileCallsRun(t *testing.T) {
	r := newRig(t)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			r.knobs.Unreachable(r.self, i%2 == 0)
			r.knobs.DropReceipt(1)
			r.knobs.HoldReports("m1")
			r.knobs.Release("m1")
			r.knobs.Mutate("m1")
			_ = r.knobs.Hits(transport.KnobDropReceipt)
			_ = r.knobs.Waiting()
		}
	}()
	for i := 0; i < 200; i++ {
		_, _ = r.send(r.message(t, "m1", "hello"))
		_, _ = r.pull("m1")
	}
	close(stop)
	wg.Wait()
}

// TestKnobNamesAreDistinct keeps the names the observer logs from colliding.
func TestKnobNamesAreDistinct(t *testing.T) {
	seen := map[string]transport.Knob{}
	for _, k := range allKnobs {
		name := k.String()
		if name == "" || strings.HasPrefix(name, "invalid") {
			t.Errorf("knob %d has no name", k)
		}
		if prev, dup := seen[name]; dup {
			t.Errorf("knobs %d and %d share the name %q", prev, k, name)
		}
		seen[name] = k
	}
}

// TestMutateKeepsTheThreadValidAtItsLongestLength pins how the replay of an
// envelope without a body differs from it: the last character of the thread is
// replaced, and an envelope with no thread gets one, so the replay is always an
// envelope the target can decode, at the longest thread too.
func TestMutateKeepsTheThreadValidAtItsLongestLength(t *testing.T) {
	longest := strings.Repeat("a", envelope.MaxIDLen)
	for name, thread := range map[string]string{
		"no thread":                           "",
		"a thread of one character":           "t",
		"a thread of the longest length":      longest,
		"a long thread that ends in m":        longest[:len(longest)-1] + "m",
		"a thread of one character that is m": "m",
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.knobs.Mutate("s1")
			if _, err := r.send(r.statusInThread(t, "s1", thread)); err != nil {
				t.Fatalf("send: %v", err)
			}
			calls := r.target.inbound()
			if len(calls) != 2 {
				t.Fatalf("target saw %d envelopes, want the original and its replay", len(calls))
			}
			got := calls[1].env.Thread
			if got == thread || got == "" || len(got) > envelope.MaxIDLen {
				t.Fatalf("replay thread %q of %d bytes, want a valid one that differs from %q", got, len(got), thread)
			}
			if thread != "" && (len(got) != len(thread) || got[:len(got)-1] != thread[:len(thread)-1]) {
				t.Errorf("replay thread %q differs from %q in more than its last character", got, thread)
			}
			if rep, ok := r.knobs.Mutated("s1"); !ok || !errors.Is(rep.Err, errConflict) {
				t.Errorf("Mutated: ok=%v err=%v, want the target's conflict: the replay was built and judged", ok, rep.Err)
			}
			r.wantHits(t, map[transport.Knob]int{transport.KnobMutate: 1})
		})
	}
}

// TestWaitingFailsLoudlyWhenTheClockIsSetBack pins that a count of waiting calls
// is never read against a clock that has gone below the moment one of them began:
// the due times mean nothing then, and a count that went on would hide the test's
// own mistake.
func TestWaitingFailsLoudlyWhenTheClockIsSetBack(t *testing.T) {
	r := newRig(t)
	r.knobs.Delay(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := r.loop.Pull(ctx, r.self, transport.PullRequest{Message: "m1", Recipients: pulled})
		done <- err
	}()
	waitFor(t, "the pull to wait out its delay", func() bool { return r.knobs.Waiting() == 1 })
	r.clk.Set(t0.Add(-time.Minute))
	func() {
		defer func() {
			if recover() == nil {
				t.Error("Waiting() returned with the clock set back below the start of a wait")
			}
		}()
		r.knobs.Waiting()
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("pull: error %v, want context.Canceled", err)
	}
	if n := r.knobs.Waiting(); n != 0 {
		t.Errorf("Waiting() = %d after the call ended, want 0", n)
	}
}

// TestDelayZeroLeavesACallThatIsAlreadyWaiting pins that Delay(0) is for the
// calls that start afterwards: a call already waiting keeps waiting, and a call
// that starts after it goes through without a wait.
func TestDelayZeroLeavesACallThatIsAlreadyWaiting(t *testing.T) {
	r := newRig(t)
	r.knobs.Delay(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := r.loop.Pull(ctx, r.self, transport.PullRequest{Message: "m1", Recipients: pulled})
		done <- err
	}()
	waitFor(t, "the pull to wait out its delay", func() bool { return r.knobs.Waiting() == 1 })
	r.knobs.Delay(0)
	if _, err := r.send(r.message(t, "m1", "hello")); err != nil {
		t.Fatalf("send after Delay(0): %v", err)
	}
	if n := r.knobs.Waiting(); n != 1 {
		t.Errorf("Waiting() = %d after Delay(0), want the call that was already waiting", n)
	}
	select {
	case err := <-done:
		t.Fatalf("the waiting call returned after Delay(0): %v", err)
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("pull: error %v, want context.Canceled", err)
	}
	r.wantHits(t, map[transport.Knob]int{transport.KnobDelay: 1})
}

// TestCallKindNames keeps the call a hit names readable in a ledger and an
// unknown one visible.
func TestCallKindNames(t *testing.T) {
	for kind, want := range map[transport.CallKind]string{
		transport.CallSend:     "send",
		transport.CallPull:     "pull",
		transport.CallKind(0):  "invalid(0)",
		transport.CallKind(77): "invalid(77)",
	} {
		if got := kind.String(); got != want {
			t.Errorf("CallKind(%d).String() = %q, want %q", uint8(kind), got, want)
		}
	}
	if got := transport.Knob(0).String(); got != "invalid(0)" {
		t.Errorf("Knob(0).String() = %q, want %q", got, "invalid(0)")
	}
}
