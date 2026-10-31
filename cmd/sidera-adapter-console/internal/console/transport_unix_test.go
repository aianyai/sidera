//go:build !windows

package console

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestListenTakesOverAStaleSocketAndLeavesOtherFilesAlone(t *testing.T) {
	dir, err := os.MkdirTemp("", "cn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, socketName)

	// A socket a dead adapter left behind.
	old, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	old.(*net.UnixListener).SetUnlinkOnClose(false)
	old.Close()
	l, err := listenLocal(dir)
	if err != nil {
		t.Fatalf("listening over a stale socket: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("socket %v, %v, want mode 0600", info, err)
	}
	l.Close()

	// Anything else at the path is not ours to remove.
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenLocal(dir); err == nil {
		t.Error("listening over a regular file succeeded")
	}
	if data, _ := os.ReadFile(path); string(data) != "data" {
		t.Errorf("the file was changed: %q", data)
	}
}
