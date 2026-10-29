package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
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

func (f *Fixture) prepareInputs() error {
	// Direct endpoints need no runtime-spawned CLI. That path must first have a
	// public input seam for clearing inherited daemon crash controls.
	opts := fakeadapter.DefaultManifestOptions()
	opts.Runtime = false
	opts.Interrupt = false
	opts.AckOnBehalf = false
	opts.Frontends = nil
	opts.RecvWaitS = nil
	opts.RuntimeName, opts.RuntimeVersion, opts.Tested, opts.AckMarkers = "", "", nil, nil
	opts.Budgets = []manifest.Budget{{Frontend: manifest.FrontendDirect, Unit: manifest.UnitChars, Limit: 9000}}
	config := filepath.Join(f.root, "input", "fake.json")
	opts.Args = fakeadapter.ConfigArgs(config)
	source := filepath.Join(f.root, "input", "package")
	if err := opts.WritePackage(source, f.binaries.Adapter); err != nil {
		return err
	}
	f.inputs = []adapterInput{{source: source, config: config, process: fakeadapter.ProcessConfig{InstanceID: "fake", Manifest: opts, LedgerDir: filepath.Join(f.root, "l"), RuntimeDir: filepath.Join(f.root, "r"), RuntimeBinary: f.binaries.Runtime, RuntimeSeed: 1, RuntimeTestSeed: f.opts.Seed + "/runtime", Control: f.control.Addr()}}}
	for _, in := range f.inputs {
		f.bootstrapSteps = append(f.bootstrapSteps,
			bootstrapStep{method: "adapters.install", request: manageapi.Request{Params: map[string]any{"source": in.source}}},
			bootstrapStep{method: "adapters.enable", collection: "adapters.list", request: manageapi.Request{Params: map[string]any{"adapter": in.process.Manifest.ID, "instance": in.process.InstanceID}}},
		)
	}
	f.bootstrapSteps = append(f.bootstrapSteps, bootstrapStep{method: "presets.add", collection: "presets.list", request: manageapi.Request{Params: map[string]any{"name": "fixture", "preset": map[string]any{"local_sources": "allow"}}}})
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
