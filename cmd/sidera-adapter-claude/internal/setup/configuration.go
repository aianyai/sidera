package setup

import (
	"path/filepath"
	"strings"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/claudeargs"
	"github.com/aianyai/sidera/sdk/go/adapterkit/environ"
)

// configVar is the variable that moves Claude Code's configuration directory.
const configVar = "CLAUDE_CONFIG_DIR"

// configuration is the Claude Code configuration an item lives in. What setup made in one
// is not in another, so it is part of what setup recorded: undo, check and refresh act on
// the configuration the items were made in, whatever the environment of that run names.
type configuration struct {
	// dir is the directory: CLAUDE_CONFIG_DIR, else .claude in the user's home. Empty when
	// neither is known.
	dir string
	// fromEnv says that CLAUDE_CONFIG_DIR names it. With the variable set Claude Code keeps
	// its user-scope settings (the MCP servers) in <dir>/.claude.json, and without it in
	// ~/.claude.json, so the default directory and the same directory named explicitly are
	// two configurations.
	fromEnv bool
}

// currentConfiguration is the configuration a process with this environment and home
// directory uses.
func currentConfiguration(env []string, home string) configuration {
	if dir := claudeargs.ConfigDir(env); dir != "" {
		return configuration{dir: dir, fromEnv: true}
	}
	return configuration{dir: claudeargs.ConfigDirOrDefault(env, home)}
}

// settings is the path of the settings.json of the configuration, empty when its
// directory is not known.
func (c configuration) settings() string {
	if c.dir == "" {
		return ""
	}
	return filepath.Join(c.dir, "settings.json")
}

// environ is the environment Claude Code is run in for this configuration: base with the
// variable set to the directory, or removed when the configuration is the default one.
func (c configuration) environ(base []string) []string {
	if c.fromEnv {
		return environ.Compose(base, environ.Spec{Set: map[string]string{configVar: c.dir}})
	}
	return environ.Compose(base, environ.Spec{Drop: func(name string) bool { return name == configVar }})
}

// A target is what the item is, then the marker, then the configuration. The what may
// hold the marker's words (a settings path below a directory called "team in prod"), and so
// may the directory; the configuration is always last, so it is read from the right.
const (
	inSeparator   = " in "
	envMarker     = inSeparator + configVar + "="
	defaultMarker = inSeparator + "the default configuration ("
)

// text names the configuration, as it appears in the target of a recorded item.
func (c configuration) text() string {
	if c.fromEnv {
		return strings.TrimPrefix(envMarker, inSeparator) + c.dir
	}
	return strings.TrimPrefix(defaultMarker, inSeparator) + c.dir + ")"
}

// target is the target of an item that lives in the configuration: what it is, where.
func (c configuration) target(what string) string { return what + inSeparator + c.text() }

// configurationOf reads the configuration from a target made by target.
func configurationOf(target string) (configuration, bool) {
	env, def := strings.LastIndex(target, envMarker), strings.LastIndex(target, defaultMarker)
	switch {
	case env >= 0 && env > def:
		return configuration{dir: target[env+len(envMarker):], fromEnv: true}, true
	case def >= 0 && strings.HasSuffix(target, ")"):
		return configuration{dir: strings.TrimSuffix(target[def+len(defaultMarker):], ")")}, true
	}
	return configuration{}, false
}
