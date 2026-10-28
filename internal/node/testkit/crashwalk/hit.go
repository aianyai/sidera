package crashwalk

import (
	"path"
	"strings"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// Hit is a crash point a run reached.
type Hit struct {
	// Gen is the process generation: 1 for the first process, 2 for the one
	// started after the first crash, and so on.
	Gen int
	// Point is the step and which time it was reached.
	Point fsx.Point
	// Path is the step's primary path relative to the data directory, with
	// temporary suffixes normalized. For renames it is the target. A logical
	// commit has no path; the root directory is ".".
	Path string
	// From is the normalized source of Rename or RenameDir, empty otherwise.
	From string
	// Setup marks a hit made while the scenario was being set up, before its first
	// step. The startup scenario covers what the setup writes.
	Setup bool
}

// NewHit converts one complete observation into a trace fingerprint. Gen and
// setup belong to the backend's recording window. CreateTemp reports its parent
// before a name exists, so its fingerprint names that parent's future temporary
// file; later steps already carry the opened file's actual name.
func NewHit(gen int, observation fsx.Observation, setup bool) Hit {
	p := observation.Path
	if observation.Point.Step == fsx.StepCreate {
		p = path.Join(p, fsx.TempPrefix+"*")
	}
	return Hit{
		Gen:   gen,
		Point: observation.Point,
		Path:  NormalizePath(p),
		From:  NormalizePath(observation.From),
		Setup: setup,
	}
}

// NormalizePath replaces the random suffix of a temporary file or of a discarded
// directory, element by element, so that the path of the same write is the same
// in two runs.
func NormalizePath(p string) string {
	parts := strings.Split(p, "/")
	for i, e := range parts {
		switch {
		case strings.HasPrefix(e, fsx.TempPrefix):
			parts[i] = fsx.TempPrefix + "*"
		case strings.HasPrefix(e, fsx.TrashPrefix):
			parts[i] = fsx.TrashPrefix + "*"
		}
	}
	return strings.Join(parts, "/")
}

// IsSync reports whether a step only makes earlier work durable. A process that is
// killed leaves what it wrote in the page cache, so a kill before a sync and a kill
// after the step before it leave the same disk; the power-loss model covers what
// a sync is for.
func IsSync(s fsx.Step) bool { return s == fsx.StepSync || s == fsx.StepSyncDir }
