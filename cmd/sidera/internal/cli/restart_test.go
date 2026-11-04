package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/manageapi"
)

// scriptedNode answers management calls from a script, and records them.
type scriptedNode struct {
	restart []func() (json.RawMessage, error) // the answers to daemon.restart, in turn; the last repeats
	status  []func() (json.RawMessage, error) // the answers to status, in turn; the last repeats

	restartCalls []manageapi.Request
	statusCalls  int
}

func (n *scriptedNode) Call(_ context.Context, method string, in manageapi.Request) (json.RawMessage, error) {
	switch method {
	case "daemon.restart":
		n.restartCalls = append(n.restartCalls, in)
		return n.restart[min(len(n.restartCalls), len(n.restart))-1]()
	case "status":
		n.statusCalls++
		return n.status[min(n.statusCalls, len(n.status))-1]()
	}
	return nil, errors.New("unexpected method " + method)
}

func answered(raw string) func() (json.RawMessage, error) {
	return func() (json.RawMessage, error) { return json.RawMessage(raw), nil }
}

func failed(err error) func() (json.RawMessage, error) {
	return func() (json.RawMessage, error) { return nil, err }
}

func statusOf(state, startID string) func() (json.RawMessage, error) {
	return answered(`{"result":{"node":{"id":"n1","version":"1.2.3"},"daemon":{"state":"` + state + `","started_at":"2026-11-03T10:00:00Z","start_id":"` + startID + `"}}}`)
}

func restartAnswer(operationID string, replayed bool, previous string) string {
	r := "false"
	if replayed {
		r = "true"
	}
	return `{"operation_id":"` + operationID + `","replayed":` + r + `,"result":{"restarting":true,"program":"/opt/sidera","version":"1.2.3","start_id":"` + previous + `"}}`
}

var (
	notRunning   = failed(apiwire.New(apiwire.CategoryUnavailable, "daemon_not_running", "the daemon is not running"))
	shuttingDown = failed(apiwire.New(apiwire.CategoryUnavailable, "shutting_down", "the node is stopping"))
	connLost     = failed(apiwire.OutcomeUnknown(""))
)

// The wait ends when status answers running with a start identifier other than the
// one that went away. Until then an old daemon that still answers, one that is
// going or gone, one that is starting, and an answer that was cut off are all "not
// yet"; any other failure is the answer, and a wait that is spent says what to check.
func TestWaitingForARestartedDaemon(t *testing.T) {
	const wait = 2 * time.Second
	for _, tc := range []struct {
		name    string
		status  []func() (json.RawMessage, error)
		reads   int
		slept   time.Duration
		wantErr string // a word of the failure; empty for none
	}{
		{"the new daemon answers at once", []func() (json.RawMessage, error){statusOf("running", "new")}, 1, 0, ""},
		{"through the old daemon, its going and the new one starting", []func() (json.RawMessage, error){
			statusOf("running", "old"), shuttingDown, notRunning, connLost, statusOf("recovering", "new"), statusOf("running", "new")},
			6, 5 * restartPollInterval, ""},
		{"an old daemon that never goes", []func() (json.RawMessage, error){statusOf("running", "old")}, 11, 10 * restartPollInterval, "no new daemon was running within 2s"},
		{"a new daemon that never runs", []func() (json.RawMessage, error){statusOf("recovering", "new")}, 11, 10 * restartPollInterval, "no new daemon was running within 2s"},
		{"nothing comes back", []func() (json.RawMessage, error){notRunning}, 11, 10 * restartPollInterval, "no new daemon was running within 2s"},
		{"another failure is the answer", []func() (json.RawMessage, error){
			statusOf("running", "old"), failed(apiwire.New(apiwire.CategoryUnavailable, "peer_check_failed", "not the same user"))}, 2, restartPollInterval, "peer_check_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &scriptedNode{status: tc.status}
			var slept time.Duration
			sleep := func(_ context.Context, d time.Duration) error { slept += d; return nil }
			_, err := waitForRestart(context.Background(), node, "old", wait, "/home/x", sleep)
			if node.statusCalls != tc.reads || slept != tc.slept {
				t.Errorf("%d reads and %s slept, want %d and %s", node.statusCalls, slept, tc.reads, tc.slept)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("error %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error %v, want one with %q", err, tc.wantErr)
			}
		})
	}

	t.Run("a wait that is spent says what to check", func(t *testing.T) {
		node := &scriptedNode{status: []func() (json.RawMessage, error){notRunning}}
		_, err := waitForRestart(context.Background(), node, "old", time.Second, "/home/x", func(context.Context, time.Duration) error { return nil })
		var f *failure
		if !errors.As(err, &f) {
			t.Fatalf("error %v, want a failure", err)
		}
		for _, word := range []string{filepath.Join("/home/x", "logs", "daemon.log"), "started"} {
			if !strings.Contains(f.message, word) {
				t.Errorf("the failure %q does not say %q", f.message, word)
			}
		}
	})

	t.Run("an interrupted wait ends with the error of its context", func(t *testing.T) {
		node := &scriptedNode{status: []func() (json.RawMessage, error){statusOf("running", "old")}}
		_, err := waitForRestart(context.Background(), node, "old", time.Minute, "/home/x", func(context.Context, time.Duration) error { return context.Canceled })
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error %v", err)
		}
	})
}

// The restart is requested with one operation identifier and the expected version
// only when it was given; an unknown outcome is asked again with the same request;
// the wait follows the answer, and a wait of zero does not happen.
func TestRestartingTheDaemon(t *testing.T) {
	const id = "0102030405060708090a0b0c0d0e0f10"
	random := func() *bytes.Reader {
		return bytes.NewReader([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	}
	noSleep := func(context.Context, time.Duration) error { return nil }

	t.Run("one request, then the wait", func(t *testing.T) {
		node := &scriptedNode{
			restart: []func() (json.RawMessage, error){answered(restartAnswer(id, false, "old"))},
			status:  []func() (json.RawMessage, error){statusOf("running", "old"), notRunning, statusOf("running", "new")},
		}
		var told []restartResult
		got, err := restartDaemon(context.Background(), node, random(), restartRequest{ExpectVersion: "1.2.3", Wait: time.Minute, Home: "/home/x", Sleep: noSleep},
			func(r restartResult) error { told = append(told, r); return nil })
		if err != nil {
			t.Fatal(err)
		}
		want := restartResult{Restarting: true, Program: "/opt/sidera", Version: "1.2.3", PreviousStartID: "old", Running: true, StartID: "new", OperationID: id}
		if got != want {
			t.Errorf("result %+v, want %+v", got, want)
		}
		if len(told) != 1 || told[0].Running || told[0].StartID != "" || told[0].PreviousStartID != "old" {
			t.Errorf("announced %+v, want the answer alone, before the wait", told)
		}
		if len(node.restartCalls) != 1 {
			t.Fatalf("%d requests, want 1", len(node.restartCalls))
		}
		req := node.restartCalls[0]
		if req.OperationID.IsZero() || !reflect.DeepEqual(req.Params, map[string]any{"expect_version": "1.2.3"}) || req.ExpectedVersion != nil {
			t.Errorf("request %+v, want an operation identifier and the expected version only", req)
		}
	})

	t.Run("no expected version sends none", func(t *testing.T) {
		node := &scriptedNode{
			restart: []func() (json.RawMessage, error){answered(restartAnswer(id, false, "old"))},
			status:  []func() (json.RawMessage, error){statusOf("running", "new")},
		}
		if _, err := restartDaemon(context.Background(), node, random(), restartRequest{Wait: time.Minute, Sleep: noSleep}, nil); err != nil {
			t.Fatal(err)
		}
		if p := node.restartCalls[0].Params; len(p.(map[string]any)) != 0 {
			t.Errorf("params %v, want none", p)
		}
	})

	t.Run("an unknown outcome is asked again with the same request", func(t *testing.T) {
		node := &scriptedNode{
			restart: []func() (json.RawMessage, error){connLost, answered(restartAnswer(id, true, "old"))},
			status:  []func() (json.RawMessage, error){statusOf("running", "new")},
		}
		got, err := restartDaemon(context.Background(), node, random(), restartRequest{ExpectVersion: "1.2.3", Wait: time.Minute, Sleep: noSleep}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(node.restartCalls) != 2 || !reflect.DeepEqual(node.restartCalls[0], node.restartCalls[1]) {
			t.Errorf("requests %+v, want two equal ones", node.restartCalls)
		}
		if !got.Replayed || got.StartID != "new" {
			t.Errorf("result %+v, want a replay seen through to the new daemon", got)
		}
	})

	t.Run("a refusal is the answer and nothing is waited for", func(t *testing.T) {
		refusal := apiwire.New(apiwire.CategoryConflict, "restart_version_mismatch", "the program reports 1.2.2")
		node := &scriptedNode{restart: []func() (json.RawMessage, error){failed(refusal)}}
		_, err := restartDaemon(context.Background(), node, random(), restartRequest{ExpectVersion: "1.2.3", Wait: time.Minute, Sleep: noSleep}, nil)
		if !errors.Is(err, refusal) || node.statusCalls != 0 || len(node.restartCalls) != 1 {
			t.Errorf("error %v after %d requests and %d reads", err, len(node.restartCalls), node.statusCalls)
		}
	})

	t.Run("a daemon without the method is told to be stopped and started", func(t *testing.T) {
		unknown := apiwire.New(apiwire.CategoryNotFound, apiwire.CodeMethodUnknown, "there is no such method")
		node := &scriptedNode{restart: []func() (json.RawMessage, error){failed(unknown)}}
		_, err := restartDaemon(context.Background(), node, random(), restartRequest{Wait: time.Minute, Sleep: noSleep}, nil)
		var f *failure
		if !errors.As(err, &f) {
			t.Fatalf("error %v, want a failure", err)
		}
		for _, word := range []string{"predates `daemon restart`", "sidera daemon stop", "started"} {
			if !strings.Contains(f.message, word) {
				t.Errorf("the failure %q does not say %q", f.message, word)
			}
		}
		if node.statusCalls != 0 || len(node.restartCalls) != 1 {
			t.Errorf("%d requests and %d reads, want one request and no wait", len(node.restartCalls), node.statusCalls)
		}
	})

	t.Run("a wait of zero returns the answer", func(t *testing.T) {
		node := &scriptedNode{restart: []func() (json.RawMessage, error){answered(restartAnswer(id, false, "old"))}}
		got, err := restartDaemon(context.Background(), node, random(), restartRequest{Sleep: noSleep}, nil)
		if err != nil || node.statusCalls != 0 || got.Running || got.StartID != "" {
			t.Errorf("result %+v, error %v after %d reads", got, err, node.statusCalls)
		}
	})
}

// The human output is the key value lines of the other commands: the answer first,
// so that it shows while the daemon is restarting, then what the wait found, then
// the operation. The JSON is one object, written when the wait is over.
func TestRestartOutput(t *testing.T) {
	const id = "0102030405060708090a0b0c0d0e0f10"
	node := func() *scriptedNode {
		return &scriptedNode{
			restart: []func() (json.RawMessage, error){answered(restartAnswer(id, false, "old"))},
			status:  []func() (json.RawMessage, error){statusOf("running", "new")},
		}
	}
	run := func(t *testing.T, json bool, wait time.Duration) string {
		t.Helper()
		var out bytes.Buffer
		c := command{Config: Config{Stdout: nopCloser{&out}}, json: json}
		err := c.restartCommand(context.Background(), node(), bytes.NewReader([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}),
			restartRequest{Wait: wait, Home: "/home/x", Sleep: func(context.Context, time.Duration) error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	if got, want := run(t, false, time.Minute), "restarting true\nprogram /opt/sidera\nversion 1.2.3\nprevious_start_id old\nrunning true\nstart_id new\noperation_id "+id+"\nreplayed false\n"; got != want {
		t.Errorf("human output:\n%s\nwant\n%s", got, want)
	}
	if got, want := run(t, false, 0), "restarting true\nprogram /opt/sidera\nversion 1.2.3\nprevious_start_id old\noperation_id "+id+"\nreplayed false\n"; got != want {
		t.Errorf("human output without a wait:\n%s\nwant\n%s", got, want)
	}
	if got, want := run(t, true, time.Minute), `{"operation_id":"`+id+`","replayed":false,"result":{"restarting":true,"program":"/opt/sidera","version":"1.2.3","previous_start_id":"old","running":true,"start_id":"new"}}`+"\n"; got != want {
		t.Errorf("JSON output:\n%s\nwant\n%s", got, want)
	}
	if got, want := run(t, true, 0), `{"operation_id":"`+id+`","replayed":false,"result":{"restarting":true,"program":"/opt/sidera","version":"1.2.3","previous_start_id":"old"}}`+"\n"; got != want {
		t.Errorf("JSON output without a wait:\n%s\nwant\n%s", got, want)
	}
}

// `daemon restart` plans its method with the expected version when given, keeps
// the wait on the client's side, and is a usage error with arguments, a wait that is
// not a duration or a negative one.
func TestDaemonRestartPlansItsMethod(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		expect string
		wait   time.Duration
		usage  bool
	}{
		{args: []string{"daemon", "restart"}, wait: defaultRestartWait},
		{args: []string{"daemon", "restart", "--expect-version", "0.12.0"}, expect: "0.12.0", wait: defaultRestartWait},
		{args: []string{"daemon", "restart", "--wait=30s", "--expect-version=0.12.0"}, expect: "0.12.0", wait: 30 * time.Second},
		{args: []string{"daemon", "restart", "--wait", "0"}},
		{args: []string{"daemon", "restart", "now"}, usage: true},
		{args: []string{"daemon", "restart", "--wait", "10"}, usage: true},
		{args: []string{"daemon", "restart", "--wait", "-1s"}, usage: true},
		{args: []string{"daemon", "restart", "--expect-version"}, usage: true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			c := command{Config: Config{Getenv: func(string) string { return "" }}}
			c.arguments = arguments(tc.args)
			rest, err := c.globals(tc.args, c.arguments.flags)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := c.managementPlan(rest)
			var u *usageError
			if tc.usage {
				if !errors.As(err, &u) {
					t.Fatalf("error %v, want a usage error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.method != "daemon.restart" || !plan.change || plan.wait != tc.wait || plan.expectVersion != tc.expect {
				t.Errorf("plan %+v, want daemon.restart expecting %q and waiting %s", plan, tc.expect, tc.wait)
			}
		})
	}
}

// A restart is refused as unavailable, and not as a program that failed its check,
// when it is not known how the program was started.
func TestARestartIsUnavailableWhenTheLaunchWasNotRecorded(t *testing.T) {
	_, _, err := relauncher{launchErr: errors.New("no working directory")}.CheckRestart(context.Background())
	if !errors.Is(err, daemon.ErrRestartUnavailable) || !strings.Contains(err.Error(), "no working directory") {
		t.Errorf("error %v, want ErrRestartUnavailable with the reason", err)
	}
}
