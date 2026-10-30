//go:build acceptance && crashtest && testclock

package main_test

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
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
)

func TestTaggedProcessPrerequisites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidera")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	args := []string{"build", "-tags", "crashtest,testclock", "-o", path}
	if raceEnabled {
		args = append(args, "-race")
	}
	if out, err := exec.Command("go", append(args, ".")...).CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	env := func(overrides ...string) []string {
		result := []string{}
		for _, entry := range os.Environ() {
			name, _, _ := strings.Cut(entry, "=")
			if !strings.HasPrefix(name, "SIDERA_") {
				result = append(result, entry)
			}
		}
		return append(result, overrides...)
	}
	t.Run("deterministic init and ordinary isolation", func(t *testing.T) {
		parent, err := os.MkdirTemp("", "ft-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(parent)
		initNode := func(binary, home string, environment []string) string {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "--json", "--home", home, "init")
			cmd.Env = environment
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("init: %v %s", err, out)
			}
			var result struct{ Node string }
			if err := json.Unmarshal(out, &result); err != nil || result.Node == "" {
				t.Fatalf("init result: %s %v", out, err)
			}
			return result.Node
		}
		seed := env("SIDERA_TEST_SEED=init-check", "SIDERA_TEST_CLOCK_START_MS=1793232000000")
		if a, b := initNode(path, filepath.Join(parent, "a"), seed), initNode(path, filepath.Join(parent, "b"), seed); a != b {
			t.Fatal("same tagged seed and origin changed identity")
		}
		record := filepath.Join(parent, "unused.jsonl")
		ordinary := env("SIDERA_TEST_SEED=init-check", "SIDERA_CRASH_GEN=invalid", "SIDERA_CRASH_AT=invalid", "SIDERA_CRASH_RECORD="+record, "SIDERA_TEST_CLOCK_START_MS=invalid")
		if a, b := initNode(binary(t), filepath.Join(parent, "c"), ordinary), initNode(binary(t), filepath.Join(parent, "d"), ordinary); a == b {
			t.Fatal("ordinary binary reused a test identity")
		}
		if _, err := os.Stat(record); !os.IsNotExist(err) {
			t.Fatalf("ordinary binary used crash record: %v", err)
		}
	})
	t.Run("init uses the transaction crash observer", func(t *testing.T) {
		parent, err := os.MkdirTemp("", "fc-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(parent)
		record := filepath.Join(parent, "init.jsonl")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, "--home", filepath.Join(parent, "h"), "init")
		cmd.Env = env("SIDERA_TEST_SEED=init-crash", "SIDERA_TEST_CLOCK_START_MS=1793232000000", "SIDERA_CRASH_RECORD="+record, "SIDERA_CRASH_AT=init/dir.mkdir#1")
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("no crash: %v %s", err, out)
		}
		want := -1
		if runtime.GOOS == "windows" {
			want = 3
		}
		if exit.ExitCode() != want {
			t.Fatalf("exit=%d: %s", exit.ExitCode(), out)
		}
		raw, err := os.ReadFile(record)
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
		if len(lines) < 2 {
			t.Fatalf("trace: %s", raw)
		}
		var hit struct {
			Gen               int
			Point, Path, Kind string
			Crash             bool
		}
		if err := json.Unmarshal(lines[len(lines)-1], &hit); err != nil {
			t.Fatal(err)
		}
		if hit.Gen != 1 || hit.Point != "init/dir.mkdir#1" || hit.Path == "" || hit.Kind != "normal" || !hit.Crash {
			t.Fatalf("trace: %s", raw)
		}
	})
	t.Run("complete recv before stdout", func(t *testing.T) {
		n := newNode(t)
		sent, err := n.h.Agent("creator").Send(n.h.Ctx(), fakeagent.SendInput{To: []string{n.first.address}, Body: "visible only after the crash point"})
		if err != nil {
			t.Fatal(err)
		}
		if err := n.h.Quiesce(); err != nil {
			t.Fatal(err)
		}
		record := filepath.Join(t.TempDir(), "recv.jsonl")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, "--home", n.home, "recv")
		cmd.Env = env("SIDERA_TOKEN="+n.first.token, "SIDERA_CRASH_RECORD="+record, "SIDERA_CRASH_AT="+daemon.RecvAfterResponse, "SIDERA_CRASH_GEN=2")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err = cmd.Run()
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("no abnormal exit: %v %s", err, &stderr)
		}
		want := -1
		if runtime.GOOS == "windows" {
			want = 3
		}
		if exit.ExitCode() != want || stdout.Len() != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", exit.ExitCode(), &stdout, &stderr)
		}
		raw, err := os.ReadFile(record)
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
		if len(lines) != 2 {
			t.Fatalf("trace: %s", raw)
		}
		for i, line := range lines {
			var hit struct {
				Gen         int
				Point, Kind string
				Crash       bool
			}
			if err := json.Unmarshal(line, &hit); err != nil {
				t.Fatal(err)
			}
			if hit.Gen != 2 || hit.Point != daemon.RecvAfterResponse || hit.Kind != "proc" || hit.Crash != (i == 1) {
				t.Fatalf("trace: %s", raw)
			}
		}
		page := n.page(n.first)
		if len(page.Messages) != 1 || page.Messages[0].ID != sent.MessageID || !page.Messages[0].PossiblyDuplicate {
			t.Fatalf("retry: %+v", page)
		}
	})
}
