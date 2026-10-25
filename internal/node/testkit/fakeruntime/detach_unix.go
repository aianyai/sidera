//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package fakeruntime

import (
	"os/exec"
	"syscall"
)

// detach makes the command a session leader, so that neither the end of the
// adapter nor a signal sent to its process group reaches the runtime.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
