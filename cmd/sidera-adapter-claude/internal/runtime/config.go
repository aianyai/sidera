package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/claudeargs"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// instanceConfig holds the executables the instance was configured with, empty
// for the default (the program on PATH).
type instanceConfig struct {
	claudePath, siderPath string
}

func parseInstanceConfig(values adapterproto.ConfigValues) instanceConfig {
	str := func(key string) string {
		var s string
		if raw, ok := values[key]; ok {
			_ = json.Unmarshal(raw, &s)
		}
		return s
	}
	return instanceConfig{claudePath: str("claude_path"), siderPath: str("sidera_path")}
}

// findExecutable returns the configured executable or, without one, the program
// on PATH, as the path to start it by; "" when it cannot be found.
func (a *App) findExecutable(configured, name string) string {
	file := configured
	if file == "" {
		file = name
	}
	path, err := a.o.LookPath(file)
	if err != nil {
		return ""
	}
	return path
}

// resolvedPreset is a preset ready to start a turn with: its values, the
// working directory as a real path and the record directory derived from it.
type resolvedPreset struct {
	claudeargs.Preset
	Cwd       string
	RecordDir string
}

// presetError says why a preset cannot be used, as the failure a creation
// reports and the problems a probe lists.
type presetError struct {
	failure  adapterproto.CreateFailure
	problems []string
}

func (e *presetError) Error() string { return strings.Join(e.problems, "; ") }

// resolvePreset reads the named preset and checks what can be checked without
// starting anything: the values, the working directory and the record
// directory it leads to, under the runtime's projects directory.
func (a *App) resolvePreset(name, projectsDir string) (resolvedPreset, *presetError) {
	a.mu.Lock()
	values, ok := a.presets[name]
	a.mu.Unlock()
	if !ok {
		return resolvedPreset{}, &presetError{adapterproto.CreateFailPresetUnknown, []string{fmt.Sprintf("preset %q is not configured", name)}}
	}
	preset, problems := claudeargs.ParsePreset(values)
	if len(problems) > 0 {
		return resolvedPreset{}, &presetError{adapterproto.CreateFailPresetInvalid, problems}
	}
	cwd, err := a.o.Files.RealDir(preset.ProjectDir)
	if err != nil {
		reason := fmt.Sprintf("project_dir: %v", err)
		if errors.Is(err, fs.ErrNotExist) {
			reason = fmt.Sprintf("project_dir: %s does not exist", preset.ProjectDir)
		}
		return resolvedPreset{}, &presetError{adapterproto.CreateFailPresetInvalid, []string{reason}}
	}
	dir, err := record.Dir(projectsDir, cwd)
	if err != nil {
		return resolvedPreset{}, &presetError{adapterproto.CreateFailPresetInvalid, []string{"project_dir: " + err.Error()}}
	}
	return resolvedPreset{Preset: preset, Cwd: cwd, RecordDir: dir}, nil
}
