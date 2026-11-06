//go:build darwin || linux

package adaptersetup

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// interruptSignals are the signals that end an adapter's setup program: the ones a
// terminal and a process manager send. The client runs in a group of its own and gets
// none of them, so the program has to act on them for the client.
var interruptSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

// killGroupOnCancel puts the program in a process group of its own and makes the end
// of its context kill the whole group. A client installed with npm (`codex`) is a
// wrapper that starts the native program and cannot forward a SIGKILL to it, so
// killing the wrapper alone leaves the native program running, and it could still
// write the client's configuration after the setup has reported a failure.
//
// The group is a background group of the terminal, so a client that opens the terminal
// gets SIGTTIN, stops, and is killed when its time ends: it shows as a timeout.
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
