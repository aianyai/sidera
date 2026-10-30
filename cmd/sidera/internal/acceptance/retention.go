package acceptance

import (
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/disk"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func newMaintenanceOps() recipes.MaintenanceOps {
	return recipes.MaintenanceOps{InspectRetention: inspectRetention}
}

func inspectRetention(e scenario.Env, endpoint string) (recipes.RetentionFacts, error) {
	scan, reader, err := readSessionRecords(e)
	if err != nil {
		return recipes.RetentionFacts{}, err
	}
	return disk.InspectRetention(reader, scan, endpoint)
}
