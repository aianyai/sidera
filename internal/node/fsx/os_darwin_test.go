package fsx_test

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// macOS lets the owner change the mode bits of the link itself (chmod -h), and
// Lstat reports them. A link whose own bits are 0600, pointing at a
// world-readable file, must still not count as owner-only: ReadFile follows it.
func TestDarwinOwnerOnlyIsFalseForASymbolicLinkWithOwnerOnlyBits(t *testing.T) {
	fsys, root := openTemp(t)
	outside := filepath.Join(t.TempDir(), "exposed")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fchmodat(unix.AT_FDCWD, link, 0o600, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("link has mode %v after lchmod, want 0600: the test would not exercise the case", fi.Mode())
	}
	if info, err := fsys.Stat("link"); err != nil || info.OwnerOnly {
		t.Errorf("Stat(link) = %+v, %v; want OwnerOnly false for a symbolic link with mode 0600", info, err)
	}
}
