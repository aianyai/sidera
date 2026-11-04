package adapterhost

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// StateRoot is the directory of the data directory under which every instance
// has a directory of its own for the adapter to keep what must outlive its
// process.
const StateRoot = "adapter-state"

// StateDir is the directory an instance's adapter keeps its own state in:
// adapter-state/<instance> in the data directory. It is the one definition; the
// host sends it in initialize and in Instance.
func StateDir(home, instance string) string {
	return filepath.Join(home, filepath.FromSlash(StateDirIn(instance)))
}

// StateDirIn is StateDir as a slash-separated path inside the data directory,
// which is how the storage layer names a path: what removes the directory with
// the instance asks for it here, so that the path has one definition.
func StateDirIn(instance string) string {
	return StateRoot + "/" + instance
}

// makeStateDir makes the state directory of an instance, and adapter-state/ when
// it is missing, owner-only: fsx.MakeRoot is the one function that makes a
// directory only its owner can reach, with the protected access list on Windows.
// The supervisors of different instances may make adapter-state/ at the same
// time, so its existing is not an error.
//
// A directory that is there is judged, not trusted: it is the instance's and holds
// what the adapter put in it, but it does not inherit from the data directory, so
// the owner-only root says nothing of it, and another program (a sandbox's
// group, say) may have added an entry to it since it was made. It is refused,
// naming the path, the trustee and the rights, like the same finding at the start
// of the daemon, and the files in it are judged with it, since the adapter keeps
// a token there. Nothing is narrowed: rewriting the list would hide from the owner
// who could reach the tokens, and only the owner knows who already read them.
func makeStateDir(home, instance string) error {
	for _, dir := range []string{filepath.Join(home, StateRoot), StateDir(home, instance)} {
		if err := fsx.MakeRoot(dir); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("making the state directory of the instance: %w", err)
		}
	}
	r, err := fsx.OpenOSReader(home)
	if err != nil {
		return fmt.Errorf("examining the state directory of the instance: %w", err)
	}
	// Every directory of adapter-state/ is judged, since each is protected on its
	// own; the files only of this instance, whose adapter is about to start.
	mine := StateRoot + "/" + instance + "/"
	err = fsx.CheckPrivateTree(r, StateRoot, func(rel string) bool { return strings.HasPrefix(rel, mine) })
	var e *fsx.NotPrivateError
	switch {
	case err == nil:
	case errors.As(err, &e):
		return fmt.Errorf("the data directory is not private to its owner: %s %s", filepath.Join(home, filepath.FromSlash(e.Path)), e.Detail)
	default:
		return fmt.Errorf("examining the state directory of the instance: %w", err)
	}
	return nil
}
