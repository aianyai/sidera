//go:build !windows

package childproc

import "os/exec"

// Windowless does nothing here: only Windows gives a console program a window of
// its own, see the Windows implementation. It is still called after anything that
// assigns cmd.SysProcAttr, as there.
func Windowless(*exec.Cmd) {}
