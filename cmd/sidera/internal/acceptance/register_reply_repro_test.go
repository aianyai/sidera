//go:build acceptance && diagnostic

package acceptance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/txn"
)

func TestRegisterReplyExactTargetRepro(t *testing.T) {
	const seed = "TestCrashAtEveryCrashPointPreservesInvariants"
	scenarios, _ := bindCatalog()
	var selected crashwalk.Scenario
	for _, s := range scenarios {
		if s.Name() == "delivery-register-declare" {
			selected = s
			break
		}
	}
	if selected == nil {
		t.Fatal("delivery-register-declare recipe is missing")
	}
	target := crashwalk.Target{Gen: 1, Point: point(t, "clock-high-water/clock.rename#2")}
	t.Logf("REPRO input seed=%s scenario=%s steps=%v budget=%d target=%+v trigger=<nil>", seed, selected.Name(), selected.Steps(), selected.MaxPoints(), target)
	backend := NewWalkBackend(runRoot(t), testBinaries, Options{Seed: seed})
	table, err := daemon.Table()
	if err != nil {
		t.Fatal(err)
	}
	clean, cleanErr := backend.Run(t.Context(), selected, crashwalk.RunPlan{Record: true})
	t.Logf("REPRO result phase=record error=%q reached=%v generations=%d violations=%v", errorText(cleanErr), clean.Reached, len(clean.Hits), clean.Violations)
	if cleanErr != nil {
		t.Fatal(cleanErr)
	}
	if len(clean.Hits) != 1 || len(clean.Violations) != 0 {
		t.Fatalf("invalid clean recording: generations=%d violations=%v", len(clean.Hits), clean.Violations)
	}
	if err := crashwalk.CheckBudget(selected, clean.Hits[0]); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]txn.Kind{}
	for _, declaration := range table.Keys() {
		kinds[declaration.Key.String()] = declaration.Kind
	}
	for _, hit := range clean.Hits[0] {
		kind, declared := kinds[hit.Point.Key.String()]
		if !declared || kind == txn.Recovery {
			t.Fatalf("invalid clean declaration: point=%s declared=%t kind=%v", hit.Point, declared, kind)
		}
	}
	result, runErr := backend.Run(t.Context(), selected, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
	var comparison error
	if len(result.Hits) > 0 {
		comparison = crashwalk.CompareRuns(clean.Hits[0], result.Hits[0], target)
	} else {
		comparison = fmt.Errorf("no generation available for comparison")
	}
	diagnosticComparison(backend.root, selected.Name(), result, runErr, comparison)
	t.Logf("REPRO compare=%v", comparison)
	text := errorText(runErr) + strings.Join(result.Violations, "\n")
	symptom := strings.Contains(text, "creation has no real created reply:") && strings.Contains(text, "State:accepted") && strings.Contains(text, "ControlReply:<nil>")
	t.Logf("REPRO result phase=main exact_symptom=%t error=%q reached=%v generations=%d violations=%v", symptom, errorText(runErr), result.Reached, len(result.Hits), result.Violations)
	if runErr != nil {
		t.Fatal(runErr)
	}
	for _, generation := range result.Hits {
		for _, hit := range generation {
			if _, declared := kinds[hit.Point.Key.String()]; !declared {
				t.Fatalf("undeclared point: %s", hit.Point)
			}
		}
	}
	if len(result.Reached) != 1 || !result.Reached[0] || len(result.Hits) < 1 {
		t.Fatalf("target %s was not reached: reached=%v generations=%d", target.Point, result.Reached, len(result.Hits))
	}
	if len(result.Violations) != 0 {
		t.Fatalf("domain violations: %v", result.Violations)
	}
	if comparison != nil {
		t.Fatal(comparison)
	}
	t.Log("REPRO compare=PASS")
}

func errorText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
