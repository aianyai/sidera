package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setup controller only prints. It needs no node, so it runs with a home that does
// not exist and leaves it alone. A node in the default data directory is reached by
// the plain command; one elsewhere is named by --home in both commands, because the
// client starts the MCP server with an environment that may not carry SIDERA_HOME.
func TestSetupControllerPrintsTheCommandsAndNamesTheHomeOnlyWhenItIsNotTheDefault(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("USERPROFILE", user)
	elsewhere := filepath.Join(t.TempDir(), "node")
	spaced := filepath.Join(t.TempDir(), "a node")
	quotedSpaced := "'" + spaced + "'"
	if runtime.GOOS == "windows" {
		quotedSpaced = `"` + spaced + `"`
	}
	env := map[string]string{"LOCALAPPDATA": filepath.Join(user, "local")}
	withHomeEnv := map[string]string{"LOCALAPPDATA": env["LOCALAPPDATA"], "SIDERA_HOME": elsewhere}

	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		mcp  string
	}{
		{"the default data directory", env, nil, "sidera mcp"},
		{"a data directory named by --home", env, []string{"--home", elsewhere}, "sidera --home " + elsewhere + " mcp"},
		{"a data directory named by SIDERA_HOME", withHomeEnv, nil, "sidera --home " + elsewhere + " mcp"},
		{"a path the shell would split", env, []string{"--home", spaced}, "sidera --home " + quotedSpaced + " mcp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errs := runCLI(t, tc.env, append(append([]string{}, tc.args...), "setup", "controller")...)
			if code != 0 || errs != "" {
				t.Fatalf("exit %d, stderr %q", code, errs)
			}
			for _, line := range []string{"claude mcp add --scope user sidera -- " + tc.mcp + "\n", "codex mcp add sidera -- " + tc.mcp + "\n"} {
				if !strings.Contains(out, line) {
					t.Errorf("missing %q in %q", line, out)
				}
			}
			for _, made := range []string{elsewhere, spaced} {
				if _, err := os.Stat(made); err == nil {
					t.Errorf("%s was made", made)
				}
			}
		})
	}

	if code, _, _ := runCLI(t, env, "setup"); code != 2 {
		t.Fatalf("setup without a topic: exit %d, want 2", code)
	}
}
