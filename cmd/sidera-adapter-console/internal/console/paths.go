package console

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/aianyai/sidera/internal/datahome"
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

// resolveHome picks the data directory the way the sidera command does, with the
// same function: the flag, then SIDERA_HOME, then the default of the platform,
// made absolute.
func resolveHome(flagValue string, getenv func(string) string) (string, error) {
	return datahome.Resolve(flagValue, datahome.Host{OS: runtime.GOOS, Getenv: getenv, UserHomeDir: os.UserHomeDir, Lstat: os.Lstat})
}
