package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterkit/realpath"
)

// RealDir is the path a turn process is started in and the record directory is
// derived from, so a link is followed and a file is not a directory.
func TestOSFilesRealDir(t *testing.T) {
	root, err := realpath.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "work")
	file := filepath.Join(root, "file")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	got, err := osFiles{}.RealDir(link)
	if err != nil || got != dir {
		t.Fatalf("RealDir(link) = %q, %v; want %q", got, err, dir)
	}
	if _, err := (osFiles{}).RealDir(file); err == nil {
		t.Fatal("a file is not a directory")
	}
}
