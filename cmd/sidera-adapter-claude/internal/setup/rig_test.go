package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// rig is one instance's setup against a fake Claude Code: a state directory, a
// configuration directory of Claude Code, and the program the server is registered with.
type rig struct {
	t        *testing.T
	state    string
	config   string
	program  string
	version  string
	home     string
	homeDef  bool
	values   map[string]json.RawMessage
	claude   *fakeClaude
	clientIs error
	// userHome is the home directory of the person; configEnv is CLAUDE_CONFIG_DIR, empty
	// when it is unset (the configuration is then .claude in userHome).
	userHome  string
	configEnv string
	// clientEnv is the environment the handler asked for Claude Code with, last time.
	clientEnv []string
	// declined is sent with an apply, as the CLI does for the items a person said no to.
	declined []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{t: t, state: filepath.Join(dir, "state"), config: filepath.Join(dir, "claude"), version: "1.0.0",
		home: filepath.Join(dir, "home"), homeDef: true, values: map[string]json.RawMessage{}, userHome: filepath.Join(dir, "user")}
	r.configEnv = r.config
	if err := os.Mkdir(r.state, 0o700); err != nil {
		t.Fatal(err)
	}
	r.program = filepath.Join(dir, "sidera")
	if err := os.WriteFile(r.program, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.claude = &fakeClaude{fail: map[string]error{}}
	r.claude.version = func() string {
		data, err := os.ReadFile(filepath.Join(r.srcDir(), "plugins", "sidera", ".claude-plugin", "plugin.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m struct{ Version string }
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		return m.Version
	}
	return r
}

func (r *rig) srcDir() string   { return sourceDir(r.state) }
func (r *rig) settings() string { return filepath.Join(r.config, "settings.json") }

func (r *rig) handler() *Handler {
	var environ []string
	if r.configEnv != "" {
		environ = []string{"CLAUDE_CONFIG_DIR=" + r.configEnv}
	}
	return New(Options{Version: r.version, Environ: environ, Home: r.userHome, NewClient: func(_ adaptersetup.Request, env []string) (Client, error) {
		r.clientEnv = env
		if r.clientIs != nil {
			return nil, r.clientIs
		}
		return r.claude, nil
	}})
}

func (r *rig) request(op adaptersetup.Op, accepted ...string) adaptersetup.Request {
	return adaptersetup.Request{Version: adaptersetup.Version, Op: op, Home: r.home, HomeIsDefault: r.homeDef, Instance: "claude",
		StateDir: r.state, Config: r.values, SideraProgram: r.program, Accepted: accepted, Declined: r.declined}
}

// run sends a request through the contract's helper, as the program does.
func (r *rig) run(op adaptersetup.Op, accepted ...string) (adaptersetup.Result, error) {
	return adaptersetup.Handle(context.Background(), r.handler(), r.request(op, accepted...))
}

func (r *rig) mustRun(op adaptersetup.Op, accepted ...string) adaptersetup.Result {
	r.t.Helper()
	res, err := r.run(op, accepted...)
	if err != nil {
		r.t.Fatalf("%s: %v", op, err)
	}
	return res
}

func (r *rig) undo(accepted ...string) adaptersetup.Result {
	r.t.Helper()
	req := r.request(adaptersetup.OpApply, accepted...)
	req.Undo = true
	res, err := adaptersetup.Handle(context.Background(), r.handler(), req)
	if err != nil {
		r.t.Fatalf("undo: %v", err)
	}
	return res
}

// setUp does everything setup would do, accepting every item.
func (r *rig) setUp() {
	r.t.Helper()
	plan := r.mustRun(adaptersetup.OpPlan)
	res := r.mustRun(adaptersetup.OpApply, ids(plan.Items)...)
	if res.Status != adaptersetup.StatusOK {
		r.t.Fatalf("setup ended %s: %+v", res.Status, res.Outcomes)
	}
	r.claude.calls = nil
}

func ids(items []adaptersetup.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func outcomeIDs(os []adaptersetup.Outcome, status adaptersetup.OutcomeStatus) []string {
	var out []string
	for _, o := range os {
		if o.Status == status {
			out = append(out, o.ID)
		}
	}
	return out
}

func driftOf(res adaptersetup.Result) map[string]adaptersetup.Problem {
	out := map[string]adaptersetup.Problem{}
	for _, d := range res.Drift {
		out[d.ID] = d.Problem
	}
	return out
}

func (r *rig) record() *adaptersetup.Record {
	r.t.Helper()
	rec, err := adaptersetup.ReadRecord(r.state)
	if err != nil {
		r.t.Fatal(err)
	}
	return rec
}

func (r *rig) writeSettings(text string) {
	r.t.Helper()
	if err := os.MkdirAll(r.config, 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(r.settings(), []byte(text), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) readSettings() string {
	data, err := os.ReadFile(r.settings())
	if err != nil {
		r.t.Fatal(err)
	}
	return string(data)
}

func equal(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s: got %q, want %q", what, got, want)
	}
}

// envValue is the value of a variable in an environment, and whether it is there.
func envValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v, true
		}
	}
	return "", false
}
