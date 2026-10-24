//go:build unix

package fsx_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
)

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestOSUnixCreatesFilesAndDirectoriesWithTheDocumentedModes(t *testing.T) {
	fsys, root := openTemp(t)
	plain, err := fsys.CreateTemp(tx, ".", fsx.ModePrivate)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	exec, err := fsys.CreateTemp(tx, ".", fsx.ModePrivateExec)
	if err != nil {
		t.Fatal(err)
	}
	defer exec.Close()
	appended, err := fsys.OpenAppend(tx, "log")
	if err != nil {
		t.Fatal(err)
	}
	defer appended.Close()
	if err := fsys.Mkdir(tx, "d"); err != nil {
		t.Fatal(err)
	}
	lock, err := fsys.Lock("daemon.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock()

	for path, want := range map[string]os.FileMode{
		plain.Name(): 0o600, exec.Name(): 0o700, "log": 0o600, "d": 0o700, "daemon.lock": 0o600,
	} {
		if got := modeOf(t, filepath.Join(root, path)); got != want {
			t.Errorf("%s has mode %o, want %o", path, got, want)
		}
	}
}

func TestOSUnixOwnerOnlyFollowsThePermissionBits(t *testing.T) {
	fsys, root := openTemp(t)
	if err := fsx.Replace(fsys, tx, "key", []byte("secret"), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
	if err := fsx.MakeDir(fsys, tx, "keys"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"key", "keys"} {
		abs := filepath.Join(root, path)
		info, err := fsys.Stat(path)
		if err != nil || !info.OwnerOnly {
			t.Fatalf("Stat(%s) = %+v, %v; want OwnerOnly for a fresh private entry", path, info, err)
		}
		base := modeOf(t, abs)
		for _, extra := range []os.FileMode{0o040, 0o004, 0o020, 0o002, 0o070, 0o007} {
			if err := os.Chmod(abs, base|extra); err != nil {
				t.Fatal(err)
			}
			if info, err := fsys.Stat(path); err != nil || info.OwnerOnly {
				t.Errorf("%s with mode %o: OwnerOnly = %v (err %v), want false", path, base|extra, info.OwnerOnly, err)
			}
		}
		if err := os.Chmod(abs, base); err != nil {
			t.Fatal(err)
		}
		if info, _ := fsys.Stat(path); !info.OwnerOnly {
			t.Errorf("%s restored to %o: OwnerOnly = false", path, base)
		}
	}
}

func TestOSUnixLockConflictsAcrossInstancesAndIsReleasedOnUnlock(t *testing.T) {
	root := t.TempDir()
	a, err := fsx.OpenOS(root, &countingReader{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := fsx.OpenOS(root, &countingReader{})
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := a.Lock("daemon.lock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Lock("daemon.lock"); !errors.Is(err, fsx.ErrLocked) {
		t.Errorf("Lock through a second instance = %v, want fsx.ErrLocked", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	unlockB, err := b.Lock("daemon.lock")
	if err != nil {
		t.Fatalf("Lock after release: %v", err)
	}
	unlockB()
}

func TestMakeRootCreatesAnOwnerOnlyDirectoryAndRefusesAnExistingOne(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, ".sidera")
	if err := fsx.MakeRoot(root); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, root); got != 0o700 {
		t.Errorf("root has mode %o, want 700", got)
	}
	fsys, err := fsx.OpenOS(root, &countingReader{})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := fsys.Stat("."); err != nil || !info.IsDir || !info.OwnerOnly {
		t.Errorf("Stat(root) = %+v, %v; want an owner-only directory", info, err)
	}
	if err := fsx.MakeRoot(root); !errors.Is(err, os.ErrExist) {
		t.Errorf("MakeRoot of an existing directory = %v, want os.ErrExist", err)
	}
	if err := fsx.MakeRoot(filepath.Join(parent, "missing", ".sidera")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("MakeRoot without a parent = %v, want os.ErrNotExist", err)
	}
}
