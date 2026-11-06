//go:build !darwin && !linux

package adaptersetup

import (
	"os"
	"os/exec"
	"syscall"
)

// interruptSignals are the signals that end an adapter's setup program.
var interruptSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// killGroupOnCancel does nothing on every platform but Linux and macOS, Windows included:
// the end of the context kills the program itself (exec's default), and a process the
// program started may survive it.
func killGroupOnCancel(*exec.Cmd) {}
