//go:build !windows

package server

import (
	"os"
	"syscall"
)

// detachAttr starts the server in a session of its own: it must not receive the
// signals aimed at the adapter's process group, and its pid is then the id of the
// process group the cleanup signals. breakaway has no meaning here.
func detachAttr(breakaway bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// breakawayRefused reports whether starting with the breakaway flag failed because
// the job the adapter runs in forbids it; never on this platform.
func breakawayRefused(error) bool { return false }

// signalGroup sends the signal to the whole process group of the server, or to
// the process alone where groups do not exist.
func signalGroup(p *os.Process, kill bool) error {
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	return syscall.Kill(-p.Pid, sig)
}

// groupGone reports whether no process of the group is left.
func groupGone(pid int) bool { return syscall.Kill(-pid, 0) == syscall.ESRCH }
