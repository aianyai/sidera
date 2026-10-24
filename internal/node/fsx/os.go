package fsx

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sync"
	"syscall"
)

// osFS is the real FS rooted at one directory. Everything platform specific is
// a small function in os_unix.go or os_windows.go; the contract (what is
// checked, what each method returns) lives here.
type osFS struct {
	root string

	randMu sync.Mutex
	rand   io.Reader
}

var _ FS = (*osFS)(nil)

// OpenOS returns the FS for the existing directory root. rand supplies the
// random part of temporary file names.
func OpenOS(root string, rand io.Reader) (FS, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, &fs.PathError{Op: "open", Path: abs, Err: syscall.ENOTDIR}
	}
	return &osFS{root: abs, rand: rand}, nil
}

// MakeRoot creates the data directory so that only its owner can reach it,
// and so that everything created inside inherits that. The parent must exist
// and the directory must not. It is called once, by init, before any
// transaction can exist.
func MakeRoot(p string) error { return makeRoot(p) }

// abs maps a relative slash path to a path under the root.
func (o *osFS) abs(p string) (string, error) {
	if err := CheckPath(p); err != nil {
		return "", err
	}
	return filepath.Join(o.root, filepath.FromSlash(p)), nil
}

func (o *osFS) ReadFile(p string) ([]byte, error) {
	a, err := o.abs(p)
	if err != nil {
		return nil, err
	}
	return readFile(a)
}

func (o *osFS) ReadDir(dir string) ([]Entry, error) {
	a, err := o.abs(dir)
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(a) // sorted by name
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, len(des))
	for i, de := range des {
		entries[i] = Entry{Name: de.Name(), IsDir: de.IsDir()}
	}
	return entries, nil
}

func (o *osFS) Stat(p string) (Info, error) {
	a, err := o.abs(p)
	if err != nil {
		return Info{}, err
	}
	fi, err := os.Lstat(a)
	if err != nil {
		return Info{}, err
	}
	return Info{
		Name:      path.Base(p),
		Size:      fi.Size(),
		IsDir:     fi.IsDir(),
		OwnerOnly: ownerOnly(a, fi),
	}, nil
}

func (o *osFS) Available(dir string) (uint64, error) {
	a, err := o.abs(dir)
	if err != nil {
		return 0, err
	}
	return available(a)
}

func permOf(m Mode) os.FileMode {
	if m == ModePrivateExec {
		return 0o700
	}
	return 0o600
}

// tempAttempts bounds the retries after an exclusive-create collision of two
// random names, which a working random source makes astronomically unlikely.
const tempAttempts = 8

func (o *osFS) CreateTemp(_ Tag, dir string, mode Mode) (File, error) {
	if _, err := o.abs(dir); err != nil {
		return nil, err
	}
	for range tempAttempts {
		var suffix [8]byte
		o.randMu.Lock()
		_, err := io.ReadFull(o.rand, suffix[:])
		o.randMu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("fsx: random source for a temporary name: %w", err)
		}
		rel := path.Join(dir, TempPrefix+hex.EncodeToString(suffix[:]))
		a, err := o.abs(rel)
		if err != nil {
			return nil, err
		}
		f, err := os.OpenFile(a, os.O_RDWR|os.O_CREATE|os.O_EXCL, permOf(mode))
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &osFile{File: f, name: rel}, nil
	}
	return nil, fmt.Errorf("fsx: no unused temporary name in %q after %d attempts", dir, tempAttempts)
}

func (o *osFS) OpenAppend(_ Tag, p string) (File, error) {
	a, err := o.abs(p)
	if err != nil {
		return nil, err
	}
	f, err := openAppend(a)
	if err != nil {
		return nil, err
	}
	return &osFile{File: f, name: p}, nil
}

func (o *osFS) Rename(_ Tag, from, to string) error {
	if err := CheckRename(from, to); err != nil {
		return err
	}
	af, _ := o.abs(from)
	at, _ := o.abs(to)
	if err := requireFile("rename", af, true); err != nil {
		return err
	}
	if err := requireFile("rename", at, false); err != nil {
		return err
	}
	return renameFile(af, at)
}

func (o *osFS) SyncDir(_ Tag, dir string) error {
	a, err := o.abs(dir)
	if err != nil {
		return err
	}
	return syncDir(a)
}

func (o *osFS) Remove(_ Tag, p string) error {
	if err := checkNotRoot(p); err != nil {
		return err
	}
	a, _ := o.abs(p)
	if err := requireFile("remove", a, true); err != nil {
		return err
	}
	return removeFile(a)
}

func (o *osFS) RemoveAll(_ Tag, dir string) error {
	if err := checkNotRoot(dir); err != nil {
		return err
	}
	a, _ := o.abs(dir)
	return removeAll(a)
}

func (o *osFS) Mkdir(_ Tag, dir string) error {
	if err := checkNotRoot(dir); err != nil {
		return err
	}
	a, _ := o.abs(dir)
	return os.Mkdir(a, 0o700)
}

func (o *osFS) RenameDir(_ Tag, from, to string) error {
	if err := CheckRename(from, to); err != nil {
		return err
	}
	af, _ := o.abs(from)
	at, _ := o.abs(to)
	if _, err := os.Lstat(at); err == nil {
		return &fs.PathError{Op: "rename", Path: to, Err: fs.ErrExist}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	fi, err := os.Lstat(af)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return &fs.PathError{Op: "rename", Path: from, Err: syscall.ENOTDIR}
	}
	return renameDir(af, at)
}

func (o *osFS) Lock(p string) (func() error, error) {
	if err := checkNotRoot(p); err != nil {
		return nil, err
	}
	a, _ := o.abs(p)
	return lockFile(a)
}

// requireFile fails if abs is a directory, so that file operations behave the
// same on every platform (os.Remove would also remove an empty directory).
// With mustExist, a missing path is an error too.
func requireFile(op, abs string, mustExist bool) error {
	fi, err := os.Lstat(abs)
	switch {
	case err == nil && fi.IsDir():
		return &fs.PathError{Op: op, Path: abs, Err: syscall.EISDIR}
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist) && !mustExist:
		return nil
	default:
		return err
	}
}

// osFile is a file of the real FS; Name is relative to the root.
type osFile struct {
	*os.File
	name string
}

func (f *osFile) Name() string { return f.name }
