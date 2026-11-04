package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/update"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/manageapi"
)

// Everything that stops `update` before it reaches the network or the node is the
// command's: the arguments, and the question it cannot ask.
func TestUpdateRefusesBeforeDoingAnything(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no terminal and no --yes", []string{"update"}, "--yes"},
		{"no terminal with a version", []string{"update", "--version", "0.12.0"}, "--yes"},
		{"a version that is not one", []string{"update", "--check", "--version", "latest"}, "--version"},
		{"an argument", []string{"update", "--check", "now"}, "no arguments"},
		{"an unknown flag", []string{"update", "--force"}, "unknown flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errs := runCLI(t, nil, tc.args...)
			if code != apiwire.ExitUsage || out != "" || !strings.Contains(errs, tc.want) {
				t.Errorf("exit %d, stdout %q, stderr %q; want a usage error that says %q", code, out, errs, tc.want)
			}
		})
	}
}

// updateScript answers the management methods update calls.
type updateScript struct {
	answers map[string][]func() (json.RawMessage, error)
	calls   []manageapi.Request
	methods []string
}

func (s *updateScript) Call(_ context.Context, method string, in manageapi.Request) (json.RawMessage, error) {
	s.methods = append(s.methods, method)
	s.calls = append(s.calls, in)
	n := 0
	for _, m := range s.methods {
		if m == method {
			n++
		}
	}
	answers := s.answers[method]
	if len(answers) == 0 {
		return nil, errors.New("unexpected method " + method)
	}
	return answers[min(n, len(answers))-1]()
}

// The node is read loosely (fields it does not know are ignored), a daemon that is
// not running is told apart from one that fails, and an installation is asked again
// with the same identifier when its outcome is unknown.
func TestManagementNode(t *testing.T) {
	ctx := context.Background()
	random := bytes.NewReader(bytes.Repeat([]byte{7}, 64))

	t.Run("status", func(t *testing.T) {
		n := &managementNode{node: &updateScript{answers: map[string][]func() (json.RawMessage, error){
			"status": {answered(`{"result":{"node":{"id":"n","version":"0.12.0","future":1},"daemon":{"state":"running"}}}`)},
		}}}
		got, err := n.Status(ctx)
		if err != nil || got.Version != "0.12.0" {
			t.Errorf("Status = %+v, %v", got, err)
		}
	})
	t.Run("no daemon", func(t *testing.T) {
		n := &managementNode{node: &updateScript{answers: map[string][]func() (json.RawMessage, error){"status": {notRunning}}}}
		if _, err := n.Status(ctx); !errors.Is(err, update.ErrNotRunning) {
			t.Errorf("Status = %v, want ErrNotRunning", err)
		}
	})
	t.Run("a failing daemon is not a missing one", func(t *testing.T) {
		n := &managementNode{node: &updateScript{answers: map[string][]func() (json.RawMessage, error){
			"status": {failed(apiwire.New(apiwire.CategoryUnavailable, "peer_check_failed", "not the same user"))}}}}
		if _, err := n.Status(ctx); err == nil || errors.Is(err, update.ErrNotRunning) {
			t.Errorf("Status = %v", err)
		}
	})
	t.Run("adapters", func(t *testing.T) {
		n := &managementNode{node: &updateScript{answers: map[string][]func() (json.RawMessage, error){
			"adapters.list": {answered(`{"result":{"version":3,"adapters":[{"id":"claude","version":"0.11.0","x":1},{"id":"codex","version":"0.12.0"},` +
				`{"id":"console","version":"","fault":{"reason":"package_invalid"}},{"id":"relay","version":"0.12.0"},{"id":"chat","version":"0.12.0"}],` +
				`"instances":[{"instance":"codex","adapter":"codex","enabled":true,"fault":{"reason":"program_missing"}},` +
				`{"instance":"relay","adapter":"relay","enabled":true,"probe":{"state":"pending","frontends":[]}},` +
				`{"instance":"chat","adapter":"chat","enabled":false}]}}`)},
		}}}
		got, err := n.Adapters(ctx)
		want := []update.Adapter{
			{ID: "claude", Version: "0.11.0"}, {ID: "codex", Version: "0.12.0", Faulted: true},
			{ID: "console", Faulted: true}, {ID: "relay", Version: "0.12.0"}, {ID: "chat", Version: "0.12.0"},
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Adapters = %+v, %v\nwant %+v", got, err, want)
		}
	})
	t.Run("install", func(t *testing.T) {
		script := &updateScript{answers: map[string][]func() (json.RawMessage, error){
			"adapters.install": {connLost, answered(`{"operation_id":"x","result":{"adapter":{"id":"claude","version":"0.12.0"},"replaced":true}}`)},
		}}
		n := &managementNode{node: script, random: random}
		got, err := n.Install(ctx, "/cache/adapters/claude")
		if err != nil || got != (update.Installed{ID: "claude", Version: "0.12.0", Replaced: true}) {
			t.Fatalf("Install = %+v, %v", got, err)
		}
		if len(script.calls) != 2 || script.calls[0].OperationID != script.calls[1].OperationID || script.calls[0].OperationID.IsZero() {
			t.Fatalf("calls = %+v, want the same request twice", script.calls)
		}
		if p, _ := script.calls[0].Params.(map[string]any); p["source"] != "/cache/adapters/claude" {
			t.Errorf("params = %+v", script.calls[0].Params)
		}
	})
	t.Run("restart", func(t *testing.T) {
		script := &updateScript{answers: map[string][]func() (json.RawMessage, error){
			"daemon.restart": {answered(restartAnswer("id", false, "old"))},
			"status":         {statusOf("running", "new")},
		}}
		n := &managementNode{node: script, random: random}
		if err := n.Restart(ctx, "0.12.0"); err != nil {
			t.Fatal(err)
		}
		if p, _ := script.calls[0].Params.(map[string]any); p["expect_version"] != "0.12.0" {
			t.Errorf("params = %+v, want the version the program must report", script.calls[0].Params)
		}
	})
}

func TestConfirmUpdate(t *testing.T) {
	plan := update.Plan{
		Current: "0.11.0", Target: "0.12.0", ReleaseURL: "https://example.test/releases/tag/v0.12.0",
		Replace: []string{"/bin/sidera"}, Daemon: true, Restart: true, DaemonVersion: "0.11.0",
		Adapters: []string{"claude"}, Missing: []string{"codex"}, Faulted: []string{"relay"}, FaultedMissing: []string{"chat"},
	}
	for _, tc := range []struct {
		name, input string
		yes, want   bool
		asked       bool
	}{
		{"y", "y\n", false, true, true},
		{"yes in capitals", "YES\n", false, true, true},
		{"n", "n\n", false, false, true},
		{"nothing", "", false, false, true},
		{"anything else", "sure\n", false, false, true},
		{"--yes does not ask", "", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &command{Config: Config{Stdin: io.NopCloser(strings.NewReader(tc.input))}}
			var out bytes.Buffer
			got, err := c.confirmUpdate(&out, plan, tc.yes)
			if err != nil || got != tc.want {
				t.Fatalf("confirmUpdate = %v, %v; want %v", got, err, tc.want)
			}
			if asked := strings.Contains(out.String(), "Update? [y/N]"); asked != tc.asked {
				t.Errorf("asked = %v, want %v", asked, tc.asked)
			}
			for _, line := range []string{"current 0.11.0", "target 0.12.0", "release https://example.test/releases/tag/v0.12.0", "replace /bin/sidera", "restart daemon (running 0.11.0)", "install adapters claude", "codex", "faulted adapters, installed again relay", "install them again by hand) chat"} {
				if !strings.Contains(out.String(), line) {
					t.Errorf("the plan does not show %q:\n%s", line, out.String())
				}
			}
		})
	}
}

func TestPrintUpdate(t *testing.T) {
	rep := update.Report{
		Release: update.Release{Current: "0.11.0", Target: "0.12.0", ReleaseURL: "https://example.test/v0.12.0", Floor: "0.8.0", Admitted: true},
		Program: update.ProgramReport{Updated: true, Replaced: []string{"/bin/sidera"}},
		Daemon:  update.DaemonReport{Running: true, Restarted: true, Version: "0.12.0"},
		Adapters: []update.AdapterReport{{ID: "claude", Version: "0.12.0", Replaced: true}, {ID: "codex", Skipped: "the release has no package of this adapter"},
			{ID: "relay", Version: "0.12.0", Replaced: true, Faulted: true}},
	}
	t.Run("human run", func(t *testing.T) {
		var out bytes.Buffer
		c := &command{Config: Config{Stdout: nopWriteCloser{&out}}}
		if err := c.printUpdate(rep, nil); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"program updated /bin/sidera", "daemon restarted version 0.12.0", "adapter claude version 0.12.0 replaced true", "adapter codex skipped", "adapter relay was faulted, installed again version 0.12.0", "removed at the next daemon start", ".old"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("missing %q in\n%s", want, out.String())
			}
		}
		if strings.Contains(out.String(), "update_available") {
			t.Errorf("a run reports what a check does:\n%s", out.String())
		}
	})
	t.Run("human check", func(t *testing.T) {
		var out bytes.Buffer
		c := &command{Config: Config{Stdout: nopWriteCloser{&out}}}
		check := update.Verdict{Release: update.Release{Current: "0.11.0", Target: "0.12.0", Admitted: true, Floor: "0.8.0"}, UpdateAvailable: true}
		if err := c.printCheck(check); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"update_available true", "admitted true", "floor 0.8.0"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("missing %q in\n%s", want, out.String())
			}
		}
	})
	t.Run("json with a failure", func(t *testing.T) {
		var out bytes.Buffer
		c := &command{Config: Config{Stdout: nopWriteCloser{&out}}, json: true}
		if err := c.printUpdate(rep, errors.New("restart the daemon: boom")); err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("%v: %s", err, out.String())
		}
		if got["error"] != "restart the daemon: boom" || got["target"] != "0.12.0" || got["admitted"] != true {
			t.Errorf("json = %v", got)
		}
		if _, ok := got["update_available"]; ok {
			t.Errorf("a run reports what a check does: %v", got)
		}
	})
	// A check looked at the release only; it must not report the daemon or the programs as
	// if it had found them as they are.
	t.Run("json check", func(t *testing.T) {
		var out bytes.Buffer
		c := &command{Config: Config{Stdout: nopWriteCloser{&out}}, json: true}
		check := update.Verdict{Release: update.Release{Current: "0.11.0", Target: "0.12.0", Admitted: true}, UpdateAvailable: true}
		if err := c.printCheck(check); err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("%v: %s", err, out.String())
		}
		if got["update_available"] != true || got["admitted"] != true || got["target"] != "0.12.0" {
			t.Errorf("json = %v", got)
		}
		for _, key := range []string{"program", "daemon", "adapters", "already_current"} {
			if _, ok := got[key]; ok {
				t.Errorf("a check reports %q: %v", key, got)
			}
		}
	})
	t.Run("no daemon", func(t *testing.T) {
		var out bytes.Buffer
		c := &command{Config: Config{Stdout: nopWriteCloser{&out}}}
		if err := c.printUpdate(update.Report{Release: rep.Release}, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "daemon not running") || strings.Contains(out.String(), "already_current") {
			t.Errorf("report:\n%s", out.String())
		}
	})
}
