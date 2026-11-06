//go:build !darwin && !linux

package adaptersetup

import (
	"os"
	"os/exec"
	"syscall"
)

// interruptSignals are the signals that end an adapter's setup program.
var interruptSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// killGroupOnCancel does nothing here: the end of the context kills the program itself
// (exec's default). On Windows a process the program started survives it.
func killGroupOnCancel(*exec.Cmd) {}
