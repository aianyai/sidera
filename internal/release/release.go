// Package release is the one place that knows which release a program belongs to.
// Every shipped program (the daemon's command line and each adapter) reports the
// same version, set when it is built; the adapter protocol has a version of its
// own and is independent of it.
//
// The release build sets the version with the linker:
//
//	go build -ldflags "-X github.com/aianyai/sidera/internal/release.stamped=0.8.0"
//
// It may also state the commit (stampedCommit) and whether the tree had uncommitted
// changes (stampedModified, "true"): Go records the commit itself only for a build
// made in a repository's own checkout, and records another one for a build made in
// a linked git worktree.
//
// A release archive build also states how it is distributed (stampedDistribution,
// DistributionArchive); a build that does not, such as one from a `go install`, a
// package manager or a checkout, reports none.
//
// A build without it, such as a plain `go build` on a development machine, reports
// DevVersion, so that it cannot be taken for a release.
package release

import (
	"bytes"
	_ "embed"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// DevVersion is the version of a build that was not given one. It is a valid
// SemVer version, because adapters write their version into a manifest and
// answer it in initialize, and it sorts before every release.
const DevVersion = "0.0.0-dev"

// DistributionArchive is the distribution of the programs in the release archives
// built by scripts/release.sh, which `sidera update` may replace. A program built any
// other way has no distribution.
const DistributionArchive = "archive"

// stamped, stampedDistribution, stampedCommit and stampedModified are set at link
// time, see the package documentation.
var stamped, stampedDistribution, stampedCommit, stampedModified string

//go:embed minisign.pub
var signingKey []byte

// SigningKey returns the public key the releases are signed with, the content of
// minisign.pub. It is not parsed here: the adapters import this package, and the
// parser must not reach them.
func SigningKey() []byte { return bytes.Clone(signingKey) }

// commitLength is how many characters of the commit are reported.
const commitLength = 12

// Info says what a program was built from.
type Info struct {
	Version string `json:"version"`
	// Distribution says how the program was distributed: DistributionArchive for the
	// programs of a release archive, empty for a build that did not state one.
	Distribution string `json:"distribution,omitempty"`
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
	return resolve(stamped, stampedDistribution, stampedCommit, stampedModified, settings)
}

// Version returns the version of the running program.
func Version() string { return Read().Version }

// resolve is Read from what the build stated and what Go recorded; a stated commit
// wins over the recorded one, with its modified flag.
func resolve(version, distribution, commit, modified string, settings []debug.BuildSetting) Info {
	info := Info{Version: version, Distribution: distribution, GoVersion: runtime.Version()}
	if info.Version == "" {
		info.Version = DevVersion
	}
	if commit != "" {
		info.Commit = abbreviate(commit)
		info.Modified = modified == "true"
		return info
	}
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			info.Commit = abbreviate(s.Value)
		case "vcs.modified":
			info.Modified = s.Value == "true"
		}
	}
	return info
}

func abbreviate(commit string) string { return commit[:min(len(commit), commitLength)] }

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
