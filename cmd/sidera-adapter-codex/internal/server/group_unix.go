//go:build !windows

package server

import (
	"os"
	"syscall"
)

// signalGroup sends the signal to the whole process group of the server. The
// server's pid is the id of its group: process.Start gives it a session of its own.
func signalGroup(p *os.Process, kill bool) error {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	return syscall.Kill(-p.Pid, sig)
}

// groupGone reports whether no process of the group is left.
func groupGone(pid int) bool { return syscall.Kill(-pid, 0) == syscall.ESRCH }
