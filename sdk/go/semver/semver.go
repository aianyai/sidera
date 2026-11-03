// Package semver parses Semantic Versioning 2.0.0 versions and orders them by
// the precedence rules of section 11 of the specification. The adapter manifest
// uses it for the adapter version, and any other reader of a version, inside or
// outside the repository, applies the same syntax and the same order because it
// calls the same code.
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

// Compare returns -1, 0 or +1 as a has lower, equal or higher precedence than b,
// by section 11 of the specification: major, minor and patch are compared as
// numbers; a version with a pre-release part precedes the same version without
// one; pre-release identifiers are compared from left to right, numeric ones as
// numbers and before alphanumeric ones, alphanumeric ones in ASCII order, and a
// shorter list precedes a longer one that begins with it. Build metadata takes
// no part, so two versions that differ only in it compare equal.
func Compare(a, b Version) int {
	for _, d := range [...]int{a.Major - b.Major, a.Minor - b.Minor, a.Patch - b.Patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		if c := compareIdentifier(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	return sign(len(a.Pre) - len(b.Pre))
}

// compareIdentifier orders two pre-release identifiers. A numeric identifier
// has no leading zero and no length limit, so it is compared by length and then
// by digits, which orders numbers without converting them.
func compareIdentifier(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		if len(a) != len(b) {
			return sign(len(a) - len(b))
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
