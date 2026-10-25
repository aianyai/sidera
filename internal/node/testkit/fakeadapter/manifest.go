package fakeadapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// ManifestOptions says what the fake adapter declares: in its manifest, which a
// fixture installs like any adapter's, and in the capabilities it answers
// initialize with. Both come from the same options, so they cannot disagree.
// Start from DefaultManifestOptions: a zero value declares nothing and is refused.
type ManifestOptions struct {
	ID      string
	Version string

	// Runtime and Direct say which kinds of endpoint the adapter provides.
	Runtime bool
	Direct  bool

	// Frontends, Interrupt, AckOnBehalf and ServerRequests are the runtime
	// capabilities.
	Frontends      []adapterproto.Frontend
	Interrupt      bool
	AckOnBehalf    bool
	ServerRequests bool

	// MaxDirectEndpoints is the direct endpoint limit.
	MaxDirectEndpoints int

	// RecvWaitS is the longest, in seconds, an agent's recv may wait through each
	// front end. The defaults differ from the daemon's default (8) and from the
	// protocol's cap (120), so a test can tell which of the three it got.
	RecvWaitS map[adapterproto.Frontend]int

	// Budgets is the output budget of recv per front end.
	Budgets []manifest.Budget

	// RuntimeName and RuntimeVersion are what a probe reports; Tested is the
	// range of runtime versions the manifest says the adapter was tested with.
	RuntimeName    string
	RuntimeVersion string
	Tested         []manifest.TestedRange

	// AckMarkers declares how the runtime shows a replaced or cut tool result. The
	// observed knob builds its replaced and truncated texts from them.
	AckMarkers manifest.AckMarkers

	// Program and Args are the entry of the adapter package.
	Program string
	Args    []string
}

// Texts the default markers match; the observed knob writes them.
const (
	replacedMarker     = "[result saved to file]"
	truncatedMarkerRE  = `\.\.\. \d+ more lines`
	truncatedMarkerSay = "... 5 more lines"
)

// DefaultManifestOptions returns the declaration the plan gives the fake adapter:
// a runtime with command-line and MCP front ends that can interrupt and
// acknowledge on behalf, direct endpoints, and receive wait limits of 20 seconds
// for the command line and 45 for MCP.
func DefaultManifestOptions() ManifestOptions {
	lit := replacedMarker
	re := truncatedMarkerRE
	return ManifestOptions{
		ID: "fake", Version: "0.1.0",
		Runtime: true, Direct: true,
		Frontends:   []adapterproto.Frontend{adapterproto.FrontendCLI, adapterproto.FrontendMCP},
		Interrupt:   true,
		AckOnBehalf: true,

		MaxDirectEndpoints: 10,
		RecvWaitS:          map[adapterproto.Frontend]int{adapterproto.FrontendCLI: 20, adapterproto.FrontendMCP: 45},
		Budgets: []manifest.Budget{
			{Frontend: manifest.FrontendCLI, Unit: manifest.UnitChars, Limit: 27000},
			{Frontend: manifest.FrontendMCP, Unit: manifest.UnitTokens, Limit: 22500},
			{Frontend: manifest.FrontendDirect, Unit: manifest.UnitChars, Limit: 9000},
		},
		RuntimeName: "fake-runtime", RuntimeVersion: "1.0.0",
		Tested: []manifest.TestedRange{{Runtime: "fake-runtime", Min: "1.0.0", Max: "1.9.9"}},
		AckMarkers: manifest.AckMarkers{
			Replaced:  []manifest.Marker{{Literal: &lit}},
			Truncated: []manifest.Marker{{Regexp: &re}},
		},
		Program: "bin/sidera-fake-adapter",
	}
}

// Capabilities returns the declaration the adapter answers initialize with.
func (o ManifestOptions) Capabilities() adapterproto.Capabilities {
	var c adapterproto.Capabilities
	if o.Runtime {
		c.Runtime = &adapterproto.RuntimeCapabilities{
			Interrupt: o.Interrupt, AckOnBehalf: o.AckOnBehalf, ServerRequests: o.ServerRequests,
			Frontends: append([]adapterproto.Frontend(nil), o.Frontends...),
		}
	}
	if o.Direct {
		c.Direct = &adapterproto.DirectCapabilities{MaxEndpoints: o.MaxDirectEndpoints}
	}
	return c
}

// Manifest returns the manifest of the adapter package.
func (o ManifestOptions) Manifest() manifest.Manifest {
	m := manifest.Manifest{
		ManifestVersion: manifest.Version,
		ID:              o.ID,
		Version:         o.Version,
		Protocol:        adapterproto.SupportedRange(),
		Entry:           manifest.Entry{Program: o.Program, Args: append([]string(nil), o.Args...)},
		Platforms:       []manifest.Platform{{OS: runtime.GOOS, Arch: runtime.GOARCH}},
		Tested:          append([]manifest.TestedRange(nil), o.Tested...),
		Budgets:         append([]manifest.Budget(nil), o.Budgets...),
	}
	for _, f := range []adapterproto.Frontend{adapterproto.FrontendCLI, adapterproto.FrontendMCP} {
		if s, ok := o.RecvWaitS[f]; ok {
			m.RecvWaitLimits = append(m.RecvWaitLimits, manifest.RecvWaitLimit{Frontend: f, MaxS: s})
		}
	}
	if len(o.AckMarkers.Replaced) > 0 || len(o.AckMarkers.Truncated) > 0 {
		am := o.AckMarkers
		m.AckMarkers = &am
	}
	return m
}

// Validate checks that the options describe an adapter the protocol and the
// manifest rules accept.
func (o ManifestOptions) Validate() error {
	if !o.Runtime && !o.Direct {
		return fmt.Errorf("fakeadapter: an adapter that provides neither runtime nor direct endpoints is refused by the daemon")
	}
	if err := o.Capabilities().Validate(); err != nil {
		return fmt.Errorf("fakeadapter: capabilities: %w", err)
	}
	if err := o.Manifest().Validate(); err != nil {
		return fmt.Errorf("fakeadapter: manifest: %w", err)
	}
	return nil
}

// Encode returns the manifest as the JSON of sidera-adapter.json.
func (o ManifestOptions) Encode() ([]byte, error) {
	m := o.Manifest()
	if err := m.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WritePackage writes an adapter package into dir: the manifest, and a copy of the
// adapter program at the manifest's entry. The program is a binary built from
// cmd/sidera-fake-adapter.
func (o ManifestOptions) WritePackage(dir, program string) error {
	raw, err := o.Encode()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(filepath.FromSlash(o.Program))), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), raw, 0o600); err != nil {
		return err
	}
	src, err := os.Open(program)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(dir, filepath.FromSlash(o.Program)), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}
