package adapterhost

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// stateRoot is the directory of the data directory under which every instance
// has a directory of its own for the adapter to keep what must outlive its
// process.
const stateRoot = "adapter-state"

// StateDir is the directory an instance's adapter keeps its own state in:
// adapter-state/<instance> in the data directory. It is the one definition; the
// host sends it in initialize and in Instance.
func StateDir(home, instance string) string {
	return filepath.Join(home, stateRoot, instance)
}

// makeStateDir makes the state directory of an instance, and adapter-state/ when
// it is missing, owner-only: fsx.MakeRoot is the one function that makes a
// directory only its owner can reach, with the protected access list on Windows.
// A directory that is there is left as it is: it is the instance's, it holds what
// the adapter put in it, and the data directory around it is already one that only
// the owner can reach. The supervisors of different instances may make
// adapter-state/ at the same time, so its existing is not an error.
func makeStateDir(home, instance string) error {
	for _, dir := range []string{filepath.Join(home, stateRoot), StateDir(home, instance)} {
		if err := fsx.MakeRoot(dir); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("making the state directory of the instance: %w", err)
		}
	}
	return nil
}
