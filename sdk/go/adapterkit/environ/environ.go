// Package environ composes the environment an adapter gives a child process: the
// adapter's own, less what the child must not inherit, plus what the adapter adds.
// It has no I/O and imports the standard library only, so that a package that must
// stay pure can use it.
package environ

import (
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// Spec says how Compose changes an environment.
type Spec struct {
	// Drop reports whether to leave a variable out. It receives the name in upper
	// case on Windows, where names are compared without case, and as written
	// elsewhere, so a list of upper-case names works on both. nil drops nothing.
	Drop func(name string) bool
	// Set gives variables that replace one of the same name (compared as for Drop)
	// in place, or are appended, in name order.
	Set map[string]string
	// PathPrefix is a directory put in front of PATH (on Windows, whatever spelling
	// the environment uses, such as Path); PATH becomes just this directory when it
	// is unset or empty. Empty leaves PATH alone.
	PathPrefix string
}

// Compose returns base changed as s says. First it leaves out every pair that is
// not valid UTF-8, has no '=' or has an empty name. A leading '=' is part of the
// name on Windows (=C:), so those pairs are kept. Then it applies Drop, then Set,
// then PathPrefix: a variable Set gives is never dropped, and a PATH that Set gives
// is prefixed. The result holds each name once; a name base holds twice keeps its
// last value, as os/exec does. The result is a new slice, never nil: an empty
// environment stays empty.
func Compose(base []string, s Spec) []string {
	return compose(base, s, foldCase)
}

// compose is Compose with the case rule given, so that both rules can be
// exercised on every platform.
func compose(base []string, s Spec, fold bool) []string {
	key := func(name string) string {
		if fold {
			return strings.ToUpper(name)
		}
		return name
	}

	// Keep the last pair of each name, at the position it has in base.
	seen := make(map[string]bool, len(base))
	out := make([]string, 0, len(base)+len(s.Set)+1)
	for i := len(base) - 1; i >= 0; i-- {
		name, ok := nameOf(base[i], fold)
		if !ok || !utf8.ValidString(base[i]) {
			continue
		}
		k := key(name)
		if seen[k] {
			continue
		}
		seen[k] = true
		if s.Drop != nil && s.Drop(k) {
			continue
		}
		out = append(out, base[i])
	}
	slices.Reverse(out)

	// set replaces the pair of the name in place or appends one.
	set := func(name, value string) {
		k := key(name)
		for i, kv := range out {
			if n, _ := nameOf(kv, fold); key(n) == k {
				out[i] = name + "=" + value
				return
			}
		}
		out = append(out, name+"="+value)
	}
	names := make([]string, 0, len(s.Set))
	for name := range s.Set {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		set(name, s.Set[name])
	}

	if s.PathPrefix != "" {
		prefixed := false
		for i, kv := range out {
			n, _ := nameOf(kv, fold)
			if key(n) != "PATH" {
				continue
			}
			value := kv[len(n)+1:]
			if value != "" {
				value = s.PathPrefix + string(filepath.ListSeparator) + value
			} else {
				value = s.PathPrefix
			}
			out[i] = n + "=" + value
			prefixed = true
			break
		}
		if !prefixed {
			out = append(out, "PATH="+s.PathPrefix)
		}
	}
	return out
}

// nameOf returns the name of a pair and whether the pair has one: an '=' that
// ends a non-empty name. On Windows a leading '=' belongs to the name.
func nameOf(kv string, windows bool) (name string, ok bool) {
	from := 0
	if windows && strings.HasPrefix(kv, "=") {
		from = 1
	}
	i := strings.IndexByte(kv[from:], '=')
	if i < 0 || from+i == 0 {
		return "", false
	}
	return kv[:from+i], true
}
