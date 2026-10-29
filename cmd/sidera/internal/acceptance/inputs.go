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
	for _, in := range f.inputs {
		if err := f.change(ctx, "adapters.install", nil, map[string]any{"source": in.source}); err != nil {
			return fmt.Errorf("acceptance: install: %w", err)
		}
		v, err := f.collectionVersion(ctx, "adapters.list")
		if err != nil {
			return err
		}
		if err = f.change(ctx, "adapters.enable", &v, map[string]any{"adapter": in.process.Manifest.ID, "instance": in.process.InstanceID}); err != nil {
			return err
		}
	}
	v, err := f.collectionVersion(ctx, "presets.list")
	if err != nil {
		return err
	}
	return f.change(ctx, "presets.add", &v, map[string]any{"name": "fixture", "preset": map[string]any{"local_sources": "allow"}})
}
