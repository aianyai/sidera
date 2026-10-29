package inproc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func TestBodyRetentionJudgementRejectsAnUnsweptSource(t *testing.T) {
	s := freshScenario(t, "recovery-body-archive").(*harness.WalkScenario)
	// Keep the actual periodic task beyond the drain's bounded retention window.
	s.Options.ArchiveSweepEvery = 365 * 24 * time.Hour
	trigger := crashwalk.CorruptOffline("source-body", "flip-body")
	r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Trigger: &trigger})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Violations) != 1 || !strings.Contains(r.Violations[0], "retained source body") {
		t.Fatalf("unswept source accepted: %v", r.Violations)
	}
}

// A send cut before returning has no public message identifier to remember, but
// its actual persisted source still has to finish through the bounded drain.
func TestBodyCleanupDrainsASendCutBeforeItsResult(t *testing.T) {
	for _, name := range []string{"recovery-body-feedback", "recovery-body-archive"} {
		t.Run(name, func(t *testing.T) {
			target := crashwalk.Target{Gen: 1, Point: fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "intake", Role: "out"}, Step: fsx.StepRename}, Hit: 1}}
			r, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, name), crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Reached) != 1 || !r.Reached[0] || len(r.Hits) != 2 || len(r.Violations) != 0 {
				t.Fatalf("send cut reached=%v generations=%d violations=%v", r.Reached, len(r.Hits), r.Violations)
			}
		})
	}
}
