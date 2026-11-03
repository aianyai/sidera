package update

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// programFile is one program a replacement would replace.
type programFile struct {
	name   string // file name next to the running program, the target in swap
	path   string // absolute path
	source string // the program's name in the release archive
}

// findPrograms returns the programs a replacement would replace, console first and
// the running program last: a cut short replacement then leaves the running
// program at its old version, so that the next run replaces the set again. It
// refuses a place a replacement would not be right in:
//
//   - a program or a directory that is a symbolic link, because the replacement
//     would change a place other than the one shown (a package manager's link, or a
//     release program someone linked elsewhere);
//   - a program or directory owned by another user (not checked on Windows, see
//     ownedByCaller). A run under sudo would write programs that belong to root into
//     a directory of the user, and a user cannot tell the programs of root from their
//     own on the next run.
//
// The console program is replaced only when it is already there.
func (s *session) findPrograms() ([]programFile, error) {
	ext := ""
	if s.cfg.GOOS == "windows" {
		ext = ".exe"
	}
	dir := filepath.Dir(s.cfg.Program)
	if err := checkPlace(dir, true); err != nil {
		return nil, err
	}
	running := programFile{name: filepath.Base(s.cfg.Program), path: s.cfg.Program, source: programName + ext}
	if err := checkPlace(running.path, false); err != nil {
		return nil, err
	}
	console := programFile{name: consoleName + ext, path: filepath.Join(dir, consoleName+ext), source: consoleName + ext}
	// A console program that an earlier, cut short update set aside is still there:
	// the replacement puts it back first.
	there := console.path
	if _, err := os.Lstat(there); os.IsNotExist(err) {
		there += oldSuffix
	}
	switch _, err := os.Lstat(there); {
	case os.IsNotExist(err):
		s.rep.Program.ConsoleMissing = true
		return []programFile{running}, nil
	case err != nil:
		return nil, fmt.Errorf("look for %s: %w", console.path, err)
	}
	if err := checkPlace(there, false); err != nil {
		return nil, err
	}
	return []programFile{console, running}, nil
}

// checkPlace refuses a path that is a link, is not what it should be, or belongs to
// another user.
func checkPlace(path string, directory bool) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("look at %s: %w", path, err)
	}
	what := "program"
	if directory {
		what = "directory"
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("the %s %s is a symbolic link: update would replace the file it leads to, not the one you run; update it the way it was installed, or run the real file", what, path)
	case directory != fi.IsDir():
		return fmt.Errorf("%s is not a %s", path, what)
	}
	owned, owner, err := ownedByCaller(path, fi)
	if err != nil {
		return fmt.Errorf("find the owner of %s: %w", path, err)
	}
	if !owned {
		return fmt.Errorf("the %s %s is owned by %s, not by the user running update: do not run sidera update with sudo; run it as the user who owns the installation", what, path, owner)
	}
	return nil
}

// checkWritable proves the directory of the programs accepts a new file, so that a
// read-only installation is refused before anything is downloaded. The probe is
// removed at once.
func (s *session) checkWritable() error {
	dir := filepath.Dir(s.cfg.Program)
	f, err := os.CreateTemp(dir, fsx.TempPrefix+"update-")
	if err != nil {
		return fmt.Errorf("cannot write to %s, where the programs are: %w; update it as a user who can, or install the new programs by hand", dir, err)
	}
	name := f.Name()
	f.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove the probe file %s: %w", name, err)
	}
	return nil
}

func joinNames(names []string) string { return strings.Join(names, " and ") }

// absolute makes names, which are relative to dir, paths.
func absolute(dir string, names []string) []string {
	var out []string
	for _, n := range names {
		out = append(out, filepath.Join(dir, n))
	}
	return out
}

// oldSuffix is the name swap gives a program it has set aside.
const oldSuffix = ".old"

func trimOld(name string) string { return strings.TrimSuffix(name, oldSuffix) }
