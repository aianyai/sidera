package release

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aianyai/sidera/sdk/go/semver"
)

// ErrInvalidVersion means text is not a release version.
var ErrInvalidVersion = errors.New("not a release version")

// ParseVersion reads a release version: a Semantic Versioning 2.0.0 version as
// sdk/go/semver reads it, without a leading "v" and without build metadata. This
// is the one place that says what a release version is in Go; scripts/lib/release-format.sh
// writes the same rule in shell for the release scripts, and a test requires the two
// to agree.
//
// Build metadata is refused here because semver.Parse drops it silently, and a
// version with a suffix that is ignored is not the version that was released. A
// failure is ErrInvalidVersion.
func ParseVersion(text string) (semver.Version, error) {
	if strings.Contains(text, "+") {
		return semver.Version{}, fmt.Errorf("%w: %q has build metadata", ErrInvalidVersion, text)
	}
	v, err := semver.Parse(text)
	if err != nil {
		return semver.Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, text)
	}
	return v, nil
}
