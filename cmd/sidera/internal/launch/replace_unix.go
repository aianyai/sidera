//go:build !windows

package launch

import (
	"fmt"
	"os"
	"syscall"
)

// Restartable is always true on Unix: Replace replaces this process in place.
func Restartable() (ok bool, why string) { return true, "" }

// Replace replaces this process with the program, started again as it was: the
// start directory, the arguments and the environment. The process keeps its
// number, its terminal and its standard streams. It returns only on failure.
func (l Launch) Replace() error {
	if err := os.Chdir(l.Dir); err != nil {
		return fmt.Errorf("launch: return to %s: %w", l.Dir, err)
	}
	return fmt.Errorf("launch: exec %s: %w", l.Program, syscall.Exec(l.Program, l.Args, os.Environ()))
}
