// Package codexcfg turns the configuration the owner gave the adapter into what
// Codex is told: the checks on an instance and a preset, the thread-level
// configuration that gives a session its credentials, the sandbox policy and the
// front ends a preset supports. It does no I/O; the field descriptions that the
// manifest declares are here too, so that what is declared and what is read are
// one list.
package codexcfg

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"unicode/utf8"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/guidance"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// Keys of the instance configuration.
const (
	KeyCodexProgram  = "codex_program"
	KeySideraProgram = "sidera_program"
)

// Keys of a preset's configuration.
const (
	KeyCWD            = "cwd"
	KeySandbox        = "sandbox"
	KeyApprovalPolicy = "approval_policy"
	KeyApprovals      = "approvals"
	KeyModel          = "model"
	KeyEffort         = "effort"

	KeyDeveloperInstructions = "developer_instructions"
)

// MaxDeveloperInstructions is the longest developer_instructions of a preset, in
// bytes: the text goes to the model with every turn, so it is meant to be a few
// paragraphs of policy, not a document.
const MaxDeveloperInstructions = 32 * 1024

// Sandbox modes, as Codex names them.
const (
	SandboxReadOnly         = "read-only"
	SandboxWorkspaceWrite   = "workspace-write"
	SandboxDangerFullAccess = "danger-full-access"
)

// Approval policies Codex applies on its own.
const (
	ApprovalNever     = "never"
	ApprovalOnRequest = "on-request"
	ApprovalUntrusted = "untrusted"
)

// What the adapter answers when Codex asks it to approve something.
const (
	ApprovalsDeny    = "deny"
	ApprovalsApprove = "approve"
)

// Defaults of a preset.
const (
	DefaultSandbox        = SandboxWorkspaceWrite
	DefaultApprovalPolicy = ApprovalNever
	DefaultApprovals      = ApprovalsDeny
)

// Defaults of the instance: the programs are looked up in PATH.
const (
	DefaultCodexProgram  = "codex"
	DefaultSideraProgram = "sidera"
)

var (
	sandboxes       = []string{SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess}
	approvalPolicys = []string{ApprovalNever, ApprovalOnRequest, ApprovalUntrusted}
	approvals       = []string{ApprovalsDeny, ApprovalsApprove}
	efforts         = []string{"low", "medium", "high", "xhigh"}
)

func raw(v string) json.RawMessage { b, _ := json.Marshal(v); return b }

// InstanceFields describes the instance configuration for the manifest.
func InstanceFields() []manifest.Field {
	return []manifest.Field{
		{Key: KeyCodexProgram, Type: manifest.FieldString, Title: "Codex program",
			Description: "The Codex executable: a path, or a name looked up in PATH. On Windows it must be the native codex.exe, not a .cmd wrapper."},
		{Key: KeySideraProgram, Type: manifest.FieldString, Title: "Sidera program",
			Description: "The sidera executable the agent's tools run: a path, or a name looked up in PATH."},
	}
}

// PresetFields describes a preset's configuration for the manifest.
func PresetFields() []manifest.Field {
	return []manifest.Field{
		{Key: KeyCWD, Type: manifest.FieldDirectory, Title: "Project directory", Required: true,
			Description: "The directory the agent works in."},
		{Key: KeySandbox, Type: manifest.FieldEnum, Title: "Sandbox", Options: slices.Clone(sandboxes), Default: raw(DefaultSandbox),
			Description: "What the agent's commands may touch. The command-line front end needs danger-full-access: a sandbox keeps the agent's commands from reaching the daemon."},
		{Key: KeyApprovalPolicy, Type: manifest.FieldEnum, Title: "Approval policy", Options: slices.Clone(approvalPolicys), Default: raw(DefaultApprovalPolicy),
			Description: "When Codex asks for approval. With never it asks for none and refuses what the sandbox does not allow."},
		{Key: KeyApprovals, Type: manifest.FieldEnum, Title: "Approvals", Options: slices.Clone(approvals), Default: raw(DefaultApprovals),
			Description: "How the adapter answers an approval Codex asks for: approve it or deny it. Nobody is there to ask."},
		{Key: KeyModel, Type: manifest.FieldString, Title: "Model",
			Description: "The model; the owner's own Codex configuration decides when it is left out."},
		{Key: KeyEffort, Type: manifest.FieldEnum, Title: "Reasoning effort", Options: slices.Clone(efforts),
			Description: "The reasoning effort; the owner's own Codex configuration decides when it is left out."},
		{Key: KeyDeveloperInstructions, Type: manifest.FieldString, Title: "Developer instructions",
			Description: "Instructions for the agent, after the adapter's own statement of what the Sidera channel is. How the agent treats the messages it receives is decided here."},
	}
}

// Instance is the configuration of the adapter instance.
type Instance struct {
	CodexProgram  string
	SideraProgram string
}

// ParseInstance reads the instance configuration, with the defaults.
func ParseInstance(v adapterproto.ConfigValues) (Instance, error) {
	in := Instance{CodexProgram: DefaultCodexProgram, SideraProgram: DefaultSideraProgram}
	var err error
	if in.CodexProgram, err = str(v, KeyCodexProgram, in.CodexProgram); err != nil {
		return Instance{}, err
	}
	if in.SideraProgram, err = str(v, KeySideraProgram, in.SideraProgram); err != nil {
		return Instance{}, err
	}
	return in, nil
}

// Preset is a preset's configuration, checked.
type Preset struct {
	Name           string
	CWD            string
	Sandbox        string
	ApprovalPolicy string
	Approvals      string
	Model          string
	Effort         string
	// DeveloperInstructions is the owner's own text; the package function of the
	// same name puts the adapter's statement before it.
	DeveloperInstructions string
}

// ParsePreset reads a preset's configuration, with the defaults, and lists every
// problem it finds; a preset with problems is not usable.
func ParsePreset(name string, v adapterproto.ConfigValues) (Preset, []string) {
	p := Preset{Name: name}
	var problems []string
	read := func(key string, def string, options []string) string {
		s, err := str(v, key, def)
		if err != nil {
			problems = append(problems, err.Error())
			return def
		}
		if options != nil && !slices.Contains(options, s) {
			problems = append(problems, fmt.Sprintf("%s: %q is not one of %v", key, s, options))
		}
		return s
	}
	p.CWD = read(KeyCWD, "", nil)
	switch {
	case p.CWD == "":
		problems = append(problems, KeyCWD+": a project directory is required")
	case !filepath.IsAbs(p.CWD):
		problems = append(problems, KeyCWD+": the project directory must be an absolute path")
	}
	p.Sandbox = read(KeySandbox, DefaultSandbox, sandboxes)
	p.ApprovalPolicy = read(KeyApprovalPolicy, DefaultApprovalPolicy, approvalPolicys)
	p.Approvals = read(KeyApprovals, DefaultApprovals, approvals)
	p.Model = read(KeyModel, "", nil)
	p.Effort = read(KeyEffort, "", nil)
	if p.Effort != "" && !slices.Contains(efforts, p.Effort) {
		problems = append(problems, fmt.Sprintf("%s: %q is not one of %v", KeyEffort, p.Effort, efforts))
	}
	if raw, ok := v[KeyDeveloperInstructions]; ok && !utf8.Valid(raw) {
		problems = append(problems, KeyDeveloperInstructions+": not valid UTF-8")
	}
	p.DeveloperInstructions = read(KeyDeveloperInstructions, "", nil)
	if len(p.DeveloperInstructions) > MaxDeveloperInstructions {
		problems = append(problems, fmt.Sprintf("%s: %d bytes, at most %d", KeyDeveloperInstructions, len(p.DeveloperInstructions), MaxDeveloperInstructions))
	}
	return p, problems
}

func str(v adapterproto.ConfigValues, key, def string) (string, error) {
	r, ok := v[key]
	if !ok {
		return def, nil
	}
	var s string
	if err := json.Unmarshal(r, &s); err != nil {
		return "", fmt.Errorf("%s: a string is expected", key)
	}
	return s, nil
}

// Frontends are the front ends a session of the preset is set up with. The MCP
// server is started by Codex outside the sandbox, so it always reaches the daemon.
// A command run by the agent is inside the sandbox, which keeps it from connecting
// to the daemon's socket, so the command line is offered only when there is none.
func (p Preset) Frontends() []adapterproto.Frontend {
	if p.Sandbox == SandboxDangerFullAccess {
		return []adapterproto.Frontend{adapterproto.FrontendMCP, adapterproto.FrontendCLI}
	}
	return []adapterproto.Frontend{adapterproto.FrontendMCP}
}

// Approves reports whether the adapter says yes when Codex asks it to approve.
func (p Preset) Approves() bool { return p.Approvals == ApprovalsApprove }

// Sandboxed reports whether the preset's commands run in a sandbox, which is then
// a permission profile of the thread (see ThreadConfig) and not a legacy sandbox
// mode.
func (p Preset) Sandboxed() bool {
	return p.Sandbox == SandboxReadOnly || p.Sandbox == SandboxWorkspaceWrite
}

// ThreadSandbox is the `sandbox` member of thread/start and thread/resume. Only a
// preset without a sandbox sets it: when the thread's sandbox is also given as a
// legacy mode, Codex silently drops the permission profile, and with it the denial
// of the data directory.
func (p Preset) ThreadSandbox() string {
	if p.Sandboxed() {
		return ""
	}
	return p.Sandbox
}

// SandboxPolicy is the sandbox as a turn or a command is given it. A preset with a
// sandbox has none: the thread's permission profile governs its turns, and a
// per-turn policy would replace it. A command/exec cannot carry the profile, so the
// only command the adapter runs this way is the probe of a preset without a sandbox.
func (p Preset) SandboxPolicy() map[string]any {
	if p.Sandboxed() {
		return nil
	}
	return map[string]any{"type": "dangerFullAccess"}
}

// Permission profile names of the built-in profiles a preset's own profile extends.
const (
	profileWorkspace = ":workspace"
	profileReadOnly  = ":read-only"
)

// ProfileName is the name of the permission profile the adapter defines in a
// thread's configuration. It is long enough not to meet a profile of the owner's
// own, whose settings would otherwise be merged into it.
const ProfileName = "sidera-adapter-codex"

// ExpectedProfile is the permission profile a thread of the preset must report as
// active: its name and the built-in profile it extends. A preset without a sandbox
// has none.
func (p Preset) ExpectedProfile() (name, extends string, ok bool) {
	switch p.Sandbox {
	case SandboxReadOnly:
		return ProfileName, profileReadOnly, true
	case SandboxWorkspaceWrite:
		return ProfileName, profileWorkspace, true
	}
	return "", "", false
}

// Access is what a session needs to reach the daemon.
type Access struct {
	// Program is the sidera executable, as a path.
	Program string
	// Env is SIDERA_TOKEN and SIDERA_HOME, as the daemon gave them.
	Env map[string]string
	// DataDir is the data directory (SIDERA_HOME) as the operating system names it:
	// absolute, with symbolic links resolved, and existing. A sandboxed session is
	// denied every read under it, and the sandbox compares the paths a command opens
	// with this one, so a link or a path that does not exist would deny nothing.
	DataDir string
}

// ServerName is the name of the MCP server of the channel in a thread's
// configuration; the adapter also watches for it to start.
const ServerName = "sidera"

// toolTimeoutS is how long Codex lets a tool call run. It is longer than the
// longest wait the manifest allows a receive, so Codex never cuts a receive off.
const toolTimeoutS = 120

// ThreadConfig is the thread-level configuration of a session: the environment
// its commands run in and the MCP server of the channel, both carrying the
// session's credentials, and for a preset with a sandbox the permission profile
// that denies the commands every read of the data directory, which holds every
// session's token. Codex keeps none of it, so it is sent again on every resume. The
// tools of the channel are approved in advance because with an approval policy of
// never Codex fails a call that would need approval.
//
// A preset with a sandbox needs a DataDir: without one the sandbox would leave the
// data directory readable, so there is no configuration to give.
func (p Preset) ThreadConfig(a Access) (map[string]any, error) {
	env := func() map[string]any {
		m := make(map[string]any, len(a.Env))
		for k, v := range a.Env {
			m[k] = v
		}
		return m
	}
	cfg := map[string]any{
		"shell_environment_policy": map[string]any{"set": env()},
		"mcp_servers": map[string]any{
			ServerName: map[string]any{
				// The owner's own config.toml may name the same server (that is how
				// a person's session reaches Sidera), and Codex merges a thread's
				// table into it key by key, so a key left out here keeps the owner's
				// value. Every key the session depends on is therefore stated, and
				// the owner's "enabled = false" cannot switch the channel off. A
				// "url" of the owner's cannot be removed by a merge; it makes the
				// thread fail to start, and is the owner's to take out.
				"enabled":                     true,
				"command":                     a.Program,
				"args":                        []any{"mcp"},
				"env":                         env(),
				"default_tools_approval_mode": "approve",
				"tool_timeout_sec":            toolTimeoutS,
			},
		},
	}
	if name, extends, ok := p.ExpectedProfile(); ok {
		if !filepath.IsAbs(a.DataDir) {
			return nil, fmt.Errorf("the data directory %q to deny to the sandbox is not an absolute path", a.DataDir)
		}
		cfg["default_permissions"] = name
		cfg["permissions"] = map[string]any{
			name: map[string]any{
				"extends":    extends,
				"filesystem": map[string]any{a.DataDir: "deny"},
			},
		}
	}
	return cfg, nil
}

// ChannelStatement tells the agent what Sidera is and how to use it, as facts: the
// statement every adapter gives, then the sentence that is true only of Codex. It
// says nothing about how to treat a message, which is the owner's policy and is
// written in the preset. The agent has to be told because a wake-up notice only
// says that a message is waiting.
const ChannelStatement = guidance.Full + "\n\n" +
	"Prefer the tools of the `sidera` MCP server: the `sidera` command works only where the sandbox lets it reach the channel."

// DeveloperInstructions is the developer instructions of a thread: the adapter's
// statement of what the channel is, then the preset's own text. Codex keeps none
// of it with the thread, so it is sent at every start and every resume.
func DeveloperInstructions(preset string) string {
	if preset == "" {
		return ChannelStatement
	}
	return ChannelStatement + "\n\n" + preset
}
