package daemon

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/engine"
)

// The pause of a clock set back keeps the time it began while it lasts, and is
// forgotten when the owner clears it: a pause that begins after that begins at its
// own time. A state that carried the time of an earlier pause would tell the next
// start that the node had been paused since then.
func TestThePauseOfTheClockStateBeginsAtItsOwnTimeAfterTheOwnerClearedAnEarlierOne(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 10, 24, 9, m, 0, 0, time.UTC) }
	var p pauseClock
	for _, step := range []struct {
		name      string
		in        engine.TaskInput
		wantSince time.Time
	}{
		{"no pause", engine.TaskInput{Now: at(1), HighWater: at(1)}, time.Time{}},
		{"the pause begins", engine.TaskInput{Now: at(2), HighWater: at(30), RollbackPaused: true}, at(2)},
		{"it keeps the time it began", engine.TaskInput{Now: at(3), HighWater: at(30), RollbackPaused: true}, at(2)},
		{"the owner cleared it", engine.TaskInput{Now: at(4), HighWater: at(4)}, time.Time{}},
		{"a later pause begins at its own time", engine.TaskInput{Now: at(5), HighWater: at(40), RollbackPaused: true}, at(5)},
	} {
		st := p.state(step.in)
		if !st.RollbackPausedSince.Equal(step.wantSince) || !st.HighWater.Equal(step.in.HighWater) {
			t.Errorf("%s: state = pause since %v, mark %v; want pause since %v, mark %v",
				step.name, st.RollbackPausedSince, st.HighWater, step.wantSince, step.in.HighWater)
		}
		if st.Paused() != !step.wantSince.IsZero() {
			t.Errorf("%s: Paused() = %v", step.name, st.Paused())
		}
	}
}
