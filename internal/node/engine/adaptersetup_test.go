package engine_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// setupConfig is the configuration of the rig with instances and packages chosen for
// the question what an instance's setup is: one package that declares a setup, one
// that declares none, one that cannot be loaded, and an instance of an adapter that
// is not installed.
type setupConfig struct{ engine.Config }

var setupAdapters = []config.Adapter{
	{ID: "with", Version: "2.0.0", Revision: 3, Manifest: manifest.Manifest{Setup: &manifest.Entry{Program: "bin/setup", Args: []string{"--stdio"}}}},
	{ID: "without", Version: "1.0.0", Revision: 1},
	{ID: "broken", Fault: &config.FileError{Path: config.AdapterCurrentPath("broken"), Kind: config.KindAdapterCurrent, Reason: config.FileInvalid}},
}

func (setupConfig) Adapters() []config.Adapter { return setupAdapters }

func (setupConfig) Instances() config.Instances {
	return config.Instances{Items: map[string]config.Instance{
		"configured": {Adapter: "with", Config: map[string]json.RawMessage{"token": json.RawMessage(`"abc"`)}},
		"bare":       {Adapter: "with"},
		"plain":      {Adapter: "without"},
		"faulted":    {Adapter: "broken"},
		"ghost":      {Adapter: "missing"},
	}}
}

// TestAdapterSetupInfoResolvesTheSetupProgramOfTheInstalledPackage: the setup program
// is resolved by the daemon to an absolute path inside the package of the version the
// instance runs, so that a caller never reads a manifest; an adapter that declares no
// setup says so with a nil argv, and the instances that have no setup to give are
// refused with the reason that tells them apart.
func TestAdapterSetupInfoResolvesTheSetupProgramOfTheInstalledPackage(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	d := r.deps()
	d.Config = setupConfig{Config: r.cfg}
	e, _, err := engine.Open(ctx, d, r.params)
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	if err := e.RecoverDelivery(ctx); err != nil {
		t.Fatal(err)
	}
	e.Serve()
	t.Cleanup(func() { r.stop(e) })

	packageDir := adapterhost.PackageDir(testHome, setupAdapters[0])
	tests := []struct {
		name     string
		instance string
		want     engine.AdapterSetupInfo
		reason   engine.Reason
		change   config.ChangeReason
	}{
		{name: "setup declared, with configuration", instance: "configured", want: engine.AdapterSetupInfo{
			Instance: "configured", Adapter: "with", StateDir: adapterhost.StateDir(testHome, "configured"),
			Config:    map[string]json.RawMessage{"token": json.RawMessage(`"abc"`)},
			SetupArgv: []string{filepath.Join(packageDir, "bin", "setup"), "--stdio"},
		}},
		{name: "setup declared, no configuration", instance: "bare", want: engine.AdapterSetupInfo{
			Instance: "bare", Adapter: "with", StateDir: adapterhost.StateDir(testHome, "bare"),
			Config:    map[string]json.RawMessage{},
			SetupArgv: []string{filepath.Join(packageDir, "bin", "setup"), "--stdio"},
		}},
		{name: "no setup declared", instance: "plain", want: engine.AdapterSetupInfo{
			Instance: "plain", Adapter: "without", StateDir: adapterhost.StateDir(testHome, "plain"),
			Config: map[string]json.RawMessage{},
		}},
		{name: "unknown instance", instance: "nobody", reason: engine.ReasonInstanceUnknown},
		{name: "adapter not installed", instance: "ghost", change: config.ChangeAdapterNotInstalled},
		{name: "package cannot be loaded", instance: "faulted", reason: engine.ReasonPackageInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.AdapterSetupInfo(ctx, tc.instance)
			switch {
			case tc.reason != 0:
				wantReason(t, err, tc.reason)
			case tc.change != 0:
				var ce *config.ChangeError
				if !errors.As(err, &ce) || ce.Reason != tc.change {
					t.Fatalf("error = %v, want the change reason %v", err, tc.change)
				}
			case err != nil:
				t.Fatal(err)
			case !reflect.DeepEqual(got, tc.want):
				t.Fatalf("AdapterSetupInfo = %+v, want %+v", got, tc.want)
			}
		})
	}
}
