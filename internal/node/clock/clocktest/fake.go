// Package clocktest is the fake clock that tests inject in place of
// [clock.System], so deadlines are crossed by calling Advance or Set and never
// by sleeping.
package clocktest

import (
	"slices"
	"sync"
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
)

// Fake is a [clock.Clock] whose time moves only when the test says so. It is
// safe for concurrent use. Hand a fake adapter the method value f.Now, so it
// reports times from the same clock as the code under test.
type Fake struct {
	mu    sync.Mutex
	now   time.Time
	seq   uint64
	armed []*fakeTimer
}

var _ clock.Clock = (*Fake)(nil)

// NewFake returns a fake clock reading start. start must not be the zero time.
// Like the real clock, the fake reports UTC with no monotonic reading, so start
// is converted.
func NewFake(start time.Time) *Fake {
	if start.IsZero() {
		panic("clocktest: NewFake needs a non-zero start time")
	}
	return &Fake{now: start.UTC()}
}

// Now returns the fake's current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// NewTimer returns a timer that fires when the fake reaches now+d. A timer with
// d <= 0 has already fired when NewTimer returns.
func (f *Fake) NewTimer(d time.Duration) clock.Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{f: f, c: make(chan time.Time, 1)}
	f.arm(t, f.now.Add(d))
	return t
}

// Advance moves the clock forward by d and fires every timer due at or before
// the new time. Values are sent in deadline order, equal deadlines in arming
// order, but each timer has its own channel, so receivers cannot observe that
// order: code and tests must not rely on which ready timer is read first. It
// panics when d is negative: going back is what Set is for.
func (f *Fake) Advance(d time.Duration) {
	if d < 0 {
		panic("clocktest: Advance needs a non-negative duration; use Set to move backward")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set(f.now.Add(d))
}

// Set moves the clock to the absolute time t. Moving forward fires due timers
// like Advance. Moving backward fires none and keeps every armed timer's
// remaining duration, which is how a test makes the wall clock roll back: timers
// measure elapsed time, so a wall clock set back does not move them, as with
// [clock.System]. The fake has one time axis, so the move forward that undoes a
// set-back is elapsed time like any other: setting back and then forward to the
// original instant fires the timers whose remaining duration has elapsed.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set(t.UTC())
}

// Timers returns the number of armed timers: those neither fired nor stopped.
// A test that starts a waiter reads it to wait until the waiter's timer exists
// before advancing.
func (f *Fake) Timers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.armed)
}

type fakeTimer struct {
	f        *Fake
	c        chan time.Time // capacity 1: a timer fires once per setting
	deadline time.Time
	seq      uint64 // arming order, which breaks ties between equal deadlines
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }

func (t *fakeTimer) Stop() bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	return t.f.disarm(t)
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	pending := t.f.disarm(t)
	t.f.arm(t, t.f.now.Add(d))
	return pending
}

func (t *fakeTimer) ResetAt(at time.Time) bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	pending := t.f.disarm(t)
	t.f.arm(t, at.UTC())
	return pending
}

// set moves the clock. A backward move shifts every armed timer's deadline by
// the same amount, so each keeps its remaining duration, and fires nothing; a
// forward move fires what became due. The caller holds f.mu.
func (f *Fake) set(t time.Time) {
	delta := t.Sub(f.now)
	f.now = t
	if delta < 0 {
		for _, armed := range f.armed {
			armed.deadline = armed.deadline.Add(delta)
		}
		return
	}
	for _, due := range f.takeDue(t) {
		// The channel is empty: a timer is armed only after its channel was
		// drained, so this never blocks.
		due.c <- due.deadline
	}
}

// arm starts t's next setting, due at deadline. The caller holds f.mu and t is
// not armed and its channel is empty.
func (f *Fake) arm(t *fakeTimer, deadline time.Time) {
	t.deadline = deadline
	if !deadline.After(f.now) {
		t.c <- f.now
		return
	}
	f.seq++
	t.seq = f.seq
	f.armed = append(f.armed, t)
}

// disarm cancels t's pending setting and reports whether there was one: the
// timer was armed, or it fired and its value was not received, in which case
// the value is discarded. The caller holds f.mu.
func (f *Fake) disarm(t *fakeTimer) bool {
	if i := slices.Index(f.armed, t); i >= 0 {
		f.armed = slices.Delete(f.armed, i, i+1)
		return true
	}
	select {
	case <-t.c:
		return true
	default:
		return false
	}
}

// takeDue removes and returns the armed timers due at or before t, earliest
// deadline first and equal deadlines in arming order. The caller holds f.mu.
func (f *Fake) takeDue(t time.Time) []*fakeTimer {
	var due []*fakeTimer
	f.armed = slices.DeleteFunc(f.armed, func(x *fakeTimer) bool {
		if x.deadline.After(t) {
			return false
		}
		due = append(due, x)
		return true
	})
	slices.SortFunc(due, func(a, b *fakeTimer) int {
		if c := a.deadline.Compare(b.deadline); c != 0 {
			return c
		}
		switch {
		case a.seq < b.seq:
			return -1
		case a.seq > b.seq:
			return 1
		}
		return 0
	})
	return due
}
