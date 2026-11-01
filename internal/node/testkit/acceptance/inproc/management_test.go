package inproc

import (
	"context"
	"errors"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func TestManagementRejectsASuccessWithoutTheRequestedSetting(t *testing.T) {
	b := newBindings()
	actual := b.Management.NodeSet
	b.Management.NodeSet = func(e scenario.Env, r recipes.ManagementRequest, patch string) (recipes.ManagementOutcome, error) {
		return actual(e, r, `{"node_queue_limit":10000}`)
	}
	for _, r := range recipes.New(b).Scripts {
		if r.Script.Name != "management-settings" {
			continue
		}
		_, err := harness.NewWalkBackend(t).Run(context.Background(), &harness.WalkScenario{Script: r.Script}, crashwalk.RunPlan{Record: true})
		if !errors.Is(err, recipes.ErrManagementEffect) {
			t.Fatalf("wrong setting reported success: %v", err)
		}
		return
	}
	t.Fatal("management recipe missing")
}

func TestManagementMaintenanceWalksEveryMainPoint(t *testing.T) {
	const name = "management-maintenance"
	result, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, name), crashwalk.RunPlan{Record: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || len(result.Violations) != 0 {
		t.Fatalf("record generations=%d violations=%v", len(result.Hits), result.Violations)
	}
	points := crashwalk.Plan(result.Hits[0], crashwalk.Full, nil)
	t.Logf("maintenance points=%d budget=%d", len(points), freshScenario(t, name).MaxPoints())
	for _, point := range points {
		t.Run(point.Point.String(), func(t *testing.T) {
			target := point
			r, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, name), crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
			if err != nil {
				t.Fatal(err)
			}
			if err := crashwalk.CompareRuns(result.Hits[0], r.Hits[0], target); err != nil {
				t.Fatal(err)
			}
			if len(r.Hits) != 2 || len(r.Reached) != 1 || !r.Reached[0] || len(r.Violations) != 0 {
				t.Fatalf("cut reached=%v generations=%d violations=%v", r.Reached, len(r.Hits), r.Violations)
			}
		})
	}
}
