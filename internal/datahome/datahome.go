// Package datahome is the one place that decides which directory is the node's
// data directory when the owner did not name it with a flag.
//
// The command line and the console bridge both resolve it, and a client that
// resolved a different directory than the daemon would talk to nothing, or to the
// wrong node. The package reads the machine only through Host, so every rule is
// tested on every platform.
package datahome

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// Env is the environment variable that names the data directory.
const Env = "SIDERA_HOME"

// Host is what resolution reads of the machine.
type Host struct {
	// OS is the operating system, as runtime.GOOS spells it.
	OS string
	// Getenv reads an environment variable.
	Getenv func(string) string
	// UserHomeDir is the user's home directory, as os.UserHomeDir finds it.
	UserHomeDir func() (string, error)
	// Lstat looks at a path without following a link, as os.Lstat does.
	Lstat func(string) (fs.FileInfo, error)
}

// MovedError is the refusal to resolve the default on Windows when the data
// directory is still where earlier versions kept it.
type MovedError struct {
	// Old is %USERPROFILE%\.sidera, which exists.
	Old string
	// New is %LOCALAPPDATA%\Sidera, which does not.
	New string
}

func (e *MovedError) Error() string {
	return fmt.Sprintf("the data directory of Sidera on Windows is now %s, which does not exist, and the former one, %s, does. "+
		"Nothing is moved for you. Stop the daemon and move it with: "+
		`Move-Item "$env:USERPROFILE\.sidera" "$env:LOCALAPPDATA\Sidera"`+
		", or keep it where it is by setting SIDERA_HOME to it (or passing --home)", e.New, e.Old)
}

// Resolve returns the absolute data directory: explicit when it is not empty
// (the --home flag), then SIDERA_HOME, then the default of the platform.
//
// The default is ~/.sidera, except on Windows, where it is %LOCALAPPDATA%\Sidera.
// A directory made there with a protected access list does not take on what other
// programs, sandboxes among them, add to the folders of the user's profile; the
// profile's own first-level folders do. A Windows machine that has the former
// default and not the new one gets a *MovedError and no directory: nothing is
// moved and nothing is made behind the owner's back, because a node that starts
// empty beside the one that holds its identity is worse than one that does not
// start. Neither the flag nor SIDERA_HOME is subject to the check.
func Resolve(explicit string, h Host) (string, error) {
	dir := explicit
	if dir == "" {
		dir = h.Getenv(Env)
	}
	if dir == "" {
		var err error
		if dir, err = defaultDir(h); err != nil {
			return "", err
		}
	}
	return filepath.Abs(dir)
}

func defaultDir(h Host) (string, error) {
	user, userErr := h.UserHomeDir()
	if h.OS != "windows" {
		if userErr != nil {
			return "", userErr
		}
		return filepath.Join(user, ".sidera"), nil
	}
	local := h.Getenv("LOCALAPPDATA")
	if local == "" {
		return "", errors.New("LOCALAPPDATA is not set, so the default data directory cannot be found; set SIDERA_HOME or pass --home")
	}
	dir := filepath.Join(local, "Sidera")
	if userErr != nil {
		return dir, nil // without a profile there is no former directory to have left
	}
	former := filepath.Join(user, ".sidera")
	present, err := exists(h, dir)
	if err != nil {
		return "", err
	}
	if present {
		return dir, nil
	}
	if present, err = exists(h, former); err != nil {
		return "", err
	}
	if present {
		return "", &MovedError{Old: former, New: dir}
	}
	return dir, nil
}

func exists(h Host, p string) (bool, error) {
	_, err := h.Lstat(p)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, fmt.Errorf("looking for the data directory %s: %w", p, err)
}
