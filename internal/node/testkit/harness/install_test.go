package harness_test

import (
	"crypto/sha256"
	"encoding/json"
	"path"
	"slices"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func TestPackageSourceCanInstallAndUpgradeAfterRestart(t *testing.T) {
	const source = "./fixture package"
	var opened []string
	var src fsx.Reader
	h := harness.New(t, harness.Options{
		OpenPackage: func(name string) (fsx.Reader, error) {
			opened = append(opened, name)
			return src, nil
		},
	})
	for i, version := range []string{"1.0.0", "2.0.0"} {
		if i > 0 {
			h.Restart()
		}
		manifest := harness.AdapterManifestOf("package-reader", 1)
		manifest.Version, manifest.Program = version, "adapter"
		raw, err := manifest.Encode()
		if err != nil {
			t.Fatal(err)
		}
		pkg := fsxtest.NewMem()
		for name, body := range map[string][]byte{
			"sidera-adapter.json": raw,
			manifest.Program:      []byte("program " + version),
		} {
			if err := fsx.Replace(pkg, fsx.Tag{Tx: "package", Role: "file"}, name, body, fsx.ModePrivate); err != nil {
				t.Fatal(err)
			}
		}
		src = pkg
		result, err := h.Engine().Execute(bg(), engine.ManagementCommand{
			Key:         admin.KeyFor(admin.LocalOwner(), admin.OperationID{200 + byte(i)}),
			Fingerprint: sha256.Sum256([]byte("install " + source)),
			Name:        "adapters.install",
			Operation: engine.InstallAdapterOp(source, func(installed engine.AdapterInstalled) ([]byte, error) {
				return json.Marshal(installed)
			}),
		})
		if err != nil || result.Replayed {
			t.Fatalf("install %s: %+v, %v", version, result, err)
		}
		var installed engine.AdapterInstalled
		if err := json.Unmarshal(result.Result, &installed); err != nil {
			t.Fatal(err)
		}
		if want := (engine.AdapterInstalled{ID: manifest.ID, Version: version}); installed != want {
			t.Fatalf("install %s returned %+v, want %+v", version, installed, want)
		}
		if err := h.Quiesce(); err != nil {
			t.Fatal(err)
		}
		current, err := h.Disk().ReadFile(config.AdapterCurrentPath(manifest.ID))
		if err != nil {
			t.Fatal(err)
		}
		if got, err := config.DecodeAdapterCurrent(manifest.ID, current); err != nil || got != version {
			t.Fatalf("current version is %q (%v), want %s", got, err, version)
		}
		program := path.Join(config.AdapterVersionDir(manifest.ID, version), manifest.Program)
		if got, err := h.Disk().ReadFile(program); err != nil || string(got) != "program "+version {
			t.Fatalf("installed program is %q (%v), want program %s", got, err, version)
		}
	}
	if !slices.Equal(opened, []string{source, source}) {
		t.Fatalf("opened package sources %q, want the unchanged source for both installations", opened)
	}
}
