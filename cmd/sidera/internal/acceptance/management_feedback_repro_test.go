//go:build acceptance && diagnostic

package acceptance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

func TestBinaryManagementFeedbackRepro(t *testing.T) {
	const seed = "TestCrashAtEveryCrashPointPreservesInvariants"
	scenarios, _ := bindCatalog()
	for _, s := range scenarios {
		if s.Name() != "management-instance" {
			continue
		}
		backend := NewWalkBackend(runRoot(t), testBinaries, Options{Seed: seed})
		clean, err := backend.Run(t.Context(), s, crashwalk.RunPlan{Record: true})
		t.Logf("seed=%s scenario=%s clean_generations=%d clean_violations=%v clean_error=%v budget=%d steps=%v", seed, s.Name(), len(clean.Hits), clean.Violations, err, s.MaxPoints(), s.Steps())
		checkRun(t, clean, err, 1)
		if err := crashwalk.CheckBudget(s, clean.Hits[0]); err != nil {
			t.Fatal(err)
		}
		target := crashwalk.Target{Gen: 1, Point: point(t, "source-update/out.write#4")}
		result, runErr := backend.Run(t.Context(), s, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
		var comparison error
		if len(result.Hits) > 0 {
			comparison = crashwalk.CompareRuns(clean.Hits[0], result.Hits[0], target)
		} else {
			comparison = fmt.Errorf("no generation available for comparison")
		}
		diagnosticComparison(backend.root, s.Name(), result, runErr, comparison)
		t.Logf("seed=%s scenario=%s phase=main target=%+v trigger=<nil> actualReached=%v generations=%d oracle=%v original_error=%v compare=%v", seed, s.Name(), target, result.Reached, len(result.Hits), result.Violations, runErr, comparison)
		if runErr != nil {
			t.Fatal(runErr)
		}
		if len(result.Reached) != 1 || !result.Reached[0] {
			t.Fatalf("target was not reached: %v", result.Reached)
		}
		if len(result.Violations) > 0 {
			t.Fatalf("crashwalk: scenario %q killed at %s: %s", s.Name(), target.Point, strings.Join(result.Violations, "; "))
		}
		if comparison != nil {
			t.Fatal(comparison)
		}
		checkRun(t, result, runErr, 2)
		return
	}
	t.Fatal("management-instance recipe is missing")
}
