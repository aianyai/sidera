package disk

import (
	"fmt"
	"path"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
)

// InspectRetention counts current records, bodies, archives and intact summaries.
func InspectRetention(r fsx.Reader, scan *store.Scan, endpoint string) (recipes.RetentionFacts, error) {
	result := recipes.RetentionFacts{Records: len(scan.In) + len(scan.Out) + len(scan.Ctl) + len(scan.OutLost), Bodies: len(scan.Bodies), Archives: len(scan.Archive)}
	for _, ep := range scan.Endpoints {
		if ep.ID.String() == endpoint {
			result.SessionPresent = true
		}
	}
	for _, day := range scan.Archive {
		data, err := r.ReadFile(path.Join(store.ArchiveDir, day))
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
}
