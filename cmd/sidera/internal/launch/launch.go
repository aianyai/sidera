// Package launch records how this program was started and starts it again.
package launch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Launch is how this process was started, captured in main before anything changes
// the working directory.
type Launch struct {
	// Program is the absolute path the program was started from: argv[0] resolved as
	// the system resolved it at start (the start directory, or PATH for a bare name),
	// with symbolic links kept. A link is what a package manager keeps pointing at the
	// current version, so starting the link again starts the new one.
	Program string
	// Args is os.Args as started, Args[0] included.
	Args []string
	// Dir is the working directory at start.
	Dir string
}

// Capture records how this process was started. Call it first in main: the daemon
// changes the working directory, which a relative argv[0] or a relative argument
// depends on.
func Capture() (Launch, error) {
	dir, err := os.Getwd()
	if err != nil {
		return Launch{}, fmt.Errorf("launch: read the working directory: %w", err)
	}
	return capture(os.Args, dir, os.Executable)
}

// capture is Capture with the process facts given. When argv[0] cannot be resolved
// to the running program, or resolves to another file (a stale name, a program
// replaced and started under the old name), the executable the system reports is
// used instead: it is what runs, though on Linux it has its links resolved.
func capture(argv []string, dir string, executable func() (string, error)) (Launch, error) {
	if len(argv) == 0 {
		return Launch{}, errors.New("launch: the program was started without arguments")
	}
	resolved, resolveErr := resolve(argv[0], dir)
	exe, exeErr := executable()
	program := exe
	switch {
	case resolveErr != nil && exeErr != nil:
		return Launch{}, fmt.Errorf("launch: find the program %q: %w", argv[0], errors.Join(resolveErr, exeErr))
	case resolveErr != nil:
	case exeErr != nil || sameFile(resolved, exe):
		program = resolved
	}
	return Launch{Program: program, Args: append([]string(nil), argv...), Dir: dir}, nil
}

// resolve makes argv[0] absolute the way starting the program did.
func resolve(arg0, dir string) (string, error) {
	switch {
	case filepath.IsAbs(arg0):
		return filepath.Clean(arg0), nil
	case strings.ContainsAny(arg0, `/`+string(filepath.Separator)):
		return filepath.Join(dir, arg0), nil
	}
	path, err := exec.LookPath(arg0)
	if errors.Is(err, exec.ErrDot) {
		err = nil // found in the start directory through a relative PATH entry
	}
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return path, nil
}

func sameFile(a, b string) bool {
	infoA, err := os.Stat(a)
	if err != nil {
		return false
	}
	infoB, err := os.Stat(b)
	return err == nil && os.SameFile(infoA, infoB)
}
