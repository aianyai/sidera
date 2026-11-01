//go:build windows

package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/release"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// A machine that still has the data directory where earlier versions kept it is
// refused by every command that uses the directory, with the way to move it; the
// commands that use none keep working.
func TestWindowsTheFormerDataDirectoryRefusesTheCommandsThatUseIt(t *testing.T) {
	profile, local := t.TempDir(), t.TempDir()
	t.Setenv("USERPROFILE", profile)
	if err := os.Mkdir(filepath.Join(profile, ".sidera"), 0o700); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "LOCALAPPDATA" {
			return local
		}
		return ""
	}
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"init"}, 1},
		{[]string{"daemon"}, 1},
		{[]string{"status"}, 1},
		{[]string{"clock", "clear-rollback"}, 1},
		{[]string{"version"}, 0},
		{[]string{"--help"}, 0},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), append([]string{"sidera"}, tc.args...), Config{
				Stdin: io.NopCloser(strings.NewReader("")), Stdout: nopWriteCloser{&stdout}, Stderr: &stderr,
				Getenv: getenv, Release: release.Read(),
			})
			if code != tc.code {
				t.Fatalf("exit code %d, want %d; stderr %q", code, tc.code, &stderr)
			}
			if tc.code != 0 && !strings.Contains(stderr.String(), "Move-Item") {
				t.Errorf("stderr %q does not say how to move the directory", &stderr)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(local, "Sidera")); err == nil {
		t.Error("a refused command made the new data directory")
	}
}
