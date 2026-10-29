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
