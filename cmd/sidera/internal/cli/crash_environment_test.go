//go:build crashtest

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// Only this helper process mutates its environment. The outer test and other
// tests keep their own environment, including when they run in parallel.
func TestDaemonCrashEnvironmentStaysWithItsOwner(t *testing.T) {
	if dir := os.Getenv("SIDERA_CRASH_ENV_HELPER"); dir != "" {
		runCrashEnvironmentHelper(t, dir)
		return
	}
	dir, err := os.MkdirTemp("", "ce-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	for _, program := range []string{"sidera", "sidera-fake-adapter", "sidera-fake-runtime"} {
		cmd := exec.Command("go", "build", "-tags", "crashtest,testclock", "-o", crashEnvironmentBinary(dir, program), "./cmd/"+program)
		cmd.Dir = "../../../.."
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", program, err, out)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDaemonCrashEnvironmentStaysWithItsOwner$")
	cmd.Env = append(os.Environ(), "SIDERA_CRASH_ENV_HELPER="+dir,
		"SIDERA_CRASH_RECORD="+filepath.Join(dir, "daemon.jsonl"), "SIDERA_CRASH_AT=init/dir.mkdir#1", "SIDERA_CRASH_GEN=7",
		"SIDERA_TEST_SEED=environment-owner", "SIDERA_TEST_CLOCK_START_MS=1793232000000")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("environment helper: %v %s", err, out)
	}
}

func crashEnvironmentBinary(dir, name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}

func runCrashEnvironmentHelper(t *testing.T, dir string) {
	t.Helper()
	c := command{Config: Config{Getenv: os.Getenv}}
	closeRecord, err := c.configureTestProcess("daemon")
	if err != nil {
		t.Fatal(err)
	}
	defer closeRecord()
	for _, name := range []string{"SIDERA_CRASH_RECORD", "SIDERA_CRASH_AT", "SIDERA_CRASH_GEN"} {
		if _, present := os.LookupEnv(name); present {
			t.Fatalf("daemon left %s in its child environment", name)
		}
	}
	if os.Getenv("SIDERA_TEST_SEED") != "environment-owner" || os.Getenv("SIDERA_TEST_CLOCK_START_MS") != "1793232000000" {
		t.Fatal("daemon removed a seed or clock input")
	}
	c.observer.Reached(fsx.Observation{Point: fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "sample", Role: "file"}, Step: fsx.StepWrite}, Hit: 1}}, txn.Normal)
	baseline, err := os.ReadFile(filepath.Join(dir, "daemon.jsonl"))
	if err != nil || !bytes.Contains(baseline, []byte(`"gen":7`)) {
		t.Fatalf("daemon lost its own generation: %q %v", baseline, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	server, err := fakectl.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	argv, _ := json.Marshal([]string{crashEnvironmentBinary(dir, "sidera"), "--json", "--home", filepath.Join(dir, "n"), "init"})
	cfg := fakeadapter.ProcessConfig{
		InstanceID: "fake", Manifest: fakeadapter.DefaultManifestOptions(),
		LedgerDir: filepath.Join(dir, "ledger"), RuntimeDir: filepath.Join(dir, "runtime"),
		RuntimeBinary: crashEnvironmentBinary(dir, "sidera-fake-runtime"), RuntimeSeed: 9,
		ClockStartMS: 1793232000000, Control: server.Addr(),
		RuntimeFirstTurn: fakeruntime.PlanKnob{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionExec, Args: map[string]string{"argv": string(argv)}}}, EndAfter: true},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	launcher := adapterhost.ExecLauncher{Env: []string{fakeadapter.ConfigEnv + "=" + string(raw)}}
	process, err := launcher.Launch(ctx, adapterhost.Instance{
		ID: "fake", Dir: dir, Stderr: io.Discard,
		Adapter: config.Adapter{Manifest: manifest.Manifest{Entry: manifest.Entry{Program: filepath.Base(crashEnvironmentBinary(dir, "sidera-fake-adapter"))}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = process.Kill(); _ = process.Wait() }()
	r, w := process.Stream()
	p, err := peer.New(r, w, peer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.Handle(adapterproto.MethodCheckpointPut, func(context.Context, json.RawMessage) (any, error) { return adapterproto.Empty{}, nil })
	ended := make(chan struct{}, 1)
	p.HandleNotification(adapterproto.MethodTurnEnded, func(context.Context, json.RawMessage) { ended <- struct{}{} })
	p.Start()
	var initialized adapterproto.InitializeResult
	if err := p.Call(ctx, adapterproto.MethodInitialize, adapterproto.InitializeParams{
		Protocol: adapterproto.SupportedRange(), Daemon: adapterproto.DaemonInfo{Version: "0.1.0"},
		Instance: adapterproto.InstanceInfo{ID: "fake", LockPath: filepath.Join(dir, "instance.lock")},
	}, &initialized); err != nil {
		t.Fatal(err)
	}
	var created adapterproto.SessionCreateResult
	attempt := adapterproto.AttemptID("00000000-0000-4000-8000-000000000001")
	if err := p.Call(ctx, adapterproto.MethodSessionCreate, adapterproto.SessionCreateParams{
		Attempt: attempt, RuntimeEndpoint: "rt-1", SessionEndpoint: "sess-1", Preset: "default",
		FirstPrompt: adapterproto.FirstPrompt{Attempt: attempt, Text: "initialize a node"},
		ToolAccess:  adapterproto.ToolAccess{Env: map[string]string{}},
	}, &created); err != nil {
		t.Fatal(err)
	}
	if created.Outcome != adapterproto.CreateCreated || created.Process == nil || created.Process.PID == int64(os.Getpid()) || created.Process.PID == int64(process.PID()) {
		t.Fatalf("expected a separate native process: %+v", created)
	}
	controller := fakeruntime.RemoteController{Server: server}
	defer controller.KillNative(string(created.NativeRef))
	select {
	case <-ended:
	case <-ctx.Done():
		t.Fatal("native CLI action did not finish")
	}
	record, err := os.ReadFile(filepath.Join(cfg.RuntimeDir, string(created.NativeRef)+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := fakeruntime.ParseRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	results := 0
	for _, event := range events {
		if event.Kind != fakeruntime.EventToolResult {
			continue
		}
		results++
		var result struct{ Node string }
		if event.Error != "" || json.Unmarshal([]byte(event.Text), &result) != nil || result.Node == "" {
			t.Fatalf("native CLI inherited the daemon controls: %+v", event)
		}
	}
	if results != 1 {
		t.Fatalf("native CLI results: %d, want 1", results)
	}
	if record, err := os.ReadFile(filepath.Join(dir, "daemon.jsonl")); err != nil || !bytes.Equal(record, baseline) {
		t.Fatalf("child wrote the daemon's crash record: %q %v", record, err)
	}

	// An independent CLI can still choose its own target and record explicitly.
	cliRecord := filepath.Join(dir, "cli.jsonl")
	cmd := exec.CommandContext(ctx, crashEnvironmentBinary(dir, "sidera"), "--home", filepath.Join(dir, "e"), "init")
	cmd.Env = append(os.Environ(), "SIDERA_CRASH_RECORD="+cliRecord, "SIDERA_CRASH_AT=init/dir.mkdir#1", "SIDERA_CRASH_GEN=2")
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("explicit CLI target did not kill the CLI: %v", err)
	}
	trace, err := os.ReadFile(cliRecord)
	if err != nil || !bytes.Contains(trace, []byte(`"gen":2`)) || !strings.Contains(string(trace), `"crash":true`) {
		t.Fatalf("explicit CLI trace: %q %v", trace, err)
	}
}
