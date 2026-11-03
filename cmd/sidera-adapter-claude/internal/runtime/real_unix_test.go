//go:build realclaude && !windows

package runtime

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// holdInput returns a path that a process can open as its input and that never
// delivers a byte nor the end of the input while the test runs: a FIFO held open
// for writing.
func holdInput(t *testing.T, dir string) string {
	t.Helper()
	fifo := filepath.Join(dir, "in")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}
	hold, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hold.Close() })
	return fifo
}
