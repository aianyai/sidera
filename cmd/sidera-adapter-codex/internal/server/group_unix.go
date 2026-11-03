//go:build !windows

package server

import (
	"syscall"

	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
)

// signalGroup sends the signal to the whole process group of the server. The
// server's pid is the id of its group: process.Start gives it a session of its own.
func signalGroup(s process.Started, kill bool) error {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	return syscall.Kill(-s.PID, sig)
}

// groupGone reports whether no process of the group is left.
func groupGone(pid int) bool { return syscall.Kill(-pid, 0) == syscall.ESRCH }
