package fsx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCreatePrivateFileIsPrivateBeforeTheFirstWrite(t *testing.T) {
	parent := t.TempDir()
	widenPrivateFileParent(t, parent)
	p := filepath.Join(parent, "export")
	f, err := CreatePrivateFile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		t.Fatalf("new handle = %v, %v; want an empty regular file", info, err)
	}
	assertPrivateFile(t, f)
	if _, err := f.WriteString("secret"); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil || string(got) != "secret" {
		t.Fatalf("saved content = %q, %v", got, err)
	}
}

func TestCreatePrivateFileNeverReplacesAnExistingName(t *testing.T) {
	for _, kind := range []string{"file", "link", "dangling-link", "directory"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			p := filepath.Join(parent, "export")
			target := filepath.Join(parent, "target")
			if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "file":
				if err := os.WriteFile(p, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(p, 0o700); err != nil {
					t.Fatal(err)
				}
			default:
				linkTarget := target
				if kind == "dangling-link" {
					linkTarget += "-missing"
				}
				if err := os.Symlink(linkTarget, p); err != nil {
					t.Skipf("cannot create symbolic links: %v", err)
				}
			}
			before, err := os.Lstat(p)
			if err != nil {
				t.Fatal(err)
			}
			f, err := CreatePrivateFile(p)
			if f != nil || err == nil {
				t.Fatalf("existing name returned %v, %v", f, err)
			}
			after, err := os.Lstat(p)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("existing object changed: %v", err)
			}
			if kind == "dangling-link" {
				if _, err := os.Lstat(target + "-missing"); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("dangling link target was created: %v", err)
				}
			}
			for _, name := range []string{target, p} {
				if name == p && kind != "file" {
					continue
				}
				got, err := os.ReadFile(name)
				if err != nil || string(got) != "keep" {
					t.Fatalf("existing content = %q, %v", got, err)
				}
			}
		})
	}
}

func TestPrivateFileVerificationFailureClosesAndRemovesTheEmptyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "export")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("verification failed")
	got, err := finishPrivateFile(f, func(f *os.File) error {
		info, err := f.Stat()
		if err != nil || info.Size() != 0 {
			t.Fatalf("verification must precede secret writes: %v, %v", info, err)
		}
		return want
	})
	if got != nil || !errors.Is(err, want) {
		t.Fatalf("verification failure returned %v, %v", got, err)
	}
	if _, err := f.WriteString("secret"); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("rejected handle Write = %v, want closed", err)
	}
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rejected file remains: %v", err)
	}
}

func TestPrivateFileVerificationFailureIncludesCleanupFailures(t *testing.T) {
	p := filepath.Join(t.TempDir(), "export")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("verification failed")
	_, err = finishPrivateFile(f, func(f *os.File) error {
		// Fault injection: make both cleanup operations fail after verification.
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		return want
	})
	for _, cause := range []error{want, fs.ErrClosed, fs.ErrNotExist} {
		if !errors.Is(err, cause) {
			t.Errorf("verification/cleanup error %v lost %v", err, cause)
		}
	}
}
