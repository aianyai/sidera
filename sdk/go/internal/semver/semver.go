// Package semver parses Semantic Versioning 2.0.0 versions, which the adapter
// manifest uses for the adapter version. Nothing orders them: the daemon never
// compares versions of an adapter, it only installs the one it is given.
//
// Each of major, minor and patch has at most nine digits so that comparison
// never overflows; versions beyond that are not real releases and are refused.
package semver

import (
	"errors"
	"regexp"
	"strings"
)

// MaxLen is the longest version text accepted.
const MaxLen = 64

// Pattern is the accepted syntax as a regular expression in the subset shared by
// RE2 and ECMAScript, so the same text appears in the JSON Schema.
const Pattern = `^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})` +
	`(?:-((?:0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
	`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`

var re = regexp.MustCompile(Pattern)

// ErrInvalid reports text that is not an accepted version.
var ErrInvalid = errors.New("semver: invalid version")

// Version is a parsed version. Build metadata is dropped because it takes no
// part in precedence.
type Version struct {
	Major, Minor, Patch int
	// Pre holds the dot-separated pre-release identifiers, empty for a release.
	Pre []string
}

// Parse parses text.
func Parse(s string) (Version, error) {
	if len(s) > MaxLen {
		return Version{}, ErrInvalid
	}
	m := re.FindStringSubmatch(s)
	if m == nil {
		return Version{}, ErrInvalid
	}
	v := Version{Major: atoi(m[1]), Minor: atoi(m[2]), Patch: atoi(m[3])}
	if m[4] != "" {
		v.Pre = strings.Split(m[4], ".")
	}
	return v, nil
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
