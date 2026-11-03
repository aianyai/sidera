//go:build realclaude && windows

package runtime

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// holdInput returns a path that a process can open as its input and that never
// delivers a byte nor the end of the input while the test runs: the client end of
// a named pipe whose server end the test holds.
func holdInput(t *testing.T, _ string) string {
	t.Helper()
	path := fmt.Sprintf(`\\.\pipe\sidera-realclaude-%d`, os.Getpid())
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_OUTBOUND,
		windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, 1, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(h) })
	return path
}
