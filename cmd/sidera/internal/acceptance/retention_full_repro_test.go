//go:build acceptance

package acceptance

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

func TestBinaryRetentionResumesInterruptedIntakeCommit(t *testing.T) {
	const seed = "TestCrashAtEveryCrashPointPreservesInvariants"
	scenarios, _ := bindCatalog()
	for _, scenario := range scenarios {
		if scenario.Name() != "maintenance-retention" {
			continue
		}
		for _, at := range []string{"intake/commit#1", "intake/commit#3"} {
			t.Run(at, func(t *testing.T) {
				backend := NewWalkBackend(runRoot(t), testBinaries, Options{Seed: seed})
				target := crashwalk.Target{Gen: 1, Point: point(t, at)}
				result, err := backend.Run(t.Context(), scenario, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
				t.Logf("seed=%s scenario=%s target=%+v reached=%v generations=%d violations=%v", seed, scenario.Name(), target, result.Reached, len(result.Hits), result.Violations)
				checkRun(t, result, err, 2)
			})
		}
		return
	}
	t.Fatal("maintenance-retention recipe is missing")
}
