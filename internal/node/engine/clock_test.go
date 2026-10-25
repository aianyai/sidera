package engine_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/transport"
)

// encodeClear is the result of clearing the pause as the management interface words
// it: whether a pause was cleared, the mark the clock follows now, and the time the
// clear was made at.
func encodeClear(c engine.ClockClear) ([]byte, error) {
	return json.Marshal(struct {
		Cleared   bool   `json:"cleared"`
		HighWater string `json:"high_water"`
		Now       string `json:"now"`
	}{c.Cleared, c.HighWater.UTC().Format(time.RFC3339), c.Now.UTC().Format(time.RFC3339)})
}

func clearCommand(b byte, content string) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b), Fingerprint: fingerprintOf(content), Name: "clock.clear_rollback",
		Operation: engine.ClearClockRollbackOp(encodeClear),
	}
}

// A wall clock set back beyond the tolerance stops everything that signs or that
// depends on a deadline: a message to send is refused with back pressure, an
// envelope that arrives is not taken in, and no report is signed. Moving the clock
// forward again does not lift it, because the owner has not looked at the clock;
// only the owner's clear does, and it sets the mark to the clock as it reads now,
// in the file that survives the deletion of state/, so that work goes on and the
// pause is not back at the next start. What the node took in before the pause is
// untouched by it.
func TestClockRollbackPausesSigningUntilCleared(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	ctx := context.Background()
	before := send(t, h, "a", "b", "taken in before the clock was set back")
	quiet(t, h)
	h.Advance(time.Hour)
	send(t, h, "a", "b", "raises the mark to an hour on") // a command reads the clock and raises the mark
	quiet(t, h)
	mark := h.Clock().Now()

	h.Set(mark.Add(-30 * time.Minute)) // the wall clock was set back by half an hour
	refused := func(what string) {
		t.Helper()
		_, err := trySend(h, "a", "b", what, "")
		if fakeagent.CategoryOf(err) != fakeagent.CategoryBackpressure || fakeagent.CodeOf(err) != "clock_rollback" {
			t.Fatalf("send %q: %v; want backpressure/clock_rollback", what, err)
		}
	}
	refused("while the clock is behind the mark")
	_, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), addrOf(t, h, "b").Endpoint, forged(t, h, "m", addrOf(t, h, "a"), "", "x"))
	if reason, ok := engine.ReasonOf(err); !ok || reason != engine.ReasonClockRollback {
		t.Errorf("an envelope taken in while the clock is in doubt: %v; want the refusal for the clock", err)
	}
	if _, err := h.Loopback().Pull(h.Ctx(), harness.NodeID(), transport.PullRequest{Message: delivery.MessageID(before)}); err == nil {
		t.Error("a report was signed while the clock is in doubt")
	}

	h.Set(mark.Add(2 * time.Hour)) // ahead of the mark again: the owner has not cleared anything
	refused("after the clock moved on")
	if in := inOf(t, h, before); in.Record.State != delivery.StateAccepted {
		t.Errorf("the message taken in before the pause is %s, want it untouched", in.Record.State)
	}

	// The owner clears the pause.
	e := h.Engine()
	res, err := e.Execute(ctx, clearCommand(1, "clear"))
	if err != nil || res.Replayed || res.Stop {
		t.Fatalf("the clear: %+v, %v", res, err)
	}
	now := h.Clock().Now()
	if want := `{"cleared":true,"high_water":"` + now.UTC().Format(time.RFC3339) + `","now":"` + now.UTC().Format(time.RFC3339) + `"}`; string(res.Result) != want {
		t.Errorf("the result of the clear is %s, want %s", res.Result, want)
	}
	cleared := send(t, h, "a", "b", "after the owner cleared the pause")
	quiet(t, h)
	if in := inOf(t, h, cleared); in.Record.State != delivery.StateAccepted {
		t.Errorf("the message after the clear is %s, want accepted", in.Record.State)
	}
	if _, err := h.Loopback().Pull(h.Ctx(), harness.NodeID(), transport.PullRequest{Message: delivery.MessageID(before)}); err != nil {
		t.Errorf("a report is still not signed after the clear: %v", err)
	}

	// The mark on disk is the clock as it read at the clear, with no pause.
	st, err := keys.ReadClock(h.Disk())
	if err != nil || st.Paused() || !st.HighWater.Equal(now.Truncate(time.Second)) {
		t.Errorf("keys/clock.rec after the clear: %+v, %v; want no pause and the mark at %v", st, err, now)
	}

	// The same request again is the stored result; another request, with no pause
	// to clear, finds nothing to do and says so.
	again, err := e.Execute(ctx, clearCommand(1, "clear"))
	if err != nil || !again.Replayed || string(again.Result) != string(res.Result) {
		t.Errorf("the clear sent again: %+v, %v; want the stored result", again, err)
	}
	second, err := e.Execute(ctx, clearCommand(2, "clear, with another identifier"))
	if err != nil || second.Replayed {
		t.Fatalf("a second clear: %+v, %v", second, err)
	}
	var got struct {
		Cleared bool `json:"cleared"`
	}
	if err := json.Unmarshal(second.Result, &got); err != nil || got.Cleared {
		t.Errorf("a clear when no pause is in force says %s, want cleared false", second.Result)
	}
	send(t, h, "a", "b", "the clear that found nothing to clear changed nothing")
}

// A pause that is persisted holds from the start: the clear goes through the same
// door, so a node that was restarted while paused is cleared by the owner and by
// nothing else.
func TestARestartedNodeThatWasPausedIsClearedByTheOwnerAlone(t *testing.T) {
	h := nodeWith(t, harness.Options{RollbackPaused: true}, "a", "b")
	if _, err := trySend(h, "a", "b", "x", ""); fakeagent.CodeOf(err) != "clock_rollback" {
		t.Fatalf("send on a paused node: %v", err)
	}
	h.Restart()
	if _, err := trySend(h, "a", "b", "x", ""); fakeagent.CodeOf(err) != "clock_rollback" {
		t.Fatalf("send on a restarted paused node: %v", err)
	}
	if _, err := h.Engine().Execute(context.Background(), clearCommand(3, "clear")); err != nil {
		t.Fatal(err)
	}
	send(t, h, "a", "b", "after the clear")
}
