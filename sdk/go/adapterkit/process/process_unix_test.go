//go:build unix

package process_test

import (
	"runtime"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func wantContainerKinds() []adapterproto.ContainerKind {
	if runtime.GOOS == "linux" {
		return []adapterproto.ContainerKind{adapterproto.ContainerProcessGroup, adapterproto.ContainerSystemdUnit}
	}
	return []adapterproto.ContainerKind{adapterproto.ContainerProcessGroup}
}

// checkGroupLeader: a started process leads a process group of its own, which is
// what a caller that signals or cleans up the group relies on.
func checkGroupLeader(t *testing.T, pid int) {
	t.Helper()
	if pgid, err := syscall.Getpgid(pid); err != nil || pgid != pid {
		t.Fatalf("Getpgid(%d) = %d, %v, want the process to lead its group", pid, pgid, err)
	}
}
