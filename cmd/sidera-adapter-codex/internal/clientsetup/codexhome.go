package clientsetup

import (
	"path/filepath"
	"strings"

	"github.com/aianyai/sidera/sdk/go/adapterkit/environ"
)

// homeVar is the variable that moves Codex's home directory.
const homeVar = "CODEX_HOME"

// codexHome is the Codex home an item lives in: the configuration (config.toml, the
// marketplaces, the plugin cache) that Codex reads and writes. What setup made in one is
// not in another, so it is part of what setup recorded: undo, check and refresh act on the
// home the items were made in, whatever the environment of that run names.
type codexHome struct {
	// dir is the directory CODEX_HOME names, absolute; empty for the default home
	// (~/.codex). Codex takes an unset and an empty variable for the default.
	dir string
}

// currentHome is the Codex home a process with this environment uses. A relative
// directory is made absolute here, in the directory of the person: Codex is run in the
// instance's state directory, where the same text would name another directory.
func currentHome(env []string) codexHome {
	only := environ.Compose(env, environ.Spec{Drop: func(name string) bool { return name != homeVar }})
	for _, kv := range only {
		if _, dir, ok := strings.Cut(kv, "="); ok && dir != "" {
			if abs, err := filepath.Abs(dir); err == nil {
				dir = abs
			}
			return codexHome{dir: dir}
		}
	}
	return codexHome{}
}

// environ is the environment Codex is run in for this home: base with the variable set to
// the directory, or removed when the home is the default one.
func (h codexHome) environ(base []string) []string {
	if h.dir != "" {
		return environ.Compose(base, environ.Spec{Set: map[string]string{homeVar: h.dir}})
	}
	return environ.Compose(base, environ.Spec{Drop: func(name string) bool { return name == homeVar }})
}

const (
	inSeparator = " in "
	envTarget   = homeVar + "="
	// defaultTarget is how the default home is named, to a person and in a recorded target.
	defaultTarget = "the default Codex home (~/.codex)"
)

// text names the home, as it appears in the target of a recorded item.
func (h codexHome) text() string {
	if h.dir != "" {
		return envTarget + h.dir
	}
	return defaultTarget
}

// setting is how the environment of a command names the home.
func (h codexHome) setting() string {
	if h.dir != "" {
		return envTarget + h.dir
	}
	return homeVar + " unset"
}

// target is the target of an item that lives in the home: what it is, where.
func (h codexHome) target(what string) string { return what + inSeparator + h.text() }

// homeOf reads the home from a target made by target.
func homeOf(target string) (codexHome, bool) {
	_, text, ok := strings.Cut(target, inSeparator)
	switch {
	case !ok:
		return codexHome{}, false
	case text == defaultTarget:
		return codexHome{}, true
	case strings.HasPrefix(text, envTarget):
		return codexHome{dir: strings.TrimPrefix(text, envTarget)}, true
	}
	return codexHome{}, false
}
