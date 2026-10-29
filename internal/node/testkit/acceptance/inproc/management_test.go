package inproc

import (
	"context"
	"errors"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
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

func TestManagementMaintenanceUsesRealVersionWarning(t *testing.T) {
	const name = "management-maintenance"
	result, err := harness.NewWalkBackend(t).Run(context.Background(), versionWarningScenario(t), crashwalk.RunPlan{Record: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || len(result.Violations) != 0 {
		t.Fatalf("record generations=%d violations=%v", len(result.Hits), result.Violations)
	}
	points := crashwalk.Plan(result.Hits[0], crashwalk.Full, nil)
	t.Logf("real warning maintenance points=%d budget=%d", len(points), freshScenario(t, name).MaxPoints())
	raised, acknowledged := false, false
	for _, hit := range result.Hits[0] {
		if hit.Point.Key.Tag.Tx == "alert-raise" && !hit.Setup {
			raised = true
		}
		if hit.Point.Key.Tag.Tx == "alert-ack" && !hit.Setup {
			acknowledged = true
		}
	}
	if !raised || !acknowledged {
		t.Fatalf("alert writes missing from recipe window: raised=%v acknowledged=%v", raised, acknowledged)
	}
	for _, point := range points {
		switch point.Point.Key.Tag.Tx {
		case "adapter-enable", "runtime-admit", "alert-raise", "alert-ack":
		default:
			continue
		}
		t.Run(point.Point.String(), func(t *testing.T) {
			scenario := versionWarningScenario(t)
			target := point
			r, err := harness.NewWalkBackend(t).Run(context.Background(), scenario, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
			if err != nil {
				t.Fatal(err)
			}
			if err := crashwalk.CompareRuns(result.Hits[0], r.Hits[0], target); err != nil {
				t.Fatal(err)
			}
			if len(r.Hits) != 2 || len(r.Reached) != 1 || !r.Reached[0] || len(r.Violations) != 0 {
				t.Fatalf("warning cut reached=%v generations=%d violations=%v", r.Reached, len(r.Hits), r.Violations)
			}
		})
	}
}

func versionWarningScenario(t *testing.T) *harness.WalkScenario {
	t.Helper()
	scenario := freshScenario(t, "management-maintenance").(*harness.WalkScenario)
	actualCheck := scenario.Check
	scenario.Check = func(h *harness.H) error {
		if err := actualCheck(h); err != nil {
			return err
		}
		scan, err := h.Records()
		if err != nil {
			return err
		}
		var alerts []recipes.VersionAlertFacts
		for _, a := range scan.Alerts {
			alerts = append(alerts, recipes.VersionAlertFacts{ID: a.ID.String(), Kind: a.Kind, Subject: a.Subject})
		}
		_, err = recipes.RequireVersionAlert(alerts, versionWarningInstance)
		return err
	}
	return scenario
}

func TestManagementVersionWarningSurvivesAnotherDrainCrash(t *testing.T) {
	firstPoint, err := fsx.ParsePoint("alert-raise/alert.create#1")
	if err != nil {
		t.Fatal(err)
	}
	first := crashwalk.Target{Gen: 1, Point: firstPoint}
	backend := harness.NewWalkBackend(t)
	baseline, err := backend.Run(context.Background(), versionWarningScenario(t), crashwalk.RunPlan{Targets: []crashwalk.Target{first}})
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Hits) != 2 || len(baseline.Reached) != 1 || !baseline.Reached[0] || len(baseline.Violations) != 0 {
		t.Fatalf("warning drain baseline: %+v", baseline)
	}
	var second crashwalk.Target
	for _, hit := range baseline.Hits[1] {
		if hit.Point.Tag.Tx == "alert-raise" && hit.Point.Step == fsx.StepRename {
			second = crashwalk.Target{Gen: 2, Point: hit.Point}
			break
		}
	}
	if second.Gen != 2 {
		t.Fatal("complete drain did not record a real warning replacement")
	}
	r, err := backend.Run(context.Background(), versionWarningScenario(t), crashwalk.RunPlan{Targets: []crashwalk.Target{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Reached) != 2 || !r.Reached[0] || !r.Reached[1] {
		t.Fatalf("warning drain did not reach both cuts: %v", r.Reached)
	}
	r.Reached = r.Reached[1:]
	if err := crashwalk.CheckRecoveryTarget(baseline.Hits[1], second, r); err != nil {
		t.Fatal(err)
	}
}
