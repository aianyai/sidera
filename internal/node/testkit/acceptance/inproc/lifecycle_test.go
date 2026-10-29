package inproc

import (
	"context"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func TestClockRecipeRejectsAStartThatNeverPersistsTheAdvancedClock(t *testing.T) {
	s := freshScenario(t, "startup-clock").(*harness.StartupWalkScenario)
	s.AdvanceAfterStart = 0
	r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Violations) != 1 || !strings.Contains(r.Violations[0], "persisted high water") {
		t.Fatalf("unadvanced clock accepted: %v", r.Violations)
	}
}
