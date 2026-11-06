package cli

import "os/exec"

// askToStop does nothing here: Windows has no signal to ask a program to stop, so the
// end of the context kills it, as exec does by default.
func askToStop(*exec.Cmd) {}
