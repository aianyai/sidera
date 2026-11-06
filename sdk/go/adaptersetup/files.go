package adaptersetup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Digest is the text a record keeps of a file's content: "sha256:" and the hex of
// the SHA-256 of the bytes.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ErrNoStateDir is wrapped by the error RequireStateDir returns.
var ErrNoStateDir = errors.New("the instance's state directory does not exist")

// RequireStateDir checks that the instance's state directory is there. Setup
// writes under it but never makes it: the daemon makes it, with the checks it
// applies to everything in it, when the instance first runs. A directory made here
// would be made without them, and a setup of an instance that never ran has no
// state to keep.
func RequireStateDir(stateDir string) error {
	info, err := os.Stat(stateDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w (%s): enable the instance and start the daemon so that it runs once, then run setup again", ErrNoStateDir, stateDir)
	}
	return nil
}

// WritePrivateFile writes data to path so that a reader sees the old content or
// the new, never a part: it writes a file of its own beside path, flushes it and
// renames it over path. The file is readable by its owner alone (mode 0600), and
// so are the directories made above it (0700). On Windows a mode has no effect and
// the file has the access its directory gives it, which for a state directory is the
// owner's alone. The rename is not flushed to the directory: a crash can lose the
// write, and setup is run again.
func WritePrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return replaceFile(path, data, 0o600, nil)
}

// replaceFile writes data to a new file beside path with the given mode and
// renames it over path. before, when not nil, runs after the data is written and
// before the rename; an error from it leaves path as it was.
func replaceFile(path string, data []byte, mode os.FileMode, before func() error) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if before != nil {
		if err = before(); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), path)
}
