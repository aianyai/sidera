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
		// `sidera setup controller` printed commands for a person to run; every
		// form of it now points at the command that installs them.
		{"the removed setup", []string{"setup"}, "sidera adapter setup <instance>"},
		{"the removed setup controller", []string{"setup", "controller"}, "sidera adapter setup <instance>"},
		{"the removed setup controller with a flag", []string{"setup", "controller", "--yes"}, "sidera adapter setup <instance>"},
		{"the removed setup after a global flag", []string{"--json", "setup", "controller"}, "sidera adapter setup <instance>"},
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

// infoAnswer is the daemon's answer to adapters.setup_info.
func (r *setupRig) infoAnswer() func() (json.RawMessage, error) {
	if r.infoError != nil {
		return failed(r.infoError)
	}
	cfg, _ := json.Marshal(map[string]any{"setup": r.script})
	stateDir, _ := json.Marshal(r.state)
	return answered(fmt.Sprintf(`{"result":{"instance":"work","adapter":"fake","state_dir":%s,"config":%s,"setup_argv":%s}}`, stateDir, cfg, r.argv))
}

func (r *setupRig) session() (*setupSession, error) {
	r.t.Helper()
	node := &updateScript{answers: map[string][]func() (json.RawMessage, error){"adapters.setup_info": {r.infoAnswer()}}}
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
	res, applied, err := runSetup(context.Background(), s, mode, setupDialog{out: plain(&questions), answers: bufio.NewReader(strings.NewReader(answers)), yes: yes})
	if err != nil {
		return questions.String(), "", err
	}
	var out bytes.Buffer
	c := commandOn(&out)
	if err := c.printSetup(mode, "work", res, applied, setupEnv{homeIsDefault: true}); err != nil {
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
		{"yes and no", "y\nn\n", false, map[string]bool{"a": true, "b": false, "o": true}, []string{"write a? [Y/n]", "write b? [Y/n]"}, []string{"write o?"}, "plan apply"},
		{"the whole word, in capitals", "YES\nyes\n", false, map[string]bool{"a": true, "b": true, "o": true}, []string{"write a?"}, nil, "plan apply"},
		{"Enter accepts", "\n\n", false, map[string]bool{"a": true, "b": true, "o": true}, []string{"write a? [Y/n]", "write b? [Y/n]"}, nil, "plan apply"},
		{"Enter accepts, n declines", "\nn\n", false, map[string]bool{"a": true, "b": false, "o": true}, nil, nil, "plan apply"},
		{"spaces are an Enter", "  \n \n", false, map[string]bool{"a": true, "b": true, "o": true}, nil, nil, "plan apply"},
		{"the word no, in capitals", "NO\nNo\n", false, map[string]bool{"a": false, "b": false, "o": true}, nil, nil, "plan apply"},
		{"other text is a no", "sure\nmaybe\n", false, map[string]bool{"a": false, "b": false, "o": true}, nil, nil, "plan apply"},
		{"the input ends at once", "", false, map[string]bool{"a": false, "b": false, "o": true}, nil, nil, "plan apply"},
		{"the input ends after Enter", "\n", false, map[string]bool{"a": true, "b": false, "o": true}, nil, nil, "plan apply"},
		{"--yes does not ask", "", true, map[string]bool{"a": true, "b": true, "o": true}, nil, []string{"[Y/n]"}, "plan apply"},
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
				if got := hasRow(report, id+" done"); got != want {
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
	// Only to remember the decline: nothing is accepted, so nothing is done.
	if verdict != nil || r.exec.ops() != "plan apply" || !strings.Contains(report, "Nothing was applied") || r.exists(filepath.Join(r.client, "a")) {
		t.Errorf("verdict %v, ops %q, report %q", verdict, r.exec.ops(), report)
	}
	r = newRig(t) // nothing wanted at all
	_, report, verdict = r.run(modeSetup, "", true)
	if verdict != nil || r.exec.ops() != "plan" || !strings.Contains(report, "Nothing to do") {
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
	if !strings.Contains(asked, "delete a? [Y/n]") || strings.Contains(asked, "delete o?") {
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

// In --remove every question defaults to yes too: Enter undoes the item, and the end of
// the input undoes nothing. A no is not a decline there, so nothing is remembered.
func TestAdapterSetupRemoveDefaultsToYes(t *testing.T) {
	for _, tc := range []struct {
		name         string
		answers      string
		aGone, bGone bool
	}{
		{"Enter undoes", "\n\n", true, true},
		{"Enter undoes, n keeps", "\nn\n", true, false},
		{"the input ends", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, clientA, clientB, ownO)
			if _, _, err := r.run(modeSetup, "", true); err != nil {
				t.Fatal(err)
			}
			if _, _, err := r.run(modeRemove, tc.answers, false); err != nil {
				t.Fatal(err)
			}
			if got := !r.exists(filepath.Join(r.client, "a")); got != tc.aGone {
				t.Errorf("a removed = %v, want %v", got, tc.aGone)
			}
			if got := !r.exists(filepath.Join(r.client, "b")); got != tc.bGone {
				t.Errorf("b removed = %v, want %v", got, tc.bGone)
			}
			if rec, _ := adaptersetup.ReadRecord(r.state); rec != nil && len(rec.Declined) != 0 {
				t.Errorf("a no in --remove was remembered as a decline: %+v", rec.Declined)
			}
		})
	}
}

// check is exit 0 without drift and 1 with drift, which includes what setup would still
// have to make; refresh asks nothing, rewrites what is out of date and never makes again
// what was removed.
func TestAdapterSetupCheckAndRefresh(t *testing.T) {
	// An adapter with nothing wanted has no record and no drift. One that wants
	// something and has no record says what setup would have to make, and that is drift.
	if _, report, err := newRig(t).run(modeCheck, "", false); err != nil || !strings.Contains(report, "has no recorded setup") || !strings.Contains(squash(report), "Recorded no") {
		t.Errorf("check with nothing wanted: %v\n%s", err, report)
	}
	r := newRig(t, clientA, ownO)
	if _, report, err := r.run(modeCheck, "", false); err == nil || !strings.Contains(report, "has drifted from its setup") || !strings.Contains(squash(report), "Recorded no") ||
		!hasRow(report, "a needs_setup") || !hasRow(report, "o needs_setup") {
		t.Errorf("check before setup: %v\n%s", err, report)
	}
	if _, report, err := r.run(modeRefresh, "", false); err != nil || !strings.Contains(report, "has no recorded setup") {
		t.Errorf("refresh before setup: %v\n%s", err, report)
	}
	if _, _, err := r.run(modeSetup, "", true); err != nil {
		t.Fatal(err)
	}
	if _, report, err := r.run(modeCheck, "", false); err != nil || !strings.Contains(report, "is as recorded") || !strings.Contains(squash(report), "Recorded yes") {
		t.Errorf("clean check: %v\n%s", err, report)
	}

	// The adapter now writes other content: stale, and refresh brings it up to date
	// without a question.
	r.script.Items = []fakeadapter.SetupItem{{ID: "a", Consent: true, Content: "A2"}, ownO}
	if _, report, err := r.run(modeCheck, "", false); err == nil || !hasRow(report, "a stale") {
		t.Errorf("stale check: %v\n%s", err, report)
	}
	asked, report, err := r.run(modeRefresh, "", false)
	if err != nil || asked != "" || !strings.Contains(report, "Refreshed the setup") || !hasRow(report, "a done") {
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
		if _, report, err := r.run(mode, "", false); err == nil || !hasRow(report, "a changed") {
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
	if _, report, err := r.run(modeRefresh, "", false); err == nil || !hasRow(report, "a missing") {
		t.Errorf("refresh of a removed file: %v\n%s", err, report)
	}
	if r.exists(filepath.Join(r.client, "a")) {
		t.Error("refresh made again a file the user removed")
	}

	// An item the adapter now wants and no record has needs setup, never made by refresh.
	r.script.Items = append(r.script.Items, clientB)
	if _, report, err := r.run(modeRefresh, "", false); err == nil || !hasRow(report, "b needs_setup") || r.exists(filepath.Join(r.client, "b")) {
		t.Errorf("refresh with a new item: %v\n%s", err, report)
	}
}

func TestAdapterSetupFailuresAreExitOne(t *testing.T) {
	t.Run("an item fails", func(t *testing.T) {
		r := newRig(t, clientA, clientB)
		r.script.Fail = []string{"b"}
		_, report, err := r.run(modeSetup, "", true)
		var f *failure
		if !errors.As(err, &f) || !strings.Contains(f.message, "1 item(s) failed") || !hasRow(report, "a done") || !hasRow(report, "b failed scripted to fail") {
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
		if _, report, err := r.run(modeSetup, "", true); err != nil || !strings.Contains(report, "Nothing to do") {
			t.Errorf("err %v\n%s", err, report)
		}
	})
}

// Notes are advice that is neither an item nor drift. A plan shows them before it asks
// (also when it has nothing to ask), a check after its drift lines, and they change
// neither the status nor the exit: a check with only notes is still clean.
func TestAdapterSetupShowsNotes(t *testing.T) {
	const note = "enable the kanban toolset in config.yaml"
	t.Run("a plan shows them before the questions", func(t *testing.T) {
		r := newRig(t, clientA)
		r.script.Notes = []string{note}
		asked, report, err := r.run(modeSetup, "y\n", false)
		if err != nil || strings.Index(asked, "Note: "+note) < 0 || strings.Index(asked, "Note: "+note) > strings.Index(asked, "write a? [Y/n]") {
			t.Errorf("err %v, questions:\n%s", err, asked)
		}
		if strings.Contains(report, note) {
			t.Errorf("the report on standard output carries the plan's note:\n%s", report)
		}
	})
	t.Run("a plan with no items still shows them", func(t *testing.T) {
		r := newRig(t)
		r.script.Notes = []string{note}
		asked, report, err := r.run(modeSetup, "", true)
		if err != nil || !strings.Contains(asked, "Note: "+note) || !strings.Contains(report, "Nothing to do") {
			t.Errorf("err %v, questions:\n%s\nreport:\n%s", err, asked, report)
		}
	})
	t.Run("a check shows them after the drift and is clean without drift", func(t *testing.T) {
		r := newRig(t, clientA)
		r.script.Notes = []string{note}
		_, report, err := r.run(modeCheck, "", false)
		var f *failure
		if !errors.As(err, &f) || strings.Index(report, "Note: "+note) < strings.Index(report, "a has no record") {
			t.Errorf("err %v\n%s", err, report)
		}
		if _, _, err := r.run(modeSetup, "", true); err != nil {
			t.Fatal(err)
		}
		_, report, err = r.run(modeCheck, "", false)
		if err != nil || !strings.Contains(report, "is as recorded") || !strings.Contains(report, "Note: "+note) {
			t.Errorf("err %v\n%s", err, report)
		}
	})
	t.Run("--json carries them", func(t *testing.T) {
		for _, mode := range []setupMode{modeCheck, modeSetup} {
			r := newRig(t, clientA)
			r.script.Notes = []string{note}
			s, err := r.session()
			if err != nil {
				t.Fatal(err)
			}
			res, applied, err := runSetup(context.Background(), s, mode, setupDialog{out: plain(&bytes.Buffer{}), yes: true})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			c := commandOn(&out)
			c.json = true
			if err := c.printSetup(mode, "work", res, applied, setupEnv{}); err != nil {
				t.Fatal(err)
			}
			var got struct{ Notes []string }
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || !slices.Equal(got.Notes, []string{note}) {
				t.Errorf("mode %d: notes %v, err %v in %s", mode, got.Notes, err, out.String())
			}
		}
	})
}

func TestAdapterSetupJSON(t *testing.T) {
	r := newRig(t, clientA)
	s, err := r.session()
	if err != nil {
		t.Fatal(err)
	}
	res, applied, err := runSetup(context.Background(), s, modeSetup, setupDialog{out: plain(&bytes.Buffer{}), yes: true})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := commandOn(&out)
	c.json = true
	if err := c.printSetup(modeSetup, "work", res, applied, setupEnv{}); err != nil {
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
		case "escape":
			fmt.Fprint(os.Stderr, "\x1b]0;pwned\x07\u202ereason")
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
	// What a setup program or the client it drives wrote is shown as visible escapes.
	_, err = run("escape")
	if err == nil || !strings.HasSuffix(err.Error(), `\x1b]0;pwned\x07\u202ereason`) {
		t.Errorf("escape = %v", err)
	}
	if _, err := (execSetup{}).Run(context.Background(), []string{filepath.Join(t.TempDir(), "nothing")}, nil); err == nil {
		t.Error("a program that is not there ran")
	}
}

// An instance whose adapter has recorded a setup is not removed: its state directory
// would go with it, and with that the source of what setup installed in the client.
func TestRefuseRemoveWhileSetUp(t *testing.T) {
	env := setupEnv{home: "/home/sidera", program: "/opt/sidera"}
	setUp := func(r *setupRig) {
		if _, _, err := r.run(modeSetup, "", true); err != nil {
			t.Fatal(err)
		}
		r.exec.requests = nil
	}
	for _, tc := range []struct {
		name    string
		prepare func(*setupRig)
		want    string // in the refusal; empty for a removal that goes on
		checked bool   // whether the program was asked
		force   bool   // whether the refusal offers --force: only when the check itself failed
	}{
		{"a record exists", setUp, "adapter setup work --remove", true, false},
		{"a record exists although what it recorded is gone", func(r *setupRig) { setUp(r); os.Remove(filepath.Join(r.client, "a")) }, "adapter setup work --remove", true, false},
		{"no record", func(*setupRig) {}, "", true, false},
		{"setup was undone", func(r *setupRig) {
			setUp(r)
			if _, _, err := r.run(modeRemove, "", true); err != nil {
				t.Fatal(err)
			}
			r.exec.requests = nil
		}, "", true, false},
		{"the adapter has no setup", func(r *setupRig) { r.argv = "null" }, "", false, false},
		{"an older daemon", func(r *setupRig) {
			r.infoError = apiwire.New(apiwire.CategoryNotFound, "method_unknown", "no such method")
		}, "", false, false},
		{"an instance the daemon does not know", func(r *setupRig) {
			r.infoError = apiwire.New(apiwire.CategoryNotFound, "instance_unknown", "no such instance")
		}, "", false, false},
		{"an adapter that is not installed", func(r *setupRig) {
			r.infoError = apiwire.New(apiwire.CategoryNotFound, "adapter_unknown", "no such adapter")
		}, "", false, false},
		{"a package that cannot be loaded", func(r *setupRig) {
			r.infoError = apiwire.New(apiwire.CategoryRefused, "package_invalid", "the package cannot be loaded")
		}, "package_invalid", false, true},
		{"the setup program answers nonsense", func(r *setupRig) {
			setUp(r)
			os.RemoveAll(r.state)
			r.script.Items = []fakeadapter.SetupItem{{ID: "bad", Content: "x"}}
			r.exec.raw = "garbage"
		}, "cannot be told whether its setup has recorded anything", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, clientA, ownO)
			tc.prepare(r)
			node := &updateScript{answers: map[string][]func() (json.RawMessage, error){}}
			node.answers["adapters.setup_info"] = []func() (json.RawMessage, error){r.infoAnswer()}
			err := refuseRemoveWhileSetUp(context.Background(), node, r.exec, "work", env)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			// Every refusal is a failure (exit 1) that says the instance was not removed. Only
			// one because the check itself failed offers --force, so that an instance whose
			// setup cannot run is not stuck; with a record, --force would leave the client
			// pointing at the directory the removal deletes.
			if tc.want != "" {
				var f *failure
				if !errors.As(err, &f) || !strings.Contains(err.Error(), "the instance was not removed") {
					t.Errorf("err = %v; want a failure that says the instance was not removed", err)
				}
				if offers := strings.Contains(err.Error(), "--force"); offers != tc.force {
					t.Errorf("err = %v; offers --force = %v, want %v", err, offers, tc.force)
				}
			}
			if asked := len(r.exec.requests) > 0; asked != tc.checked {
				t.Errorf("the program was asked = %v, want %v (%s)", asked, tc.checked, r.exec.ops())
			}
			if len(r.exec.requests) > 0 && r.exec.ops() != "check" {
				t.Errorf("ops = %q; the guard only checks", r.exec.ops())
			}
		})
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
		{"required item declined", nil, "n\n", "c", "plan apply", "Left alone: write b (it needs a, which is not going ahead)", "c done", ""},
		{"required item accepted", nil, "y\n", "abc", "plan apply apply", "", "b done", ""},
		{"required item failed", []string{"a"}, "y\n", "c", "plan apply", "", "b skipped left alone: it needs a, which did not complete", "1 item(s) failed"},
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
			if !strings.Contains(asked, tc.prompt) || !hasRow(report, tc.report) {
				t.Errorf("questions:\n%s\nreport:\n%s", asked, report)
			}
			if tc.prompt != "" && strings.Contains(asked, "write b? [Y/n]") {
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
		if err == nil || !hasRow(report, "b skipped left alone:") || !r.exists(filepath.Join(r.state, "setup", "files", "b")) {
			t.Errorf("err %v\n%s", err, report)
		}
	})
}

// An item that needs no consent exists for the items that require it (the source of a
// plugin is read by the plugin's install). When the person declines every one of them it
// is not applied: it would be recorded, the instance would then count as set up in the
// client, and `adapter remove` would refuse for something that serves nothing. It is
// left alone and counts as declined, so that check and refresh do not call it drift.
func TestAdapterSetupLeavesAloneWhatNothingDependsOn(t *testing.T) {
	source := fakeadapter.SetupItem{ID: "s", Content: "S"}
	middle := fakeadapter.SetupItem{ID: "t", Content: "T", Requires: []string{"s"}}
	plugin := fakeadapter.SetupItem{ID: "d", Consent: true, Content: "D", Requires: []string{"t"}}
	other := fakeadapter.SetupItem{ID: "e", Consent: true, Content: "E", Requires: []string{"s"}}
	files := func(r *setupRig) string {
		var present []string
		for id, path := range map[string]string{"s": filepath.Join(r.state, "setup", "files", "s"), "t": filepath.Join(r.state, "setup", "files", "t"),
			"d": filepath.Join(r.client, "d"), "e": filepath.Join(r.client, "e")} {
			if r.exists(path) {
				present = append(present, id)
			}
		}
		slices.Sort(present)
		return strings.Join(present, "")
	}

	sameSet := func(got []string, want ...string) bool {
		return len(got) == len(want) && slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want)))
	}

	t.Run("every dependent declined", func(t *testing.T) {
		r := newRig(t, source, middle, plugin, other)
		asked, _, err := r.run(modeSetup, "n\nn\n", false)
		if err != nil {
			t.Fatal(err)
		}
		if got := files(r); got != "" {
			t.Errorf("files made = %q, want none", got)
		}
		for _, want := range []string{"Left alone: write s", "Left alone: write t", "Declined: write s"} {
			if !strings.Contains(asked, want) {
				t.Errorf("the dialogue does not say %q:\n%s", want, asked)
			}
		}
		for _, req := range r.exec.requests {
			if req.Op == adaptersetup.OpApply && len(req.Accepted) != 0 {
				t.Errorf("apply sent accepted = %v, want none", req.Accepted)
			}
		}
		if rec, err := adaptersetup.ReadRecord(r.state); err != nil || rec == nil || rec.Installed() || !sameSet(rec.Declined, "s", "t", "d", "e") {
			t.Errorf("record = %+v (%v), want nothing installed and s, t, d, e declined", rec, err)
		}
		for _, mode := range []setupMode{modeCheck, modeRefresh} {
			if _, report, err := r.run(mode, "", false); err != nil || strings.Contains(report, "drift") {
				t.Errorf("mode %d reports drift: %v\n%s", mode, err, report)
			}
		}
		env := setupEnv{home: "/home/sidera", program: "/opt/sidera"}
		node := &updateScript{answers: map[string][]func() (json.RawMessage, error){"adapters.setup_info": {r.infoAnswer()}}}
		if err := refuseRemoveWhileSetUp(context.Background(), node, r.exec, "work", env); err != nil {
			t.Errorf("adapter remove is refused although nothing is installed: %v", err)
		}
	})

	t.Run("one dependent accepted is enough", func(t *testing.T) {
		r := newRig(t, source, middle, plugin, other)
		if _, _, err := r.run(modeSetup, "n\ny\n", false); err != nil {
			t.Fatal(err)
		}
		if got := files(r); got != "es" {
			t.Errorf("files made = %q, want es (t serves only the declined d)", got)
		}
	})

	t.Run("--yes applies everything", func(t *testing.T) {
		r := newRig(t, source, middle, plugin, other)
		if _, _, err := r.run(modeSetup, "", true); err != nil {
			t.Fatal(err)
		}
		if got := files(r); got != "dest" {
			t.Errorf("files made = %q, want dest", got)
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
		if err == nil || !hasRow(report, "a moved a moved") {
			t.Errorf("err %v\n%s", err, report)
		}
	})
	t.Run("an action", func(t *testing.T) {
		r := newRig(t)
		r.exec.byOp = map[adaptersetup.Op]string{"plan": `{"version":1,"op":"plan","status":"ok","items":[{"id":"a","kind":"k","target":"t","action":"teleport","summary":"teleport a","consent":true}]}`}
		asked, _, err := r.run(modeSetup, "n\n", false)
		if err != nil || !strings.Contains(asked, "teleport a? [Y/n]") {
			t.Errorf("err %v\n%s", err, asked)
		}
	})
	t.Run("an outcome status", func(t *testing.T) {
		r := newRig(t, clientA)
		r.exec.byOp = map[adaptersetup.Op]string{"apply": `{"version":1,"op":"apply","status":"ok","outcomes":[{"id":"a","kind":"k","target":"t","status":"pending","message":"queued"}]}`}
		_, report, err := r.run(modeSetup, "y\n", false)
		var f *failure
		if !errors.As(err, &f) || !strings.Contains(f.message, "1 item(s) failed") || !hasRow(report, `a failed (its status "pending" is not known to this program) queued`) {
			t.Errorf("err %v\n%s", err, report)
		}
	})
	t.Run("an error that names no failed item", func(t *testing.T) {
		r := newRig(t, clientA)
		r.exec.byOp = map[adaptersetup.Op]string{"refresh": `{"version":1,"op":"refresh","status":"error","recorded":true}`}
		_, _, err := r.run(modeRefresh, "", false)
		var f *failure
		if !errors.As(err, &f) || strings.Contains(f.message, "0 item(s)") || !strings.Contains(f.message, "without a failed item") {
			t.Errorf("err %v, want the error said without a count of zero items", err)
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

// What the person declines is remembered, so that update (refresh) and check stop calling
// it drift, while the plan of a setup run by hand still offers it. Only an answer of no
// is a decline: --yes asks nothing, and an item left alone because the item it requires
// was declined was never asked.
func TestAdapterSetupRemembersWhatWasDeclined(t *testing.T) {
	clientDependent := fakeadapter.SetupItem{ID: "d", Consent: true, Content: "D", Requires: []string{"a"}}
	lastApply := func(r *setupRig) adaptersetup.Request {
		for i := len(r.exec.requests) - 1; i >= 0; i-- {
			if r.exec.requests[i].Op == adaptersetup.OpApply {
				return r.exec.requests[i]
			}
		}
		t.Fatal("no apply was sent")
		return adaptersetup.Request{}
	}
	record := func(r *setupRig) *adaptersetup.Record {
		rec, err := adaptersetup.ReadRecord(r.state)
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}

	t.Run("declined, quiet afterwards, offered again, accepted", func(t *testing.T) {
		r := newRig(t, clientA, clientB, ownO)
		asked, _, err := r.run(modeSetup, "n\ny\n", false)
		if err != nil {
			t.Fatal(err)
		}
		if got := lastApply(r).Declined; fmt.Sprint(got) != "[a]" {
			t.Errorf("declined sent = %v, want [a]", got)
		}
		if !strings.Contains(asked, "Declined: write a") || !strings.Contains(asked, "do not report it again") {
			t.Errorf("the dialogue does not say what was declined:\n%s", asked)
		}
		for _, mode := range []setupMode{modeCheck, modeRefresh} {
			if _, report, err := r.run(mode, "", false); err != nil || strings.Contains(report, "drift") {
				t.Errorf("mode %d reports the declined item: %v\n%s", mode, err, report)
			}
		}
		// A new item that nobody declined is still reported, the declined one is not.
		r.script.Items = append(r.script.Items, fakeadapter.SetupItem{ID: "n", Consent: true, Content: "N"})
		if _, report, err := r.run(modeRefresh, "", false); err == nil || !hasRow(report, "n needs_setup") || hasRow(report, "a needs_setup") {
			t.Errorf("refresh with a new item: %v\n%s", err, report)
		}
		// The plan of a setup run by hand lists it again; accepting it records it and
		// it is no longer declined.
		asked, _, err = r.run(modeSetup, "y\nn\n", false)
		if err != nil || !strings.Contains(asked, "write a? [Y/n]") {
			t.Fatalf("the plan did not offer the declined item again: %v\n%s", err, asked)
		}
		if rec := record(r); rec == nil || !rec.Installed() || slices.Contains(rec.Declined, "a") || !slices.Contains(rec.Declined, "n") {
			t.Errorf("record = %+v, want a recorded and n declined", rec)
		}
	})

	t.Run("declining everything is remembered with no item recorded", func(t *testing.T) {
		r := newRig(t, clientA)
		if _, _, err := r.run(modeSetup, "n\n", false); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []setupMode{modeCheck, modeRefresh} {
			if _, report, err := r.run(mode, "", false); err != nil || !strings.Contains(report, "has no recorded setup") || !strings.Contains(squash(report), "Recorded no") {
				t.Errorf("mode %d: %v\n%s", mode, err, report)
			}
		}
	})

	t.Run("the end of the input is a decline", func(t *testing.T) {
		r := newRig(t, clientA, clientB, ownO)
		if _, _, err := r.run(modeSetup, "\n", false); err != nil {
			t.Fatal(err)
		}
		if got := lastApply(r).Declined; fmt.Sprint(got) != "[b]" {
			t.Errorf("declined sent = %v, want [b]", got)
		}
		if rec := record(r); rec == nil || !slices.Contains(rec.Declined, "b") || slices.Contains(rec.Declined, "a") {
			t.Errorf("record = %+v, want b declined and a recorded", rec)
		}
	})

	t.Run("--yes declines nothing", func(t *testing.T) {
		r := newRig(t, clientA, ownO)
		if _, _, err := r.run(modeSetup, "", true); err != nil {
			t.Fatal(err)
		}
		if got := lastApply(r).Declined; len(got) != 0 {
			t.Errorf("declined sent = %v", got)
		}
		if rec := record(r); rec == nil || len(rec.Declined) != 0 {
			t.Errorf("record = %+v", rec)
		}
	})

	t.Run("an item left alone because its required item was declined is declined too", func(t *testing.T) {
		r := newRig(t, clientA, clientDependent)
		asked, _, err := r.run(modeSetup, "n\n", false)
		if err != nil || !strings.Contains(asked, "Left alone: write d") || !strings.Contains(asked, "Declined: write d") {
			t.Fatalf("%v\n%s", err, asked)
		}
		if got := lastApply(r).Declined; fmt.Sprint(got) != "[a d]" {
			t.Errorf("declined sent = %v, want a and d", got)
		}
		if _, report, err := r.run(modeCheck, "", false); err != nil || strings.Contains(report, "drift") {
			t.Errorf("check: %v\n%s", err, report)
		}
	})

	t.Run("a no in a removal is not a decline", func(t *testing.T) {
		r := newRig(t, clientA, clientB)
		if _, _, err := r.run(modeSetup, "", true); err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.run(modeRemove, "n\ny\n", false); err != nil {
			t.Fatal(err)
		}
		if got := lastApply(r).Declined; len(got) != 0 {
			t.Errorf("declined sent = %v", got)
		}
	})
}

// A removal that ends with an item failed or left alone may be one whose client is
// gone for good; the report then says how to remove the instance anyway and where the
// manual cleanup is described. A removal that did everything says nothing.
func TestAdapterSetupRemoveUnfinishedSaysHowToLeave(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fail     []string
		edit     bool // change the file after setup, so the undo leaves it alone
		wantHint bool
	}{
		{"an item failed", []string{"a"}, false, true},
		{"an item was left alone", nil, true, true},
		{"everything was removed", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, clientA)
			if _, _, err := r.run(modeSetup, "", true); err != nil {
				t.Fatal(err)
			}
			r.script.Fail = tc.fail
			if tc.edit {
				if err := os.WriteFile(filepath.Join(r.client, "a"), []byte("edited"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			s, err := r.session()
			if err != nil {
				t.Fatal(err)
			}
			res, _, err := runSetup(context.Background(), s, modeRemove, setupDialog{out: plain(&bytes.Buffer{}), yes: true})
			if err != nil {
				t.Fatal(err)
			}
			hint := unfinishedRemovalHint(plain(&bytes.Buffer{}), "work", res)
			if !tc.wantHint {
				if hint != "" {
					t.Errorf("hint %q for a removal that finished", hint)
				}
				return
			}
			for _, want := range []string{"sidera adapter remove work --force", "Cleaning up an adapter's client by hand"} {
				if !strings.Contains(hint, want) {
					t.Errorf("hint %q does not say %q", hint, want)
				}
			}
		})
	}
}

// hasRow says whether the report has a line of a table that starts with these words, in this
// order, whatever the padding between them.
func hasRow(report, words string) bool {
	want := strings.Fields(words)
	for _, line := range strings.Split(report, "\n") {
		if got := strings.Fields(line); len(got) >= len(want) && slices.Equal(got[:len(want)], want) {
			return true
		}
	}
	return false
}
