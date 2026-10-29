package inproc

import (
	"context"
	"errors"
	"testing"

	"github.com/aianyai/sidera/internal/admin"

	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func TestInstallationRejectsAResultWithoutItsInstalledVersion(t *testing.T) {
	t.Run("false-result", func(t *testing.T) {
		b := newBindings()
		actual := b.Installation.Install
		b.Installation.Install = func(e scenario.Env, id admin.OperationID, spec recipes.PackageSpec) (recipes.InstallOutcome, error) {
			out, err := actual(e, id, spec)
			if err == nil && out.Code == "" {
				out.Version = "99.0.0"
			}
			return out, err
		}
		catalog := recipes.New(b)
		for _, r := range catalog.Scripts {
			if r.Script.Name != "installation-rollback" {
				continue
			}
			s := &harness.WalkScenario{Script: r.Script, Options: installationOptions()}
			_, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
			if !errors.Is(err, recipes.ErrInstallationEffect) {
				t.Fatalf("false installed result: %v", err)
			}
			return
		}
		t.Fatal("installation recipe missing")
	})
}
