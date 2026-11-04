package kanban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a stand-in for hermes: with fakeEnv set it answers
// the way the real command line does and exits, so the tests run the client's
// real process path on every operating system.
const fakeEnv = "KANBAN_TEST_FAKE_HERMES"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEnv); mode != "" {
		os.Exit(fakeHermes(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeHermes(mode string, args []string) int {
	switch mode {
	case "create":
		// hermes kanban --board <slug> create --json ... -- <title>; echoes what it
		// received as the created card.
		if len(args) < 4 || args[0] != "kanban" || args[1] != "--board" || args[3] != "create" {
			fmt.Fprintln(os.Stderr, "unexpected argv:", args)
			return 2
		}
		body, _ := io.ReadAll(os.Stdin)
		card := map[string]any{"id": "t_fake", "title": args[len(args)-1], "body": string(body),
			"assignee": nil, "status": "ready", "created_by": "user", "session_id": nil, "created_at": 1,
			"board": args[2], "hermes_home": os.Getenv("HERMES_HOME"),
			"kanban_home": os.Getenv("HERMES_KANBAN_HOME"), "worker_task": os.Getenv("HERMES_KANBAN_TASK")}
		_ = json.NewEncoder(os.Stdout).Encode(card)
		return 0
	case "recorded":
		// Answers the three commands of Probe with the recorded Hermes 0.22.2 outputs.
		file := ""
		cmd := strings.Join(args, " ")
		if len(args) > 3 && args[0] == "kanban" && args[1] == "--board" {
			cmd = strings.Join(args[3:], " ")
		}
		switch cmd {
		case "--version":
			file = "version.txt"
		case "boards list --json":
			file = "boards-list.json"
		case "assignees --json":
			file = "assignees.json"
		default:
			fmt.Fprintln(os.Stderr, "unexpected argv:", args)
			return 2
		}
		b, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		os.Stdout.Write(b)
		return 0
	case "hang":
		time.Sleep(time.Minute)
		return 0
	}
	return 3
}

func fakeClient(t *testing.T, mode string) *Client {
	t.Helper()
	t.Setenv(fakeEnv, mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{Executable: exe, Home: "/hermes/home", KanbanHome: "/hermes/kanban", Board: "lane"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A card body of any size reaches hermes through standard input, and the child
// gets the explicit homes and none of the worker variables the service inherited.
func TestCreateBodyThroughStdin(t *testing.T) {
	c := fakeClient(t, "create")
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
	// At the bound it is let through, and then fails because there is no hermes.
	err = c.Comment(ctx, "t_1", strings.Repeat("x", MaxArgBytes))
	var ke *Error
	if errors.Is(err, ErrArgTooLong) || !errors.As(err, &ke) || ke.Class != Transient {
		t.Errorf("at the bound: err = %v", err)
	}
}

func TestProbeAgainstRecordedOutputs(t *testing.T) {
	c := fakeClient(t, "recorded")
	if err := c.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	c.cfg.Board = "no-such-board"
	if err := c.Probe(context.Background()); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("Probe of a missing board: %v", err)
	}
}

func TestTimeoutIsTransient(t *testing.T) {
	c := fakeClient(t, "hang")
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
