//go:build unix

package keys_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/internal/node/keys"
)

// On the real file system: every file init creates is 0600 and the directory
// 0700, and Load refuses the first file that has any group or other permission
// bit, whichever bit it is, naming it. Nothing narrows the bits behind the
// owner's back.
func TestUnixPermissionBitsTooBroadAreRefusedNamingTheFile(t *testing.T) {
	r := newRealRig(t)
	r.initialize(t)

	for _, p := range protectedPaths {
		want := os.FileMode(0o600)
		if p == keys.Dir {
			want = 0o700
		}
		fi, err := os.Stat(filepath.Join(r.root, p))
		if err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, %v; want %v", p, fi.Mode().Perm(), err, want)
		}
	}
	if _, err := keys.Load(r.w); err != nil {
		t.Fatalf("Load of a fresh init: %v", err)
	}

	for _, p := range protectedPaths {
		good := os.FileMode(0o600)
		if p == keys.Dir {
			good = 0o700
		}
		for _, bad := range []os.FileMode{good | 0o040, good | 0o004, good | 0o020, good | 0o002, good | 0o070, good | 0o007} {
			if err := os.Chmod(filepath.Join(r.root, p), bad); err != nil {
				t.Fatal(err)
			}
			_, err := keys.Load(r.w)
			if !errors.Is(err, keys.ErrPermissionsTooBroad) {
				t.Errorf("%s at %v: Load = %v, want ErrPermissionsTooBroad", p, bad, err)
				continue
			}
			asFileError(t, err, p)
			if fi, _ := os.Stat(filepath.Join(r.root, p)); fi.Mode().Perm() != bad {
				t.Errorf("%s: Load changed the mode from %v to %v", p, bad, fi.Mode().Perm())
			}
		}
		if err := os.Chmod(filepath.Join(r.root, p), good); err != nil {
			t.Fatal(err)
		}
		if _, err := keys.Load(r.w); err != nil {
			t.Errorf("%s back at %v: Load = %v", p, good, err)
		}
	}
}

// A symbolic link in place of a key file is not an owner-only file, whatever it
// points at: refused, naming the link.
func TestUnixSymbolicLinkInPlaceOfAKeyFileIsRefused(t *testing.T) {
	r := newRealRig(t)
	r.initialize(t)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(r.root, keys.IdentityKeyPath)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := keys.Load(r.w)
	if err == nil {
		t.Fatal("Load accepted a symbolic link as identity.key")
	}
	asFileError(t, err, keys.IdentityKeyPath)
}
