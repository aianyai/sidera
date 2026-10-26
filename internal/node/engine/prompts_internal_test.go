package engine

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
)

// What waits for an agent is what a recv would hand something out of: a message never
// handed out, one a lease that ran out put back, and one handed out in parts of which
// some have not been; not one whose parts were all handed out and wait for the
// acknowledgement, and not a control signal or a message that is not accepted.
func TestWhatAwaitsAHandout(t *testing.T) {
	at := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	accepted := delivery.Record{Side: delivery.SideTarget, State: delivery.StateAccepted}
	with := func(f func(*delivery.Record)) delivery.Record { r := accepted; f(&r); return r }
	for _, c := range []struct {
		rule string
		rec  delivery.Record
		want bool
	}{
		{"a message never handed out", accepted, true},
		{"a message a lease that ran out put back", with(func(r *delivery.Record) {
			r.Lease = delivery.Lease{Chunks: 1, HandedOut: []bool{true}, Acked: []bool{false}, IssuedAt: at}
		}), true},
		{"a message handed out whole and leased", with(func(r *delivery.Record) {
			r.Lease = delivery.Lease{Active: true, Chunks: 1, HandedOut: []bool{true}, Acked: []bool{false}, IssuedAt: at, ExpiresAt: at.Add(time.Minute)}
		}), false},
		{"a message handed out in parts that has parts left", with(func(r *delivery.Record) {
			r.Lease = delivery.Lease{Active: true, Chunks: 3, HandedOut: []bool{true, false, false}, Acked: []bool{false, false, false}, IssuedAt: at, ExpiresAt: at.Add(time.Minute)}
		}), true},
		{"a control signal", with(func(r *delivery.Record) { r.Control = true }), false},
		{"a message that is no longer accepted", with(func(r *delivery.Record) { r.State = delivery.StateDelivered }), false},
	} {
		if got := awaitsHandout(c.rec); got != c.want {
			t.Errorf("%s: awaits a hand-out is %v, want %v", c.rule, got, c.want)
		}
	}
}

// The attempts of an endpoint keep what the prompt rules read: the hour of the cap,
// and the chain of repeats that begins at the newest delivered first prompt, however
// old. What is older than the hour and comes before that prompt is dropped.
func TestAttemptsKeepOnlyWhatTheRulesStillRead(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	at := func(minutes int) time.Time { return now.Add(-time.Duration(minutes) * time.Minute) }
	first := func(m int, o handoff.AttemptOutcome) handoff.Attempt {
		return handoff.Attempt{At: at(m), Outcome: o, Kind: handoff.PromptKindFirst}
	}
	re := func(m int) handoff.Attempt {
		return handoff.Attempt{At: at(m), Outcome: handoff.AttemptDelivered, Kind: handoff.PromptKindReprompt}
	}
	d, f := handoff.AttemptDelivered, handoff.AttemptFailed
	for _, c := range []struct {
		rule string
		in   []handoff.Attempt
		keep int // how many of the newest attempts remain
	}{
		{"nothing to drop in an hour", []handoff.Attempt{first(50, d), re(40), first(10, f)}, 3},
		{"older than the hour and before the newest delivered first prompt", []handoff.Attempt{first(300, d), re(290), first(200, d), re(100)}, 2},
		{"the chain that begins at a first prompt older than the hour is kept", []handoff.Attempt{first(300, d), first(200, d), re(190), first(100, f)}, 3},
		{"within the hour but before the newest first prompt", []handoff.Attempt{first(50, d), re(40), first(30, d)}, 3},
		{"no delivered first prompt: nothing is known to be unread", []handoff.Attempt{first(300, f), first(200, f)}, 2},
	} {
		got := trimAttempts(c.in, now)
		if len(got) != c.keep || got[len(got)-1] != c.in[len(c.in)-1] {
			t.Errorf("%s: %d attempts kept, want the newest %d", c.rule, len(got), c.keep)
		}
	}
}
