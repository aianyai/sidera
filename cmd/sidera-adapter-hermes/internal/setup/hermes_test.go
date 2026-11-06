package setup

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
)

// script writes an executable that stands for hermes and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in for hermes is a shell script")
	}
	path := filepath.Join(t.TempDir(), "hermes")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// ConfigFile says what Hermes said when it refuses a profile, and tells a Hermes that is
// not there from one that failed.
func TestConfigFileWordsWhyHermesRefused(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "profiles", "work", "config.yaml")
	for _, tc := range []struct {
		name, hermes string
		want         string // "" for success
	}{
		{"a profile", `echo "$2" "$3" "$4" > /dev/null; echo "Config: ` + cfgPath + `"`, ""},
		{"no such profile", `echo "Error: Profile 'nosuch' does not exist." >&2; exit 1`, "Error: Profile 'nosuch' does not exist."},
		{"not installed", "", "hermes is not installed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "absent")
			if tc.hermes != "" {
				path = script(t, tc.hermes)
			}
			got, err := NewHermes(config.Config{HermesPath: path, HermesHome: t.TempDir(), Board: "lane"}).ConfigFile(context.Background(), "work")
			if tc.want == "" {
				if err != nil || got != cfgPath {
					t.Fatalf("ConfigFile = %q, %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
