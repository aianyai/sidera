package acceptance

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

func fixture(t *testing.T) (*Fixture, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f, err := NewFixture(ctx, runRoot(t), testBinaries, Options{Seed: t.Name()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	return f, ctx
}

func TestRealBinaryRestartRetainsTimeAfterClockRPCFails(t *testing.T) {
	f, ctx := fixture(t)
	if err := f.Init(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, GenerationSpec{Number: 2}); err != nil {
		t.Fatal(err)
	}
	if err := f.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	to := f.Now().Add(2 * time.Minute)
	// A stopped daemon makes the clock RPC fail deterministically. Whether
	// that request reached the daemon must not decide the next generation's time.
	if err := f.SetTime(ctx, to); err == nil {
		t.Fatal("setting the stopped daemon's clock succeeded")
	}
	if err := f.Start(ctx, GenerationSpec{Number: 3}); err != nil {
		t.Fatal(err)
	}
	raw, err := f.Manage(ctx, "status", manageapi.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Daemon struct {
			StartedAt time.Time `json:"started_at"`
		} `json:"daemon"`
	}
	if err = resultBody(raw, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Daemon.StartedAt.Equal(to) {
		t.Fatalf("restarted daemon began at %s, want retained target %s", status.Daemon.StartedAt, to)
	}
}

func TestRealBinaryLifecycleClockAndSetupOrdinals(t *testing.T) {
	f, ctx := fixture(t)
	if err := f.Init(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, GenerationSpec{Number: 2}); err != nil {
		t.Fatal(err)
	}
	var err error
	if err = f.BeginStep(ctx, "advance"); err != nil {
		t.Fatal(err)
	}
	to := f.Now().Add(2 * time.Minute)
	if err = f.SetTime(ctx, to); err != nil {
		t.Fatal(err)
	}
	ctl, err := f.Adapter(ctx, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if err = ctl.Availability(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err = f.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := f.sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := f.sample(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("fake progress changed: %+v -> %+v", before, after)
	}
	if !f.Now().Equal(to) {
		t.Fatalf("clock = %s", f.Now())
	}
	log, err := f.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	knobs := log.Kind(ledger.KindKnobHit)
	if len(knobs) != 1 || knobs[0].AtMS != to.UnixMilli() || knobs[0].Step != "advance" {
		t.Fatalf("fake clock and step: %+v", knobs)
	}
	if err = f.BeginStep(ctx, "preset"); err != nil {
		t.Fatal(err)
	}
	v, err := f.collectionVersion(ctx, "presets.list")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.change(ctx, "presets.add", &v, map[string]any{"name": "second", "preset": map[string]any{"local_sources": "deny"}}); err != nil {
		t.Fatal(err)
	}
	if err = f.Quiesce(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	trace, err := f.ReadTrace(2)
	if err != nil {
		t.Fatal(err)
	}
	setup := map[string]int{}
	continued := false
	for _, h := range trace.Hits {
		key := h.Point.Key.String()
		if h.Setup {
			setup[key] = h.Point.Hit
		} else if h.Point.Hit > setup[key] && setup[key] > 0 {
			continued = true
		}
	}
	if !continued {
		t.Fatal("main trace did not retain setup ordinals")
	}
	final, err := f.ReadTrace(2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(trace, final) {
		t.Fatal("cleanup changed frozen trace")
	}
	l, err := f.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Kind(ledger.KindFixtureKill)) != 0 {
		t.Fatal("normal shutdown recorded a kill")
	}
	if err = f.Start(ctx, GenerationSpec{Number: 3}); err != nil {
		t.Fatal(err)
	}
	if err = f.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	if !f.current.p.exited() {
		t.Fatal("kill returned before Wait")
	}
	l, err = f.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Kind(ledger.KindFixtureKill)) != 1 {
		t.Fatal("actual kill missing from ledger")
	}
	if err = f.Start(ctx, GenerationSpec{Number: 4}); err != nil {
		t.Fatal(err)
	}
	if err = f.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRealBinaryDaemonTargetWaitsForItsOwnExit(t *testing.T) {
	f, ctx := fixture(t)
	if err := f.Init(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	target := "adapter-stage/parent.mkdir#1"
	if err := f.Start(ctx, GenerationSpec{Number: 2, Target: target}); err != nil {
		t.Fatal(err)
	}
	trace, err := f.ReadTrace(2)
	if err != nil || !trace.Reached || trace.Crash != target || !f.current.p.exited() {
		t.Fatalf("daemon target: %+v, %v", trace, err)
	}
	if err = f.Start(ctx, GenerationSpec{Number: 3}); err != nil {
		t.Fatal(err)
	}
	if err = f.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRealBinaryBootstrapResumesCommittedPreset(t *testing.T) {
	f, ctx := fixture(t)
	if err := f.Init(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	target := "presets-change/commit#1"
	if err := f.Start(ctx, GenerationSpec{Number: 2, Target: target}); err != nil {
		t.Fatal(err)
	}
	trace, err := f.ReadTrace(2)
	if err != nil || !trace.Reached || trace.Crash != target {
		t.Fatalf("bootstrap target: %+v, %v", trace, err)
	}
	// The daemon died before answering. Its committed file is the evidence of
	// the original request and result that the next generation must retain.
	raw, err := os.ReadFile(filepath.Join(f.home, "config", "presets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var committed struct {
		Version       admin.Version `json:"version"`
		LastOperation struct {
			ID     string `json:"id"`
			Result any    `json:"result"`
		} `json:"last_operation"`
	}
	if err = json.Unmarshal(raw, &committed); err != nil {
		t.Fatal(err)
	}
	if committed.Version != 2 || committed.LastOperation.Result == nil {
		t.Fatalf("preset was not committed before the kill: %s", raw)
	}
	if err = f.Start(ctx, GenerationSpec{Number: 3}); err != nil {
		t.Fatal(err)
	}
	raw, err = f.Manage(ctx, "operation.result", manageapi.Request{Params: map[string]any{"operation_id": committed.LastOperation.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var stored any
	if err = resultBody(raw, &stored); err != nil || !reflect.DeepEqual(stored, committed.LastOperation.Result) {
		t.Fatalf("original result changed: %s, %v", raw, err)
	}
	id, err := hex.DecodeString(committed.LastOperation.ID)
	if err != nil || len(id) != len(admin.OperationID{}) {
		t.Fatalf("committed operation identifier: %q, %v", committed.LastOperation.ID, err)
	}
	var operationID admin.OperationID
	copy(operationID[:], id)
	version := admin.Version(1)
	raw, err = f.Manage(ctx, "presets.add", manageapi.Request{OperationID: operationID, ExpectedVersion: &version, Params: map[string]any{"name": "fixture", "preset": map[string]any{"local_sources": "allow"}}})
	if err != nil {
		t.Fatal(err)
	}
	var replay struct {
		Replayed bool `json:"replayed"`
		Result   any  `json:"result"`
	}
	if err = json.Unmarshal(raw, &replay); err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Result, stored) {
		t.Fatalf("original request did not replay: %s, %v", raw, err)
	}
	if v, err := f.collectionVersion(ctx, "presets.list"); err != nil || v != 2 {
		t.Fatalf("preset collection changed again: %d, %v", v, err)
	}
	if err = f.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	trace, err = f.ReadTrace(3)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range trace.Hits {
		switch h.Point.Key.Tag.Tx {
		case "presets-change", "adapter-stage", "adapter-install", "adapter-enable":
			t.Fatalf("completed bootstrap stage ran again: %s", h.Point)
		}
	}
}

func TestRealBinaryTargetNeedsMarkerAndAbnormalExit(t *testing.T) {
	f, ctx := fixture(t)
	target := "init/dir.mkdir#1"
	if err := f.Init(ctx, GenerationSpec{Number: 1, Target: target}); err != nil {
		t.Fatal(err)
	}
	trace, err := f.ReadTrace(1)
	if err != nil {
		t.Fatal(err)
	}
	var exit *exec.ExitError
	if !trace.Reached || trace.Crash != target || !errors.As(f.gens[1].p.err, &exit) {
		t.Fatalf("target evidence: %+v, %v", trace, f.gens[1].p.err)
	}
	if err = f.Init(ctx, GenerationSpec{Number: 2}); err != nil {
		t.Fatal(err)
	}
	if err = f.Start(ctx, GenerationSpec{Number: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.current.p.killAndWait(); err != nil {
		t.Fatal(err)
	}
	if err = f.exitResult(f.current); err == nil {
		t.Fatal("untargeted abnormal exit accepted")
	}
	trace, err = f.ReadTrace(3)
	if err != nil || trace.Reached {
		t.Fatalf("untargeted crash: %+v %v", trace, err)
	}
}

func TestRealBinaryPlainRejectsControlsAndIgnoresTestEnvironment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := runRoot(t)
	home := filepath.Join(root, "n")
	record := filepath.Join(root, "bad-record")
	env := processEnv(map[string]string{"SIDERA_TEST_SEED": "plain", "SIDERA_TEST_CLOCK_START_MS": "invalid", "SIDERA_CRASH_GEN": "invalid", "SIDERA_CRASH_RECORD": record, "SIDERA_CRASH_AT": "not-a-point"})
	init := exec.CommandContext(ctx, testBinaries.Plain, "--home", home, "init")
	init.Env = env
	if output, err := init.CombinedOutput(); err != nil {
		t.Fatalf("plain init: %v: %s", err, output)
	}
	out, err := os.Create(filepath.Join(root, "plain.out"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	cmd := exec.Command(testBinaries.Plain, "--home", home, "daemon")
	cmd.Env = env
	cmd.Stdout = out
	cmd.Stderr = out
	p, err := startProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer p.killAndWait()
	client, err := manageapi.NewClient(manageapi.ClientConfig{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err = client.Call(ctx, "node.show", manageapi.Request{}); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-p.done:
			t.Fatalf("plain daemon exited: %v", p.err)
		case <-tick.C:
		}
	}
	for _, method := range []string{"test.clock.set", "test.quiesce", "test.crashpoints"} {
		if raw, err := client.Call(ctx, method, manageapi.Request{Params: json.RawMessage(`{}`)}); err == nil {
			t.Fatalf("plain accepted %s: %s", method, raw)
		}
	}
	if _, err = os.Stat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plain read test record controls: %v", err)
	}
}
