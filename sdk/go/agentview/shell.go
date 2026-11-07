package agentview

import (
	"regexp"
	"strings"
)

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// ShellWord quotes s for the shell a person pastes a command into on the given platform
// (a runtime.GOOS value): always double quotes on Windows, where they are safe in both cmd
// and PowerShell for every character a user name may hold (', &, ( and ) among them), and
// single quotes elsewhere unless the word is plain.
func ShellWord(goos, s string) string {
	if goos == "windows" {
		return `"` + s + `"`
	}
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
