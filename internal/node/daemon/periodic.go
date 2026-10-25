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

// clockTask makes the high water mark and the pause durable. The pause keeps the
// time it began, which is the first the node saw: a state with another start time
// is a different state, and the owner's clearing of it is the only thing that
// lifts it.
func (d *Daemon) clockTask(in engine.TaskInput) error {
	st := keys.ClockState{HighWater: in.HighWater}
	if in.RollbackPaused {
		if d.pausedSince.IsZero() {
			d.pausedSince = in.Now
		}
		st.RollbackPausedSince = d.pausedSince
	}
	return d.keys.SaveClock(st)
}
