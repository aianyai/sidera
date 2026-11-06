package claudeargs

import (
	"path/filepath"
	"strings"

	"github.com/aianyai/sidera/sdk/go/adapterkit/environ"
)

// strippedNames are variables that are never passed on to a turn process: the
// runtime's own credentials (the session uses the login the owner made, never
// a key that happens to be in the adapter's environment), the markers of an
// enclosing Claude Code session, and the channel's tokens, which a session
// receives only from the daemon for its own endpoint. They are in upper case:
// environ presents names that way where the platform compares them without case.
var strippedNames = map[string]bool{
	"ANTHROPIC_API_KEY":    true,
	"ANTHROPIC_AUTH_TOKEN": true,
	"CLAUDECODE":           true,
	"CLAUDE_PROJECT_DIR":   true,
	"SIDERA_TOKEN":         true,
	"SIDERA_PROBE_TOKEN":   true,
}

// strippedPrefix covers every setting of the form CLAUDE_CODE_*: one inherited
// from an enclosing session or the owner's shell would change how the turn
// behaves in a way the preset does not show. CLAUDE_CONFIG_DIR is kept, since
// it decides where the login is.
const strippedPrefix = "CLAUDE_CODE_"

// keptName is the one CLAUDE_CODE_* variable that is passed on:
// CLAUDE_CODE_GIT_BASH_PATH tells Claude Code on Windows where Git Bash is, which
// describes the machine and not a session, and a Git outside the standard
// places cannot be found without it.
const keptName = "CLAUDE_CODE_GIT_BASH_PATH"

// stripped reports whether a turn process must not inherit the variable.
func stripped(name string) bool {
	return strippedNames[name] || (strings.HasPrefix(name, strippedPrefix) && name != keptName)
}

// SanitizeEnv returns base without the variables a turn process must not
// inherit, and without entries whose name or value is not valid UTF-8. Names are
// compared as the platform does (without case on Windows).
func SanitizeEnv(base []string) []string {
	return environ.Compose(base, environ.Spec{Drop: stripped})
}

// Environ returns the environment of a turn process: the sanitised base, then
// the extra variables (the channel's tool access and the preset's, which win
// over the base), with the directory of the sidera executable put in front of
// PATH when there is one, so that the command-line front end finds it.
func Environ(base []string, extra map[string]string, siderDir string) []string {
	return environ.Compose(base, environ.Spec{Drop: stripped, Set: extra, PathPrefix: siderDir})
}

// ConfigDir is the CLAUDE_CONFIG_DIR of an environment, the runtime's
// configuration directory; "" when it is unset or empty. The name is found as the
// platform compares names.
func ConfigDir(env []string) string {
	only := environ.Compose(env, environ.Spec{Drop: func(name string) bool { return name != "CLAUDE_CONFIG_DIR" }})
	for _, kv := range only {
		if _, v, ok := strings.Cut(kv, "="); ok {
			return v
		}
	}
	return ""
}

// ConfigDirOrDefault is the configuration directory of Claude Code for a process with
// this environment: CLAUDE_CONFIG_DIR, else .claude in home; "" when neither is known.
func ConfigDirOrDefault(env []string, home string) string {
	if dir := ConfigDir(env); dir != "" {
		return dir
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// ExecutableProblem says why the Claude executable at path cannot be used on the
// operating system goos, empty when it can. On Windows only the native claude.exe
// can: the npm wrapper claude.cmd, which a PATH lookup finds through PATHEXT, is
// started through cmd.exe, so the turn process the adapter must recognise by its
// executable is cmd.exe's child and a multi-line argument is cut at the first
// line break.
func ExecutableProblem(path, goos string) string {
	if goos == "windows" && !strings.EqualFold(filepath.Ext(path), ".exe") {
		return "claude is " + path + ", which is not a native claude.exe: a wrapper (claude.cmd) starts through cmd.exe, " +
			"which Sidera cannot recognise as a turn or give a multi-line prompt; install Claude Code with its native installer"
	}
	return ""
}
