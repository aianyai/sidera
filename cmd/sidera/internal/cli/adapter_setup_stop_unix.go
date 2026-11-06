//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// askToStop makes the end of cmd's context a SIGTERM to the program instead of a kill,
// so that a setup program drives a client in a process group of its own can stop it
// first: a kill would leave that client running. cmd.WaitDelay then kills the program
// that does not stop in time.
func askToStop(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
}
