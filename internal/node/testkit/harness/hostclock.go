package harness

import (
	"sync"
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
)

// hostClock is the test clock as the adapter host sees it. It tells the host's
// timers apart from the engine's and the knobs', which share the one fake clock and
// are counted together by its Timers: a test that must move the clock to the moment
// the host's backoff fires needs to know that the host has armed that timer, and
// only the host's timers can say so.
//
// It keeps the remaining time of each timer the host armed. A timer is armed while
// its remaining time is above zero: the fake clock fires a timer on the move that
// reaches it, so a timer at zero has fired, and one that was stopped is forgotten.
type hostClock struct {
	clock.Clock

	mu     sync.Mutex
	timers map[*hostTimer]time.Time // the clock reading each armed timer fires at
}

func newHostClock(c clock.Clock) *hostClock {
	return &hostClock{Clock: c, timers: map[*hostTimer]time.Time{}}
}

func (c *hostClock) NewTimer(d time.Duration) clock.Timer {
	t := &hostTimer{c: c, Timer: c.Clock.NewTimer(d)}
	c.arm(t, c.Now().Add(d))
	return t
}

func (c *hostClock) arm(t *hostTimer, due time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timers[t] = due
}

func (c *hostClock) forget(t *hostTimer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.timers, t)
}

// setBack keeps the remaining time of every timer when the clock is set back by d:
// the fake clock fires none on a backward move and keeps each timer's remaining
// duration, so each moves back with it.
func (c *hostClock) setBack(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for t, due := range c.timers {
		c.timers[t] = due.Add(-d)
	}
}

// nextDue is the time from now until the earliest of the host's armed timers fires.
func (c *hostClock) nextDue() (time.Duration, bool) {
	now := c.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	var next time.Duration
	var found bool
	for t, due := range c.timers {
		left := due.Sub(now)
		if left <= 0 {
			delete(c.timers, t) // it fired
			continue
		}
		if !found || left < next {
			next, found = left, true
		}
	}
	return next, found
}

type hostTimer struct {
	clock.Timer
	c *hostClock
}

func (t *hostTimer) Stop() bool {
	t.c.forget(t)
	return t.Timer.Stop()
}

func (t *hostTimer) Reset(d time.Duration) bool {
	t.c.arm(t, t.c.Now().Add(d))
	return t.Timer.Reset(d)
}

func (t *hostTimer) ResetAt(at time.Time) bool {
	t.c.arm(t, at.UTC())
	return t.Timer.ResetAt(at)
}
