package handoff

import (
	"slices"
	"testing"
	"time"
)

// The attempts of an endpoint keep what the rules still read: every attempt of the
// hour of the cap, the newest delivered first prompt with the delivered repeats that
// follow it, however old, and the earliest failure of the failing run that blocked
// delivery is measured from. What is older than the hour and is none of these is
// dropped.
func TestTrimAttemptsKeepsOnlyWhatTheRulesStillRead(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	ago := func(m int, o AttemptOutcome, k PromptKind) Attempt {
		return Attempt{At: now.Add(-time.Duration(m) * time.Minute), Outcome: o, Kind: k}
	}
	first := func(m int, o AttemptOutcome) Attempt { return ago(m, o, PromptKindFirst) }
	re := func(m int) Attempt { return ago(m, AttemptDelivered, PromptKindReprompt) }
	d, f, u := AttemptDelivered, AttemptFailed, AttemptUnknown
	for _, c := range []struct {
		rule string
		in   []Attempt
		want []Attempt
	}{
		{"nothing to drop in an hour",
			[]Attempt{first(50, d), re(40), first(10, f)},
			[]Attempt{first(50, d), re(40), first(10, f)}},
		{"delivered attempts older than the hour and before the newest delivered first prompt go",
			[]Attempt{first(300, d), re(290), first(200, d), re(100)},
			[]Attempt{first(200, d), re(100)}},
		{"the chain that begins at a first prompt older than the hour is kept",
			[]Attempt{first(300, d), first(200, d), re(190), first(100, f)},
			[]Attempt{first(200, d), re(190), first(100, f)}},
		{"within the hour but before the newest first prompt",
			[]Attempt{first(50, d), re(40), first(30, d)},
			[]Attempt{first(50, d), re(40), first(30, d)}},
		{"failures and unknowns older than the hour go when no failing run is current",
			[]Attempt{first(300, f), first(200, u), first(100, d)},
			[]Attempt{first(100, d)}},
		{"failures older than the hour that precede the newest delivered prompt go",
			[]Attempt{first(300, f), first(200, d), re(100)},
			[]Attempt{first(200, d), re(100)}},
		{"only the earliest failure of the current failing run is kept when it is older than the hour",
			[]Attempt{first(300, d), first(200, f), first(190, u), first(180, f), first(10, f)},
			[]Attempt{first(300, d), first(200, f), first(10, f)}},
		{"a failing run with no delivered prompt before it keeps its earliest failure",
			[]Attempt{first(300, f), first(200, f)},
			[]Attempt{first(300, f)}},
		{"an unknown does not end the failing run",
			[]Attempt{first(300, f), first(250, u), first(200, f)},
			[]Attempt{first(300, f)}},
		{"no attempts", nil, nil},
	} {
		in := slices.Clone(c.in)
		got := TrimAttempts(c.in, now)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got %v\nwant %v", c.rule, got, c.want)
		}
		if !slices.Equal(c.in, in) {
			t.Errorf("%s: the input was modified", c.rule)
		}
	}
}
