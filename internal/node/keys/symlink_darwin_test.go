package keys_test

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/aianyai/sidera/internal/node/keys"
)

// macOS lets the owner set the mode bits of the link itself (chmod -h), and
// that is what a check of the entry sees. A link made 0600 that points at a
// world-readable copy of a key must still be refused.
func TestAKeyFileThatIsASymlinkWithOwnerOnlyBitsIsRefused(t *testing.T) {
	r := newRealRig(t)
	r.initialize(t)
	key := replaceKeyWithLink(t, r, 0o644)
	if err := unix.Fchmodat(unix.AT_FDCWD, key, 0o600, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(key); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("link mode %v, %v; want 0600, or the test does not exercise the case", fi.Mode(), err)
	}

	if _, err := keys.Load(r.w); !errors.Is(err, keys.ErrPermissionsTooBroad) {
		t.Errorf("Load = %v, want ErrPermissionsTooBroad", err)
	}
	if err := keys.CheckPermissions(r.w.Reader()); !errors.Is(err, keys.ErrPermissionsTooBroad) {
		t.Errorf("CheckPermissions = %v, want ErrPermissionsTooBroad", err)
	}
}
