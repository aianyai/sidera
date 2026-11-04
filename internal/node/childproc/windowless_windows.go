package childproc

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Windowless makes cmd start with a console of its own that has no window
// (CREATE_NO_WINDOW). The program and what it starts without a creation flag of
// its own share that console, so none of them gets a window. Without it a
// process that has no console, as a daemon restarted by `sidera update` has not,
// gives every console program it starts a new visible console, and with Windows
// Terminal as the default terminal that is a window. Other creation flags and
// every other field of cmd.SysProcAttr are kept. Call it after anything that
// assigns cmd.SysProcAttr, which would replace the flag.
func Windowless(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
