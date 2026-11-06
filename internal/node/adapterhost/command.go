package adapterhost

import (
	"path/filepath"

	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// PackageDir is the directory of the installed package of an adapter, as an
// operating-system path: the directory of the version and revision the adapter's
// pointer names, in the data directory. It is the one definition; the launcher runs
// the adapter from it, and the engine resolves the setup program from it.
func PackageDir(home string, a config.Adapter) string {
	return filepath.Join(home, filepath.FromSlash(a.Dir()))
}

// Command is the program and the arguments that run a program of the manifest (its
// entry, or its setup) from the installed package in packageDir: the program is
// the manifest's path, which is relative to the package and written with slashes,
// joined to the package directory. It is the one definition of what a manifest's
// program names on this machine.
func Command(packageDir string, p manifest.Entry) (program string, args []string) {
	return filepath.Join(packageDir, filepath.FromSlash(p.Program)), p.Args
}
