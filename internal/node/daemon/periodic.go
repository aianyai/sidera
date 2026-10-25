package daemon

import (
	"time"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/keys"
)

// ClockStateEvery is how often the high water mark of the wall clock is made
// durable: a crash loses at most this much of it, which is harmless because the
// next start only trusts the clock less, never more.
const ClockStateEvery = time.Minute

// PeriodicTask is a task the engine runs every Every, the first time one period
// after the start. It runs in the loop, on the injected clock: it may write
// through the transaction layer and must not wait, and a task that has long work to do
// does it outside the loop, because the longest stall of the loop is what the
// notifications of an adapter have to outlast. Names are unique.
type PeriodicTask struct {
	Name  string
	Every time.Duration
	Run   engine.Task
}

const clockStateTask = "clock-high-water"

// pauseClock remembers when the pause of a clock set back began. It is read and
// written only in the loop (the clock task) and, before the loop exists, by the
// start.
type pauseClock struct{ since time.Time }

// state is the clock state to make durable for what the loop reports. A pause
// keeps the time it began, which is the first the node saw: a state with another
// start time is a different state. When the owner clears the pause the time is
// forgotten, so that a pause that comes later begins at its own time and not at
// the one of a pause that is over.
func (p *pauseClock) state(in engine.TaskInput) keys.ClockState {
	st := keys.ClockState{HighWater: in.HighWater}
	if !in.RollbackPaused {
		p.since = time.Time{}
		return st
	}
	if p.since.IsZero() {
		p.since = in.Now
	}
	st.RollbackPausedSince = p.since
	return st
}

// clockTask makes the high water mark and the pause durable. The owner's clearing
// of the pause is the only thing that lifts it.
func (d *Daemon) clockTask(in engine.TaskInput) error {
	return d.keys.SaveClock(d.pause.state(in))
}
