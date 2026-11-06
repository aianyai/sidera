package setup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// The text of `claude mcp get`, as Claude Code 2.1 prints it, is the only form there is.
func TestParseMCPGet(t *testing.T) {
	const text = `sidera:
  Scope: User config (available in all your projects)
  Status: ✘ Failed to connect
  Issue: ENOENT: no such file or directory, posix_spawn '/Users/x y/sidera'
  Type: stdio
  Command: /Users/x y/sidera
  Args: --home /a b/c mcp
  Environment:

To remove this server, run: claude mcp remove sidera -s user
`
	got, err := parseMCPGet(text)
	if err != nil || got.Command != "/Users/x y/sidera" || got.Args != "--home /a b/c mcp" {
		t.Errorf("%+v, %v", got, err)
	}
	if got, err := parseMCPGet(strings.ReplaceAll(text, "  Args: --home /a b/c mcp\n", "")); err != nil || got.Args != "" {
		t.Errorf("no arguments: %+v, %v", got, err)
	}
	if got, _ := parseMCPGet(strings.Replace(text, "Command: /Users/x y/sidera", `Command: C:\Program Files\sidera.exe`, 1)); got.Command != `C:\Program Files\sidera.exe` {
		t.Errorf("a command with a colon: %q", got.Command)
	}
	if got, _ := parseMCPGet(strings.Replace(text, "Type: stdio", "Type: http", 1)); got.Command == "/Users/x y/sidera" {
		t.Error("a server that is not a stdio server was read as one")
	}
	// Text that is not what the parser was written for is an error, not a server with no command.
	for name, text := range map[string]string{
		"no Type line":    strings.Replace(text, "  Type: stdio\n", "", 1),
		"no Command line": strings.Replace(text, "  Command: /Users/x y/sidera\n", "", 1),
	} {
		if got, err := parseMCPGet(text); err == nil {
			t.Errorf("%s: read as %+v", name, got)
		}
	}
	for _, scope := range []string{"Project config (shared via .mcp.json)", "Local config (private to you in this project)"} {
		if _, err := parseMCPGet(strings.Replace(text, "User config (available in all your projects)", scope, 1)); err == nil {
			t.Errorf("a server of the scope %q was taken as the user's", scope)
		}
	}
}

// The real claude command, in a configuration directory of its own: the whole life of
// the setup against the commands it relies on. It runs no model turn, and skips where
// Claude Code is not installed.
func TestWithTheRealClaude(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the program the server is registered with is a shell script")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude is not installed")
	}
	// Claude Code keeps its user-scope settings in another file when CLAUDE_CONFIG_DIR is
	// set than when it is not, so both are walked.
	for _, name := range []string{"CLAUDE_CONFIG_DIR set", "CLAUDE_CONFIG_DIR unset"} {
		t.Run(name, func(t *testing.T) { walkWithTheRealClaude(t, claude, name == "CLAUDE_CONFIG_DIR set") })
	}
}

func walkWithTheRealClaude(t *testing.T, claude string, configSet bool) {
	dir := t.TempDir()
	config := filepath.Join(dir, "claude-config")
	for _, name := range []string{"HOME", "USERPROFILE"} {
		t.Setenv(name, dir)
	}
	if configSet {
		t.Setenv("CLAUDE_CONFIG_DIR", config)
	} else {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		os.Unsetenv("CLAUDE_CONFIG_DIR")
		config = filepath.Join(dir, ".claude")
	}
	state := filepath.Join(dir, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	// Claude Code runs in the state directory and must leave nothing in it but setup's own.
	stateHoldsOnlySetup := func(when string) {
		t.Helper()
		entries, err := os.ReadDir(state)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name() != "setup" {
				t.Errorf("%s: claude left %s in the state directory", when, e.Name())
			}
		}
	}
	program := filepath.Join(dir, "sidera")
	if err := os.WriteFile(program, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	version := "1.0.0"
	run := func(op adaptersetup.Op, accepted ...string) adaptersetup.Result {
		t.Helper()
		h := New(Options{Version: version, Environ: os.Environ(), Home: dir})
		res, err := adaptersetup.Handle(context.Background(), h, adaptersetup.Request{Version: adaptersetup.Version, Op: op, Home: dir,
			HomeIsDefault: false, Instance: "claude", StateDir: state, SideraProgram: program, Accepted: accepted})
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return res
	}

	plan := run(adaptersetup.OpPlan)
	if res := run(adaptersetup.OpApply, ids(plan.Items)...); res.Status != adaptersetup.StatusOK {
		t.Fatalf("apply: %+v", res.Outcomes)
	}
	if res := run(adaptersetup.OpCheck); res.Status != adaptersetup.StatusOK {
		t.Fatalf("check after setup: %s %+v", res.Status, res.Drift)
	}
	if items := run(adaptersetup.OpPlan).Items; len(items) != 0 {
		t.Fatalf("second plan: %v", ids(items))
	}

	stateHoldsOnlySetup("after setup")

	version = "1.1.0"
	if res := run(adaptersetup.OpCheck); driftOf(res)[idSource] != adaptersetup.ProblemStale || driftOf(res)[idPlugin] != adaptersetup.ProblemStale {
		t.Fatalf("check after the adapter was updated: %s %+v", res.Status, res.Drift)
	}
	if res := run(adaptersetup.OpRefresh); res.Status != adaptersetup.StatusRefreshed || len(outcomeIDs(res.Outcomes, adaptersetup.OutcomeDone)) != 2 {
		t.Fatalf("refresh: %s %+v", res.Status, res.Outcomes)
	}
	if res := run(adaptersetup.OpCheck); res.Status != adaptersetup.StatusOK {
		t.Fatalf("check after refresh: %s %+v", res.Status, res.Drift)
	}

	req := adaptersetup.Request{Version: adaptersetup.Version, Op: adaptersetup.OpRemove, Home: dir, Instance: "claude", StateDir: state, SideraProgram: program}
	listing, err := adaptersetup.Handle(context.Background(), New(Options{Version: version, Environ: os.Environ(), Home: dir}), req)
	if err != nil {
		t.Fatal(err)
	}
	req.Op, req.Undo, req.Accepted = adaptersetup.OpApply, true, ids(listing.Items)
	res, err := adaptersetup.Handle(context.Background(), New(Options{Version: version, Environ: os.Environ(), Home: dir}), req)
	if err != nil || res.Status != adaptersetup.StatusOK {
		t.Fatalf("undo: %v %+v", err, res.Outcomes)
	}
	if res := run(adaptersetup.OpCheck); *res.Recorded || driftOf(res)[idMCP] != adaptersetup.ProblemNeedsSetup {
		t.Errorf("check after undo: %s %+v", res.Status, res.Drift)
	}
	client, err := NewCLI(claude, state, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if server, err := client.MCPServer(context.Background()); err != nil || server != nil {
		t.Errorf("the server is still registered: %+v %v", server, err)
	}
	if list, err := client.Marketplaces(context.Background()); err != nil || len(list) != 0 {
		t.Errorf("marketplaces %+v %v", list, err)
	}
}

// programName is the file name of a program on this platform: Windows finds and starts
// only a name with an executable extension.
func programName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// A fake claude: a script that answers as a program does, to show how the CLI runs it.
func fakeClaudeProgram(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// A call that outlives its limit ends as a timeout that says so, not as an exit code.
func TestACallThatTimesOutIsATimeout(t *testing.T) {
	cli, err := NewCLI(fakeClaudeProgram(t, "sleep 30"), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cli.Timeout = 200 * time.Millisecond
	_, err = cli.Marketplaces(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "exit") {
		t.Errorf("error %v, want a timeout", err)
	}
}

// `claude mcp get` connects to the server, so it is told not to wait long for it.
func TestMCPGetIsRunWithAShortConnectionTimeout(t *testing.T) {
	cli, err := NewCLI(fakeClaudeProgram(t, `printf '  Scope: User config\n  Type: stdio\n  Command: %s\n  Args: mcp\n' "$MCP_TIMEOUT"`), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	server, err := cli.MCPServer(context.Background())
	if err != nil || server.Command != "5000" {
		t.Errorf("%+v %v, want the command to be the MCP_TIMEOUT the script saw, 5000", server, err)
	}
}

// A claude that cannot be started is told from one that failed.
func TestAClaudeThatCannotBeStartedIsNotStarted(t *testing.T) {
	cli, err := NewCLI(filepath.Join(t.TempDir(), programName("claude")), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cli.Plugins(context.Background())
	var notStarted *adaptersetup.NotStartedError
	if !errors.As(err, &notStarted) {
		t.Errorf("error %v, want a *NotStartedError", err)
	}
}
