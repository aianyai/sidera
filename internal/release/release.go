// Package release is the one place that knows which release a program belongs to.
// Every shipped program (the daemon's command line and each adapter) reports the
// same version, set when it is built; the adapter protocol has a version of its
// own and is independent of it.
//
// The release build sets the version with the linker:
//
//	go build -ldflags "-X github.com/aianyai/sidera/internal/release.stamped=0.8.0"
//
// A build without it, such as a plain `go build` on a development machine, reports
// DevVersion, so that it cannot be taken for a release.
package release

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// DevVersion is the version of a build that was not given one. It is a valid
// SemVer version, because adapters write their version into a manifest and
// answer it in initialize, and it sorts before every release.
const DevVersion = "0.0.0-dev"

// stamped is set at link time, see the package documentation.
var stamped string

// commitLength is how many characters of the commit are reported.
const commitLength = 12

// Info says what a program was built from.
type Info struct {
	Version string `json:"version"`
	// Commit is the abbreviated revision the build was made from; it is empty when
	// the build carries none, as when it was made outside a checkout.
	Commit string `json:"commit,omitempty"`
	// Modified is true when the checkout the build was made from had uncommitted
	// changes.
	Modified  bool   `json:"modified"`
	GoVersion string `json:"go_version"`
}

// Read returns what the running program was built from.
func Read() Info {
	var settings []debug.BuildSetting
	if bi, ok := debug.ReadBuildInfo(); ok {
		settings = bi.Settings
	}
	return resolve(stamped, settings)
}

// Version returns the version of the running program.
func Version() string { return Read().Version }

func resolve(stampedVersion string, settings []debug.BuildSetting) Info {
	info := Info{Version: stampedVersion, GoVersion: runtime.Version()}
	if info.Version == "" {
		info.Version = DevVersion
	}
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			info.Commit = s.Value[:min(len(s.Value), commitLength)]
		case "vcs.modified":
			info.Modified = s.Value == "true"
		}
	}
	return info
}

// Describe is the one line `version` prints for a program: its name, the version,
// what it was built from and with.
func (i Info) Describe(program string) string {
	commit := "commit unknown"
	if i.Commit != "" {
		commit = "commit " + i.Commit
	}
	parts := []string{commit}
	if i.Modified {
		parts = append(parts, "modified")
	}
	parts = append(parts, i.GoVersion)
	return fmt.Sprintf("%s %s (%s)", program, i.Version, strings.Join(parts, ", "))
}
