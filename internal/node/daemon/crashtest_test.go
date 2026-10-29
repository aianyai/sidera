//go:build crashtest

package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

func TestCrashObserverKillsOnlyAfterTheCompleteRecord(t *testing.T) {
	const target = "sample/file.rename#1"
	if record := os.Getenv("SIDERA_CRASH_HELPER"); record != "" {
		file, err := os.OpenFile(record, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		observer, err := daemon.NewCrashObserver(daemon.CrashConfig{Writer: file, At: target, Gen: 2})
		if err != nil {
			t.Fatal(err)
		}
		point, err := fsx.ParsePoint(target)
		if err != nil {
			t.Fatal(err)
		}
		observer.Reached(fsx.Observation{Point: point, Path: "state/new.rec", From: "state/.tmp-raw"}, txn.Recovery)
		t.Fatal("the crash point returned")
	}
	record := filepath.Join(t.TempDir(), "trace.jsonl")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCrashObserverKillsOnlyAfterTheCompleteRecord$")
	cmd.Env = append(os.Environ(), "SIDERA_CRASH_HELPER="+record)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("not an abnormal process exit: %v %s", err, out)
	}
	want := -1
	if runtime.GOOS == "windows" {
		want = 3
	}
	if exit.ExitCode() != want {
		t.Fatalf("exit %d, want %d: %s", exit.ExitCode(), want, out)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("records: %s", raw)
	}
	for i, line := range lines {
		var record struct {
			Gen                     int
			Point, Path, From, Kind string
			Crash                   bool
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.Gen != 2 || record.Point != target || record.Path != "state/new.rec" || record.From != "state/.tmp-raw" || record.Kind != "recovery" || record.Crash != (i == 1) {
			t.Fatalf("incomplete record %d: %s", i, line)
		}
	}
}

func TestCrashObserverSerializesCompleteConcurrentObservations(t *testing.T) {
	var raw bytes.Buffer
	observer, err := daemon.NewCrashObserver(daemon.CrashConfig{Writer: &raw, Gen: 1})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 1; i <= 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			observer.Reached(fsx.Observation{Point: fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "sample", Role: "file"}, Step: fsx.StepWrite}, Hit: i}, Path: "state/.tmp-unmodified"}, txn.Normal)
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(raw.String()), "\n") {
		var record struct{ Point, Path string }
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Path != "state/.tmp-unmodified" {
			t.Fatalf("operand was normalized: %s", line)
		}
		seen[record.Point] = true
	}
	if len(seen) != 50 {
		t.Fatalf("recorded %d points", len(seen))
	}
}
