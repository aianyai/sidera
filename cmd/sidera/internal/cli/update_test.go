package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera/internal/update"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
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
		Adapters: []string{"claude"}, Missing: []string{"codex"}, Faulted: []string{"relay"}, FaultedMissing: []string{"chat"}, RefreshSetup: true,
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
			for _, line := range []string{"current 0.11.0", "target 0.12.0", "release https://example.test/releases/tag/v0.12.0", "replace /bin/sidera", "restart daemon (running 0.11.0)", "install adapters claude", "codex", "faulted adapters, installed again relay", "install them again by hand) chat", "refresh the client setup"} {
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

// The instances come from adapters.list, in the daemon's order.
func TestManagementNodeInstances(t *testing.T) {
	n := &managementNode{node: &updateScript{answers: map[string][]func() (json.RawMessage, error){
		"adapters.list": {answered(`{"result":{"adapters":[],"instances":[{"instance":"work","adapter":"claude","x":1},{"instance":"chat","adapter":"console","enabled":false}]}}`)},
	}}}
	got, err := n.Instances(context.Background())
	want := []update.Instance{{ID: "work", Adapter: "claude"}, {ID: "chat", Adapter: "console"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Instances = %+v, %v; want %+v", got, err, want)
	}
}

// RefreshSetup runs the refresh of `adapter setup --refresh` and says what became of
// it in the words of the update's report.
func TestManagementNodeRefreshSetup(t *testing.T) {
	refreshOf := func(r *setupRig) (update.SetupReport, error) {
		node := &managementNode{
			node:      &updateScript{answers: map[string][]func() (json.RawMessage, error){"adapters.setup_info": {r.infoAnswer()}}},
			setupEnv:  setupEnv{home: "/home/sidera", homeIsDefault: true, program: "/opt/sidera"},
			setupExec: r.exec,
		}
		return node.RefreshSetup(context.Background(), "work")
	}
	setUp := func(t *testing.T) *setupRig {
		r := newRig(t, clientA, ownO)
		if _, _, err := r.run(modeSetup, "", true); err != nil {
			t.Fatal(err)
		}
		r.exec.requests = nil
		return r
	}

	t.Run("no record", func(t *testing.T) {
		got, err := refreshOf(newRig(t, clientA))
		if err != nil || got.Status != update.SetupNone || len(got.Items) != 0 {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("a record that is up to date", func(t *testing.T) {
		r := setUp(t)
		got, err := refreshOf(r)
		if err != nil || got.Status != update.SetupRefreshed || len(got.Items) != 0 {
			t.Errorf("got %+v, %v", got, err)
		}
		if ops := r.exec.ops(); ops != "refresh" {
			t.Errorf("ops = %q: a refresh is one call, with no questions", ops)
		}
		if req := r.exec.requests[0]; req.Home != "/home/sidera" || req.SideraProgram != "/opt/sidera" || req.Instance != "work" {
			t.Errorf("request = %+v", req)
		}
	})
	t.Run("a record that is stale", func(t *testing.T) {
		r := setUp(t)
		r.script.Items = []fakeadapter.SetupItem{{ID: "a", Consent: true, Content: "A2"}, ownO}
		got, err := refreshOf(r)
		want := update.SetupReport{Status: update.SetupRefreshed, Items: []update.SetupItem{{ID: "a", Status: "done"}}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, %v; want %+v", got, err, want)
		}
	})
	t.Run("drift", func(t *testing.T) {
		r := setUp(t)
		if err := os.Remove(filepath.Join(r.client, "a")); err != nil {
			t.Fatal(err)
		}
		got, err := refreshOf(r)
		if err != nil || got.Status != update.SetupDrift || len(got.Drift) != 1 || got.Drift[0].ID != "a" || got.Drift[0].Problem != "missing" || got.Drift[0].Summary == "" {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("an item fails", func(t *testing.T) {
		for name, tc := range map[string]struct{ raw, message string }{
			"an item failed":                       {`{"version":1,"op":"refresh","status":"error","recorded":true,"outcomes":[{"id":"a","kind":"file","target":"t","status":"failed","message":"boom"}]}`, "1 item(s) failed"},
			"an outcome status of a later release": {`{"version":1,"op":"refresh","status":"refreshed","recorded":true,"outcomes":[{"id":"a","kind":"file","target":"t","status":"a status of a later release"}]}`, "1 item(s) failed"},
			"an error without a failed item":       {`{"version":1,"op":"refresh","status":"error","recorded":true}`, "without a failed item"},
		} {
			r := newRig(t, clientA)
			r.exec.raw = tc.raw
			got, err := refreshOf(r)
			if err != nil || got.Status != update.SetupError || !strings.Contains(got.Message, tc.message) || strings.Contains(got.Message, "0 item(s)") {
				t.Errorf("%s: got %+v, %v; want an error saying %q", name, got, err, tc.message)
			}
		}
	})
	t.Run("an item is shown in the words of adapter setup", func(t *testing.T) {
		r := newRig(t, clientA)
		r.exec.raw = `{"version":1,"op":"refresh","status":"error","recorded":true,"outcomes":[{"id":"a","kind":"file","target":"t","status":"pending","message":"queued"}]}`
		got, err := refreshOf(r)
		want := []update.SetupItem{{ID: "a", Status: "failed", Message: `(its status "pending" is not known to this program) queued`}}
		if err != nil || !reflect.DeepEqual(got.Items, want) {
			t.Errorf("items = %+v, %v; want %+v", got.Items, err, want)
		}
	})
	t.Run("the program cannot be run, or answers nonsense", func(t *testing.T) {
		for _, raw := range []string{"not json", `{"version":2,"op":"refresh","status":"none","recorded":false}`, `{"version":1,"op":"refresh","status":"ok","recorded":true}`} {
			r := newRig(t, clientA)
			r.exec.raw = raw
			if _, err := refreshOf(r); err == nil || !strings.Contains(err.Error(), "work") {
				t.Errorf("%q: err = %v, want a failure naming the instance", raw, err)
			}
		}
	})
	t.Run("skipped", func(t *testing.T) {
		for name, tc := range map[string]struct {
			mod  func(*setupRig)
			word string
		}{
			"the adapter declares no setup": {func(r *setupRig) { r.argv = `null` }, "no setup"},
			"the daemon has no setup_info": {func(r *setupRig) {
				r.infoError = apiwire.New(apiwire.CategoryNotFound, "method_unknown", "no such method")
			}, "daemon"},
			"a package that cannot be loaded": {func(r *setupRig) {
				r.infoError = apiwire.New(apiwire.CategoryRefused, "package_invalid", "the package cannot be loaded")
			}, "cannot be loaded"},
			"an adapter that is not installed": {func(r *setupRig) {
				r.infoError = apiwire.New(apiwire.CategoryNotFound, "adapter_unknown", "no such adapter")
			}, "not installed"},
		} {
			r := newRig(t, clientA)
			tc.mod(r)
			got, err := refreshOf(r)
			if err != nil || got.Status != update.SetupSkipped || !strings.Contains(got.Message, tc.word) || len(r.exec.requests) != 0 {
				t.Errorf("%s: got %+v, %v, requests %d; want skipped saying %q", name, got, err, len(r.exec.requests), tc.word)
			}
		}
	})
	t.Run("the daemon cannot say for another reason", func(t *testing.T) {
		r := newRig(t, clientA)
		r.infoError = apiwire.New(apiwire.CategoryInternal, "internal", "boom")
		if _, err := refreshOf(r); err == nil {
			t.Error("a daemon that failed was taken for one with nothing to refresh")
		}
	})
}

// The command that puts drift right is rendered here, where the data directory and the
// quoting of the shell are known, and is given whenever there is drift to put right.
func TestManagementNodeRefreshSetupFix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the quoting differs on Windows; shellWord has its own test")
	}
	driftOf := func(t *testing.T, env setupEnv, raw string) update.SetupReport {
		r := newRig(t, clientA)
		r.exec.raw = raw
		node := &managementNode{
			node:      &updateScript{answers: map[string][]func() (json.RawMessage, error){"adapters.setup_info": {r.infoAnswer()}}},
			setupEnv:  env,
			setupExec: r.exec,
		}
		got, err := node.RefreshSetup(context.Background(), "work")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	const drift = `{"version":1,"op":"refresh","status":"drift","recorded":true,"drift":[{"id":"a","kind":"file","target":"t","problem":"missing","summary":"gone"}]}`
	const errorWithDrift = `{"version":1,"op":"refresh","status":"error","recorded":true,"outcomes":[{"id":"b","kind":"file","target":"t","status":"failed"}],"drift":[{"id":"a","kind":"file","target":"t","problem":"missing","summary":"gone"}]}`
	const quiet = `{"version":1,"op":"refresh","status":"refreshed","recorded":true}`
	for _, tc := range []struct {
		name, raw string
		env       setupEnv
		want      string
	}{
		{"the default data directory", drift, setupEnv{home: "/home/u/.sidera", homeIsDefault: true}, "sidera adapter setup work"},
		{"another data directory", drift, setupEnv{home: "/srv/my node"}, "sidera --home '/srv/my node' adapter setup work"},
		{"an error that left drift", errorWithDrift, setupEnv{home: "/home/u/.sidera", homeIsDefault: true}, "sidera adapter setup work"},
		{"no drift", quiet, setupEnv{home: "/srv/node"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.env.program = "/opt/sidera"
			if got := driftOf(t, tc.env, tc.raw); got.Fix != tc.want {
				t.Errorf("fix = %q, want %q", got.Fix, tc.want)
			}
		})
	}
}

func TestPrintUpdateSetup(t *testing.T) {
	rep := update.Report{
		Release: update.Release{Current: "0.15.0", Target: "0.16.0"},
		Daemon:  update.DaemonReport{Running: true, Version: "0.16.0"},
		Setup: []update.SetupReport{
			{Instance: "work", Adapter: "claude", Status: update.SetupRefreshed, Items: []update.SetupItem{{ID: "skill", Status: "done"}}},
			{Instance: "idle", Adapter: "codex", Status: update.SetupNone},
			{Instance: "moved", Adapter: "codex", Status: update.SetupDrift, Drift: []update.SetupDifference{{ID: "mcp", Problem: "missing", Summary: "gone"}}, Fix: "sidera adapter setup moved"},
			{Instance: "chat", Adapter: "console", Status: update.SetupSkipped, Message: "the adapter declares no setup"},
			{Instance: "stuck", Adapter: "hermes", Status: update.SetupError, Message: "1 item(s) failed", Items: []update.SetupItem{{ID: "p", Status: "failed", Message: "boom"}}},
		},
	}
	var out bytes.Buffer
	c := &command{Config: Config{Stdout: nopWriteCloser{&out}}}
	if err := c.printUpdate(rep, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"setup work refreshed\n", "setup work item skill done\n", "setup idle none\n",
		"setup moved drift mcp missing gone\n", "setup moved fix sidera adapter setup moved\n",
		"setup chat skipped the adapter declares no setup\n", "setup stuck error 1 item(s) failed\n", "setup stuck item p failed boom\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	out.Reset()
	c.json = true
	if err := c.printUpdate(rep, nil); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Setup []map[string]any `json:"setup"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.Setup) != 5 || got.Setup[2]["status"] != "drift" || got.Setup[2]["fix"] != "sidera adapter setup moved" || got.Setup[2]["instance"] != "moved" {
		t.Errorf("json setup = %+v, %v", got.Setup, err)
	}
}
