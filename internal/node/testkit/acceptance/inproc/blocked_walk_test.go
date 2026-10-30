package inproc

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func TestBlockedSessionInputsSurviveAdapterRestarts(t *testing.T) {
	const name = "session-blocked-recovered"
	backend := harness.NewWalkBackend(t)
	point, err := fsx.ParsePoint("blocked/attempts.rename#1")
	if err != nil {
		t.Fatal(err)
	}
	first := crashwalk.Target{Gen: 1, Point: point}
	baseline, err := backend.Run(t.Context(), freshScenario(t, name), crashwalk.RunPlan{Targets: []crashwalk.Target{first}})
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Hits) != 2 || len(baseline.Reached) != 1 || !baseline.Reached[0] || len(baseline.Violations) != 0 {
		t.Fatalf("blocking baseline: generations=%d reached=%v violations=%v", len(baseline.Hits), baseline.Reached, baseline.Violations)
	}
	assertDeclaredWrites(t, baseline)
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
	r, err := backend.Run(t.Context(), freshScenario(t, name), crashwalk.RunPlan{Targets: []crashwalk.Target{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	assertDeclaredWrites(t, r)
	if len(r.Reached) != 2 || !r.Reached[0] {
		t.Fatalf("blocking cuts reached=%v", r.Reached)
	}
	if err := crashwalk.CompareRuns(baseline.Hits[0], r.Hits[0], first); err != nil {
		t.Fatal(err)
	}
	r.Reached = r.Reached[1:]
	if err := crashwalk.CheckRecoveryTarget(baseline.Hits[1], second, r); err != nil {
		t.Fatal(err)
	}
}

func TestBlockedSessionClearedInputsStayClearedAfterRestart(t *testing.T) {
	b := newBindings()
	actualSession := b.Sessions.Session
	b.Sessions.Session = func(e scenario.Env, endpoint string) fakeagent.Agent {
		if wrapped, ok := e.(clearedInputsEnv); ok {
			e = wrapped.Env
		}
		return actualSession(e, endpoint)
	}
	var selected *harness.WalkScenario
	for _, recipe := range recipes.New(b).Scripts {
		if recipe.Script.Name == "session-blocked-recovered" {
			selected = &harness.WalkScenario{Script: recipe.Script, Options: recipeOptions(recipe.Needs), Drain: func(h *harness.H) error {
				return recipe.Drain(clearedInputsEnv{h})
			}, Check: func(h *harness.H) error { return recipe.Check(h) }}
			break
		}
	}
	if selected == nil {
		t.Fatal("session-blocked-recovered recipe is missing")
	}
	r, err := harness.NewWalkBackend(t).Run(t.Context(), selected, crashwalk.RunPlan{Targets: []crashwalk.Target{{Gen: 1, AfterStep: "restore-credentials"}}})
	if err != nil {
		t.Fatal(err)
	}
	assertDeclaredWrites(t, r)
	if len(r.Hits) != 2 || len(r.Reached) != 1 || !r.Reached[0] || len(r.Violations) != 0 {
		t.Fatalf("cleared-input restart: generations=%d reached=%v violations=%v", len(r.Hits), r.Reached, r.Violations)
	}
}

// Only the drain after the successful restore-credentials boundary uses this
// wrapper. Reapplying its empty declaration could hide stale startup inputs.
type clearedInputsEnv struct{ scenario.Env }

func (e clearedInputsEnv) SetAdapterInputs(instance string, inputs fakeadapter.ExternalInputs) error {
	if inputs.PromptRefusal == "" && len(inputs.Availability) == 0 {
		return nil
	}
	return e.Env.SetAdapterInputs(instance, inputs)
}
