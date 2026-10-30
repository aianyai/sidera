package acceptance

import (
	"fmt"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

// bindCatalog selects immutable descriptions; each Run binds its own evidence.
func bindCatalog() ([]crashwalk.Scenario, []crashwalk.RecoveryTrigger) {
	selected := recipes.New(recipes.Bindings{}).Select(recipes.BinaryCapabilities())
	var scenarios []crashwalk.Scenario
	var triggers []crashwalk.RecoveryTrigger
	for _, descriptor := range selected.Scripts {
		name := descriptor.Script.Name
		scenarios = append(scenarios, NewWalkScenario(descriptor, func(recovery recipes.RecoveryOps) (recipes.ScriptRecipe, error) {
			for _, recipe := range recipes.New(bindingsWithRecovery(recovery)).Scripts {
				if recipe.Script.Name == name {
					return recipe, nil
				}
			}
			return recipes.ScriptRecipe{}, fmt.Errorf("acceptance: unknown binary recipe %q", name)
		}))
		for _, trigger := range descriptor.RecoveryTriggers {
			triggers = append(triggers, crashwalk.RecoveryTrigger{Scenario: name, After: trigger})
		}
	}
	for _, recipe := range selected.Startups {
		scenarios = append(scenarios, &StartupWalkScenario{Recipe: recipe})
	}
	for _, recipe := range selected.Terminals {
		scenarios = append(scenarios, &TerminalWalkScenario{Recipe: recipe})
	}
	return scenarios, triggers
}

func bindingsWithRecovery(recovery recipes.RecoveryOps) recipes.Bindings {
	return recipes.Bindings{
		LocalNode:    func(e scenario.Env) (ident.NodeID, error) { return envFrom(e).localNode() },
		Delivery:     newDeliveryOps(),
		Sessions:     newSessionOps(),
		Installation: newInstallOps(),
		Management:   newManagementOps(),
		Maintenance:  newMaintenanceOps(),
		Recovery:     recovery,
	}
}
