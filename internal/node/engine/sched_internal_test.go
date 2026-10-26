package engine

import (
	"fmt"
	"testing"
	"time"
)

var base = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

func keys(ks []schedKey) string {
	s := ""
	for _, k := range ks {
		s += fmt.Sprintf("%d:%s ", k.kind, k.id)
	}
	return s
}

func TestSchedulerGivesBackWhatIsDueInTheOrderOfDueTimeThenOfSetting(t *testing.T) {
	s := newScheduler()
	a, b, c, d := schedKey{schedPeriodic, "a"}, schedKey{schedPeriodic, "b"}, schedKey{schedPeriodic, "c"}, schedKey{schedDrain, "d"}
	s.set(b, base.Add(time.Minute))
	s.set(a, base.Add(time.Minute))
	s.set(c, base.Add(30*time.Second))
	s.set(d, base.Add(time.Hour))

	if next, ok := s.next(); !ok || !next.Equal(base.Add(30*time.Second)) {
		t.Errorf("next = %v, %v; want the entry of c", next, ok)
	}
	if got := s.dueCount(base.Add(time.Minute)); got != 3 {
		t.Errorf("dueCount = %d, want 3", got)
	}
	if got := keys(s.takeDue(base.Add(10 * time.Second))); got != "" {
		t.Errorf("due before any time: %s", got)
	}
	// c is due first; b and a are due together and come in the order they were set.
	if got, want := keys(s.takeDue(base.Add(time.Minute))), keys([]schedKey{c, b, a}); got != want {
		t.Errorf("took %s, want %s", got, want)
	}
	if got := s.dueCount(base.Add(time.Minute)); got != 0 {
		t.Errorf("%d entries still due after they were taken", got)
	}
	if next, ok := s.next(); !ok || !next.Equal(base.Add(time.Hour)) {
		t.Errorf("next = %v, %v; want the entry of d", next, ok)
	}
}

func TestSchedulerHoldsOneEntryForAKeyAndSettingItAgainMovesItBehind(t *testing.T) {
	s := newScheduler()
	a, b := schedKey{schedPeriodic, "a"}, schedKey{schedPeriodic, "b"}
	s.set(a, base.Add(time.Minute))
	s.set(b, base.Add(time.Minute))
	s.set(a, base.Add(time.Minute)) // set again: a is behind b now
	if got, want := keys(s.takeDue(base.Add(time.Minute))), keys([]schedKey{b, a}); got != want {
		t.Errorf("took %s, want %s", got, want)
	}
	s.set(a, base.Add(time.Minute))
	s.set(a, base.Add(time.Hour)) // moved, not added
	if got := keys(s.takeDue(base.Add(2 * time.Minute))); got != "" {
		t.Errorf("a moved entry came out at its old time: %s", got)
	}
	s.remove(a)
	s.remove(a) // no entry: nothing happens
	if _, ok := s.next(); ok {
		t.Error("an entry is left after it was removed")
	}
}

func TestSchedulerTakesTheDueEntriesOfOneKindAndLeavesTheOthers(t *testing.T) {
	s := newScheduler()
	a, b, c := schedKey{schedTick, "a"}, schedKey{schedTick, "b"}, schedKey{schedTick, "c"}
	o, late := schedKey{schedOutbound, "o"}, schedKey{schedTick, "late"}
	s.set(b, base.Add(time.Minute))
	s.set(o, base.Add(time.Minute))
	s.set(a, base.Add(time.Minute))
	s.set(c, base.Add(30*time.Second))
	s.set(late, base.Add(time.Hour))
	if got, want := keys(s.takeDueOf(schedTick, base.Add(time.Minute))), keys([]schedKey{c, b, a}); got != want {
		t.Errorf("took %s, want %s: due time first, then the order of setting", got, want)
	}
	if got, want := keys(s.takeDue(base.Add(time.Minute))), keys([]schedKey{o}); got != want {
		t.Errorf("the entry of another kind is %s, want %s: it was not taken", got, want)
	}
	if next, ok := s.next(); !ok || !next.Equal(base.Add(time.Hour)) {
		t.Errorf("next = %v, %v; want the entry that was not due", next, ok)
	}
	if got := keys(s.takeDueOf(schedTick, base)); got != "" {
		t.Errorf("took %s before anything was due", got)
	}
}

func TestSchedulerHoldsTheEntriesOfTheRecoveryKindsAndGivesOutTheOthers(t *testing.T) {
	s := newScheduler()
	holding := true
	s.held = func() bool { return holding }
	tick, fb, out := schedKey{schedTick, "in/a"}, schedKey{schedFeedback, ""}, schedKey{schedOutbound, "o"}
	per, wait := schedKey{schedPeriodic, "p"}, schedKey{schedWait, "w"}
	s.set(tick, base)
	s.set(fb, base)
	s.set(out, base)
	s.set(wait, base.Add(time.Minute))
	s.set(per, base.Add(time.Hour))

	if next, ok := s.next(); !ok || !next.Equal(base.Add(time.Minute)) {
		t.Errorf("next = %v, %v while held; want the wait, which is not held", next, ok)
	}
	if got := s.dueCount(base.Add(time.Minute)); got != 1 {
		t.Errorf("dueCount = %d while held, want the one that is not held", got)
	}
	if got, want := keys(s.takeDue(base.Add(time.Minute))), keys([]schedKey{wait}); got != want {
		t.Errorf("took %s while held, want %s", got, want)
	}
	if got, want := keys(s.takeDueOf(schedTick, base)), keys([]schedKey{tick}); got != want {
		t.Errorf("the step took %s, want %s: it takes the entries of a held kind", got, want)
	}

	holding = false
	if got, want := keys(s.takeDue(base)), keys([]schedKey{fb, out}); got != want {
		t.Errorf("took %s once released, want %s: the held entries were kept", got, want)
	}

	holding = true
	s2 := newScheduler()
	s2.held = func() bool { return true }
	s2.set(tick, base)
	if next, ok := s2.next(); ok {
		t.Errorf("next = %v with only held entries, want none: the loop would wake for nothing", next)
	}
}
