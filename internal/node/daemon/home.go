package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync/atomic"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
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
		return fmt.Errorf("%w: %s can be reached by others; only its owner may", ErrPermissions, home)
	}
	return nil
}

// checkPermissions is the judgment of what the owner has to be the only one to
// reach: the data directory and keys/. Both refuse before anything is written,
// before the lock is taken and before a socket is made.
func checkPermissions(r fsx.Reader, home string) error {
	if err := checkRoot(r, home); err != nil {
		return err
	}
	return keys.CheckPermissions(r)
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
