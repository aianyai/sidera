package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/disk"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

func newInstallOps() recipes.InstallOps {
	// Source paths are part of the actual request fingerprint. Reentry retains
	// the completed caller-owned input instead of regenerating or renaming it.
	sources := map[recipes.PackageSpec]string{}
	return recipes.InstallOps{
		Install: func(e scenario.Env, id admin.OperationID, spec recipes.PackageSpec) (recipes.InstallOutcome, error) {
			if err := envFrom(e).check(); err != nil {
				return recipes.InstallOutcome{}, err
			}
			source, ok := sources[spec]
			if !ok {
				var err error
				source, err = installationInput(envFrom(e).fixture, spec)
				if err != nil {
					return recipes.InstallOutcome{}, err
				}
				sources[spec] = source
			}
			raw, replayed, code, err := checkedManagement(e, "adapters.install", manageapi.Request{OperationID: id, Params: map[string]any{"source": source}})
			if err != nil {
				return recipes.InstallOutcome{}, err
			}
			out := recipes.InstallOutcome{Replayed: replayed, Code: code}
			if code != "" {
				return out, nil
			}
			var result struct {
				Adapter struct {
					ID      string `json:"id"`
					Version string `json:"version"`
				} `json:"adapter"`
				AlreadyCurrent bool `json:"already_current"`
			}
			if err := json.Unmarshal(raw, &result); err != nil {
				return recipes.InstallOutcome{}, err
			}
			out.ID, out.Version, out.AlreadyCurrent = result.Adapter.ID, result.Adapter.Version, result.AlreadyCurrent
			return out, nil
		},
		FailPointer: func(e scenario.Env, _ bool) error {
			if err := envFrom(e).check(); err != nil {
				return err
			}
			return fmt.Errorf("acceptance: filesystem fault %s is unavailable in the binary layer", recipes.FaultInstallPointer)
		},
		Inspect: inspectInstalledPackage,
	}
}

func installationInput(f *Fixture, spec recipes.PackageSpec) (string, error) {
	name := spec.ID + "-" + spec.Version
	if spec.NestedFile {
		name += "-nested"
	}
	if spec.Unsupported {
		name += "-foreign"
	}
	opts := inputManifest(false)
	opts.ID, opts.Version = spec.ID, spec.Version
	source, err := f.packageInput(name, opts)
	if err != nil {
		return "", err
	}
	if spec.NestedFile {
		if err := os.MkdirAll(filepath.Join(source, "lib"), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(source, "lib", "data.txt"), []byte("nested package input"), 0o600); err != nil {
			return "", err
		}
	}
	if spec.Unsupported {
		// Only the user-supplied source changes. The public installer owns platform
		// validation and must refuse before creating an installed package directory.
		m := opts.Manifest()
		otherOS := "linux"
		if runtime.GOOS == otherOS {
			otherOS = "darwin"
		}
		m.Platforms = []manifest.Platform{{OS: otherOS, Arch: runtime.GOARCH}}
		if err := m.Validate(); err != nil {
			return "", err
		}
		raw, err := json.Marshal(m)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(source, "sidera-adapter.json"), raw, 0o600); err != nil {
			return "", err
		}
	}
	return source, nil
}

func inspectInstalledPackage(e scenario.Env, id string) (recipes.InstalledPackage, error) {
	reader, err := diskReader(e)
	if err != nil {
		return recipes.InstalledPackage{}, err
	}
	return disk.InspectInstalledPackage(reader, id)
}
