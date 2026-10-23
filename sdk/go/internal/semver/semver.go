// Package semver parses and orders Semantic Versioning 2.0.0 versions, which
// the adapter manifest uses for the adapter version and for the closed ranges of
// runtime versions it has been tested against.
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

// Compare returns -1, 0 or +1 as a has lower, equal or higher precedence than b.
func Compare(a, b Version) int {
	for _, d := range [3][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
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
		if c := comparePre(a.Pre[i], b.Pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}

func numeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// comparePre orders two pre-release identifiers: numeric ones numerically (as
// digit strings, which never overflow), numeric below alphanumeric, the rest
// in ASCII order.
func comparePre(a, b string) int {
	an, bn := numeric(a), numeric(b)
	switch {
	case an && bn:
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}
