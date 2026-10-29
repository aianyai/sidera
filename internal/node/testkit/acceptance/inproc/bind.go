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
		s := &harness.WalkScenario{Script: r.Script, Options: sessionOptions(r.Script.Name)}
		if r.Script.Name == "maintenance-retention" {
			s.Options = retentionOptions()
		}
		if r.Script.Name == "installation-rollback" {
			s.Options = installationOptions()
		}
		if r.Script.Name == "management-instance" {
			s.Options = managementOptions(r.Script.Name)
		}
		if r.Drain != nil {
			s.Drain = func(h *harness.H) error { return r.Drain(h) }
		}
		if r.Check != nil {
			s.Check = func(h *harness.H) error { return r.Check(h) }
		}
		configureRecoveryScenario(s, len(r.RecoveryTriggers) != 0)
		configureBodyRecoveryScenario(s)
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
		s := &harness.StartupWalkScenario{Label: r.Name, Operation: op, StartAfter: r.StartAfter, AdvanceAfterStart: r.AdvanceAfterStart, PointBudget: r.MaxPoints, RecoveryCases: r.Recovery}
		configureRecoveryStartup(s)
		configureLifecycleStartup(s)
		out = append(out, s)
	}
	return out
}

func newBindings() recipes.Bindings {
	return recipes.Bindings{Delivery: newDeliveryOps(), Sessions: newSessionOps(), Installation: newInstallOps(), Management: newManagementOps(), Maintenance: newMaintenanceOps(), Recovery: recoveryBindings()}
}

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
