package daemon

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
)

// ClockState is the startup snapshot passed to a new engine. Once it starts,
// the engine alone owns the in-memory high water mark and rollback pause.
type ClockState struct {
	HighWater           clock.HighWater
	RollbackPaused      bool
	RollbackPausedSince time.Time
}

// RestoreClock loads the saved mark and pause and judges now before anything
// signs. A newly detected rollback is made durable before returning; a persisted
// pause remains until the owner clears it, even if now is back above the mark.
// reader and k must belong to the same node after transaction recovery, now must
// come from the injected clock, and log must be non-nil.
func RestoreClock(reader fsx.Reader, k *keys.Keys, now time.Time, log *slog.Logger) (ClockState, error) {
	st, err := keys.ReadClock(reader)
	if err != nil {
		return ClockState{}, err
	}
	hw := clock.NewHighWater(st.HighWater)
	paused, since := st.Paused(), st.RollbackPausedSince
	if v, _ := hw.Observe(now); v == clock.WallRolledBack {
		log.Warn("the wall clock was set back; new work that depends on a deadline is refused until the owner clears it",
			"now", now, "high_water", hw.Value())
		if !paused {
			paused, since = true, now
			if err := k.SaveClock(keys.ClockState{HighWater: hw.Value(), RollbackPausedSince: now}); err != nil {
				return ClockState{}, fmt.Errorf("making the pause durable: %w", err)
			}
		}
	}
	return ClockState{HighWater: hw, RollbackPaused: paused, RollbackPausedSince: since}, nil
}

// AddClockState registers the minute persistence task before the engine starts.
// The engine also runs it immediately when it detects rollback. Each invocation
// saves the engine's live state, including the owner's clear and a new pause.
func AddClockState(e *engine.Engine, k *keys.Keys) error {
	return e.AddClockState(clockStateTask, ClockStateEvery, func(in engine.TaskInput) error {
		st := keys.ClockState{HighWater: in.HighWater}
		if in.RollbackPaused {
			st.RollbackPausedSince = in.PausedSince
		}
		return k.SaveClock(st)
	})
}
