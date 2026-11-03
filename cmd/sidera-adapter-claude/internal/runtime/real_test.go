//go:build realclaude

package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/claudeargs"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime/proc"
	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// These tests run the installed Claude Code and cost no model turn: they probe
// it, and start a turn process that is given no input and ended with an interrupt.
// Run them with
//
//	SIDERA_REALCLAUDE=1 go test -tags realclaude -run RealClaude -count=1 ./cmd/sidera-adapter-claude/internal/runtime
//
// TestMain lets the test binary serve as the SDK's interrupt helper, which the
// interrupt of a turn process on Windows starts from the adapter's own executable.
func TestMain(m *testing.M) {
	process.HandleHelperInvocation()
	os.Exit(m.Run())
}

func realApp(t *testing.T) *App {
	t.Helper()
	if os.Getenv("SIDERA_REALCLAUDE") != "1" {
		t.Skip("set SIDERA_REALCLAUDE=1 to run against the installed Claude Code")
	}
	state, work := t.TempDir(), t.TempDir()
	app := New(Options{})
	t.Cleanup(app.stop)
	app.daemon = &world{t: t, files: map[string][]byte{}, procs: map[int]*fakeProc{}, checkpts: map[string]json.RawMessage{}}
	app.instance = adapterproto.InstanceInfo{ID: "claude", LockPath: filepath.Join(state, "lock"), StateDir: state}
	// The tests never run sidera; the MCP configuration only names it.
	stand, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw := func(s string) []byte { b, _ := json.Marshal(s); return b } // a Windows path holds backslashes
	if _, err := app.Start(context.Background(), adapterproto.StartParams{
		Node: "n", Config: adapterproto.ConfigValues{"sidera_path": raw(stand)},
		Presets:  []adapterproto.PresetConfig{{Name: "dev", Config: adapterproto.ConfigValues{"project_dir": raw(work), "permission_mode": raw("dontAsk")}}},
		Sessions: []adapterproto.SessionAttach{}, DirectEndpoints: []adapterproto.DirectAttach{},
	}); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestRealClaudeProbe(t *testing.T) {
	app := realApp(t)
	res, err := app.Probe(context.Background(), adapterproto.ProbeParams{ToolProbes: []adapterproto.ToolProbeRequest{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Runtime == nil || !regexp.MustCompile(`^\d+\.\d+\.\d+`).MatchString(res.Runtime.Version) {
		t.Fatalf("runtime %+v", res.Runtime)
	}
	t.Logf("claude %s, auth %s, presets %+v", res.Runtime.Version, res.Auth, res.Presets)
	if res.Auth != adapterproto.AuthValid {
		t.Skip("claude is not logged in: nothing more to check")
	}
	if len(res.Presets) != 1 || !res.Presets[0].Usable {
		t.Fatalf("presets %+v", res.Presets)
	}
}

// The process table finds a turn process of the real Claude by what the adapter
// started it with, and the interrupt ends it.
func TestRealClaudeTurnProcessIsFoundAndInterrupted(t *testing.T) {
	app := realApp(t)
	claude, _, mcpConfig := app.executables()
	if claude == "" {
		t.Skip("no claude")
	}
	// A uuid of its own, so that another run on the machine is not mistaken for this one.
	session, err := newUUID(app.o.Rand)
	if err != nil {
		t.Fatal(err)
	}
	// The working directory is a real path, as the adapter gives a turn process.
	dir, err := app.o.Files.RealDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Nothing is ever written to the input, which the process waits on: no prompt
	// is given, so no model runs.
	input := holdInput(t, dir)

	argv, err := claudeargs.Turn{Claude: claude, SessionID: session, MCPConfig: mcpConfig, DataDir: dir, Preset: claudeargs.Preset{PermissionMode: "dontAsk", Model: "haiku"}}.Argv()
	if err != nil {
		t.Fatal(err)
	}
	table := proc.NewTable()
	started, err := proc.NewLauncher().Start(proc.Launch{
		Argv: argv, Env: claudeargs.Environ(os.Environ(), nil, ""), Dir: dir,
		Stdin: input, Stdout: filepath.Join(dir, "out"), Stderr: filepath.Join(dir, "err"),
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	t.Cleanup(func() { // a failed test must not leave the process behind
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = proc.NewSignaller().Interrupt(ctx, started.Identity)
	})
	list, err := table.List()
	if err != nil {
		t.Fatal(err)
	}
	found := app.turnProcesses(list, session)
	if len(found) != 1 || found[0].Identity != started.Identity {
		stderr, _ := os.ReadFile(filepath.Join(dir, "err"))
		t.Fatalf("found %+v, started %+v; stderr: %s", found, started.Info, stderr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := proc.NewSignaller().Interrupt(ctx, started.Identity); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started.Done:
	case <-time.After(15 * time.Second):
		t.Fatal("the turn process did not end after the interrupt")
	}
	if running, err := table.Running(started.Identity); err != nil || running {
		t.Fatalf("Running after the interrupt = %v, %v", running, err)
	}
}
