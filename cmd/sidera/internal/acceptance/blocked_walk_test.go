//go:build acceptance

package acceptance

import (
	"fmt"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func TestBinaryBlockedSessionResumesInterruptedAttempts(t *testing.T) {
	scenarios, _ := bindCatalog()
	for _, scenario := range scenarios {
		if scenario.Name() != "session-blocked-recovered" {
			continue
		}
		backend := NewWalkBackend(runRoot(t), testBinaries, Options{Seed: "TestCrashAtEveryCrashPointPreservesInvariants"})
		target := crashwalk.Target{Gen: 1, Point: point(t, "blocked/attempts.rename#1")}
		result, err := backend.Run(t.Context(), scenario, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
		checkRun(t, result, err, 2)
		return
	}
	t.Fatal("session-blocked-recovered recipe is missing")
}

func TestBinaryBlockedSessionInputsSurviveAdapterRestarts(t *testing.T) {
	scenarios, _ := bindCatalog()
	var selected crashwalk.Scenario
	for _, scenario := range scenarios {
		if scenario.Name() == "session-blocked-recovered" {
			selected = scenario
			break
		}
	}
	if selected == nil {
		t.Fatal("session-blocked-recovered recipe is missing")
	}
	backend := NewWalkBackend(runRoot(t), testBinaries, Options{Seed: "TestCrashAtEveryCrashPointPreservesInvariants"})
	first := crashwalk.Target{Gen: 1, Point: point(t, "blocked/attempts.rename#1")}
	baseline, err := backend.Run(t.Context(), selected, crashwalk.RunPlan{Targets: []crashwalk.Target{first}})
	checkRun(t, baseline, err, 2)
	var second crashwalk.Target
	for _, hit := range baseline.Hits[1] {
		if hit.Point.Tag.Tx == "blocked" && hit.Point.Step == fsx.StepRename {
			second = crashwalk.Target{Gen: 2, Point: hit.Point}
			break
		}
	}
	if second.Gen != 2 {
		t.Fatal("blocking drain recorded no real blocked rewrite")
	}
	t.Logf("second interruption: %s", second.Point)
	r, err := backend.Run(t.Context(), selected, crashwalk.RunPlan{Targets: []crashwalk.Target{first, second}})
	checkRun(t, r, err, 3)
	if err := crashwalk.CompareRuns(baseline.Hits[0], r.Hits[0], first); err != nil {
		t.Fatal(err)
	}
	r.Reached = r.Reached[1:]
	if err := crashwalk.CheckRecoveryTarget(baseline.Hits[1], second, r); err != nil {
		t.Fatal(err)
	}

	t.Run("cleared-inputs", func(t *testing.T) {
		var clearedScenario *WalkScenario
		for _, descriptor := range recipes.New(recipes.Bindings{}).Scripts {
			if descriptor.Script.Name != "session-blocked-recovered" {
				continue
			}
			clearedScenario = NewWalkScenario(descriptor, func(ops recipes.RecoveryOps) (recipes.ScriptRecipe, error) {
				b := bindingsWithRecovery(ops)
				actualSession := b.Sessions.Session
				b.Sessions.Session = func(e scenario.Env, endpoint string) fakeagent.Agent {
					if wrapped, ok := e.(clearedInputsEnv); ok {
						e = wrapped.Env
					}
					return actualSession(e, endpoint)
				}
				for _, recipe := range recipes.New(b).Scripts {
					if recipe.Script.Name == descriptor.Script.Name {
						actualDrain := recipe.Drain
						recipe.Drain = func(e scenario.Env) error { return actualDrain(clearedInputsEnv{e}) }
						return recipe, nil
					}
				}
				return recipes.ScriptRecipe{}, fmt.Errorf("session-blocked-recovered recipe is missing")
			})
			break
		}
		if clearedScenario == nil {
			t.Fatal("session-blocked-recovered recipe is missing")
		}
		r, err := backend.Run(t.Context(), clearedScenario, crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, AfterStep: "restore-credentials"}}})
		checkRun(t, r, err, 2)
	})
}

// The script completes restore-credentials before the selected kill. Its drain
// must deliver without a second empty declaration hiding stale startup inputs.
type clearedInputsEnv struct{ scenario.Env }

func (e clearedInputsEnv) SetAdapterInputs(instance string, inputs fakeadapter.ExternalInputs) error {
	if inputs.PromptRefusal == "" && len(inputs.Availability) == 0 {
		return nil
	}
	return e.Env.SetAdapterInputs(instance, inputs)
}
