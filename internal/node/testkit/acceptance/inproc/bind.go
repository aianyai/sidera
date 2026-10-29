// Package inproc binds the acceptance catalog to the real in-process backend.
package inproc

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func bindCatalog(t testing.TB) []crashwalk.Scenario {
	t.Helper()
	catalog := recipes.New(newBindings())
	var out []crashwalk.Scenario
	for _, r := range catalog.Scripts {
		s := &harness.WalkScenario{Script: r.Script}
		if r.Drain != nil {
			s.Drain = func(h *harness.H) error { return r.Drain(h) }
		}
		if r.Check != nil {
			s.Check = func(h *harness.H) error { return r.Check(h) }
		}
		out = append(out, s)
	}
	for _, r := range catalog.Startups {
		var op harness.StartupOperation
		switch r.Kind {
		case recipes.Init:
			op = harness.StartupInit
		case recipes.Start:
			op = harness.StartupStart
		default:
			t.Fatalf("unknown startup kind %d", r.Kind)
		}
		out = append(out, &harness.StartupWalkScenario{Label: r.Name, Operation: op, StartAfter: r.StartAfter, PointBudget: r.MaxPoints, RecoveryCases: r.Recovery})
	}
	return out
}

func newBindings() recipes.Bindings { return recipes.Bindings{} }

func freshScenario(t testing.TB, name string) crashwalk.Scenario {
	t.Helper()
	for _, s := range bindCatalog(t) {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("unknown acceptance recipe %q", name)
	return nil
}
