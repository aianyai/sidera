//go:build acceptance

package acceptance

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

func TestBinaryInstanceDeliveryResumesInterruptedBodyCreate(t *testing.T) {
	const seed = "TestCrashAtEveryCrashPointPreservesInvariants"
	scenarios, _ := bindCatalog()
	for _, scenario := range scenarios {
		if scenario.Name() != "management-instance" {
			continue
		}
		backend := NewWalkBackend(runRoot(t), testBinaries, Options{Seed: seed})
		target := crashwalk.Target{Gen: 1, Point: point(t, "deliver/body.create#1")}
		t.Logf("seed=%s scenario=%s target=%+v started=true", seed, scenario.Name(), target)
		result, err := backend.Run(t.Context(), scenario, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
		t.Logf("seed=%s scenario=%s target=%+v reached=%v generations=%d violations=%v error=%v", seed, scenario.Name(), target, result.Reached, len(result.Hits), result.Violations, err)
		checkRun(t, result, err, 2)
		if len(result.Reached) != 1 || !result.Reached[0] {
			t.Fatalf("actual target not reached: %v", result.Reached)
		}
		return
	}
	t.Fatal("management-instance recipe is missing")
}
