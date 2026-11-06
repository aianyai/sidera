package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// The platforms setup was run with are kept in a file of the adapter's own beside the
// record, which has no place for them. A check and a refresh get no options (`sidera update`
// passes none, and a person who runs `--check` does not repeat them), and without the file
// they would read other platforms than the ones setup advised on. The file is written when
// setup records a Skill, adds to what an earlier run named, and goes with the record. It
// only steers what a check reads, so one that cannot be decoded counts as absent and is
// written again by the next setup.
const platformsFile = "platforms.json"

type rememberedPlatforms struct {
	Platforms []string `json:"platforms"`
}

func platformsPath(stateDir string) string {
	return filepath.Join(filepath.Dir(adaptersetup.RecordPath(stateDir)), platformsFile)
}

// readPlatforms is the platforms an earlier setup was run with; none when there is no file
// or it is not one.
func readPlatforms(stateDir string) []string {
	data, err := os.ReadFile(platformsPath(stateDir))
	if err != nil {
		return nil
	}
	var r rememberedPlatforms
	if json.Unmarshal(data, &r) != nil {
		return nil
	}
	return r.Platforms
}

func writePlatforms(stateDir string, platforms []string) error {
	data, err := json.Marshal(rememberedPlatforms{Platforms: platforms})
	if err != nil {
		return err
	}
	return adaptersetup.WritePrivateFile(platformsPath(stateDir), data)
}

func removePlatforms(stateDir string) error {
	if err := os.Remove(platformsPath(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// withValue is the sorted list with v in it once.
func withValue(list []string, v string) []string {
	return slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(list), v))))
}
