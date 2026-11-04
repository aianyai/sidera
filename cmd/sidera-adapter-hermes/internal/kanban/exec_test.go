package kanban

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban/kanbantest"
)

func TestMain(m *testing.M) {
	kanbantest.MaybeRun()
	os.Exit(m.Run())
}

func fakeClient(t *testing.T, mode string) *Client {
	t.Helper()
	c, err := New(Config{Executable: kanbantest.Executable(t, mode), Home: "/hermes/home", KanbanHome: "/hermes/kanban", Board: "lane"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A card body of any size reaches hermes through standard input, and the child
// gets the explicit homes and none of the worker variables the service inherited.
func TestCreateBodyThroughStdin(t *testing.T) {
	c := fakeClient(t, kanbantest.ModeCreate)
	t.Setenv("HERMES_KANBAN_TASK", "t_inherited")
	t.Setenv("HERMES_HOME", "/inherited")
	body := strings.Repeat("0123456789abcdef", 16<<10) // 256 KiB, above the Linux limit of one argument
	out, err := c.kanban(context.Background(), "create", []byte(body), "create", "--json", "--", "-a title")
	if err != nil {
		t.Fatal(err)
	}
	var seen map[string]any
	if err := json.Unmarshal(out, &seen); err != nil {
		t.Fatal(err)
	}
	if seen["body"] != body || seen["title"] != "-a title" || seen["board"] != "lane" ||
		seen["hermes_home"] != "/hermes/home" || seen["kanban_home"] != "/hermes/kanban" || seen["worker_task"] != "" {
		t.Fatalf("child saw board %v, home %v, kanban home %v, worker task %v, title %v, a body of %d bytes",
			seen["board"], seen["hermes_home"], seen["kanban_home"], seen["worker_task"], seen["title"], len(seen["body"].(string)))
	}
	card, err := c.Create(context.Background(), NewCard{Title: "t", Body: body})
	if err != nil || card.ID != "t_fake" || card.Body != body {
		t.Fatalf("Create: %+v, %v", card, err)
	}
}

// An argument above the bound is refused before hermes is started: the
// executable here does not exist, so a run would fail differently.
func TestArgTooLongIsRefusedWithoutRunning(t *testing.T) {
	c, err := New(Config{Executable: filepath.Join(t.TempDir(), "no-hermes"), Home: "h", Board: "b"})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", MaxArgBytes+1)
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"complete summary": func() error { return c.Complete(ctx, "t_1", Completion{Summary: long}) },
		"complete result":  func() error { return c.Complete(ctx, "t_1", Completion{Summary: "s", Result: long}) },
		"metadata": func() error {
			return c.Complete(ctx, "t_1", Completion{Summary: "s", Metadata: map[string]any{"k": long}})
		},
		"comment":      func() error { return c.Comment(ctx, "t_1", long) },
		"block reason": func() error { return c.Block(ctx, "t_1", BlockTransient, long) },
		"title":        func() error { _, err := c.Create(ctx, NewCard{Title: long}); return err },
	} {
		err := call()
		var ke *Error
		if !errors.Is(err, ErrArgTooLong) || !errors.As(err, &ke) || ke.Class != Permanent || ke.ExitCode != -1 {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// A NUL byte cannot be passed to a program; the error says so instead of the
	// start failing with an error that names nothing.
	for name, call := range map[string]func() error{
		"complete result": func() error { return c.Complete(ctx, "t_1", Completion{Summary: "s", Result: "a\x00b"}) },
		"comment":         func() error { return c.Comment(ctx, "t_1", "a\x00b") },
		"block reason":    func() error { return c.Block(ctx, "t_1", BlockTransient, "a\x00b") },
		"title":           func() error { _, err := c.Create(ctx, NewCard{Title: "a\x00b"}); return err },
	} {
		err := call()
		var ke *Error
		if !errors.Is(err, ErrArgInvalid) || !errors.As(err, &ke) || ke.Class != Permanent || ke.ExitCode != -1 {
			t.Errorf("NUL in %s: err = %v", name, err)
		}
	}
	// At the bound it is let through, and then fails because there is no hermes.
	err = c.Comment(ctx, "t_1", strings.Repeat("x", MaxArgBytes))
	var ke *Error
	if errors.Is(err, ErrArgTooLong) || !errors.As(err, &ke) || ke.Class != Transient {
		t.Errorf("at the bound: err = %v", err)
	}
}

// On Windows a batch shim would hand every argument to cmd.exe, so the client
// refuses to run one, whichever command it is asked for. The rule is applied for
// another operating system here; the file is a real executable, so a run that was
// not refused would show as another error.
func TestShimIsRefusedByEveryCommand(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		file    string
		refused bool
	}{
		{"hermes.cmd", true},
		{"hermes.BAT", true},
		{"hermes.exe", false},
		{"hermes", false},
	} {
		path := filepath.Join(dir, tc.file)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		c, err := New(Config{Executable: path, Home: "h", Board: "b"})
		if err != nil {
			t.Fatal(err)
		}
		c.goos = "windows"
		_, err = c.resolve("version")
		var ke *Error
		switch {
		case tc.refused && (!errors.As(err, &ke) || ke.Class != Permanent || ke.ExitCode != -1):
			t.Errorf("%s: err = %v, want a permanent refusal", tc.file, err)
		case tc.refused:
			if _, err := c.Version(context.Background()); !errors.As(err, &ke) || ke.Class != Permanent {
				t.Errorf("%s: Version err = %v, want the refusal too", tc.file, err)
			}
		case err != nil:
			t.Errorf("%s: err = %v, want it let through", tc.file, err)
		}
	}
}

func TestProbeAgainstRecordedOutputs(t *testing.T) {
	c := fakeClient(t, kanbantest.ModeRecorded)
	if err := c.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	c.cfg.Board = "no-such-board"
	if err := c.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("Probe of a missing board: %v", err)
	}
}

func TestTimeoutIsTransient(t *testing.T) {
	c := fakeClient(t, kanbantest.ModeHang)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.List(ctx, StatusReady)
	if !errors.Is(err, ErrTransient) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatalf("hermes was not killed: %v", time.Since(start))
	}
}

// The files an Attach left because its process died hold the text of a reply; they are
// removed, and nothing else in the directory, nor anything below it, is touched.
func TestLeftoverAttachFilesAreRemoved(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("reply"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("attach-123")
	write("attach-456")
	write("notes.txt")
	if err := os.MkdirAll(filepath.Join(dir, "attach-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RemoveLeftoverAttachFiles(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if strings.Join(left, ",") != "attach-dir,notes.txt" {
		t.Errorf("left %v, want only notes.txt and the directory", left)
	}
	if err := RemoveLeftoverAttachFiles(filepath.Join(dir, "absent")); err != nil {
		t.Errorf("a directory that does not exist: %v", err)
	}
}

// A probe that arrives while another runs waits for it and returns its result, so
// that the loops do not start several at once. The executable here does not exist: a
// caller that probed would fail with another error.
func TestProbeInProgressIsShared(t *testing.T) {
	c, err := New(Config{Executable: filepath.Join(t.TempDir(), "no-hermes"), Home: "h", Board: "b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []error{errors.New("the answer of the probe in progress"), nil} {
		call := &probeCall{done: make(chan struct{}), err: want}
		close(call.done)
		c.probing = call
		if got := c.Probe(context.Background()); got != want {
			t.Errorf("got %v, want the result of the probe in progress, %v", got, want)
		}
	}
}
