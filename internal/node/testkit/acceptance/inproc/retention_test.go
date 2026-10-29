package inproc

import (
	"context"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func TestRetentionJudgementRejectsAnArchiveThatWasNeverExpired(t *testing.T) {
	s := freshScenario(t, "maintenance-retention").(*harness.WalkScenario)
	for i := range s.Script.Steps {
		if s.Script.Steps[i].Name == "archive-retention" {
			s.Script.Steps[i].Do = func(scenario.Env) error { return nil }
		}
	}
	r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Violations) != 1 || !strings.Contains(r.Violations[0], "expired archive") {
		t.Fatalf("unexpired archive accepted: %v", r.Violations)
	}
}

func TestRetentionDrainCompletesARealCutBeforeAndDuringTheSweep(t *testing.T) {
	for _, step := range []string{"creation-pull", "send-from-session", "record-retention", "archive-retention"} {
		t.Run(step, func(t *testing.T) {
			s := freshScenario(t, "maintenance-retention")
			r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, AfterStep: step}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Reached) != 1 || !r.Reached[0] || len(r.Hits) != 2 || len(r.Violations) != 0 {
				t.Fatalf("retention cut reached=%v generations=%d violations=%v", r.Reached, len(r.Hits), r.Violations)
			}
		})
	}
}
