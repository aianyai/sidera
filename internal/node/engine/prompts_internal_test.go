package engine

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/handoff"
)

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
