package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// Everything that stops `adapter setup` before it asks the daemon anything is the
// command's: the arguments, the modes that exclude each other, the question it
// cannot ask.
func TestAdapterSetupRefusesBeforeConnecting(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no instance", []string{"adapter", "setup", "--yes"}, "exactly one instance"},
		{"two instances", []string{"adapter", "setup", "a", "b", "--yes"}, "exactly one instance"},
		{"only adapter words", []string{"adapter", "setup", "--yes", "--", "work"}, "exactly one instance"},
		{"check and remove", []string{"adapter", "setup", "work", "--check", "--remove"}, "different modes"},
		{"refresh and remove", []string{"adapter", "setup", "work", "--refresh", "--remove", "--yes"}, "different modes"},
		{"check and refresh", []string{"adapter", "setup", "work", "--check", "--refresh"}, "different modes"},
		{"yes with check", []string{"adapter", "setup", "work", "--check", "--yes"}, "--yes"},
		{"yes with refresh", []string{"adapter", "setup", "work", "--refresh", "--yes"}, "--yes"},
		{"no terminal and no --yes", []string{"adapter", "setup", "work"}, "no terminal to ask on"},
		{"no terminal and no --yes, removing", []string{"adapter", "setup", "work", "--remove"}, "--yes"},
		{"an unknown flag", []string{"adapter", "setup", "work", "--apply"}, "unknown flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errs := runCLI(t, nil, tc.args...)
			if code != apiwire.ExitUsage || out != "" || !strings.Contains(errs, tc.want) {
				t.Errorf("exit %d, stdout %q, stderr %q; want a usage error that says %q", code, out, errs, tc.want)
			}
		})
	}
}

// The words after the first -- are the adapter's, untouched, flags and a second --
// included.
func TestSplitOptions(t *testing.T) {
	for _, tc := range []struct {
		args, own, options []string
	}{
		{[]string{"work", "--yes"}, []string{"work", "--yes"}, nil},
		{[]string{"work", "--", "--profile", "p", "--yes", "--"}, []string{"work"}, []string{"--profile", "p", "--yes", "--"}},
		{[]string{"--", "x"}, []string{}, []string{"x"}},
	} {
		own, options := splitOptions(tc.args)
		if fmt.Sprint(own) != fmt.Sprint(tc.own) || fmt.Sprint(options) != fmt.Sprint(tc.options) {
			t.Errorf("splitOptions(%v) = %v, %v; want %v, %v", tc.args, own, options, tc.own, tc.options)
		}
	}
}

// inProcess runs the fake adapter's setup in the test process, through the same
// command a program would, so the request and the result cross the real codec.
type inProcess struct {
	requests []adaptersetup.Request
	// raw, when set, is what the program answers, whatever it was asked; byOp, when it
	// has the op, is what it answers to that op.
	raw  string
	byOp map[adaptersetup.Op]string
	// exit, when set, is the exit code the program fails with.
	exit int
}

func (p *inProcess) Run(_ context.Context, argv []string, request []byte) ([]byte, error) {
	var req adaptersetup.Request
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, err
	}
	p.requests = append(p.requests, req)
	if raw, ok := p.byOp[req.Op]; ok {
		return []byte(raw), nil
	}
	if p.raw != "" {
		return []byte(p.raw), nil
	}
	var out, errs bytes.Buffer
	code := fakeadapter.SetupCommand()(nil, adapterkit.Streams{Stdin: bytes.NewReader(request), Stdout: &out, Stderr: &errs})
	if code != 0 {
		return nil, fmt.Errorf("exit status %d; its standard error ends: %s", code, strings.TrimSpace(errs.String()))
	}
	return out.Bytes(), nil
}

func (p *inProcess) ops() string {
	var ops []string
	for _, r := range p.requests {
		o := string(r.Op)
		if r.Undo {
			o += "(undo)"
		}
		ops = append(ops, o)
	}
	return strings.Join(ops, " ")
}

// setupRig is an instance with a state directory and a client directory, and the
// daemon's answer to adapters.setup_info for it.
type setupRig struct {
	t         *testing.T
	state     string
	client    string
	script    fakeadapter.SetupConfig
	exec      *inProcess
	options   []string
	argv      string // the setup_argv member of the answer
	infoError error
}

func newRig(t *testing.T, items ...fakeadapter.SetupItem) *setupRig {
	t.Helper()
	dir := t.TempDir()
	r := &setupRig{t: t, state: filepath.Join(dir, "state"), client: filepath.Join(dir, "client"), exec: &inProcess{}, argv: `["/pkg/sidera-fake-adapter","setup"]`}
	for _, d := range []string{r.state, r.client} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	r.script = fakeadapter.SetupConfig{ClientDir: r.client, Items: items}
	return r
}

func (r *setupRig) session() (*setupSession, error) {
	r.t.Helper()
	cfg, _ := json.Marshal(map[string]any{"setup": r.script})
	answer := failed(r.infoError)
	if r.infoError == nil {
		stateDir, _ := json.Marshal(r.state)
		answer = answered(fmt.Sprintf(`{"result":{"instance":"work","adapter":"fake","state_dir":%s,"config":%s,"setup_argv":%s}}`, stateDir, cfg, r.argv))
	}
	node := &updateScript{answers: map[string][]func() (json.RawMessage, error){"adapters.setup_info": {answer}}}
	return openSetupSession(context.Background(), node, r.exec, "work", setupEnv{home: "/home/sidera", homeIsDefault: true, program: "/opt/sidera"}, r.options)
}

// run runs a mode with the answers a person types and returns what the command would
// have shown: the questions (standard error), the report (standard output) and the
// verdict.
func (r *setupRig) run(mode setupMode, answers string, yes bool) (asked, report string, verdict error) {
	r.t.Helper()
	s, err := r.session()
	if err != nil {
		r.t.Fatal(err)
	}
	var questions bytes.Buffer
	res, applied, err := runSetup(context.Background(), s, mode, setupDialog{out: &questions, answers: bufio.NewReader(strings.NewReader(answers)), yes: yes})
	if err != nil {
		return questions.String(), "", err
	}
	var out bytes.Buffer
	c := &command{Config: Config{Stdout: nopWriteCloser{&out}}}
	if err := c.printSetup("work", res, applied); err != nil {
		r.t.Fatal(err)
	}
	return questions.String(), out.String(), setupVerdict("work", res)
}

func (r *setupRig) exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

var (
	clientA = fakeadapter.SetupItem{ID: "a", Consent: true, Content: "A"}
	clientB = fakeadapter.SetupItem{ID: "b", Consent: true, Content: "B"}
	ownO    = fakeadapter.SetupItem{ID: "o", Content: "O"}
)

// Items that need consent are asked one by one, the others are done without asking,
// and a person who declines declines only that item.
func TestAdapterSetupAsksItemByItem(t *testing.T) {
	for _, tc := range []struct {
		name     string
		answers  string
		yes      bool
		wantFile map[string]bool // by item id
		asked    []string
		notAsked []string
		ops      string
	}{
		{"yes and no", "y\nn\n", false, map[string]bool{"a": true, "b": false, "o": true}, []string{"write a? [y/N]", "write b? [y/N]"}, []string{"write o?"}, "plan apply"},
		{"the whole word, in capitals", "YES\nyes\n", false, map[string]bool{"a": true, "b": true, "o": true}, []string{"write a?"}, nil, "plan apply"},
		{"--yes does not ask", "", true, map[string]bool{"a": true, "b": true, "o": true}, nil, []string{"[y/N]"}, "plan apply"},
		{"the input ends", "y\n", false, map[string]bool{"a": true, "b": false, "o": true}, []string{"write b?"}, nil, "plan apply"},
		{"declining everything still does the item that needs no consent", "n\nn\n", false, map[string]bool{"a": false, "b": false, "o": true}, nil, nil, "plan apply"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, clientA, clientB, ownO)
			asked, report, verdict := r.run(modeSetup, tc.answers, tc.yes)
			if verdict != nil {
				t.Fatalf("verdict %v", verdict)
			}
			paths := map[string]string{"a": filepath.Join(r.client, "a"), "b": filepath.Join(r.client, "b"), "o": filepath.Join(r.state, "setup", "files", "o")}
			for id, want := range tc.wantFile {
				if got := r.exists(paths[id]); got != want {
					t.Errorf("file %s exists = %v, want %v", id, got, want)
				}
				if got := strings.Contains(report, "item "+id+" done"); got != want {
					t.Errorf("report says %s done = %v, want %v:\n%s", id, got, want, report)
				}
			}
			for _, s := range tc.asked {
				if !strings.Contains(asked, s) {
					t.Errorf("never asked %q:\n%s", s, asked)
				}
			}
			for _, s := range tc.notAsked {
				if strings.Contains(asked, s) {
					t.Errorf("asked %q:\n%s", s, asked)
				}
			}
			if !strings.Contains(asked, "no question") {
				t.Errorf("the plan does not say which item is done without a question:\n%s", asked)
			}
			if got := r.exec.ops(); got != tc.ops {
				t.Errorf("ops = %q, want %q", got, tc.ops)
			}
		})
	}
}

func TestAdapterSetupAppliesNothingWhenNothingIsAccepted(t *testing.T) {
	r := newRig(t, clientA)
	_, report, verdict := r.run(modeSetup, "n\n", false)
	if verdict != nil || r.exec.ops() != "plan" || !strings.Contains(report, "nothing applied") || r.exists(filepath.Join(r.client, "a")) {
		t.Errorf("verdict %v, ops %q, report %q", verdict, r.exec.ops(), report)
	}
	r = newRig(t) // nothing wanted at all
	_, report, verdict = r.run(modeSetup, "", true)
	if verdict != nil || r.exec.ops() != "plan" || !strings.Contains(report, "nothing to do") {
		t.Errorf("verdict %v, ops %q, report %q", verdict, r.exec.ops(), report)
	}
}

// The request carries what the program needs from the command, and the words after
// -- reach it untouched.
func TestAdapterSetupRequest(t *testing.T) {
	r := newRig(t, clientA)
	r.options = []string{"--profile", "p", "--"}
	asked, _, _ := r.run(modeSetup, "y\n", false)
	if !strings.Contains(asked, "(options: --profile p --)") {
		t.Errorf("the options did not reach the adapter:\n%s", asked)
	}
	plan, apply := r.exec.requests[0], r.exec.requests[1]
	if plan.Version != 1 || plan.Op != "plan" || plan.Home != "/home/sidera" || !plan.HomeIsDefault || plan.Instance != "work" ||
		plan.StateDir != r.state || plan.SideraProgram != "/opt/sidera" || string(plan.Config["setup"]) == "" || len(plan.Accepted) != 0 || plan.Undo {
		t.Errorf("plan request = %+v", plan)
	}
	if apply.Op != "apply" || fmt.Sprint(apply.Accepted) != "[a]" || apply.Undo {
		t.Errorf("apply request = %+v", apply)
	}
}

func TestAdapterSetupRemove(t *testing.T) {
	r := newRig(t, clientA, clientB, ownO)
	if _, _, err := r.run(modeSetup, "", true); err != nil {
		t.Fatal(err)
	}
	r.exec.requests = nil
	asked, report, verdict := r.run(modeRemove, "n\ny\n", false)
	if verdict != nil || r.exec.ops() != "remove apply(undo)" {
		t.Fatalf("verdict %v, ops %q", verdict, r.exec.ops())
	}
	if !strings.Contains(asked, "delete a? [y/N]") || strings.Contains(asked, "delete o?") {
		t.Errorf("questions:\n%s", asked)
	}
	if !r.exists(filepath.Join(r.client, "a")) || r.exists(filepath.Join(r.client, "b")) || r.exists(filepath.Join(r.state, "setup", "files", "o")) {
		t.Errorf("a was declined and stays; b was accepted and o needs no question: %s", report)
	}
	// What is left is still recorded, until the last one goes.
	if rec, _ := adaptersetup.ReadRecord(r.state); rec == nil || len(rec.Items) != 1 {
		t.Errorf("record = %+v", rec)
	}
	if _, _, err := r.run(modeRemove, "", true); err != nil {
		t.Fatal(err)
	}
	if rec, _ := adaptersetup.ReadRecord(r.state); rec != nil {
		t.Errorf("the record outlived its last item: %+v", rec)
	}
}

// check is exit 0 without drift and 1 with drift, which includes what setup would still
// have to make; refresh asks nothing, rewrites what is out of date and never makes again
// what was removed.
func TestAdapterSetupCheckAndRefresh(t *testing.T) {
	// An adapter with nothing wanted has no record and no drift. One that wants
	// something and has no record says what setup would have to make, and that is drift.
	if _, report, err := newRig(t).run(modeCheck, "", false); err != nil || !strings.Contains(report, "status none") || !strings.Contains(report, "recorded false") {
		t.Errorf("check with nothing wanted: %v\n%s", err, report)
	}
	r := newRig(t, clientA, ownO)
	if _, report, err := r.run(modeCheck, "", false); err == nil || !strings.Contains(report, "status drift") || !strings.Contains(report, "recorded false") ||
		!strings.Contains(report, "drift a needs_setup") || !strings.Contains(report, "drift o needs_setup") {
		t.Errorf("check before setup: %v\n%s", err, report)
	}
	if _, report, err := r.run(modeRefresh, "", false); err != nil || !strings.Contains(report, "status none") {
		t.Errorf("refresh before setup: %v\n%s", err, report)
	}
	if _, _, err := r.run(modeSetup, "", true); err != nil {
		t.Fatal(err)
	}
	if _, report, err := r.run(modeCheck, "", false); err != nil || !strings.Contains(report, "status ok") || !strings.Contains(report, "recorded true") {
		t.Errorf("clean check: %v\n%s", err, report)
	}

	// The adapter now writes other content: stale, and refresh brings it up to date
	// without a question.
	r.script.Items = []fakeadapter.SetupItem{{ID: "a", Consent: true, Content: "A2"}, ownO}
	if _, report, err := r.run(modeCheck, "", false); err == nil || !strings.Contains(report, "drift a stale") {
		t.Errorf("stale check: %v\n%s", err, report)
	}
	asked, report, err := r.run(modeRefresh, "", false)
	if err != nil || asked != "" || !strings.Contains(report, "status refreshed") || !strings.Contains(report, "item a done") {
		t.Errorf("refresh: %v, asked %q\n%s", err, asked, report)
	}
	if got, _ := os.ReadFile(filepath.Join(r.client, "a")); string(got) != "A2" {
		t.Errorf("a = %q", got)
	}

	// Someone edits a file: drift, and refresh leaves it alone.
	if err := os.WriteFile(filepath.Join(r.client, "a"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.script.Items[0].Content = "A3"
	for _, mode := range []setupMode{modeCheck, modeRefresh} {
		if _, report, err := r.run(mode, "", false); err == nil || !strings.Contains(report, "drift a changed") {
			t.Errorf("mode %d on an edited file: %v\n%s", mode, err, report)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(r.client, "a")); string(got) != "mine" {
		t.Errorf("refresh overwrote an edit: %q", got)
	}

	// The user removes a file: drift, and refresh does not make it again.
	if err := os.Remove(filepath.Join(r.client, "a")); err != nil {
		t.Fatal(err)
	}
	if _, report, err := r.run(modeRefresh, "", false); err == nil || !strings.Contains(report, "drift a missing") {
		t.Errorf("refresh of a removed file: %v\n%s", err, report)
	}
	if r.exists(filepath.Join(r.client, "a")) {
		t.Error("refresh made again a file the user removed")
	}

	// An item the adapter now wants and no record has needs setup, never made by refresh.
	r.script.Items = append(r.script.Items, clientB)
	if _, report, err := r.run(modeRefresh, "", false); err == nil || !strings.Contains(report, "drift b needs_setup") || r.exists(filepath.Join(r.client, "b")) {
		t.Errorf("refresh with a new item: %v\n%s", err, report)
	}
}

func TestAdapterSetupFailuresAreExitOne(t *testing.T) {
	t.Run("an item fails", func(t *testing.T) {
		r := newRig(t, clientA, clientB)
		r.script.Fail = []string{"b"}
		_, report, err := r.run(modeSetup, "", true)
		var f *failure
		if !errors.As(err, &f) || !strings.Contains(f.message, "1 item(s) failed") || !strings.Contains(report, "item a done") || !strings.Contains(report, "item b failed scripted to fail") {
			t.Errorf("err %v\n%s", err, report)
		}
	})
	t.Run("the state directory is not there", func(t *testing.T) {
		r := newRig(t, clientA)
		if err := os.Remove(r.state); err != nil {
			t.Fatal(err)
		}
		_, _, err := r.run(modeSetup, "", true)
		var f *failure
		if !errors.As(err, &f) || !strings.Contains(f.message, "work") || !strings.Contains(f.message, "start the daemon") {
			t.Errorf("err %v; want a failure naming the instance and the fix", err)
		}
	})
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"not JSON", "plan ok", "not a result"},
		{"another version", `{"version":2,"op":"plan","status":"ok"}`, "version 2"},
		{"another op", `{"version":1,"op":"check","status":"ok"}`, `"check"`},
		{"a status of a later version", `{"version":1,"op":"plan","status":"fine"}`, `"fine"`},
		{"an item id twice", `{"version":1,"op":"plan","status":"ok","items":[{"id":"a","action":"create"},{"id":"a","action":"create"}]}`, "used twice"},
	} {
		t.Run("the program answers "+tc.name, func(t *testing.T) {
			r := newRig(t, clientA)
			r.exec.raw = tc.raw
			_, _, err := r.run(modeSetup, "", true)
			var f *failure
			if !errors.As(err, &f) || !strings.Contains(f.message, "work") || !strings.Contains(f.message, tc.want) {
				t.Errorf("err %v; want a failure naming the instance and %q", err, tc.want)
			}
		})
	}
	t.Run("a member the program adds is ignored", func(t *testing.T) {
		r := newRig(t)
		r.exec.raw = `{"version":1,"op":"plan","status":"ok","future":{"x":1},"items":[]}`
		if _, report, err := r.run(modeSetup, "", true); err != nil || !strings.Contains(report, "nothing to do") {
			t.Errorf("err %v\n%s", err, report)
		}
	})
}

func TestAdapterSetupJSON(t *testing.T) {
	r := newRig(t, clientA)
	s, err := r.session()
	if err != nil {
		t.Fatal(err)
	}
	res, applied, err := runSetup(context.Background(), s, modeSetup, setupDialog{out: &bytes.Buffer{}, yes: true})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &command{Config: Config{Stdout: nopWriteCloser{&out}}, json: true}
	if err := c.printSetup("work", res, applied); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%q: %v", out.String(), err)
	}
	outcomes, _ := got["outcomes"].([]any)
	if got["instance"] != "work" || got["version"] != float64(1) || got["op"] != "apply" || got["status"] != "ok" || len(outcomes) != 1 {
		t.Errorf("JSON = %v", got)
	}
}

// How the daemon's answer is read: no setup, a daemon that is older than this program,
// and any other failure as it is.
func TestOpenSetupSession(t *testing.T) {
	notFound := apiwire.New(apiwire.CategoryNotFound, "instance_unknown", "no such instance")
	for _, tc := range []struct {
		name  string
		argv  string
		err   error
		check func(error) bool
	}{
		{"no setup", "null", nil, func(err error) bool { return errors.Is(err, errNoSetup) }},
		{"an empty argv is no setup either", "[]", nil, func(err error) bool { return errors.Is(err, errNoSetup) }},
		{"an older daemon", "", apiwire.New(apiwire.CategoryNotFound, "method_unknown", "no such method"), func(err error) bool { return errors.Is(err, errDaemonOlder) }},
		{"another failure is the daemon's", "", notFound, func(err error) bool { return errors.Is(err, notFound) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.argv, r.infoError = tc.argv, tc.err
			if _, err := r.session(); !tc.check(err) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// execSetup is the real process side: standard input to the program, its standard
// output back, and for a failure the exit and the end of its standard error.
func TestExecSetup(t *testing.T) {
	if mode := os.Getenv("SIDERA_SETUP_HELPER"); mode != "" {
		switch mode {
		case "echo":
			var in bytes.Buffer
			in.ReadFrom(os.Stdin)
			fmt.Fprintf(os.Stderr, "log line\n")
			fmt.Fprintf(os.Stdout, "echo:%s", in.String())
		case "fail":
			fmt.Fprint(os.Stderr, strings.Repeat("x", 5000)+"the reason")
			os.Exit(3)
		}
		os.Exit(0)
	}
	run := func(mode string) ([]byte, error) {
		t.Setenv("SIDERA_SETUP_HELPER", mode)
		return execSetup{}.Run(context.Background(), []string{os.Args[0], "-test.run=^TestExecSetup$"}, []byte("request"))
	}
	if out, err := run("echo"); err != nil || string(out) != "echo:request" {
		t.Errorf("echo = %q, %v", out, err)
	}
	_, err := run("fail")
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.HasSuffix(err.Error(), "the reason") || len(err.Error()) > 2200 {
		t.Errorf("fail = %v", err)
	}
	if _, err := (execSetup{}).Run(context.Background(), []string{filepath.Join(t.TempDir(), "nothing")}, nil); err == nil {
		t.Error("a program that is not there ran")
	}
}

var (
	ownDependent = fakeadapter.SetupItem{ID: "b", Content: "B", Requires: []string{"a"}}
	ownPlain     = fakeadapter.SetupItem{ID: "c", Content: "C"}
)

// An item that requires another is not asked about and not applied when the other is
// declined, is applied in a later call when it is accepted, and is not sent at all
// when the other did not complete: the CLI says so each time. Without it, declining
// "uninstall the plugin" would still delete the plugin's source.
func TestAdapterSetupHonoursRequires(t *testing.T) {
	filesOf := func(r *setupRig) string {
		var present []string
		for id, path := range map[string]string{"a": filepath.Join(r.client, "a"), "b": filepath.Join(r.state, "setup", "files", "b"), "c": filepath.Join(r.state, "setup", "files", "c")} {
			if r.exists(path) {
				present = append(present, id)
			}
		}
		slices.Sort(present)
		return strings.Join(present, "")
	}
	for _, tc := range []struct {
		name    string
		fail    []string
		answers string
		files   string
		ops     string
		prompt  string // in the questions
		report  string // in the report
		verdict string // in the failure; empty for none
	}{
		{"required item declined", nil, "n\n", "c", "plan apply", "left alone: write b (it needs a, which is not going ahead)", "item c done", ""},
		{"required item accepted", nil, "y\n", "abc", "plan apply apply", "", "item b done", ""},
		{"required item failed", []string{"a"}, "y\n", "c", "plan apply", "", "item b skipped left alone: it needs a, which did not complete", "1 item(s) failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, clientA, ownDependent, ownPlain)
			r.script.Fail = tc.fail
			asked, report, err := r.run(modeSetup, tc.answers, false)
			if got := filesOf(r); got != tc.files {
				t.Errorf("files made = %q, want %q", got, tc.files)
			}
			if got := r.exec.ops(); got != tc.ops {
				t.Errorf("ops = %q, want %q", got, tc.ops)
			}
			for _, req := range r.exec.requests {
				if req.Op == "apply" && slices.Contains(req.Accepted, "b") && tc.files == "c" {
					t.Errorf("b was sent to the adapter: %v", req.Accepted)
				}
			}
			if !strings.Contains(asked, tc.prompt) || !strings.Contains(report, tc.report) {
				t.Errorf("questions:\n%s\nreport:\n%s", asked, report)
			}
			if tc.prompt != "" && strings.Contains(asked, "write b? [y/N]") {
				t.Errorf("asked about b after a was declined:\n%s", asked)
			}
			var f *failure
			if tc.verdict == "" && err != nil || tc.verdict != "" && (!errors.As(err, &f) || !strings.Contains(f.message, tc.verdict)) {
				t.Errorf("verdict %v, want %q", err, tc.verdict)
			}
		})
	}

	t.Run("when removing", func(t *testing.T) {
		r := newRig(t, clientA, ownDependent)
		if _, _, err := r.run(modeSetup, "", true); err != nil {
			t.Fatal(err)
		}
		r.exec.requests = nil
		r.script.Fail = []string{"a"} // the undo of a fails
		_, report, err := r.run(modeRemove, "", true)
		if err == nil || !strings.Contains(report, "item b skipped left alone") || !r.exists(filepath.Join(r.state, "setup", "files", "b")) {
			t.Errorf("err %v\n%s", err, report)
		}
	})
}

// An enumeration value that a later release adds is shown as it is: a kind of drift
// the CLI does not know is drift, an outcome status it does not know is a failure with
// the adapter's message, and neither makes the result unusable.
func TestAdapterSetupShowsWhatALaterReleaseAdds(t *testing.T) {
	t.Run("a problem", func(t *testing.T) {
		r := newRig(t)
		r.exec.byOp = map[adaptersetup.Op]string{"check": `{"version":1,"op":"check","status":"drift","recorded":true,"drift":[{"id":"a","kind":"k","target":"t","problem":"moved","summary":"a moved"}]}`}
		_, report, err := r.run(modeCheck, "", false)
		if err == nil || !strings.Contains(report, "drift a moved a moved") {
			t.Errorf("err %v\n%s", err, report)
		}
	})
	t.Run("an action", func(t *testing.T) {
		r := newRig(t)
		r.exec.byOp = map[adaptersetup.Op]string{"plan": `{"version":1,"op":"plan","status":"ok","items":[{"id":"a","kind":"k","target":"t","action":"teleport","summary":"teleport a","consent":true}]}`}
		asked, _, err := r.run(modeSetup, "n\n", false)
		if err != nil || !strings.Contains(asked, "teleport teleport a") {
			t.Errorf("err %v\n%s", err, asked)
		}
	})
	t.Run("an outcome status", func(t *testing.T) {
		r := newRig(t, clientA)
		r.exec.byOp = map[adaptersetup.Op]string{"apply": `{"version":1,"op":"apply","status":"ok","outcomes":[{"id":"a","kind":"k","target":"t","status":"pending","message":"queued"}]}`}
		_, report, err := r.run(modeSetup, "y\n", false)
		var f *failure
		if !errors.As(err, &f) || !strings.Contains(f.message, "1 item(s) failed") || !strings.Contains(report, `item a failed (its status "pending" is not known to this program) queued`) {
			t.Errorf("err %v\n%s", err, report)
		}
	})
	t.Run("a status of the result stays strict", func(t *testing.T) {
		r := newRig(t)
		r.exec.byOp = map[adaptersetup.Op]string{"check": `{"version":1,"op":"check","status":"fine","recorded":true}`}
		if _, _, err := r.run(modeCheck, "", false); err == nil || !strings.Contains(err.Error(), `"fine"`) {
			t.Errorf("err %v", err)
		}
	})
	t.Run("a check that does not say whether a record exists", func(t *testing.T) {
		r := newRig(t)
		r.exec.byOp = map[adaptersetup.Op]string{"check": `{"version":1,"op":"check","status":"ok"}`}
		if _, _, err := r.run(modeCheck, "", false); err == nil || !strings.Contains(err.Error(), "recorded") {
			t.Errorf("err %v", err)
		}
	})
}
