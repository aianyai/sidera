//go:build realclaude && !windows

package runtime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/claudeargs"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime/proc"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// These tests run the installed Claude Code and cost no model turn: they probe
// it, and start a turn process that is given no input and ended with SIGINT.
// Run them with
//
//	SIDERA_REALCLAUDE=1 go test -tags realclaude -run RealClaude -count=1 ./cmd/sidera-adapter-claude/internal/runtime
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
	true_, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no true(1) to stand in for sidera")
	}
	raw := func(s string) []byte { return []byte(`"` + s + `"`) }
	if _, err := app.Start(context.Background(), adapterproto.StartParams{
		Node: "n", Config: adapterproto.ConfigValues{"sidera_path": raw(true_)},
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
// started it with, and SIGINT ends it.
func TestRealClaudeTurnProcessIsFoundAndInterrupted(t *testing.T) {
	app := realApp(t)
	claude, _, mcpConfig := app.executables()
	if claude == "" {
		t.Skip("no claude")
	}
	const session = "6ee62271-42c1-4558-89cf-0e32c50e0c1b"
	dir := t.TempDir()
	fifo := filepath.Join(dir, "in")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}
	// Holding the FIFO open for writing keeps the process waiting for input: no
	// prompt is ever given, so no model runs.
	hold, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()

	argv, err := claudeargs.Turn{Claude: claude, SessionID: session, MCPConfig: mcpConfig, DataDir: dir, Preset: claudeargs.Preset{PermissionMode: "dontAsk", Model: "haiku"}}.Argv()
	if err != nil {
		t.Fatal(err)
	}
	table := proc.NewTable()
	started, err := proc.NewLauncher(table).Start(proc.Launch{
		Argv: argv, Env: claudeargs.Environ(os.Environ(), nil, ""), Dir: dir,
		Stdin: fifo, Stdout: filepath.Join(dir, "out"), Stderr: filepath.Join(dir, "err"),
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	snap, err := table.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	found := app.turnProcesses(snap, session)
	if len(found) != 1 || found[0].PID != started.PID || found[0].StartKey() != started.StartKey() {
		stderr, _ := os.ReadFile(filepath.Join(dir, "err"))
		t.Fatalf("found %+v, started %+v; stderr: %s", found, started.Info, stderr)
	}
	if err := proc.NewSignaller().Interrupt(started.PID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started.Done:
	case <-time.After(15 * time.Second):
		t.Fatal("the turn process did not end after SIGINT")
	}
}
