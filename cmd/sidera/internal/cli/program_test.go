package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/launch"
)

// The path that adapter setup and update name is the program as launched,
// links kept; only when how it was launched is not known is it what the system reports.
func TestProgramPath(t *testing.T) {
	dir := t.TempDir()
	link, resolved := filepath.Join(dir, "bin", "sidera"), filepath.Join(dir, "versions", "0.15.0", "sidera")
	for _, tc := range []struct {
		name      string
		launch    launch.Launch
		launchErr error
		exe       func() (string, error)
		want      string // empty: any absolute path
		wantErr   bool
	}{
		{"launched through a link", launch.Launch{Program: link}, nil, func() (string, error) { return resolved, nil }, link, false},
		{"the launch was not captured", launch.Launch{}, errors.New("no cwd"), func() (string, error) { return resolved, nil }, resolved, false},
		{"no program in the launch", launch.Launch{}, nil, func() (string, error) { return resolved, nil }, resolved, false},
		{"a relative executable is made absolute", launch.Launch{}, errors.New("no cwd"), func() (string, error) { return "sidera", nil }, "", false},
		{"nothing is known", launch.Launch{}, errors.New("no cwd"), func() (string, error) { return "", errors.New("unknown") }, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &command{Config: Config{Launch: tc.launch, LaunchErr: tc.launchErr, Executable: tc.exe}}
			got, err := c.programPath()
			switch {
			case tc.wantErr:
				if err == nil {
					t.Errorf("programPath = %q, want an error", got)
				}
			case err != nil || !filepath.IsAbs(got) || tc.want != "" && got != tc.want:
				t.Errorf("programPath = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// The quoting follows the platform whose shell the command is pasted into.
func TestShellWord(t *testing.T) {
	for _, tc := range []struct{ goos, in, want string }{
		{"windows", `C:\Users\ivea\sidera\bin\sidera.exe`, `"C:\Users\ivea\sidera\bin\sidera.exe"`},
		{"windows", `C:\Users\a b\sidera\bin\sidera.exe`, `"C:\Users\a b\sidera\bin\sidera.exe"`},
		{"windows", `C:\Users\O'Neil (work) & co\sidera.exe`, `"C:\Users\O'Neil (work) & co\sidera.exe"`},
		{"linux", "/home/a/.local/bin/sidera", "/home/a/.local/bin/sidera"},
		{"linux", "/home/a b/bin/sidera", "'/home/a b/bin/sidera'"},
		{"darwin", "/Users/a/it's/sidera", `'/Users/a/it'\''s/sidera'`},
	} {
		if got := shellWord(tc.goos, tc.in); got != tc.want {
			t.Errorf("shellWord(%s, %s) = %s, want %s", tc.goos, tc.in, got, tc.want)
		}
	}
}

// A program that runs from a temporary directory, as the one `go run` builds does, is
// warned about, because the path a client keeps would not outlast the command.
func TestWarnIfTemporary(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TEMP", temp)
	for _, tc := range []struct {
		name string
		exe  string
		warn bool
	}{
		{"under the temporary directory", filepath.Join(temp, "go-build123", "b001", "exe", "sidera"), true},
		{"installed elsewhere", filepath.Join(t.TempDir(), "bin", "sidera"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var errs bytes.Buffer
			c := &command{Config: Config{Stderr: &errs, Getenv: func(string) string { return "" }}}
			c.warnIfTemporary(tc.exe, "the path kept in a client's configuration")
			if got := strings.Contains(errs.String(), "will not last"); got != tc.warn {
				t.Errorf("warned = %v (stderr %q), want %v", got, errs.String(), tc.warn)
			}
		})
	}
}

// A node whose data directory is the default is reached without --home; the
// environment's SIDERA_HOME does not make a directory the default.
func TestIsDefaultDataDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the default directory on Windows is under LOCALAPPDATA, which datahome's own tests cover")
	}
	user := t.TempDir()
	t.Setenv("HOME", user)
	elsewhere := filepath.Join(t.TempDir(), "node")
	c := &command{Config: Config{Getenv: func(name string) string {
		return map[string]string{"SIDERA_HOME": elsewhere}[name]
	}}}
	if !c.isDefaultDataDir(filepath.Join(user, ".sidera")) {
		t.Error("the directory under the profile is not the default")
	}
	if c.isDefaultDataDir(elsewhere) {
		t.Error("the directory SIDERA_HOME names is taken for the default")
	}
}
