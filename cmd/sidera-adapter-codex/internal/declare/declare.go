// Package declare holds what the adapter says about itself: its identity, its
// capabilities and its package manifest. The manifest is generated from the same
// constants the adapter answers `initialize` with, so the two cannot disagree, and
// the Codex version the adapter was tested with is stated once, here.
package declare

import (
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/codexcfg"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// Identity of the adapter and of the runtime it drives.
const (
	ID          = "codex"
	Version     = "0.1.0"
	RuntimeName = "codex"
	// TestedVersion is the Codex version the adapter was tested with. The App
	// Server protocol is not a stable one, so only this version is known to keep a
	// thread's record in the form the adapter reads it.
	TestedVersion = "0.164.0"
)

// Entry program of the package, relative to its root, without the extension of the
// platform.
const programName = "bin/sidera-adapter-codex"

// recvWaitS is the longest, in seconds, an agent's receive may wait through each
// front end. The MCP wait stays below the 120 seconds Codex lets a tool call run
// (the configuration sets that limit), so Codex never cuts a receive off; the
// command line stays below the ten seconds Codex's unified execution mode waits
// for a command before it moves it to the background.
const (
	recvWaitMCP = 100
	recvWaitCLI = 8
)

// recvBudget is the size of what one receive may print, in tokens. Codex cuts a
// tool result at 10,000 tokens; the estimate for CJK text is conservative and a
// margin is left.
const recvBudget = 8000

// Info is the identity answered in initialize.
func Info() adapterproto.AdapterInfo { return adapterproto.AdapterInfo{ID: ID, Version: Version} }

// Capabilities is what the adapter declares. It cannot interrupt a turn yet and
// does not acknowledge on the agent's behalf; it does answer the requests Codex
// sends it.
func Capabilities() adapterproto.Capabilities {
	return adapterproto.Capabilities{Runtime: &adapterproto.RuntimeCapabilities{
		ServerRequests: true,
		Frontends:      []adapterproto.Frontend{adapterproto.FrontendMCP, adapterproto.FrontendCLI},
	}}
}

// VersionTested reports whether a Codex version is the one the adapter was tested
// with.
func VersionTested(version string) bool { return version == TestedVersion }

// Manifest is the manifest of the package for one platform. A package holds one
// binary, so its platform list names only that platform: the list is what the
// installer checks.
func Manifest(goos, goarch string) manifest.Manifest {
	return manifest.Manifest{
		ManifestVersion: manifest.Version,
		ID:              ID,
		Version:         Version,
		Protocol:        adapterproto.SupportedRange(),
		Entry:           manifest.Entry{Program: programName},
		Platforms:       []manifest.Platform{{OS: goos, Arch: goarch}},
		Requires:        []manifest.Requirement{{Program: "codex"}},
		Config:          &manifest.Config{Instance: codexcfg.InstanceFields(), Preset: codexcfg.PresetFields()},
		Budgets: []manifest.Budget{
			{Frontend: manifest.FrontendMCP, Unit: manifest.UnitTokens, Limit: recvBudget},
			{Frontend: manifest.FrontendCLI, Unit: manifest.UnitTokens, Limit: recvBudget},
		},
		RecvWaitLimits: []manifest.RecvWaitLimit{
			{Frontend: adapterproto.FrontendMCP, MaxS: recvWaitMCP},
			{Frontend: adapterproto.FrontendCLI, MaxS: recvWaitCLI},
		},
	}
}

// WritePackage writes the adapter package for a platform into dir: the manifest
// and a copy of the binary, built for that platform, at the manifest's entry.
func WritePackage(dir, binary, goos, goarch string) error {
	m := Manifest(goos, goarch)
	return manifest.WritePackage(dir, m, binary)
}
