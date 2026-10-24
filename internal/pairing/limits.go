package pairing

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// ErrBudget reports a reveal that the budget does not allow.
var ErrBudget = errors.New("reveal budget exhausted")

// Limits are the two sliding-window counters of a node, kept across restarts:
// when it revealed its nonce to an initiator it did not expect, and when an
// owner reported that the codes differ. They are node-wide, not per peer: a
// man in the middle can use as many identities as it likes, so a count per
// identity would bound nothing.
//
// A window is sliding: an entry counts while it is less than the window old
// and stops counting at exactly the window. Limits is a value; every change
// returns a new one and the caller persists it before it acts on the answer.
type Limits struct {
	reveals  []time.Time
	failures []time.Time
}

// NewLimits restores limits from persisted entries, which must be whole
// seconds in ascending order.
func NewLimits(reveals, failures []time.Time) (Limits, error) {
	for name, ts := range map[string][]time.Time{"reveals": reveals, "failures": failures} {
		for i, t := range ts {
			if t.IsZero() || !t.Equal(time.Unix(t.Unix(), 0)) {
				return Limits{}, fmt.Errorf("limits: %s[%d] is not a whole-second time", name, i)
			}
			if i > 0 && t.Before(ts[i-1]) {
				return Limits{}, fmt.Errorf("limits: %s are not in ascending order", name)
			}
		}
	}
	return Limits{reveals: utcAll(reveals), failures: utcAll(failures)}, nil
}

func utcAll(ts []time.Time) []time.Time {
	out := make([]time.Time, len(ts))
	for i, t := range ts {
		out[i] = t.UTC()
	}
	return out
}

// Reveals returns the times of the reveals still kept.
func (l Limits) Reveals() []time.Time { return slices.Clone(l.reveals) }

// Failures returns the times of the failures still kept.
func (l Limits) Failures() []time.Time { return slices.Clone(l.failures) }

// inWindow keeps the entries that still count at now.
func inWindow(ts []time.Time, window time.Duration, now time.Time) []time.Time {
	edge := now.Add(-window)
	i := 0
	for i < len(ts) && !ts[i].After(edge) {
		i++
	}
	return ts[i:]
}

// retryHint turns the instant a counter frees into a hint relative to now:
// whole seconds, rounded up. An entry that still counts is less than a window
// old, so free is after now and the hint is at least one second, which matters
// because a zero hint is not a hint.
func retryHint(free, now time.Time) time.Duration {
	return (free.Sub(now) + time.Second - 1) / time.Second * time.Second
}

// RevealAllowed reports whether the target may reveal its nonce to an
// initiator it does not expect now, and if not, when the earliest slot frees.
func (l Limits) RevealAllowed(p Params, now time.Time) (bool, time.Duration) {
	live := inWindow(l.reveals, p.RevealWindow, now)
	if len(live) < p.RevealBudget {
		return true, 0
	}
	return false, retryHint(live[len(live)-p.RevealBudget].Add(p.RevealWindow), now)
}

// SpendReveal records a reveal at now. It fails with ErrBudget when the budget
// is spent; the caller persists the result before the reveal leaves the node,
// so a crash can never hand out a reveal that was not counted.
func (l Limits) SpendReveal(p Params, now time.Time) (Limits, error) {
	if ok, _ := l.RevealAllowed(p, now); !ok {
		return l, ErrBudget
	}
	live := inWindow(l.reveals, p.RevealWindow, now)
	out := Limits{failures: l.failures, reveals: append(slices.Clone(live), now)}
	return out, nil
}

// RecordFailure records that an owner reported differing codes at now. Only the
// most recent FailureLimit failures are kept: the pause depends on no others,
// and the list stays bounded.
func (l Limits) RecordFailure(p Params, now time.Time) Limits {
	live := inWindow(l.failures, p.FailureWindow, now)
	live = append(slices.Clone(live), now)
	if len(live) > p.FailureLimit {
		live = live[len(live)-p.FailureLimit:]
	}
	return Limits{reveals: l.reveals, failures: live}
}

// Paused reports whether pairing with nodes that are not expected is paused at
// now: FailureLimit failures within the window. The pause lifts by itself when
// the earliest failure that holds the count at the limit slides out; the
// second result is how long that takes. There is no manual release.
func (l Limits) Paused(p Params, now time.Time) (bool, time.Duration) {
	live := inWindow(l.failures, p.FailureWindow, now)
	if len(live) < p.FailureLimit {
		return false, 0
	}
	return true, retryHint(live[len(live)-p.FailureLimit].Add(p.FailureWindow), now)
}
