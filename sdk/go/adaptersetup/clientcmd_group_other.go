//go:build !darwin && !linux

package adaptersetup

import "os/exec"

// killGroupOnCancel does nothing here: the end of the context kills the program itself
// (exec's default). On Windows a process the program started survives it.
func killGroupOnCancel(*exec.Cmd) {}
