// Package record knows how Claude Code keeps a session on disk: where the
// directory of a working directory's sessions is, which line of a session file
// is a given prompt, and how a turn ended as its output stream tells. It is
// pure: the caller reads the files and hands over the bytes.
package record

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// MaxDirName is the longest directory name Claude Code uses as it is. Past it
// the runtime shortens the name and adds a hash, a rule this package does not
// reproduce: a working directory whose name is longer is refused, and a shorter
// path is the cure.
const MaxDirName = 200

// ErrDirNameTooLong is wrapped by the error of Dir for a working directory whose
// record directory name exceeds MaxDirName.
var ErrDirNameTooLong = errors.New("record: directory name too long")

// Sanitize returns the name of the record directory of a working directory: the
// path in Unicode normal form C, with every character that is not an ASCII
// letter or digit replaced by a hyphen. The path is the real one, with symbolic
// links resolved, which the caller resolves.
func Sanitize(realCwd string) string {
	var b strings.Builder
	for _, r := range norm.NFC.String(realCwd) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r > 0xFFFF:
			// The runtime replaces each UTF-16 code unit, and a character outside the
			// basic plane has two.
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Dir returns the record directory of a working directory: the projects
// directory the runtime reports (`claude auth status`), then the sanitised real
// working directory. This is the directory of the session files, which is not
// the working directory.
func Dir(projectsDir, realCwd string) (string, error) {
	name := Sanitize(realCwd)
	if len(name) > MaxDirName {
		return "", fmt.Errorf("%w: %d characters for %q, at most %d", ErrDirNameTooLong, len(name), realCwd, MaxDirName)
	}
	if projectsDir == "" || realCwd == "" {
		return "", errors.New("record: no projects directory or working directory")
	}
	return filepath.Join(projectsDir, name), nil
}

// File returns the path of a session's file in its record directory.
func File(recordDir, session string) string {
	return filepath.Join(recordDir, session+".jsonl")
}
