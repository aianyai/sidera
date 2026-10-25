package crashwalk

import (
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
	// Path is the file the step worked on, relative to the data directory, with
	// the random part of a temporary name replaced (see NormalizePath).
	Path string
	// Setup marks a hit made while the scenario was being set up, before its first
	// step. The startup scenario covers what the setup writes.
	Setup bool
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
