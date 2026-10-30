package acceptance

import (
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func newDeliveryOps() recipes.DeliveryOps {
	return recipes.DeliveryOps{
		FailSourceUpdate: func(e scenario.Env) error {
			return unavailableFilesystemFault(e, recipes.FaultSourceUpdate)
		},
		RetainLostSourceBody: func(e scenario.Env) error {
			return unavailableFilesystemFault(e, recipes.FaultRetainLostBody)
		},
	}
}

func unavailableFilesystemFault(e scenario.Env, fault recipes.FSFault) error {
	if err := envFrom(e).check(); err != nil {
		return err
	}
	return scenario.NotAvailable("binary acceptance has no filesystem fault injection: " + string(fault))
}
