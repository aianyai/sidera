package proc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
)

// childEnv makes the test binary a child that says it is ready and then only
// waits, for the launcher to start.
const childEnv = "SIDERA_PROC_TEST_CHILD"

func TestMain(m *testing.M) {
	process.HandleHelperInvocation()
	if os.Getenv(childEnv) != "" {
		println("ready") // to the standard error file
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func launch(t *testing.T) Launch {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	if err := os.WriteFile(in, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return Launch{
		Argv: []string{exe, "-test.run=^$"}, Env: append(os.Environ(), childEnv+"=1"), Dir: dir,
		Stdin: in, Stdout: filepath.Join(dir, "out"), Stderr: filepath.Join(dir, "err"),
	}
}

func waitForReady(t *testing.T, file string) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if raw, _ := os.ReadFile(file); strings.Contains(string(raw), "ready") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the child did not say it is ready")
		}
	}
}

// A process the launcher started is named by the same identity everywhere the table
// is asked, runs in the container the launch reports, and Interrupt ends it.
func TestLauncherStartsAProcessTheTableFindsAndInterruptEnds(t *testing.T) {
	table := NewTable()
	l := launch(t)
	started, err := NewLauncher().Start(l)
	if err != nil {
		t.Fatal(err)
	}
	if started.PID == 0 || started.StartKey == "" || started.Container.Validate() != nil {
		t.Fatalf("started = %+v", started)
	}
	if running, err := table.Running(started.Identity); err != nil || !running {
		t.Fatalf("Running = %v, %v", running, err)
	}
	found, ok, err := table.Lookup(started.PID)
	if err != nil || !ok || found != started.Info {
		t.Fatalf("Lookup = %+v, %v, %v; started %+v", found, ok, err, started.Info)
	}
	// A start in a systemd scope is still the launcher's own argument vector for an
	// instant, until it executes the program: wait for the listing to show it.
	var entry Entry
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		list, err := table.List()
		if err != nil {
			t.Fatal(err)
		}
		if i := slices.IndexFunc(list, func(e Entry) bool { return e.PID == started.PID }); i >= 0 {
			if entry = list[i]; entry.Args[0] == l.Argv[0] {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("List does not hold the process as started: %+v", entry)
		}
	}
	if entry.Info != started.Info {
		t.Fatalf("List describes the process as %+v, started as %+v", entry.Info, started.Info)
	}

	// The interrupt is for a child that is up: a Go program on windows/arm64 that
	// receives Ctrl-C while it is still starting can stay deaf to it for good.
	waitForReady(t, l.Stderr)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := NewSignaller().Interrupt(ctx, started.Identity); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started.Done:
	case <-time.After(20 * time.Second):
		t.Fatal("the process did not end after the interrupt")
	}
	if running, err := table.Running(started.Identity); err != nil || running {
		t.Fatalf("Running after the end = %v, %v", running, err)
	}
	if err := NewSignaller().Interrupt(ctx, started.Identity); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Interrupt of an ended process = %v, want ErrNotRunning", err)
	}
}

// A launch that cannot be made started nothing; only a process that runs without
// an identity is ErrStartedUnidentified.
func TestLauncherFailuresStartNothing(t *testing.T) {
	l := launch(t)
	l.Stdin = filepath.Join(filepath.Dir(l.Stdin), "missing")
	if _, err := NewLauncher().Start(l); err == nil || errors.Is(err, ErrStartedUnidentified) {
		t.Fatalf("a missing input file: %v", err)
	}
	l = launch(t)
	l.Argv[0] = "relative-claude"
	if _, err := NewLauncher().Start(l); err == nil || errors.Is(err, ErrStartedUnidentified) {
		t.Fatalf("a program that is not an absolute path: %v", err)
	}
}
