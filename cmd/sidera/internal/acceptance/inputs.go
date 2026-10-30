package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

type adapterInput struct {
	source, config string
	process        fakeadapter.ProcessConfig
}

type bootstrapStep struct {
	method, collection string
	request            manageapi.Request
}

// fixtureProfile describes caller-owned inputs, never mutations of node state.
// The public fixture's zero profile retains its direct-only fake/fixture defaults.
type fixtureProfile struct {
	mainInstance, preset       string
	runtimeAdapter             bool
	extraInstances             []string
	probeWarning, packageInput bool
}

const versionWarningInstance = "warning-probe"

func profileFromNeeds(needs recipes.Needs) (fixtureProfile, error) {
	if needs.ArchiveSweepEvery != 0 && needs.ArchiveSweepEvery != daemon.ArchiveSweepEvery {
		return fixtureProfile{}, fmt.Errorf("acceptance: archive sweep period %s differs from the executable period %s", needs.ArchiveSweepEvery, daemon.ArchiveSweepEvery)
	}
	if !recipes.BinaryCapabilities().Supports(needs) {
		return fixtureProfile{}, fmt.Errorf("acceptance: unsupported needs: %+v", needs)
	}
	return fixtureProfile{mainInstance: "chat", preset: "default", runtimeAdapter: needs.RuntimeAdapter, extraInstances: append([]string(nil), needs.ExtraInstances...), probeWarning: needs.ProbeWarning, packageInput: needs.PackageInput}, nil
}

func (p fixtureProfile) instance() string {
	if p.mainInstance != "" {
		return p.mainInstance
	}
	return "fake"
}
func (p fixtureProfile) presetName() string {
	if p.preset != "" {
		return p.preset
	}
	return "fixture"
}

// packageInput writes a real user-supplied package below this fixture's input
// directory. Installation bindings may extend that source before public install.
func (f *Fixture) packageInput(name string, opts fakeadapter.ManifestOptions) (string, error) {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return "", fmt.Errorf("acceptance: invalid package input name %q", name)
	}
	source := filepath.Join(f.root, "input", name)
	if err := opts.WritePackage(source, f.binaries.Adapter); err != nil {
		return "", err
	}
	return source, nil
}

func inputManifest(runtime bool) fakeadapter.ManifestOptions {
	opts := fakeadapter.DefaultManifestOptions()
	if runtime {
		// These binary profiles exercise the real CLI and HTTP paths. An MCP
		// declaration would promise a probe and receive path this profile does not run.
		opts.Frontends = []adapterproto.Frontend{adapterproto.FrontendCLI}
		delete(opts.RecvWaitS, adapterproto.FrontendMCP)
		budgets := opts.Budgets[:0]
		for _, budget := range opts.Budgets {
			if budget.Frontend != manifest.FrontendMCP {
				budgets = append(budgets, budget)
			}
		}
		opts.Budgets = budgets
		return opts
	}
	opts.Runtime = false
	opts.Interrupt = false
	opts.AckOnBehalf = false
	opts.Frontends = nil
	opts.RecvWaitS = nil
	opts.RuntimeName, opts.RuntimeVersion, opts.Tested, opts.AckMarkers = "", "", nil, nil
	opts.Budgets = []manifest.Budget{{Frontend: manifest.FrontendDirect, Unit: manifest.UnitChars, Limit: 9000}}
	return opts
}

func (f *Fixture) prepareInputs() error {
	names := append([]string{f.profile.instance()}, f.profile.extraInstances...)
	if f.profile.probeWarning {
		names = append(names, versionWarningInstance)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			return fmt.Errorf("acceptance: duplicate input instance %q", name)
		}
		seen[name] = true
		warning := f.profile.probeWarning && name == versionWarningInstance
		opts := inputManifest(f.profile.runtimeAdapter || warning)
		// Each extra instance owns a package with its own stable public identity.
		if name != f.profile.instance() || f.profile.mainInstance != "" {
			opts.ID = name
		}
		if f.profile.mainInstance != "" {
			opts.MaxDirectEndpoints = adapterproto.MaxDirectEndpoints
		}
		if warning {
			opts.Tested = []manifest.TestedRange{{Runtime: opts.RuntimeName, Min: "2.0.0", Max: "2.9.9"}}
		}
		config := filepath.Join(f.root, "input", name+".json")
		opts.Args = fakeadapter.ConfigArgs(config)
		source, err := f.packageInput("package-"+name, opts)
		if err != nil {
			return err
		}
		var toolProbe *fakeadapter.ToolProbeConfig
		if opts.Runtime {
			toolProbe = &fakeadapter.ToolProbeConfig{Commands: map[adapterproto.Frontend][]string{
				adapterproto.FrontendCLI: {f.binaries.Plain, "probe", "--json"},
			}}
		}
		f.inputs = append(f.inputs, adapterInput{source: source, config: config, process: fakeadapter.ProcessConfig{InstanceID: name, Manifest: opts, ToolProbe: toolProbe, LedgerDir: filepath.Join(f.root, "l"), RuntimeDir: filepath.Join(f.root, "r"), RuntimeBinary: f.binaries.Runtime, RuntimeSeed: f.runtimeSeed(name), RuntimeTestSeed: f.opts.Seed + "/runtime/" + name, Control: f.control.Addr()}})
		f.bootstrapSteps = append(f.bootstrapSteps, bootstrapStep{method: "adapters.install", request: manageapi.Request{Params: map[string]any{"source": source}}})
		if !warning {
			f.bootstrapSteps = append(f.bootstrapSteps, bootstrapStep{method: "adapters.enable", collection: "adapters.list", request: manageapi.Request{Params: map[string]any{"adapter": opts.ID, "instance": name}}})
		}
	}
	// init already owns the default preset. The base fixture adds its separate
	// named preset; catalog profiles use the real default without re-adding it.
	if f.profile.presetName() != "default" {
		f.bootstrapSteps = append(f.bootstrapSteps, bootstrapStep{method: "presets.add", collection: "presets.list", request: manageapi.Request{Params: map[string]any{"name": f.profile.presetName(), "preset": map[string]any{"local_sources": "allow"}}}})
	}
	return f.writeInputs()
}

func (f *Fixture) writeInputs() error {
	for i := range f.inputs {
		in := &f.inputs[i]
		in.process.ClockStartMS = f.Now().UnixMilli()
		raw, err := json.Marshal(in.process)
		if err != nil {
			return err
		}
		if err = os.WriteFile(in.config, raw, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (f *Fixture) collectionVersion(ctx context.Context, method string) (admin.Version, error) {
	raw, err := f.Manage(ctx, method, manageapi.Request{})
	if err != nil {
		return 0, err
	}
	var result struct {
		Version admin.Version `json:"version"`
	}
	err = resultBody(raw, &result)
	return result.Version, err
}

func (f *Fixture) bootstrap(ctx context.Context) error {
	for f.bootstrapNext < len(f.bootstrapSteps) {
		step := &f.bootstrapSteps[f.bootstrapNext]
		if step.request.OperationID.IsZero() {
			var version *admin.Version
			if step.collection != "" {
				v, err := f.collectionVersion(ctx, step.collection)
				if err != nil {
					return err
				}
				version = &v
			}
			// Retain the whole request before sending: a killed daemon may have
			// committed it even when no reply reached this fixture.
			step.request = f.changeRequest(version, step.request.Params)
		}
		if _, err := f.Manage(ctx, step.method, step.request); err != nil {
			return fmt.Errorf("acceptance: bootstrap %s: %w", step.method, err)
		}
		f.bootstrapNext++
	}
	return nil
}
