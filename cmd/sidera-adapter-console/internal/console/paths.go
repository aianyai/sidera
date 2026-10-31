package console

import (
	"fmt"
	"os"
	"path/filepath"
)

// stateDir is where the daemon keeps an instance's private state:
// adapter-state/<instance> in the data directory, as the adapter-host reference
// defines it. The adapter is given the directory in initialize; the client has
// only the data directory and the instance name, so it derives the same path.
// The SDK does not export the layout and an adapter imports nothing outside it, so
// this is the one place the console program spells it.
func stateDir(home, instance string) string {
	return filepath.Join(home, "adapter-state", instance)
}

// resolveHome picks the data directory the way the sidera command does: the flag,
// then SIDERA_HOME, then ~/.sidera, made absolute.
func resolveHome(flagValue string, getenv func(string) string) (string, error) {
	home := flagValue
	if home == "" {
		home = getenv("SIDERA_HOME")
	}
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding the data directory: %w", err)
		}
		home = filepath.Join(user, ".sidera")
	}
	return filepath.Abs(home)
}
