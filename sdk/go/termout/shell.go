package termout

import (
	"regexp"
	"strings"
)

// plainWord is what a Unix shell takes as one word without quoting.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// plainWindowsWord is what both cmd and PowerShell take as one word without quoting. It is
// narrower than plainWord because those shells give more characters a meaning: cmd expands
// %name% and splits arguments at ',' and '='; PowerShell reads a leading '@' as splatting
// or an array. The backslash is plain in both, so a path stays bare.
var plainWindowsWord = regexp.MustCompile(`^[A-Za-z0-9_+:./\\-]+$`)

// ShellWord quotes s for the shell a person pastes a command into on the given platform
// (a runtime.GOOS value). It is how a program writes a word of a command it tells the
// reader to run, so that the word reaches the command as it is, and a word that needs no
// quoting is left bare so the command reads the same everywhere. A word that does is
// wrapped in double quotes on Windows, where they are safe in both cmd and PowerShell for
// every character a user name may hold (', &, ( and ) among them), and where single quotes
// would be part of the word in cmd; elsewhere in single quotes.
func ShellWord(goos, s string) string {
	if goos == "windows" {
		if plainWindowsWord.MatchString(s) {
			return s
		}
		return `"` + s + `"`
	}
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
