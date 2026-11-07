package hostfacts

import (
	"errors"
	"io/fs"
	"strings"
)

// osReleasePaths are read in this order, and the second only when the first
// does not exist. The files are never merged. They are relative to the root of
// the file system, the form fsx takes.
var osReleasePaths = [...]string{"etc/os-release", "usr/lib/os-release"}

// linuxLabel names the Linux distribution from os-release, reading files
// through read. It is PRETTY_NAME, else NAME followed by VERSION, else "Linux".
// The error says that a file exists but could not be read; the label is then
// "Linux", because a file that is there describes the system better than the
// other one would.
func linuxLabel(read func(path string) ([]byte, error)) (string, error) {
	for _, path := range osReleasePaths {
		data, err := read(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "Linux", err
		}
		return osReleaseLabel(parseOSRelease(data)), nil
	}
	return "Linux", nil
}

func osReleaseLabel(vars map[string]string) string {
	if v := vars["PRETTY_NAME"]; v != "" {
		return v
	}
	name := vars["NAME"]
	if name == "" {
		return "Linux"
	}
	if v := vars["VERSION"]; v != "" {
		return name + " " + v
	}
	return name
}

// parseOSRelease reads the shell-style assignments of an os-release file: one
// KEY=value per line, the value bare, single-quoted or double-quoted, with
// backslash escapes, blank lines and # comments ignored. Variables are not
// expanded. A later assignment of a key replaces an earlier one; a line that is
// not a valid assignment is ignored.
func parseOSRelease(data []byte) map[string]string {
	vars := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		key, rest, ok := strings.Cut(line, "=")
		if !ok || !validVarName(key) {
			continue
		}
		if value, ok := parseShellValue(rest); ok {
			vars[key] = value
		}
	}
	return vars
}

func validVarName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range []byte(s) {
		switch {
		case c == '_', 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z':
		case i > 0 && '0' <= c && c <= '9':
		default:
			return false
		}
	}
	return true
}

// parseShellValue reads one word: quoted and bare parts joined, ending at the
// first space outside quotes. It reports false when a quote is not closed.
func parseShellValue(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ' ', '\t':
			return b.String(), true
		case '\\':
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return "", false
			}
			b.WriteString(s[i+1 : i+1+end])
			i += 1 + end
		case '"':
			i++
			closed := false
			for ; i < len(s); i++ {
				if s[i] == '"' {
					closed = true
					break
				}
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
					i++
				}
				b.WriteByte(s[i])
			}
			if !closed {
				return "", false
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}
