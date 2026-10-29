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
	state := &damageState{}
	catalog := recipes.New(bindingsWithRecovery(state))
	capabilities := inProcessCapabilities()
	var out []crashwalk.Scenario
	for _, r := range catalog.Scripts {
		if !capabilities.Supports(r.Needs) {
			t.Fatalf("unsupported in-process recipe %q: %+v", r.Script.Name, r.Needs)
		}
		s := &harness.WalkScenario{Script: r.Script, Options: recipeOptions(r.Needs)}
		if r.Drain != nil {
			s.Drain = func(h *harness.H) error { return r.Drain(h) }
		}
		if r.Check != nil {
			s.Check = func(h *harness.H) error { return r.Check(h) }
		}
		configureRecoveryScenario(s, r.Needs.Damage, state)
		out = append(out, s)
	}
	for _, r := range catalog.Startups {
		if !(recipes.Capabilities{Damage: []recipes.DamageKind{recipes.DamageLayout}}).Supports(r.Needs) {
			t.Fatalf("unsupported in-process startup %q: %+v", r.Name, r.Needs)
		}
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
		configureRecoveryStartup(s, r.Needs.Damage)
		configureLifecycleStartup(s)
		out = append(out, s)
	}
	return out
}

func newBindings() recipes.Bindings { return bindingsWithRecovery(&damageState{}) }

func bindingsWithRecovery(state *damageState) recipes.Bindings {
	return recipes.Bindings{Delivery: newDeliveryOps(), Sessions: newSessionOps(), Installation: newInstallOps(), Management: newManagementOps(), Maintenance: newMaintenanceOps(), Recovery: recoveryBindings(state)}
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

func inProcessCapabilities() recipes.Capabilities {
	capabilities := recipes.BinaryCapabilities()
	capabilities.FSFaults = []recipes.FSFault{recipes.FaultSourceUpdate, recipes.FaultRetainLostBody, recipes.FaultCreateSettlement, recipes.FaultInstallPointer}
	return capabilities
}

func recipeOptions(needs recipes.Needs) harness.Options {
	options := harness.Options{ArchiveSweepEvery: needs.ArchiveSweepEvery}
	if needs.RuntimeAdapter || len(needs.ExtraInstances) != 0 {
		options.Adapter = &harness.AdapterOptions{Runtime: needs.RuntimeAdapter, ExtraInstances: needs.ExtraInstances}
	}
	if needs.PackageInput {
		options.OpenPackage = installationOptions().OpenPackage
	}
	return options
}
