package inproc

import (
	"fmt"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func configureLifecycleStartup(s *harness.StartupWalkScenario) {
	if s.Name() != "startup-clock" {
		return
	}
	minimum := harness.DefaultStart.Add(s.StartAfter + s.AdvanceAfterStart)
	s.Check = func(r fsx.Reader) error {
		state, err := keys.ReadClock(r)
		if err != nil {
			return err
		}
		// Every successful generation advances once; a retry may persist a later
		// mark, but a completed run must retain at least its first due period.
		if state.HighWater.Before(minimum) || state.Paused() {
			return fmt.Errorf("persisted high water %s (paused=%v), want at least %s without a pause", state.HighWater, state.Paused(), minimum)
		}
		return nil
	}
}
