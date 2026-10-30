package inproc

import (
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/disk"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func newMaintenanceOps() recipes.MaintenanceOps {
	return recipes.MaintenanceOps{InspectRetention: func(e scenario.Env, endpoint string) (recipes.RetentionFacts, error) {
		h := e.(*harness.H)
		scan, err := h.Records()
		if err != nil {
			return recipes.RetentionFacts{}, err
		}
		return disk.InspectRetention(h.Disk(), scan, endpoint)
	}}
}
