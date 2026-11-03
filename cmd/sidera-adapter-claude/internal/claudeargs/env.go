package claudeargs

import (
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
