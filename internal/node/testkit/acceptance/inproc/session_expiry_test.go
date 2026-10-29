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

func TestUncontrolledCreationJudgementRequiresItsActualOwnerAlert(t *testing.T) {
	b := newBindings()
	b.Sessions.HasUnknownCreationAlert = func(scenario.Env, string) (bool, error) { return false, nil }
	for _, r := range recipes.New(b).Scripts {
		if r.Script.Name != "session-control-write-expiry" {
			continue
		}
		s := &harness.WalkScenario{Script: r.Script, Options: sessionOptions(r.Script.Name), Drain: func(h *harness.H) error { return r.Drain(h) }, Check: func(h *harness.H) error { return r.Check(h) }}
		result, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Violations) != 1 || !strings.Contains(result.Violations[0], "no unique owner alert") {
			t.Fatalf("uncontrolled creation without its alert was accepted: %v", result.Violations)
		}
		return
	}
	t.Fatal("control write-expiry recipe missing")
}
