package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setup controller only prints. It needs no node, so it runs with a home that does
// not exist and leaves it alone. The command names the running executable by its
// absolute path, because a client starts the server without a shell and may not have
// the executable's directory on its PATH. A node in the default data directory is
// reached without --home; one elsewhere is named by --home in both commands, because
// the client starts the MCP server with an environment that may not carry SIDERA_HOME.
func TestSetupControllerPrintsTheCommandsNamingTheExecutableAndTheHomeOnlyWhenItIsNotTheDefault(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("USERPROFILE", user)
	// The program that runs from a temporary directory is warned about, so the
	// temporary directory is one the executables below are not in.
	temp := filepath.Join(user, "tmp")
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, temp)
	}
	elsewhere := filepath.Join(t.TempDir(), "node")
	spaced := filepath.Join(t.TempDir(), "a node")
	exe := filepath.Join(t.TempDir(), "bin", "sidera")
	spacedExe := filepath.Join(t.TempDir(), "my tools", "sidera")
	quote := func(s string) string { return shellWord(runtime.GOOS, s) }
	env := map[string]string{"LOCALAPPDATA": filepath.Join(user, "local")}
	relocated := map[string]string{"LOCALAPPDATA": env["LOCALAPPDATA"], "CODEX_HOME": filepath.Join(user, "codex"), "CLAUDE_CONFIG_DIR": filepath.Join(user, "claude")}
	withHomeEnv := map[string]string{"LOCALAPPDATA": env["LOCALAPPDATA"], "SIDERA_HOME": elsewhere}

	for _, tc := range []struct {
		name string
		env  map[string]string
		exe  string
		args []string
		mcp  string
	}{
		{"the default data directory", env, exe, nil, quote(exe) + " mcp"},
		{"clients whose configuration is relocated", relocated, exe, nil, quote(exe) + " mcp"},
		{"a data directory named by --home", env, exe, []string{"--home", elsewhere}, quote(exe) + " --home " + quote(elsewhere) + " mcp"},
		{"a data directory named by SIDERA_HOME", withHomeEnv, exe, nil, quote(exe) + " --home " + quote(elsewhere) + " mcp"},
		{"a home the shell would split", env, exe, []string{"--home", spaced}, quote(exe) + " --home " + quote(spaced) + " mcp"},
		{"an executable the shell would split", env, spacedExe, nil, quote(spacedExe) + " mcp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errs := runCLIAs(t, func() (string, error) { return tc.exe, nil }, tc.env, append(append([]string{}, tc.args...), "setup", "controller")...)
			if code != 0 || errs != "" {
				t.Fatalf("exit %d, stderr %q", code, errs)
			}
			for _, line := range []string{
				"claude mcp add --scope user sidera -- " + tc.mcp + "\n",
				"codex mcp add sidera -- " + tc.mcp + "\n",
				"  \"mcp__sidera\"\n",
				"  default_tools_approval_mode = \"approve\"\n",
			} {
				if !strings.Contains(out, line) {
					t.Errorf("missing %q in %q", line, out)
				}
			}
			// Both clients' files are named as the environment relocates them, not at
			// their default places under the profile.
			named := func(parts ...string) bool {
				for _, part := range parts {
					if !strings.Contains(out, part) {
						return false
					}
				}
				return true
			}
			if tc.env["CODEX_HOME"] != "" {
				if !named("CODEX_HOME", "CLAUDE_CONFIG_DIR") || named(".codex") || named(".claude") {
					t.Errorf("the files are not named where the environment puts them: %q", out)
				}
			} else if !named(".codex", ".claude") {
				t.Errorf("the files are not named at their default places: %q", out)
			}
			for _, made := range []string{elsewhere, spaced} {
				if _, err := os.Stat(made); err == nil {
					t.Errorf("%s was made", made)
				}
			}
		})
	}

	t.Run("a program in a temporary directory", func(t *testing.T) {
		goBuild := filepath.Join(temp, "go-build123", "b001", "exe", "sidera")
		code, out, errs := runCLIAs(t, func() (string, error) { return goBuild, nil }, env, "setup", "controller")
		if code != 0 || !strings.Contains(errs, "will not last") || !strings.Contains(out, "mcp add") {
			t.Fatalf("exit %d, stderr %q, stdout %q; want 0, the warning and the commands", code, errs, out)
		}
	})
	if code, _, _ := runCLI(t, env, "setup"); code != 2 {
		t.Fatalf("setup without a topic: exit %d, want 2", code)
	}
}

// The quoting follows the platform whose shell the command is pasted into, and the
// files the person edits are named as they find them there.
func TestSetupQuotesForThePlatformAndNamesItsFiles(t *testing.T) {
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

	relocated := func(name string) string { return map[string]string{"CODEX_HOME": "/x"}[name] }
	for _, tc := range []struct {
		goos   string
		getenv func(string) string
		want   string
	}{
		{"windows", func(string) string { return "" }, `%USERPROFILE%\.codex\config.toml`},
		{"windows", relocated, `%CODEX_HOME%\config.toml`},
		{"linux", func(string) string { return "" }, "~/.codex/config.toml"},
		{"darwin", relocated, "$CODEX_HOME/config.toml"},
	} {
		if got := userFile(tc.goos, tc.getenv, "CODEX_HOME", ".codex", "config.toml"); got != tc.want {
			t.Errorf("userFile(%s) = %s, want %s", tc.goos, got, tc.want)
		}
	}
}
