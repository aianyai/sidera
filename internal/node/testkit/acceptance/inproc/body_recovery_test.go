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

func TestBodyRetentionPreparationSurvivesAnotherCrash(t *testing.T) {
	const name = "recovery-body-archive"
	trigger := crashwalk.CorruptOffline("source-body", "flip-body")
	clean, err := recordScenario(t, freshScenario(t, name))
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, name), crashwalk.RunPlan{Trigger: &trigger})
	if err != nil {
		t.Fatal(err)
	}
	assertDeclaredWrites(t, baseline)
	if err := crashwalk.CheckRecoveryRecord(clean.Hits[0], trigger, baseline); err != nil {
		t.Fatal(err)
	}
	// These early writes interrupt the prepared process before it closes its
	// sender. Startup may replay its feedback before the next drain resumes.
	for _, pointName := range []string{
		"clock-high-water/clock.create#1",
		"clock-high-water/clock.write#1",
		"clock-high-water/clock.rename#1",
		"clock-high-water/commit#1",
	} {
		t.Run(pointName, func(t *testing.T) {
			point, err := fsx.ParsePoint(pointName)
			if err != nil {
				t.Fatal(err)
			}
			target := crashwalk.Target{Gen: 2, Point: point}
			r, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, name), crashwalk.RunPlan{Trigger: &trigger, Targets: []crashwalk.Target{target}})
			if err != nil {
				t.Fatal(err)
			}
			assertDeclaredWrites(t, r)
			if err := crashwalk.CheckRecoveryTrigger(clean.Hits[0], trigger, r); err != nil {
				t.Fatal(err)
			}
			if err := crashwalk.CheckRecoveryTarget(baseline.Hits[1], target, r); err != nil {
				t.Fatal(err)
			}
		})
	}
}
