package claudeargs

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// strippedNames are variables that are never passed on to a turn process: the
// runtime's own credentials (the session uses the login the owner made, never
// a key that happens to be in the adapter's environment), the markers of an
// enclosing Claude Code session, and the channel's tokens, which a session
// receives only from the daemon for its own endpoint.
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

// SanitizeEnv returns base without the variables a turn process must not
// inherit, and without entries whose name or value is not valid UTF-8.
func SanitizeEnv(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		name, _, ok := strings.Cut(kv, "=")
		switch {
		case !ok, name == "", !utf8.ValidString(kv):
		case strippedNames[name], strings.HasPrefix(name, strippedPrefix):
		default:
			out = append(out, kv)
		}
	}
	return out
}

// Environ returns the environment of a turn process: the sanitised base, then
// the extra variables (the channel's tool access and the preset's, which win
// over the base), with the directory of the sidera executable put in front of
// PATH when there is one, so that the command-line front end finds it.
func Environ(base []string, extra map[string]string, siderDir string) []string {
	out := SanitizeEnv(base)
	set := func(name, value string) {
		prefix := name + "="
		for i, kv := range out {
			if strings.HasPrefix(kv, prefix) {
				out[i] = prefix + value
				return
			}
		}
		out = append(out, prefix+value)
	}
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		set(name, extra[name])
	}
	if siderDir != "" {
		path := siderDir
		for _, kv := range out {
			if v, ok := strings.CutPrefix(kv, "PATH="); ok && v != "" {
				path = siderDir + string(filepath.ListSeparator) + v
			}
		}
		set("PATH", path)
	}
	return out
}
