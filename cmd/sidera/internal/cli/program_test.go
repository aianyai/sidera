package cli

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/launch"
)

// The path that setup, update and setup controller name is the program as launched,
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
