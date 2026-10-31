//go:build windows

package server

import (
	"errors"
	"os"
	"syscall"
)

const (
	createNewProcessGroup  = 0x00000200
	detachedProcess        = 0x00000008
	createBreakawayFromJob = 0x01000000
	errorAccessDenied      = syscall.Errno(5)
)

// detachAttr starts the server detached from the adapter's console and process
// group, and, with breakaway, outside the job object the daemon may have put the
// adapter in, so that it outlives the adapter and the daemon.
func detachAttr(breakaway bool) *syscall.SysProcAttr {
	flags := uint32(createNewProcessGroup | detachedProcess)
	if breakaway {
		flags |= createBreakawayFromJob
	}
	return &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
}

// breakawayRefused reports whether the start failed because the job forbids
// breakaway, which is the only reason worth a second try without the flag.
func breakawayRefused(err error) bool { return errors.Is(err, errorAccessDenied) }

// signalGroup ends the server process; Windows has no process groups to signal,
// so what the server started is not reached.
func signalGroup(p *os.Process, _ bool) error { return p.Kill() }

// groupGone cannot be told on this platform.
func groupGone(int) bool { return true }
