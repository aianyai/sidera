//go:build acceptance

package acceptance

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

func TestBinaryRefusedCreationResumesInterruptedControlIntake(t *testing.T) {
	const seed = "TestCrashAtEveryCrashPointPreservesInvariants"
	scenarios, _ := bindCatalog()
	for _, scenario := range scenarios {
		if scenario.Name() != "session-create-failure" {
			continue
		}
		backend := NewWalkBackend(runRoot(t), testBinaries, Options{Seed: seed})
		target := crashwalk.Target{Gen: 1, Point: point(t, "control-intake/derived.rename#1")}
		result, err := backend.Run(t.Context(), scenario, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
		t.Logf("seed=%s scenario=%s target=%+v reached=%v generations=%d", seed, scenario.Name(), target, result.Reached, len(result.Hits))
		checkRun(t, result, err, 2)
		return
	}
	t.Fatal("session-create-failure recipe is missing")
}
