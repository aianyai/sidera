package inproc

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"sort"
	"strings"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

var errPointerFailure = errors.New("acceptance: recoverable current pointer failure")

func newInstallOps() recipes.InstallOps {
	failures := 0
	return recipes.InstallOps{
		Install: func(e scenario.Env, id admin.OperationID, spec recipes.PackageSpec) (recipes.InstallOutcome, error) {
			h := e.(*harness.H)
			node := h.Engine()
			source := packageSource(spec)
			cmd := engine.ManagementCommand{Key: admin.KeyFor(admin.LocalOwner(), id), Fingerprint: sha256.Sum256([]byte("adapters.install " + source)), Name: "adapters.install",
				Operation: engine.InstallAdapterOp(source, func(v engine.AdapterInstalled) ([]byte, error) { return json.Marshal(v) })}
			out, err := node.Execute(e.Ctx(), cmd)
			if reason, ok := engine.ReasonOf(err); ok && reason == engine.ReasonPlatformUnsupported {
				if _, found, readErr := node.OperationResult(e.Ctx(), cmd.Key); readErr != nil || found {
					return recipes.InstallOutcome{}, fmt.Errorf("refused install stored a result: found=%v err=%v", found, readErr)
				}
				return recipes.InstallOutcome{Code: "platform_unsupported"}, nil
			}
			if errors.Is(err, errPointerFailure) {
				if node.Phase() != engine.PhaseRunning {
					return recipes.InstallOutcome{}, fmt.Errorf("recoverable install stopped the engine: %w", err)
				}
				if _, found, readErr := node.OperationResult(e.Ctx(), cmd.Key); readErr != nil || found {
					return recipes.InstallOutcome{}, fmt.Errorf("failed install stored a result: found=%v err=%v", found, readErr)
				}
				return recipes.InstallOutcome{Code: "pointer_failed"}, nil
			}
			if err != nil {
				return recipes.InstallOutcome{}, err
			}
			stored, found, err := node.OperationResult(e.Ctx(), cmd.Key)
			if err != nil {
				return recipes.InstallOutcome{}, err
			}
			if !found || !bytes.Equal(stored, out.Result) {
				return recipes.InstallOutcome{}, fmt.Errorf("%w: missing durable result", recipes.ErrInstallationEffect)
			}
			again, err := node.Execute(e.Ctx(), cmd)
			if err != nil {
				return recipes.InstallOutcome{}, err
			}
			if !again.Replayed || again.Stop != out.Stop || !bytes.Equal(again.Result, out.Result) {
				return recipes.InstallOutcome{}, fmt.Errorf("%w: replay differs from actual installation result", recipes.ErrInstallationEffect)
			}
			var installed engine.AdapterInstalled
			if err := json.Unmarshal(out.Result, &installed); err != nil {
				return recipes.InstallOutcome{}, err
			}
			return recipes.InstallOutcome{ID: installed.ID, Version: installed.Version, AlreadyCurrent: installed.AlreadyCurrent, Replayed: out.Replayed}, nil
		},
		FailPointer: func(e scenario.Env, cleanup bool) error {
			h := e.(*harness.H)
			// These ordinals follow the recipe's first install and two failed upgrades.
			failures++
			h.Faults().Fail(fsxtest.Match{Tx: "adapter-install", Role: "current", Step: fsx.StepCreate, Hit: 1 + failures}, errPointerFailure)
			if cleanup {
				h.Faults().Fail(fsxtest.Match{Tx: "adapter-unstage", Role: "version", Step: fsx.StepRenameDir, Hit: 2}, errors.New("acceptance: recoverable version cleanup failure"))
			}
			return nil
		},
		Inspect: func(e scenario.Env, id string) (recipes.InstalledPackage, error) {
			h := e.(*harness.H)
			raw, err := h.Disk().ReadFile(config.AdapterCurrentPath(id))
			if errors.Is(err, fs.ErrNotExist) {
				if _, statErr := h.Disk().Stat(config.AdapterDir(id)); !errors.Is(statErr, fs.ErrNotExist) {
					return recipes.InstalledPackage{}, fmt.Errorf("%w: refused package left directory: %v", recipes.ErrInstallationEffect, statErr)
				}
				return recipes.InstalledPackage{}, nil
			}
			if err != nil {
				return recipes.InstalledPackage{}, err
			}
			current, err := config.DecodeAdapterCurrent(id, raw)
			if err != nil {
				return recipes.InstalledPackage{}, err
			}
			entries, err := h.Disk().ReadDir(config.AdapterVersionsDir(id))
			if err != nil {
				return recipes.InstalledPackage{}, err
			}
			out := recipes.InstalledPackage{Current: current}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name, ".") {
					return recipes.InstalledPackage{}, fmt.Errorf("%w: unfinished package directory %s", recipes.ErrInstallationEffect, entry.Name)
				}
				out.Versions = append(out.Versions, entry.Name)
			}
			sort.Strings(out.Versions)
			return out, nil
		},
	}
}

func packageSource(s recipes.PackageSpec) string {
	name := s.ID + "/" + s.Version
	if s.NestedFile {
		name += "/nested"
	}
	if s.Unsupported {
		name += "/foreign"
	}
	return name
}

func installationOptions() harness.Options {
	return harness.Options{OpenPackage: func(source string) (fsx.Reader, error) {
		parts := strings.Split(source, "/")
		if len(parts) != 2 && len(parts) != 3 {
			return nil, fmt.Errorf("acceptance: invalid package source %q", source)
		}
		manifest := harness.AdapterManifestOf(parts[0], 1)
		manifest.Version, manifest.Program = parts[1], "adapter"
		if len(parts) == 3 && parts[2] == "foreign" {
			otherOS := "linux"
			if runtime.GOOS == otherOS {
				otherOS = "darwin"
			}
			manifestRaw, encodeErr := manifest.Encode()
			if encodeErr != nil {
				return nil, encodeErr
			}
			manifestRaw = bytes.ReplaceAll(manifestRaw, []byte(runtime.GOOS), []byte(otherOS))
			return packageInput(manifestRaw, parts[1], false)
		}
		raw, err := manifest.Encode()
		if err != nil {
			return nil, err
		}
		return packageInput(raw, parts[1], len(parts) == 3)
	}}
}

func packageInput(raw []byte, version string, nested bool) (fsx.Reader, error) {
	pkg := fsxtest.NewMem()
	tag := fsx.Tag{Tx: "package-input", Role: "file"}
	files := map[string][]byte{"sidera-adapter.json": raw, "adapter": []byte("program " + version)}
	if nested {
		if err := fsx.MakeDir(pkg, tag, "lib"); err != nil {
			return nil, err
		}
		files["lib/data.txt"] = []byte("nested package input")
	}
	for name, data := range files {
		if err := fsx.Replace(pkg, tag, name, data, fsx.ModePrivate); err != nil {
			return nil, err
		}
	}
	return pkg, nil
}
