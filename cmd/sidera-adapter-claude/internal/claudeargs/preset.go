package claudeargs

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/aianyai/sidera/sdk/go/adapterkit/modelchoice"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// PermissionModes are the values of the preset's permission_mode, the modes
// `claude --permission-mode` accepts.
var PermissionModes = []string{"default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"}

// Efforts are the values of the preset's effort, lowest first. SupportedEfforts derives
// from them, so one list decides both what a preset may name and what a requester may
// choose.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// SupportedEfforts are the efforts `claude --effort` takes, which the adapter
// declares as the efforts a requester may choose.
func SupportedEfforts() []adapterproto.Effort {
	out := make([]adapterproto.Effort, len(Efforts))
	for i, e := range Efforts {
		out[i] = adapterproto.Effort(e)
	}
	return out
}

// Preset is the runtime preset of a Claude session: how its turn processes are
// started. It holds what the owner wrote, with no default for the permission
// mode: loosening permissions is the owner's decision to make in the preset.
type Preset struct {
	// ProjectDir is the working directory of every turn process, an absolute path.
	ProjectDir     string
	PermissionMode string
	// Model and Effort are the preset's own defaults, empty when it sets none. A
	// session whose requester chose runs with the chosen pair instead.
	Model  string
	Effort string
	// AllowedModels is what a requester may choose; the zero value allows no choice.
	AllowedModels modelchoice.Allowed
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
// preset is usable when there are none. The daemon checks the values against
// the manifest when they are written, but not those already stored when it began
// to, nor those of a file the owner edited by hand, so every problem is still
// reported here, those only the adapter can judge (a directory that is gone)
// among them.
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

	// The model is also reported to the daemon and kept with a session that chose.
	_, defaultProblems := modelchoice.ParseDefaults(p.Model, p.Effort, SupportedEfforts())
	problems = append(problems, defaultProblems...)
	if raw, ok := cfg[modelchoice.Key]; ok {
		allowed, err := modelchoice.ParseAllowed(raw, SupportedEfforts())
		if err != nil {
			problems = append(problems, err.Error())
		}
		p.AllowedModels = allowed
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
