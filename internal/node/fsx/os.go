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
	if rand == nil {
		return nil, errors.New("fsx: OpenOS needs a random source for temporary names")
	}
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

// abs maps a relative slash path to a path under the root. filepath.Localize
// is the platform's own judgement of what is a local path (on Windows it also
// refuses reserved device names such as NUL and COM1), a second line of defence
// behind CheckPath against a path that escapes the root.
func (o *osFS) abs(p string) (string, error) {
	if err := CheckPath(p); err != nil {
		return "", err
	}
	local, err := filepath.Localize(p)
	if err != nil {
		return "", &fs.PathError{Op: "path", Path: p, Err: fs.ErrInvalid}
	}
	return filepath.Join(o.root, local), nil
}

func (o *osFS) ReadFile(p string) ([]byte, error) {
	a, err := o.abs(p)
	if err != nil {
		return nil, err
	}
	return readFile(a, -1)
}

func (o *osFS) ReadFileLimit(p string, maxBytes int64) ([]byte, error) {
	if maxBytes < 0 {
		return nil, &fs.PathError{Op: "read", Path: p, Err: fs.ErrInvalid}
	}
	a, err := o.abs(p)
	if err != nil {
		return nil, err
	}
	return readFile(a, maxBytes)
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
		entries[i] = Entry{Name: de.Name(), IsDir: de.IsDir(), IsSymlink: de.Type()&fs.ModeSymlink != 0}
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
		OwnerOnly: fi.Mode().Type()&^fs.ModeDir == 0 && ownerOnly(a, fi),
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

// readRegularFile checks the opened object, so following a link or replacing a
// name between the open and the check cannot turn the read into a device read.
func readRegularFile(abs string, maxBytes int64) ([]byte, error) {
	f, err := openRead(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &fs.PathError{Op: "read", Path: abs, Err: ErrNotRegular}
	}
	if maxBytes < 0 {
		return readAllWithSize(f, info.Size())
	}
	tooLarge := &fs.PathError{Op: "read", Path: abs, Err: ErrTooLarge}
	if info.Size() > maxBytes {
		return nil, tooLarge
	}
	data, err := readAllWithSize(io.LimitReader(f, maxBytes), info.Size())
	if err != nil {
		return nil, err
	}
	// The file may grow after fstat. Probe once beyond the limit, without
	// overflowing maxBytes or reading the rest of a growing file into memory.
	var extra [1]byte
	n, err := f.Read(extra[:])
	if n != 0 {
		return nil, tooLarge
	}
	if err != nil && err != io.EOF {
		return nil, err
	}
	return data, nil
}

// readAllWithSize uses the opened handle's size as a hint, never an EOF check.
func readAllWithSize(r io.Reader, size int64) ([]byte, error) {
	capacity := int(size) + 1 // leave room to observe EOF for an unchanged file
	if int64(int(size)) != size || capacity <= 0 {
		capacity = 0 // a size that cannot fit in an int is not an allocation hint
	}
	data := make([]byte, capacity)
	n, err := io.ReadFull(r, data)
	data = data[:n]
	switch err {
	case io.EOF, io.ErrUnexpectedEOF:
		return data, nil
	case nil:
		// A zero reported size or growth can fill the hint before EOF.
		tail, err := io.ReadAll(r)
		return append(data, tail...), err
	default:
		return data, err
	}
}
