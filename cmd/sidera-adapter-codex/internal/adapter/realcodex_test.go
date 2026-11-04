//go:build realcodex

// This test runs the adapter against the Codex installed on this machine, with a
// real model turn. It is opt-in twice: the build tag, and SIDERA_REALCODEX=1.
//
//	SIDERA_REALCODEX=1 go test -tags realcodex -count=1 -run RealCodex ./cmd/sidera-adapter-codex/internal/adapter
//
// The App Server it uses is its own, started in a state directory of the test, so it
// never touches one that another adapter runs; the threads it creates stay in the
// Codex home, like any other.
package adapter_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/adapter"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/server"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// cheapestModel returns the model the server lists as the affordable one, from a
// server of its own that is stopped again.
func cheapestModel(t *testing.T, program string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "m")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	srv := server.New(server.Config{Program: program, StateDir: dir})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ep, err := srv.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = srv.Terminate(context.Background()) }()
	c, err := appserver.Dial(ctx, ep, appserver.Handlers{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Initialize(ctx, appserver.InitializeParams{ClientInfo: appserver.ClientInfo{Name: "sidera-realcodex", Version: "0"}}); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		} `json:"data"`
	}
	if err := c.Call(ctx, "model/list", map[string]any{}, &list); err != nil {
		t.Fatal(err)
	}
	for _, m := range list.Data {
		if strings.Contains(strings.ToLower(m.Description), "affordable") {
			return m.ID
		}
	}
	t.Fatalf("no model is described as affordable in %+v", list.Data)
	return ""
}

// running says whether a process with the command line given is alive.
func running(marker string) bool {
	return exec.Command("pgrep", "-f", marker).Run() == nil
}

// An interrupt of a turn that waits for a long command stops the turn and the
// command, which the server leaves running, and the session takes its next turn.
func TestRealCodexInterruptEndsTheRunningCommand(t *testing.T) {
	if os.Getenv("SIDERA_REALCODEX") != "1" {
		t.Skip("set SIDERA_REALCODEX=1 to run against the installed Codex")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the command is looked for with pgrep")
	}
	program, err := exec.LookPath("codex")
	if err != nil {
		t.Fatalf("codex is not installed: %v", err)
	}
	model := cheapestModel(t, program)
	t.Logf("model: %s", model)

	// A sleep no other process has the command line of.
	const marker = "sleep 40.7319"
	// The server is stopped after the adapter is: an adapter that is still up starts
	// the server again when it loses it. Cleanups run in the reverse order they were
	// registered, so this one comes before the harness makes its own.
	var srv *server.Server
	t.Cleanup(func() {
		if srv != nil {
			_ = srv.Terminate(context.Background())
		}
	})
	h := newHarnessWith(t, nil, func(o *adapter.Options) {
		o.NewServer = func(c server.Config) adapter.ServerManager { srv = server.New(c); return srv }
		o.LookPath = exec.LookPath
		o.MCPWait = 60 * time.Second
	})
	h.startWith([]adapterproto.PresetConfig{{Name: "dev", Config: presetConfig(t, map[string]any{
		"cwd": h.dir, "sandbox": "workspace-write", "model": model,
	})}})

	params := createParams(attempt1, sessionEP, "dev")
	params.FirstPrompt.Text = "Run exactly this shell command and nothing else, then report: " + marker
	var created adapterproto.SessionCreateResult
	h.mustCall(adapterproto.MethodSessionCreate, params, &created)
	t.Logf("create: %+v (thread %s)", created, created.NativeRef)
	if created.Outcome != adapterproto.CreateCreated {
		t.Fatalf("create = %+v", created)
	}
	deadline := time.Now().Add(90 * time.Second)
	for !running(marker) {
		if time.Now().After(deadline) {
			t.Fatal("the turn never ran the command")
		}
		time.Sleep(200 * time.Millisecond)
	}

	start := time.Now()
	res := h.interrupt()
	t.Logf("interrupt: %+v after %v", res, time.Since(start))
	if res.Stage != adapterproto.InterruptStopped {
		t.Fatalf("interrupt = %+v", res)
	}
	if running(marker) {
		t.Error("the command is still running after the interrupt answered stopped")
	}
	h.waitNote(adapterproto.MethodTurnEnded, func(r json.RawMessage) bool { return ended(r, adapterproto.TurnInterrupted) })

	var again adapterproto.PromptDeliverResult
	h.mustCall(adapterproto.MethodPromptDeliver, adapterproto.PromptDeliverParams{Endpoint: sessionEP, Attempt: attempt2, Text: "Reply with the single word ok."}, &again)
	if again.Stage == adapterproto.PromptRefused {
		t.Fatalf("the next prompt was refused: %+v", again)
	}
	h.waitNote(adapterproto.MethodTurnEnded, func(r json.RawMessage) bool { return ended(r, adapterproto.TurnCompleted) })
	if got := h.interrupt(); got.Stage != adapterproto.InterruptNotRunning {
		t.Errorf("interrupt of an idle session = %+v", got)
	}
}
