package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/aianyai/sidera/internal/datahome"
)

// programPath is the absolute path of this program as the commands that name it use
// it: the program as it was launched, with links kept, and only when the launch path is
// not known what the system reports for the running executable, made absolute.
//
// It is the file `update` replaces in place. A client's configuration that names it
// (the MCP server a setup registers) therefore stays valid across an update, a link
// that a package manager keeps pointing at the current version keeps doing so, and
// setup and a later refresh pass the same string.
// The system's own answer can differ from it (on Linux it has the links resolved, and
// then names the file behind a link that a package manager repoints), which is why it
// is only the fallback.
func (c *command) programPath() (string, error) {
	if c.LaunchErr == nil && c.Launch.Program != "" {
		return c.Launch.Program, nil
	}
	path, err := c.Executable()
	if err == nil && !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
	}
	if err != nil {
		return "", &failure{"cannot find the program that is running: " + err.Error()}
	}
	return path, nil
}

// warnIfTemporary warns when the program at exe is in a temporary directory, as the
// program that `go run` builds is: it is deleted when the command ends, so the path
// that whatWontLast names would not last.
func (c *command) warnIfTemporary(exe, whatWontLast string) {
	for _, dir := range []string{os.TempDir(), c.Getenv("GOTMPDIR")} {
		if rel, err := filepath.Rel(dir, exe); dir != "" && err == nil && filepath.IsLocal(rel) {
			io.WriteString(c.errOut, c.errOut.Warn(fmt.Sprintf("This program runs from %s, a temporary directory, so %s will not last.\nRun the command from an installed sidera.", exe, whatWontLast))+"\n")
			return
		}
	}
}

// isDefaultDataDir reports whether home is where a node of this user keeps its data
// when neither --home nor SIDERA_HOME says otherwise.
func (c *command) isDefaultDataDir(home string) bool {
	byDefault, err := datahome.Resolve("", datahome.Host{
		OS: runtime.GOOS, UserHomeDir: os.UserHomeDir, Lstat: os.Lstat,
		Getenv: func(name string) string {
			if name == datahome.Env {
				return ""
			}
			return c.Getenv(name)
		},
	})
	return err == nil && home == byDefault
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellWord quotes s for the shell a person pastes the command into on the given
// platform: always double quotes on Windows, where they are safe in both cmd and
// PowerShell for every character a user name may hold (', &, ( and ) among them), and
// single quotes elsewhere unless the word is plain.
func shellWord(goos, s string) string {
	if goos == "windows" {
		return `"` + s + `"`
	}
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
