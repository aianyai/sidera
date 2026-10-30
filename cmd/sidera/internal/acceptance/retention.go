package acceptance

import (
	"fmt"
	"path"

	"github.com/aianyai/sidera/internal/node/store"
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
	facts := recipes.RetentionFacts{Records: len(scan.In) + len(scan.Out) + len(scan.Ctl) + len(scan.OutLost), Bodies: len(scan.Bodies), Archives: len(scan.Archive)}
	for _, ep := range scan.Endpoints {
		if ep.ID.String() == endpoint {
			facts.SessionPresent = true
		}
	}
	for _, day := range scan.Archive {
		data, err := reader.ReadFile(path.Join(store.ArchiveDir, day))
		if err != nil {
			return facts, err
		}
		content := store.ParseArchive(data)
		if content.Dropped != 0 {
			return facts, fmt.Errorf("archive %s has %d damaged lines", day, content.Dropped)
		}
		facts.Summaries += len(content.Summaries)
	}
	return facts, nil
}
