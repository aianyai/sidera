//go:build acceptance

package acceptance

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

func TestBinaryRecoveryCreatingResumesClockInterruption(t *testing.T) {
	scenarios, _ := bindCatalog()
	for _, scenario := range scenarios {
		if scenario.Name() != "recovery-creating" {
			continue
		}
		backend := NewWalkBackend(runRoot(t), testBinaries, Options{Seed: "TestCrashAtEveryCrashPointPreservesInvariants"})
		target := crashwalk.Target{Gen: 1, Point: point(t, "clock-high-water/clock.rename#1")}
		result, err := backend.Run(t.Context(), scenario, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
		checkRun(t, result, err, 2)
		return
	}
	t.Fatal("recovery-creating recipe is missing")
}
