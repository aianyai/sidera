//go:build realcodex

// These tests run against the Codex installed on this machine. They start a real
// App Server and run no model turn, so they cost no quota. They are opt-in twice:
// the build tag, and SIDERA_REALCODEX=1 in the environment.
//
//	SIDERA_REALCODEX=1 go test -tags realcodex -count=1 ./cmd/sidera-adapter-codex/...
//
// On Unix the temporary directory must be short enough for a socket path (about a
// hundred bytes); TMPDIR can name a shorter one.
package server_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/codexcfg"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/declare"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/server"
)

func requireRealCodex(t *testing.T) string {
	t.Helper()
	if os.Getenv("SIDERA_REALCODEX") != "1" {
		t.Skip("set SIDERA_REALCODEX=1 to run against the installed Codex")
	}
	program, err := exec.LookPath("codex")
	if err != nil {
		t.Fatalf("codex is not installed: %v", err)
	}
	v, err := server.CLIVersion(context.Background(), program)
	if err != nil {
		t.Fatal(err)
	}
	if !declare.VersionTested(v) {
		t.Logf("codex %s is not the tested version %s: a failure below may be the version", v, declare.TestedVersion)
	}
	return program
}

// The credentials of a session reach its commands through the thread's own
// configuration, in a server shared by all sessions; and the server this process
// started goes away, with its process group, when it is stopped.
func TestThreadEnvironmentAndServerCleanup(t *testing.T) {
	program := requireRealCodex(t)
	dir, err := os.MkdirTemp("", "k4")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	srv := server.New(server.Config{Program: program, StateDir: dir})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ep, err := srv.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !srv.Started() {
		t.Fatal("the server was not started by this process")
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = srv.Stop()
		}
	}()

	type itemEvent struct {
		ThreadID string `json:"threadId"`
		Item     struct {
			Type             string `json:"type"`
			AggregatedOutput string `json:"aggregatedOutput"`
		} `json:"item"`
	}
	outputs := make(chan string, 8)
	client, err := appserver.Dial(ctx, ep, appserver.Handlers{Notification: func(n appserver.Notification) {
		if n.Method != appserver.NotifyItemCompleted {
			return
		}
		if e, err := appserver.DecodeParams[itemEvent](n); err == nil && e.Item.Type == "commandExecution" {
			outputs <- e.Item.AggregatedOutput
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	init, err := client.Initialize(ctx, appserver.InitializeParams{
		ClientInfo:   appserver.ClientInfo{Name: "sidera-realcodex", Version: "0"},
		Capabilities: &appserver.InitializeCapabilities{OptOutNotificationMethods: appserver.QuietNotifications},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v := appserver.ServerVersion(init.UserAgent); v == "" {
		t.Errorf("no version in the user agent %q", init.UserAgent)
	}
	acct, err := client.AccountRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("signed in: %v", acct.SignedIn())

	preset := codexcfg.Preset{Sandbox: codexcfg.SandboxReadOnly}
	cfg := preset.ThreadConfig(codexcfg.Access{Program: "true", Env: map[string]string{"SIDERA_TOKEN": "tok-real", "SIDERA_HOME": "/real/home"}})
	th, err := client.ThreadStart(ctx, appserver.ThreadStartParams{
		CWD: dir, ApprovalPolicy: codexcfg.ApprovalNever, Sandbox: codexcfg.SandboxReadOnly, Ephemeral: true, Config: cfg,
	})
	if err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	if err := client.ThreadShellCommand(ctx, th.ID, "env | grep '^SIDERA_' | sort", 20000); err != nil {
		t.Fatalf("thread/shellCommand: %v", err)
	}
	select {
	case out := <-outputs:
		if !strings.Contains(out, "SIDERA_TOKEN=tok-real") || !strings.Contains(out, "SIDERA_HOME=/real/home") {
			t.Fatalf("the thread's commands see\n%s", out)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the command did not complete")
	}

	// A command runs under each sandbox policy a preset can name, which is how the
	// command-line front end is probed without a model turn.
	for _, sandbox := range []string{codexcfg.SandboxReadOnly, codexcfg.SandboxWorkspaceWrite, codexcfg.SandboxDangerFullAccess} {
		r, err := client.CommandExec(ctx, appserver.CommandExecParams{
			Command: []string{"/usr/bin/true", "probe"}, CWD: dir, SandboxPolicy: codexcfg.Preset{Sandbox: sandbox}.SandboxPolicy(), TimeoutMs: 20000,
		})
		if err != nil || r.ExitCode != 0 {
			t.Errorf("command/exec under %s: %+v, %v", sandbox, r, err)
		}
	}

	// The words that say a thread is gone, for a thread that never existed.
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	missing := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
	for name, call := range map[string]func() error{
		"thread/resume": func() error {
			_, err := client.ThreadResume(ctx, appserver.ThreadResumeParams{ThreadID: missing, ExcludeTurns: true})
			return err
		},
		"thread/archive":     func() error { return client.ThreadArchive(ctx, missing) },
		"thread/unsubscribe": func() error { return client.ThreadUnsubscribe(ctx, missing) },
	} {
		err := call()
		t.Logf("%s of a thread that does not exist: %v", name, err)
		if name != "thread/unsubscribe" && !appserver.IsThreadGone(err) {
			t.Errorf("%s of a thread that does not exist is not recognised as gone: %v", name, err)
		}
	}

	_ = client.Close()
	if err := srv.Stop(); err != nil {
		t.Fatal(err)
	}
	stopped = true
	if !srv.GroupGone() {
		t.Error("the server's process group is still there after Stop")
	}
	pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
	defer pcancel()
	if appserver.Probe(pctx, ep) == nil {
		t.Error("the server still answers after Stop")
	}
}
