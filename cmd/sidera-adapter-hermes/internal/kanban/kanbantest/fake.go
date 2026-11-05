// Package kanbantest is a stand-in for the hermes program, for the tests of the
// kanban client and of everything that drives it. The test binary doubles as
// hermes: a test points the client at its own executable (Executable), and the
// process, started with the mode in its environment, answers the way the recorded
// Hermes does and exits. Tests so run the client's real process path on every
// operating system, with no Hermes installed.
//
// A test package calls MaybeRun from TestMain, before it runs its tests.
package kanbantest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	// Env holds the mode: it is set exactly in the environment of a fake hermes.
	Env = "KANBAN_TEST_FAKE_HERMES"
	// ControlEnv names the directory that steers the "board" mode.
	ControlEnv = "KANBAN_TEST_BOARD_DIR"
	// DownFile, in the control directory, makes every command fail the way a
	// locked database does. ReadyFile holds the answer to `list --status ready`.
	DownFile  = "down"
	ReadyFile = "ready.json"
	// VersionFile, in the control directory, replaces the recorded output of
	// `--version`.
	VersionFile = "version.txt"
	// NotifierFile, in the control directory, replaces the recorded output of
	// `config get kanban.notify_in_gateway`, whichever profile is asked; a file named
	// NotifierFile with "-<profile>" before the extension answers for that profile only.
	NotifierFile = "notifier.txt"
	// ToolsFile, in the control directory, replaces the recorded output of
	// `tools list --platform=<platform>`, whichever profile and platform are asked. The
	// recording is a platform whose list names kanban and skills, both enabled.
	ToolsFile = "tools.txt"
)

// NotifierFileOf is the file that answers `config get kanban.notify_in_gateway` for one
// profile.
func NotifierFileOf(profile string) string {
	return strings.TrimSuffix(NotifierFile, ".txt") + "-" + profile + ".txt"
}

// The modes.
const (
	// ModeCreate echoes what `create` received as the created card, with the
	// environment the child saw.
	ModeCreate = "create"
	// ModeRecorded answers version, boards and assignees with the recorded outputs.
	ModeRecorded = "recorded"
	// ModeBoard answers like ModeRecorded and also `list --status ready` (from the
	// control directory's ReadyFile when it has one, else the recorded output); it
	// fails while the control directory holds DownFile.
	ModeBoard = "board"
	// ModeHang never answers.
	ModeHang = "hang"
)

// MaybeRun runs the fake hermes and exits when the process was started as one;
// otherwise it returns at once.
func MaybeRun() {
	if mode := os.Getenv(Env); mode != "" {
		os.Exit(run(mode, os.Args[1:]))
	}
}

// Executable makes the child processes of this test binary fake hermes of the
// mode, for the rest of the test, and returns the path to give the client.
func Executable(t testing.TB, mode string) string {
	t.Helper()
	t.Setenv(Env, mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// samples is the directory of the recordings of the kanban package.
func samples() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "testdata")
}

func run(mode string, args []string) int {
	switch mode {
	case ModeCreate:
		// hermes kanban --board <slug> create --json ... -- <title>
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
	case ModeRecorded:
		return recorded(args, "")
	case ModeBoard:
		return recorded(args, os.Getenv(ControlEnv))
	case ModeLane:
		return lane(args, os.Getenv(ControlEnv))
	case ModeHang:
		time.Sleep(time.Minute)
		return 0
	}
	return 3
}

func recorded(args []string, control string) int {
	if control != "" {
		if _, err := os.Stat(filepath.Join(control, DownFile)); err == nil {
			fmt.Fprintln(os.Stderr, "sqlite3.OperationalError: database is locked")
			return 1
		}
	}
	cmd := strings.Join(args, " ")
	if len(args) > 3 && args[0] == "kanban" && args[1] == "--board" {
		cmd = strings.Join(args[3:], " ")
	}
	if len(args) > 2 && args[0] == "-p" {
		cmd = strings.Join(args[2:], " ") // hermes -p <profile> <command>
	}
	if strings.HasPrefix(cmd, "tools list --platform=") {
		cmd = "tools list" // the recording is the same for every platform
	}
	file := ""
	switch cmd {
	case "--version":
		file = "version.txt"
		if control != "" {
			if b, err := os.ReadFile(filepath.Join(control, VersionFile)); err == nil {
				os.Stdout.Write(b)
				return 0
			}
		}
	case "config get kanban.notify_in_gateway":
		file = filepath.Join("config-get", "unset-default.txt")
		if control != "" {
			for _, name := range []string{NotifierFileOf(args[1]), NotifierFile} {
				if b, err := os.ReadFile(filepath.Join(control, name)); err == nil {
					os.Stdout.Write(b)
					return 0
				}
			}
		}
	case "tools list":
		file = filepath.Join("tools-list", "explicit-both.txt")
		if control != "" {
			if b, err := os.ReadFile(filepath.Join(control, ToolsFile)); err == nil {
				os.Stdout.Write(b)
				return 0
			}
		}
	case "boards list --json":
		file = "boards-list.json"
	case "assignees --json":
		file = "assignees.json"
	case "list --status=ready --json":
		file = "list-ready.json"
		if control != "" {
			if b, err := os.ReadFile(filepath.Join(control, ReadyFile)); err == nil {
				os.Stdout.Write(b)
				return 0
			}
		}
	default:
		fmt.Fprintln(os.Stderr, "unexpected argv:", args)
		return 2
	}
	b, err := os.ReadFile(filepath.Join(samples(), file))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	os.Stdout.Write(b)
	return 0
}
