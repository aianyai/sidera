package claudeargs

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
)

// PermissionModes are the values of the preset's permission_mode, the modes
// `claude --permission-mode` accepts.
var PermissionModes = []string{"default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"}

// Efforts are the values of the preset's effort.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// Preset is the runtime preset of a Claude session: how its turn processes are
// started. It holds what the owner wrote, with no default for the permission
// mode: loosening permissions is the owner's decision to make in the preset.
type Preset struct {
	// ProjectDir is the working directory of every turn process, an absolute path.
	ProjectDir     string
	PermissionMode string
	Model          string
	Effort         string
	// AllowedTools are extra `--allowedTools` rules, after the ones for the
	// channel's tools.
	AllowedTools []string
	// AppendSystemPrompt follows the adapter's own system prompt.
	AppendSystemPrompt string
	// BackgroundWaitMS is how long a turn process waits for background tasks
	// before it exits; nil leaves the runtime's default.
	BackgroundWaitMS *int64
}

// ParsePreset reads the configuration values of a preset. It returns the preset
// as far as it could be read and one problem per value that cannot be used; the
// preset is usable when there are none. The daemon already checked the values
// against the manifest's field types, so what is left to find is the meaning:
// a mode the runtime does not know, a relative directory.
func ParsePreset(cfg map[string]json.RawMessage) (Preset, []string) {
	var p Preset
	var problems []string
	str := func(key string, dst *string) {
		raw, ok := cfg[key]
		if !ok {
			return
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			problems = append(problems, fmt.Sprintf("%s: not a string", key))
		}
	}
	str("project_dir", &p.ProjectDir)
	str("permission_mode", &p.PermissionMode)
	str("model", &p.Model)
	str("effort", &p.Effort)
	str("append_system_prompt", &p.AppendSystemPrompt)
	if raw, ok := cfg["allowed_tools"]; ok {
		if err := json.Unmarshal(raw, &p.AllowedTools); err != nil {
			problems = append(problems, "allowed_tools: not a list of strings")
		}
	}
	if raw, ok := cfg["background_wait_ms"]; ok {
		n, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil || n < 0 {
			problems = append(problems, "background_wait_ms: not a non-negative integer")
		} else {
			p.BackgroundWaitMS = &n
		}
	}

	switch {
	case p.ProjectDir == "":
		problems = append(problems, "project_dir: required")
	case !filepath.IsAbs(p.ProjectDir):
		problems = append(problems, "project_dir: not an absolute path")
	}
	switch {
	case p.PermissionMode == "":
		problems = append(problems, "permission_mode: required")
	case !slices.Contains(PermissionModes, p.PermissionMode):
		problems = append(problems, fmt.Sprintf("permission_mode: %q is not one of %v", p.PermissionMode, PermissionModes))
	}
	if p.Effort != "" && !slices.Contains(Efforts, p.Effort) {
		problems = append(problems, fmt.Sprintf("effort: %q is not one of %v", p.Effort, Efforts))
	}
	return p, problems
}

// Env is the environment the preset adds to every turn process.
func (p Preset) Env() map[string]string {
	env := map[string]string{}
	if p.BackgroundWaitMS != nil {
		env["CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS"] = strconv.FormatInt(*p.BackgroundWaitMS, 10)
	}
	return env
}
