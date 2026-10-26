package engine_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/engine"
)

// movingClock is the test clock whose timers run a hook, once, at the first
// arming after the test sets pending: the time between the loop reading the
// clock and arming the timer, made as long as the test wants.
type movingClock struct {
	clock.Clock
	pending atomic.Bool
	hook    func()
}

func (c *movingClock) runHook() {
	if c.pending.CompareAndSwap(true, false) {
		c.hook()
	}
}

func (c *movingClock) NewTimer(d time.Duration) clock.Timer {
	return &movingTimer{Timer: c.Clock.NewTimer(d), c: c}
}

type movingTimer struct {
	clock.Timer
	c *movingClock
}

func (t *movingTimer) Reset(d time.Duration) bool {
	t.c.runHook()
	return t.Timer.Reset(d)
}

func (t *movingTimer) ResetAt(at time.Time) bool {
	t.c.runHook()
	return t.Timer.ResetAt(at)
}

// The clock moves between the loop's decision to arm the timer for the earliest
// entry and the arming itself, past that entry. The timer must still wake the
// loop for it: a wait counted from a reading that went stale would be longer by
// the distance the clock moved, and the entry would wait for that long.
func TestAClockThatMovesWhileTheTimerIsArmedStillWakesTheLoopForADueEntry(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	mc := &movingClock{Clock: r.clk, hook: func() { r.clk.Advance(10 * time.Minute) }}
	r.engineClock = mc
	rs := newRuns()
	e := openWith(t, r, func(e *engine.Engine) {
		if err := e.AddPeriodic("tick", time.Minute, rs.task("tick", nil)); err != nil {
			t.Fatal(err)
		}
	})
	settle(t, e)
	// The loop arms its timer after each command. Arm the hook, then run one
	// command to reach it; from then on nothing but the timer may wake the loop,
	// because a command reads the clock itself and would run the due entry.
	mc.pending.Store(true)
	settle(t, e)
	deadline := time.Now().Add(10 * time.Second)
	for rs.count("tick") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the timer did not wake the loop for an entry the clock passed while the timer was being armed")
		}
		time.Sleep(time.Millisecond)
	}
}
