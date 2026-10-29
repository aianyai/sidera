package inproc

import (
	"fmt"
	"path"

	"github.com/aianyai/sidera/internal/node/store"
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
		result := recipes.RetentionFacts{Records: len(scan.In) + len(scan.Out) + len(scan.Ctl) + len(scan.OutLost), Bodies: len(scan.Bodies), Archives: len(scan.Archive)}
		for _, ep := range scan.Endpoints {
			if ep.ID.String() == endpoint {
				result.SessionPresent = true
			}
		}
		for _, day := range scan.Archive {
			data, err := h.Disk().ReadFile(path.Join(store.ArchiveDir, day))
			if err != nil {
				return result, err
			}
			content := store.ParseArchive(data)
			if content.Dropped != 0 {
				return result, fmt.Errorf("archive %s has %d damaged lines", day, content.Dropped)
			}
			result.Summaries += len(content.Summaries)
		}
		return result, nil
	}}
}
