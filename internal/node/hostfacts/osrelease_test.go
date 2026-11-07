package hostfacts

import (
	"errors"
	"io/fs"
	"maps"
	"testing"
)

func TestParseOSRelease(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]string
	}{
		{"a double-quoted value", `PRETTY_NAME="Ubuntu 24.04.1 LTS"`, map[string]string{"PRETTY_NAME": "Ubuntu 24.04.1 LTS"}},
		{"a single-quoted value is literal", `PRETTY_NAME='It is $HOME \n'`, map[string]string{"PRETTY_NAME": `It is $HOME \n`}},
		{"an unquoted value", "ID=ubuntu\nVERSION_ID=24.04", map[string]string{"ID": "ubuntu", "VERSION_ID": "24.04"}},
		{"an unquoted value ends at a space", "ID=ubuntu extra", map[string]string{"ID": "ubuntu"}},
		{"a backslash escapes a quote inside double quotes", `PRETTY_NAME="Say \"hi\""`, map[string]string{"PRETTY_NAME": `Say "hi"`}},
		{"a backslash escapes a backslash and a dollar inside double quotes", `A="a\\b\$c"`, map[string]string{"A": `a\b$c`}},
		{"a backslash before another character stays inside double quotes", `A="a\nb"`, map[string]string{"A": `a\nb`}},
		{"a backslash takes the next character outside quotes", `A=a\ b`, map[string]string{"A": "a b"}},
		{"adjacent quoted parts join", `A="a"'b'c`, map[string]string{"A": "abc"}},
		{"a comment line is skipped", "# PRETTY_NAME=nope\nID=x", map[string]string{"ID": "x"}},
		{"an indented comment line is skipped", "   # ID=nope\nID=x", map[string]string{"ID": "x"}},
		{"blank lines are skipped", "\n\nID=x\n\n", map[string]string{"ID": "x"}},
		{"a hash inside quotes is a character", `A="x # y"`, map[string]string{"A": "x # y"}},
		{"a later duplicate key wins", "ID=a\nID=b", map[string]string{"ID": "b"}},
		{"carriage returns end a line like newlines", "ID=a\r\nNAME=b\r\n", map[string]string{"ID": "a", "NAME": "b"}},
		{"a line without an equals sign is ignored", "garbage\nID=x", map[string]string{"ID": "x"}},
		{"an invalid key is ignored", "1BAD=x\nA-B=y\nID=z", map[string]string{"ID": "z"}},
		{"an unterminated quote drops the line", "A=\"open\nID=x", map[string]string{"ID": "x"}},
		{"an empty value is kept", `A=""`, map[string]string{"A": ""}},
		{"an empty file", "", map[string]string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseOSRelease([]byte(tc.in))
			if !maps.Equal(got, tc.want) {
				t.Fatalf("parseOSRelease(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// files serves paths from a map and reports every other path as absent, as the
// file system does.
func files(m map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if s, ok := m[path]; ok {
			return []byte(s), nil
		}
		return nil, fs.ErrNotExist
	}
}

func TestLinuxLabel(t *testing.T) {
	const etc, usr = "etc/os-release", "usr/lib/os-release"
	denied := errors.New("permission denied")
	tests := []struct {
		name    string
		read    func(string) ([]byte, error)
		want    string
		wantErr bool
	}{
		{"PRETTY_NAME is the label", files(map[string]string{etc: `PRETTY_NAME="Ubuntu 24.04.1 LTS"` + "\nNAME=Ubuntu\nVERSION=24.04"}), "Ubuntu 24.04.1 LTS", false},
		{"the second file is read when the first does not exist", files(map[string]string{usr: `PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"`}), "Debian GNU/Linux 12 (bookworm)", false},
		{"the files are never merged", files(map[string]string{etc: "NAME=Alpine", usr: `PRETTY_NAME="Other"`}), "Alpine", false},
		{"NAME and VERSION stand in for a missing PRETTY_NAME", files(map[string]string{etc: `NAME="Fedora Linux"` + "\nVERSION=\"41 (Workstation Edition)\""}), "Fedora Linux 41 (Workstation Edition)", false},
		{"NAME alone stands in for a missing PRETTY_NAME", files(map[string]string{etc: "NAME=Alpine"}), "Alpine", false},
		{"an empty PRETTY_NAME falls through to NAME", files(map[string]string{etc: "PRETTY_NAME=\nNAME=Arch"}), "Arch", false},
		{"VERSION without NAME is not used", files(map[string]string{etc: "VERSION=12"}), "Linux", false},
		{"an empty file gives Linux", files(map[string]string{etc: ""}), "Linux", false},
		{"an empty first file does not fall back to the second", files(map[string]string{etc: "", usr: `PRETTY_NAME="Other"`}), "Linux", false},
		{"neither file gives Linux", files(nil), "Linux", false},
		{"the last duplicate PRETTY_NAME is used", files(map[string]string{etc: "PRETTY_NAME=a\nPRETTY_NAME=b"}), "b", false},
		{"an unreadable first file gives Linux and an error", func(string) ([]byte, error) { return nil, denied }, "Linux", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := linuxLabel(tc.read)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("linuxLabel = %q, %v; want %q, error %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}
