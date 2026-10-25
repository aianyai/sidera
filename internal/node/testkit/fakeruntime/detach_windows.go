//go:build windows

package fakeruntime

import (
	"os/exec"
	"syscall"
)

// detach puts the command in a process group of its own, so that a console
// control event sent to the adapter does not reach the runtime. That the runtime
// then outlives the adapter on Windows is pending verification by CI on main.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
