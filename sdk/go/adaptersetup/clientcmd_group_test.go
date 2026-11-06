//go:build darwin || linux

package adaptersetup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// A client installed by npm is a wrapper that cannot forward a kill to the program it
// started, so the end of a run has to reach the whole process group: the grandchild
// here holds no pipe, and nothing but the group's kill stops it.
func TestClientCommandTimeoutKillsTheGrandchild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	_, err := helper(t, 500*time.Millisecond, "grandchild", pidFile).Run(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want a timeout", err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	// The grandchild lives five seconds if nothing stops it. Once killed it is reaped
	// by init, so the signal 0 probe fails with ESRCH soon after.
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("process %d, a grandchild of the run, is still running after the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
