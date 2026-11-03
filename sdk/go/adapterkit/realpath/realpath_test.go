package realpath_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterkit/realpath"
)

// root returns a directory whose own path is already resolved, so that the paths a
// test builds under it are the ones Resolve should return.
func root(t *testing.T) string {
	t.Helper()
	dir, err := realpath.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveFollowsASymlink(t *testing.T) {
	dir := root(t)
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	for _, path := range []string{link, filepath.Join(link, "file")} {
		got, err := realpath.Resolve(path)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", path, err)
		}
		want := target
		if path != link {
			want = filepath.Join(target, "file")
		}
		if got != want {
			t.Errorf("Resolve(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestResolveMakesARelativePathAbsolute(t *testing.T) {
	dir := root(t)
	t.Chdir(dir)
	got, err := realpath.Resolve(".")
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("Resolve(.) = %q, want %q", got, dir)
	}
}

func TestResolveOfAMissingPathIsNotExist(t *testing.T) {
	_, err := realpath.Resolve(filepath.Join(root(t), "missing"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Resolve of a missing path = %v, want an error that is fs.ErrNotExist", err)
	}
}
