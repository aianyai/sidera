//go:build unix

package fsx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func widenPrivateFileParent(t *testing.T, parent string) {
	t.Helper()
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
}

func TestUnixPrivateFileRejectsActualGroupPermissionsBeforeWriting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "export")
	f, err := openPrivateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Chmod(0o640); err != nil {
		t.Fatal(err)
	}
	got, err := finishPrivateFile(f, verifyPrivateFile)
	if got != nil || err == nil {
		t.Fatalf("non-private file returned %v, %v", got, err)
	}
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rejected empty file remains: %v", err)
	}
}

func assertPrivateFile(t *testing.T, f *os.File) {
	t.Helper()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !ownerOnly(f.Name(), info) || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("new file is not private: %v", info.Mode())
	}
}
