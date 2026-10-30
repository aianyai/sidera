//go:build acceptance

package acceptance

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

func TestBinaryInstanceClockCutPreservesRecordedTrace(t *testing.T) {
	const seed = "TestCrashAtEveryCrashPointPreservesInvariants"
	scenarios, _ := bindCatalog()
	for _, scenario := range scenarios {
		if scenario.Name() != "management-instance" {
			continue
		}
		backend := NewWalkBackend(runRoot(t), testBinaries, Options{Seed: seed})
		clean, err := backend.Run(t.Context(), scenario, crashwalk.RunPlan{Record: true})
		t.Logf("seed=%s scenario=%s clean_generations=%d clean_violations=%v", seed, scenario.Name(), len(clean.Hits), clean.Violations)
		checkRun(t, clean, err, 1)
		if err := crashwalk.CheckBudget(scenario, clean.Hits[0]); err != nil {
			t.Fatal(err)
		}
		target := crashwalk.Target{Gen: 1, Point: point(t, "clock-high-water/clock.rename#1")}
		result, err := backend.Run(t.Context(), scenario, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
		t.Logf("seed=%s scenario=%s target=%+v reached=%v generations=%d violations=%v", seed, scenario.Name(), target, result.Reached, len(result.Hits), result.Violations)
		checkRun(t, result, err, 2)
		for _, recording := range []struct {
			name string
			hits []crashwalk.Hit
		}{{"recorded", clean.Hits[0]}, {"rerun", result.Hits[0]}} {
			for _, hit := range recording.hits {
				if hit.Point == target.Point || hit.Point == point(t, "control-intake/ctl.rename#1") {
					t.Logf("%s gen=%d setup=%v point=%s path=%q from=%q", recording.name, hit.Gen, hit.Setup, hit.Point, hit.Path, hit.From)
				}
			}
		}
		if err := crashwalk.CompareRuns(clean.Hits[0], result.Hits[0], target); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("management-instance recipe is missing")
}
