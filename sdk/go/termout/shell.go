package termout

import (
	"regexp"
	"strings"
)

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// ShellWord quotes s for the shell a person pastes a command into on the given platform
// (a runtime.GOOS value). It is how a program writes a word of a command it tells the
// reader to run, so that the word reaches the command as it is: always double quotes on
// Windows, where they are safe in both cmd and PowerShell for every character a user name
// may hold (', &, ( and ) among them), and where single quotes would be part of the word in
// cmd; elsewhere single quotes unless the word is plain.
func ShellWord(goos, s string) string {
	if goos == "windows" {
		return `"` + s + `"`
	}
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
