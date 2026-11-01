package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
)

// lockFile is the instance lock of the data directory, at its root and not in
// state/: state/ can be deleted as a whole, and a lock inside it would let a
// second daemon in while it is gone.
const lockFile = "daemon.lock"

// openHome opens the file system of the data directory, and says where it does not
// exist. A data directory is made by init and by nothing else.
func openHome(cfg Config, home string) (fsx.FS, error) {
	if cfg.FS != nil {
		return cfg.FS, nil
	}
	fsys, err := fsx.OpenOS(home, cfg.TempRand)
	switch {
	case err == nil:
		return fsys, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: %s does not exist", ErrNotInitialized, home)
	}
	return nil, fmt.Errorf("daemon: opening the data directory %s: %w", home, err)
}

// checkRoot refuses a data directory that is not a directory only the owner can
// reach, and it fails closed. A symbolic link is refused whatever it points to:
// the file layer's Stat does not follow it, so it is not a directory there, and
// it could be replaced between this check and the use. Nothing is narrowed.
func checkRoot(r fsx.Reader, home string) error {
	info, err := r.Stat(".")
	switch {
	case err != nil:
		return fmt.Errorf("%w: %s cannot be examined: %v", ErrPermissions, home, err)
	case !info.IsDir:
		return fmt.Errorf("%w: %s is not a directory (a symbolic link is not accepted)", ErrPermissions, home)
	case !info.OwnerOnly:
		return notPrivate(home, ".", &fsx.NotPrivateError{Path: ".", Detail: reachedByOthers(info)})
	}
	return nil
}

// reachedByOthers is what the file layer says of a path that only its owner may
// reach, or the plain statement when it says nothing more.
func reachedByOthers(info fsx.Info) string {
	if info.Exposure != "" {
		return info.Exposure
	}
	return "can be reached by others; only its owner may"
}

// notPrivate is the refusal for a path of the data directory that others can
// reach. It names the path as the operating system does, so that the owner can
// open it, and carries what the file layer found.
func notPrivate(home, rel string, e *fsx.NotPrivateError) error {
	return fmt.Errorf("%w: %s %s", ErrPermissions, filepath.Join(home, filepath.FromSlash(rel)), e.Detail)
}

// holdsSecret says which files of the data directory are judged one by one, by
// their own access list, when the tree is checked: the files that hold a token or
// a key. state/endpoints/ keeps the token of every session endpoint in clear, and
// everything under adapter-state/ is written by an adapter, which keeps there
// what it needs to reach its runtime, such as a token (the Codex adapter's
// ws-token, the console's console.addr). The files of keys/ are judged by the keys
// package, which owns them. The other files of state/ hold no secret and rely on
// the directory that holds them, which is judged.
func holdsSecret(rel string) bool {
	return strings.HasPrefix(rel, store.EndpointsDir+"/") || strings.HasPrefix(rel, adapterhost.StateRoot+"/")
}

// checkTree refuses a data directory with a directory, or a file that holds a
// secret, that someone other than the owner can reach. The root alone is not
// enough: a directory made protected, as adapter-state/<instance> is, does not
// inherit from the root, so an entry added to the root or to it by another
// program (a sandbox's group, say) is seen only on the directory itself. It names
// the path, the trustee and the rights. Nothing is narrowed or repaired: only
// the owner knows who already read what.
func checkTree(r fsx.Reader, home string) error {
	err := fsx.CheckPrivateTree(r, ".", holdsSecret)
	var e *fsx.NotPrivateError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &e):
		return notPrivate(home, e.Path, e)
	}
	return fmt.Errorf("%w: %s cannot be examined: %v", ErrPermissions, home, err)
}

// checkPermissions is the judgment of what the owner has to be the only one to
// reach: the data directory, keys/ and the rest of the tree. All of it refuses
// before anything is written, before the lock is taken and before a socket is
// made.
func checkPermissions(r fsx.Reader, home string) error {
	if err := checkRoot(r, home); err != nil {
		return err
	}
	if err := keys.CheckPermissions(r); err != nil {
		return err
	}
	return checkTree(r, home)
}

// enterHome makes the data directory the working directory of the daemon, and so
// of every adapter process it starts, which inherits it. Without it they would run
// in whatever directory the daemon was started from, which can be removed while the
// daemon runs (a git worktree, say), and a program started in a directory that no
// longer exists fails. The data directory is the one directory the daemon owns and
// keeps. An explicit Config.FS is not the machine's file system and its home need
// not be a directory there, so it is left out. Adapters that run a native runtime
// do it in the directory their preset names, never in this one.
func (d *Daemon) enterHome() error {
	if d.cfg.FS != nil {
		return nil
	}
	if err := os.Chdir(d.home); err != nil {
		return fmt.Errorf("daemon: changing the working directory to the data directory: %w", err)
	}
	return nil
}

// instanceLock is the lock of the data directory, which the listeners ask the
// caller to assert it holds.
type instanceLock struct {
	unlock func() error
	held   atomic.Bool
}

// takeLock takes the instance lock, or says that another daemon, or an init,
// holds it.
func takeLock(fsys fsx.FS, home string) (*instanceLock, error) {
	unlock, err := fsys.Lock(lockFile)
	switch {
	case err == nil:
	case errors.Is(err, fsx.ErrLocked):
		return nil, fmt.Errorf("%w (%s)", ErrAlreadyRunning, home)
	default:
		return nil, fmt.Errorf("daemon: taking the instance lock of %s: %w", home, err)
	}
	l := &instanceLock{unlock: unlock}
	l.held.Store(true)
	return l, nil
}

// Held reports whether the lock is held: the assertion the listeners ask for.
func (l *instanceLock) Held() bool { return l != nil && l.held.Load() }

// release lets the lock go; it is safe to call twice.
func (l *instanceLock) release() error {
	if l == nil || !l.held.CompareAndSwap(true, false) {
		return nil
	}
	return l.unlock()
}
