//go:build windows

package launch

import (
	"errors"
	"fmt"
	"os"

	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
)

// Restartable says whether a process Replace starts would outlive this one: it
// stays in the job this process runs in, and a job that ends its processes when it
// closes, or that cannot be read, would end it with this one.
func Restartable() (ok bool, why string) {
	if process.HostDetachment().OutlivesDaemon {
		return true, ""
	}
	return false, "the job this process runs in ends its processes when it closes, or could not be read, " +
		"so a process started now would not outlive this one"
}

// Replace starts the program again, detached, with the arguments, environment and
// start directory of this process, and returns once it runs; the caller then exits.
// The new process cannot replace this one, so it is detached and this one ends. The
// standard output and error go on when they are files and to the null device
// otherwise: a pipe would tie the new process to this one.
func (l Launch) Replace() error {
	if ok, why := Restartable(); !ok {
		return fmt.Errorf("launch: restart %s: %s", l.Program, why)
	}
	_, err := process.Start(process.Spec{
		Argv:   append([]string{l.Program}, l.Args[1:]...),
		Env:    os.Environ(),
		Dir:    l.Dir,
		Stdout: ifFile(os.Stdout),
		Stderr: ifFile(os.Stderr),
	})
	// The process runs even when its identity could not be read.
	if err != nil && !errors.Is(err, process.ErrStartedUnidentified) {
		return fmt.Errorf("launch: start %s: %w", l.Program, err)
	}
	return nil
}

func ifFile(f *os.File) *os.File {
	if info, err := f.Stat(); err == nil && info.Mode().IsRegular() {
		return f
	}
	return nil
}
