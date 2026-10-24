package keys_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/internal/node/keys"
)

// replaceKeyWithLink replaces the identity key by a symbolic link to a copy of
// it outside keys/ whose mode is targetMode, and returns the link's path.
func replaceKeyWithLink(t *testing.T, r *realRig, targetMode os.FileMode) string {
	t.Helper()
	key := filepath.Join(r.root, keys.IdentityKeyPath)
	data, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(r.root), "exposed.key")
	if err := os.WriteFile(outside, data, targetMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, targetMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, key); err != nil {
		t.Skipf("the account cannot create symbolic links: %v", err)
	}
	return key
}

// The permission check looks at the entry itself and reading follows a link, so
// the bits of a link say nothing about who can read what it points to. A key
// file that is a link is refused whatever it points to, and is never read.
func TestAKeyFileThatIsASymlinkIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name       string
		targetMode os.FileMode
	}{
		{"link to a file others can read", 0o644},
		{"link to a private file", 0o600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRealRig(t)
			r.initialize(t)
			replaceKeyWithLink(t, r, tc.targetMode)

			_, err := keys.Load(r.w)
			if !errors.Is(err, keys.ErrPermissionsTooBroad) {
				t.Fatalf("Load = %v, want ErrPermissionsTooBroad", err)
			}
			asFileError(t, err, keys.IdentityKeyPath)
			err = keys.CheckPermissions(r.w.Reader())
			if !errors.Is(err, keys.ErrPermissionsTooBroad) {
				t.Fatalf("CheckPermissions = %v, want ErrPermissionsTooBroad", err)
			}
			asFileError(t, err, keys.IdentityKeyPath)
		})
	}
}
