//go:build darwin || linux

package adaptersetup

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// killGroupOnCancel puts the program in a process group of its own and makes the end
// of its context kill the whole group. A client installed with npm (`codex`) is a
// wrapper that starts the native program and cannot forward a SIGKILL to it, so
// killing the wrapper alone leaves the native program running, and it could still
// write the client's configuration after the setup has reported a failure.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// The group ID is the process ID: Setpgid made the program the group's leader.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
