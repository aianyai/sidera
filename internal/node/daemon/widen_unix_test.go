//go:build unix

package daemon_test

import (
	"os"
	"testing"
)

// widen lets the group and others reach a path: it is made readable by them, and
// enterable when it is a directory.
func widen(t testing.TB, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o644)
	if info.IsDir() {
		mode = 0o755
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
