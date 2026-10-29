package inproc

import (
	"context"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func TestRecoveryRecipesRequireActualOwnerEvidence(t *testing.T) {
	for _, test := range []struct {
		recipe    string
		trigger   crashwalk.Trigger
		omit      func(*recipes.DamageFacts)
		violation string
	}{
		{"recovery-source-trusted", crashwalk.CorruptOffline("source", "preserve-header"), func(f *recipes.DamageFacts) { f.OwnerAlerts = 0 }, "quarantined record has no owner alert"},
		{"recovery-creating", crashwalk.CorruptOffline("creating-control", "unreadable"), func(f *recipes.DamageFacts) { f.ControlUnknownAlerts = 0 }, "creating quarantine did not report control uncertainty"},
		{"recovery-body-feedback", crashwalk.CorruptOffline("source-body", "flip-body"), func(f *recipes.DamageFacts) { f.CorruptBodyAlerts = 0 }, "damaged source body has 0 corresponding owner alerts"},
	} {
		t.Run(test.recipe, func(t *testing.T) {
			state := &damageState{}
			bindings := bindingsWithRecovery(state)
			inspect := bindings.Recovery.InspectDamage
			bindings.Recovery.InspectDamage = func(e scenario.Env) (recipes.DamageFacts, error) {
				facts, err := inspect(e)
				test.omit(&facts)
				return facts, err
			}
			for _, recipe := range recipes.New(bindings).Scripts {
				if recipe.Script.Name != test.recipe {
					continue
				}
				bound := &harness.WalkScenario{Script: recipe.Script, Options: recipeOptions(recipe.Needs), Drain: func(h *harness.H) error { return recipe.Drain(h) }, Check: func(h *harness.H) error { return recipe.Check(h) }}
				configureRecoveryScenario(bound, recipe.Needs.Damage, state)
				result, err := harness.NewWalkBackend(t).Run(context.Background(), bound, crashwalk.RunPlan{Trigger: &test.trigger})
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Violations) != 1 || !strings.Contains(result.Violations[0], test.violation) {
					t.Fatalf("missing owner evidence was not rejected: %v", result.Violations)
				}
				return
			}
			t.Fatalf("recipe %s is absent", test.recipe)
		})
	}
}
