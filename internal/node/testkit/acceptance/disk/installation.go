package disk

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
)

// InspectInstalledPackage reads the current pointer and every version directory.
func InspectInstalledPackage(r fsx.Reader, id string) (recipes.InstalledPackage, error) {
	raw, err := r.ReadFile(config.AdapterCurrentPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		if _, statErr := r.Stat(config.AdapterDir(id)); !errors.Is(statErr, fs.ErrNotExist) {
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
	entries, err := r.ReadDir(config.AdapterVersionsDir(id))
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
}
