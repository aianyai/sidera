package adaptersetup_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// script is a handler whose answers the test sets, and that remembers the request
// it was called with.
type script struct {
	items    []adaptersetup.Item
	outcomes []adaptersetup.Outcome
	check    adaptersetup.Check
	refresh  adaptersetup.Refresh
	err      error

	called string
	got    adaptersetup.Request
}

func (s *script) note(name string, req adaptersetup.Request) { s.called, s.got = name, req }
func (s *script) Plan(_ context.Context, r adaptersetup.Request) ([]adaptersetup.Item, error) {
	s.note("Plan", r)
	return s.items, s.err
}
func (s *script) Remove(_ context.Context, r adaptersetup.Request) ([]adaptersetup.Item, error) {
	s.note("Remove", r)
	return s.items, s.err
}
func (s *script) Apply(_ context.Context, r adaptersetup.Request) ([]adaptersetup.Outcome, error) {
	s.note("Apply", r)
	return s.outcomes, s.err
}
func (s *script) Undo(_ context.Context, r adaptersetup.Request) ([]adaptersetup.Outcome, error) {
	s.note("Undo", r)
	return s.outcomes, s.err
}
func (s *script) Refresh(_ context.Context, r adaptersetup.Request) (adaptersetup.Refresh, error) {
	s.note("Refresh", r)
	return s.refresh, s.err
}
func (s *script) Check(_ context.Context, r adaptersetup.Request) (adaptersetup.Check, error) {
	s.note("Check", r)
	return s.check, s.err
}

// runSetup runs the command with stdin as the request. A request that names no
// state_dir gets a state directory that exists: most tests are not about it.
func runSetup(t *testing.T, h adaptersetup.Handler, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	if strings.HasPrefix(stdin, "{") && !strings.Contains(stdin, "state_dir") {
		quoted, _ := json.Marshal(t.TempDir())
		stdin = `{"state_dir":` + string(quoted) + `,` + stdin[1:]
	}
	var out, errs bytes.Buffer
	code = adaptersetup.Command("prog", h)(args, adapterkit.Streams{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errs})
	return code, out.String(), errs.String()
}

func item(id string) adaptersetup.Item {
	return adaptersetup.Item{ID: id, Kind: "file", Target: "/t/" + id, Action: adaptersetup.ActionCreate, Summary: "make " + id, Consent: true}
}
func outcome(id string, s adaptersetup.OutcomeStatus) adaptersetup.Outcome {
	return adaptersetup.Outcome{ID: id, Kind: "file", Target: "/t/" + id, Status: s}
}
func drift(id string) adaptersetup.Drift {
	return adaptersetup.Drift{ID: id, Kind: "file", Target: "/t/" + id, Problem: adaptersetup.ProblemMissing, Summary: id + " is gone"}
}

// Every op reaches its method and the status of the result follows from the lists the
// method returned, so an adapter cannot report one that contradicts them.
func TestCommandRunsTheOpAndDerivesTheStatus(t *testing.T) {
	for _, tc := range []struct {
		name       string
		request    string
		h          *script
		method     string
		wantStatus adaptersetup.Status
		recorded   *bool
	}{
		{"plan", `{"version":1,"op":"plan"}`, &script{items: []adaptersetup.Item{item("a")}}, "Plan", adaptersetup.StatusOK, nil},
		{"remove", `{"version":1,"op":"remove"}`, &script{items: []adaptersetup.Item{item("a")}}, "Remove", adaptersetup.StatusOK, nil},
		{"apply done", `{"version":1,"op":"apply","accepted":["a"]}`, &script{outcomes: []adaptersetup.Outcome{outcome("a", adaptersetup.OutcomeDone)}}, "Apply", adaptersetup.StatusOK, nil},
		{"apply skipped is not a failure", `{"version":1,"op":"apply"}`, &script{outcomes: []adaptersetup.Outcome{outcome("a", adaptersetup.OutcomeSkipped)}}, "Apply", adaptersetup.StatusOK, nil},
		{"apply failed", `{"version":1,"op":"apply"}`, &script{outcomes: []adaptersetup.Outcome{outcome("a", adaptersetup.OutcomeDone), outcome("b", adaptersetup.OutcomeFailed)}}, "Apply", adaptersetup.StatusError, nil},
		{"undo goes to Undo", `{"version":1,"op":"apply","undo":true}`, &script{outcomes: []adaptersetup.Outcome{outcome("a", adaptersetup.OutcomeDone)}}, "Undo", adaptersetup.StatusOK, nil},
		{"check clean", `{"version":1,"op":"check"}`, &script{check: adaptersetup.Check{Recorded: true}}, "Check", adaptersetup.StatusOK, ptr(true)},
		{"check drift", `{"version":1,"op":"check"}`, &script{check: adaptersetup.Check{Recorded: true, Drift: []adaptersetup.Drift{drift("a")}}}, "Check", adaptersetup.StatusDrift, ptr(true)},
		{"check no record", `{"version":1,"op":"check"}`, &script{}, "Check", adaptersetup.StatusNone, ptr(false)},
		{"check no record but needs setup", `{"version":1,"op":"check"}`, &script{check: adaptersetup.Check{Drift: []adaptersetup.Drift{drift("a")}}}, "Check", adaptersetup.StatusDrift, ptr(false)},
		{"refresh current", `{"version":1,"op":"refresh"}`, &script{refresh: adaptersetup.Refresh{Recorded: true}}, "Refresh", adaptersetup.StatusRefreshed, ptr(true)},
		{"refresh rewrote", `{"version":1,"op":"refresh"}`, &script{refresh: adaptersetup.Refresh{Recorded: true, Outcomes: []adaptersetup.Outcome{outcome("a", adaptersetup.OutcomeDone)}}}, "Refresh", adaptersetup.StatusRefreshed, ptr(true)},
		{"refresh drift", `{"version":1,"op":"refresh"}`, &script{refresh: adaptersetup.Refresh{Recorded: true, Drift: []adaptersetup.Drift{drift("a")}}}, "Refresh", adaptersetup.StatusDrift, ptr(true)},
		{"refresh failed", `{"version":1,"op":"refresh"}`, &script{refresh: adaptersetup.Refresh{Recorded: true, Outcomes: []adaptersetup.Outcome{outcome("a", adaptersetup.OutcomeFailed)}, Drift: []adaptersetup.Drift{drift("b")}}}, "Refresh", adaptersetup.StatusError, ptr(true)},
		{"refresh no record", `{"version":1,"op":"refresh"}`, &script{}, "Refresh", adaptersetup.StatusNone, ptr(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := tc.request
			if tc.recorded != nil && *tc.recorded {
				// The instance has a record, which a refresh is held to.
				state := t.TempDir()
				rec := adaptersetup.Record{}
				rec.Set(adaptersetup.RecordItem{ID: "a"})
				rec.Set(adaptersetup.RecordItem{ID: "b"})
				if err := adaptersetup.WriteRecord(state, rec); err != nil {
					t.Fatal(err)
				}
				quoted, _ := json.Marshal(state)
				request = `{"state_dir":` + string(quoted) + `,` + request[1:]
			}
			code, out, errs := runSetup(t, tc.h, request)
			if code != adapterkit.ExitOK || errs != "" {
				t.Fatalf("exit %d, stderr %q", code, errs)
			}
			var res adaptersetup.Result
			if err := json.Unmarshal([]byte(out), &res); err != nil {
				t.Fatalf("stdout %q: %v", out, err)
			}
			if tc.h.called != tc.method || res.Status != tc.wantStatus || res.Version != 1 {
				t.Errorf("called %s, result %+v; want %s with status %s", tc.h.called, res, tc.method, tc.wantStatus)
			}
			if (res.Recorded == nil) != (tc.recorded == nil) || (res.Recorded != nil && *res.Recorded != *tc.recorded) {
				t.Errorf("recorded = %v, want %v", res.Recorded, tc.recorded)
			}
		})
	}
}

func ptr(b bool) *bool { return &b }

// The request reaches the handler whole, and a member the program does not know is
// ignored: a newer core program may send more.
func TestCommandPassesTheWholeRequestAndIgnoresUnknownMembers(t *testing.T) {
	h := &script{}
	code, _, errs := runSetup(t, h, `{"version":1,"op":"plan","home":"/h","home_is_default":true,"instance":"work",
		"config":{"k":"v","n":[1]},"sidera_program":"/bin/sidera","options":["--profile","p"],"accepted":["x"],"future":{"a":1}}`)
	if code != adapterkit.ExitOK {
		t.Fatalf("exit %d: %s", code, errs)
	}
	g := h.got
	if g.Home != "/h" || !g.HomeIsDefault || g.Instance != "work" || g.StateDir == "" || g.SideraProgram != "/bin/sidera" ||
		strings.Join(g.Options, " ") != "--profile p" || strings.Join(g.Accepted, " ") != "x" || string(g.Config["n"]) != "[1]" {
		t.Errorf("request = %+v", g)
	}
}

// A request the program cannot take is a usage error and the handler is not called;
// a handler that fails is an ordinary failure; an invalid result is the adapter's
// bug, also an ordinary failure, and nothing is written.
func TestCommandMapsErrorsToExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		h       *script
		stdin   string
		args    []string
		code    int
		message string
	}{
		{"not JSON", &script{}, `plan`, nil, adapterkit.ExitUsage, "not a JSON object"},
		{"empty", &script{}, ``, nil, adapterkit.ExitUsage, "not a JSON object"},
		{"a newer version", &script{}, `{"version":2,"op":"plan"}`, nil, adapterkit.ExitUsage, "version 2"},
		{"no version", &script{}, `{"op":"plan"}`, nil, adapterkit.ExitUsage, "version 0"},
		{"unknown op", &script{}, `{"version":1,"op":"explode"}`, nil, adapterkit.ExitUsage, `"explode"`},
		{"an argument", &script{}, `{"version":1,"op":"plan"}`, []string{"plan"}, adapterkit.ExitUsage, "takes no argument"},
		{"the handler fails", &script{err: errors.New("no client")}, `{"version":1,"op":"plan"}`, nil, adapterkit.ExitFailed, "no client"},
		{"duplicate ids", &script{items: []adaptersetup.Item{item("a"), item("a")}}, `{"version":1,"op":"plan"}`, nil, adapterkit.ExitFailed, "used twice"},
		{"no action", &script{items: []adaptersetup.Item{{ID: "a"}}}, `{"version":1,"op":"plan"}`, nil, adapterkit.ExitFailed, "no action"},
		{"a removal in a plan without consent", &script{items: []adaptersetup.Item{{ID: "a", Action: adaptersetup.ActionRemove}}}, `{"version":1,"op":"plan"}`, nil, adapterkit.ExitFailed, "consent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errs := runSetup(t, tc.h, tc.stdin, tc.args...)
			if code != tc.code || out != "" || !strings.Contains(errs, tc.message) {
				t.Errorf("exit %d, stdout %q, stderr %q; want exit %d and %q", code, out, errs, tc.code, tc.message)
			}
			if tc.code == adapterkit.ExitUsage && tc.h.called != "" {
				t.Errorf("the handler was called (%s) for a request that cannot be taken", tc.h.called)
			}
		})
	}
}

func TestCommandHelpIsDone(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		code, out, _ := runSetup(t, &script{}, "", flag)
		if code != adapterkit.ExitOK || !strings.Contains(out, "usage: prog setup") {
			t.Errorf("%s: exit %d, stdout %q", flag, code, out)
		}
	}
}

// The core program reads a result it did not write, so the checks it relies on are
// pinned: the version and op it asked for, the enumerations, unique ids.
func TestResultValidate(t *testing.T) {
	ok := adaptersetup.Result{Version: 1, Op: adaptersetup.OpPlan, Status: adaptersetup.StatusOK, Items: []adaptersetup.Item{item("a"), item("b")}}
	for _, tc := range []struct {
		name   string
		mutate func(*adaptersetup.Result)
		want   string
	}{
		{"valid", func(*adaptersetup.Result) {}, ""},
		{"another version", func(r *adaptersetup.Result) { r.Version = 2 }, "version 2"},
		{"another op", func(r *adaptersetup.Result) { r.Op = adaptersetup.OpCheck }, `"check"`},
		{"unknown status", func(r *adaptersetup.Result) { r.Status = "fine" }, `"fine"`},
		{"item without id", func(r *adaptersetup.Result) { r.Items[0].ID = "" }, "no id"},
		{"item without action", func(r *adaptersetup.Result) { r.Items[0].Action = "" }, "no action"},
		{"outcome without id", func(r *adaptersetup.Result) { r.Outcomes = []adaptersetup.Outcome{{Status: "done"}} }, "no id"},
		{"drift without problem", func(r *adaptersetup.Result) { r.Drift = []adaptersetup.Drift{{ID: "a"}} }, "no problem"},
		{"a requirement that is not an item", func(r *adaptersetup.Result) { r.Items[1].Requires = []string{"zzz"} }, `"zzz"`},
		{"a requirement that comes later", func(r *adaptersetup.Result) { r.Items[0].Requires = []string{"b"} }, `"b"`},
		{"an item that requires itself", func(r *adaptersetup.Result) { r.Items[1].Requires = []string{"b"} }, `"b"`},
		{"a requirement that is an earlier item", func(r *adaptersetup.Result) { r.Items[1].Requires = []string{"a"} }, ""},
		{"a removal in a plan without consent", func(r *adaptersetup.Result) { r.Items[0].Action, r.Items[0].Consent = adaptersetup.ActionRemove, false }, "consent"},
		{"a removal in a plan with consent", func(r *adaptersetup.Result) { r.Items[0].Action = adaptersetup.ActionRemove }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ok
			r.Items = append([]adaptersetup.Item(nil), ok.Items...)
			tc.mutate(&r)
			err := r.Validate(adaptersetup.OpPlan, adaptersetup.Version)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("Validate = %v, want %q", err, tc.want)
			}
		})
	}
}

// The status decides the exit code and the report line, so a result whose status the op
// can never have is refused: two callers would otherwise read, say, a refresh that
// answers "ok" differently. The table is the one Handle derives the status from.
func TestResultValidateRefusesAStatusTheOpDoesNotAllow(t *testing.T) {
	const (
		ok, refreshed, none, drift, failed = adaptersetup.StatusOK, adaptersetup.StatusRefreshed, adaptersetup.StatusNone, adaptersetup.StatusDrift, adaptersetup.StatusError
	)
	allowed := map[adaptersetup.Op][]adaptersetup.Status{
		adaptersetup.OpPlan:    {ok},
		adaptersetup.OpRemove:  {ok},
		adaptersetup.OpApply:   {ok, failed},
		adaptersetup.OpCheck:   {ok, none, drift},
		adaptersetup.OpRefresh: {refreshed, none, drift, failed},
	}
	for op, statuses := range allowed {
		for _, status := range []adaptersetup.Status{ok, refreshed, none, drift, failed} {
			t.Run(string(op)+" "+string(status), func(t *testing.T) {
				r := adaptersetup.Result{Version: 1, Op: op, Status: status, Recorded: ptr(true)}
				err := r.Validate(op, adaptersetup.Version)
				if want := slices.Contains(statuses, status); want && err != nil {
					t.Errorf("Validate = %v, want it accepted", err)
				} else if !want && (err == nil || !strings.Contains(err.Error(), string(status))) {
					t.Errorf("Validate = %v, want the status %q refused for %s", err, status, op)
				}
			})
		}
	}
}

// The version a result must carry is the one of the request it answers, not the newest
// version this program speaks: a later release raises Version and still answers the
// requests of older core programs.
func TestResultValidateChecksTheVersionAsked(t *testing.T) {
	for _, tc := range []struct {
		name          string
		resultVersion int
		asked         int
		refused       bool
	}{
		{"the version asked", 2, 2, false},
		{"an older version than the one asked", 1, 2, true},
		{"a newer version than the one asked", 2, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := adaptersetup.Result{Version: tc.resultVersion, Op: adaptersetup.OpPlan, Status: adaptersetup.StatusOK}
			err := r.Validate(adaptersetup.OpPlan, tc.asked)
			if refused := err != nil; refused != tc.refused {
				t.Errorf("Validate = %v, want refused %v", err, tc.refused)
			}
		})
	}
}

// Values that a later release adds to an enumeration are shown and not refused, so
// that an older program keeps working with the adapter an update has just installed.
// Only the status of the result as a whole, which decides the exit code, is strict;
// an outcome status this program does not know counts as a failure.
func TestResultValidateAcceptsValuesOfLaterReleases(t *testing.T) {
	later := adaptersetup.Result{Version: 1, Op: adaptersetup.OpCheck, Status: adaptersetup.StatusDrift, Recorded: ptr(true),
		Drift:    []adaptersetup.Drift{{ID: "a", Problem: "moved", Summary: "a moved"}},
		Outcomes: []adaptersetup.Outcome{{ID: "a", Status: "pending", Message: "later"}}}
	if err := later.Validate(adaptersetup.OpCheck, adaptersetup.Version); err != nil {
		t.Errorf("a problem and an outcome status of a later release: %v", err)
	}
	plan := adaptersetup.Result{Version: 1, Op: adaptersetup.OpPlan, Status: adaptersetup.StatusOK, Items: []adaptersetup.Item{{ID: "a", Action: "teleport", Consent: true}}}
	if err := plan.Validate(adaptersetup.OpPlan, adaptersetup.Version); err != nil {
		t.Errorf("an action of a later release: %v", err)
	}
	later.Status = "fine"
	if err := later.Validate(adaptersetup.OpCheck, adaptersetup.Version); err == nil {
		t.Error("a status of a later release was accepted; it decides the exit code")
	}
	for status, failed := range map[adaptersetup.OutcomeStatus]bool{
		adaptersetup.OutcomeDone: false, adaptersetup.OutcomeSkipped: false, adaptersetup.OutcomeFailed: true, "pending": true, "": true,
	} {
		if got := (adaptersetup.Outcome{Status: status}).Failed(); got != failed {
			t.Errorf("Outcome{%q}.Failed() = %v, want %v", status, got, failed)
		}
	}
}

// check and refresh answer whether a record exists; the guard of `adapter remove`
// acts on it, so a result without it is refused rather than read as "no record".
func TestResultValidateRequiresRecordedOfCheckAndRefresh(t *testing.T) {
	for _, op := range []adaptersetup.Op{adaptersetup.OpCheck, adaptersetup.OpRefresh} {
		r := adaptersetup.Result{Version: 1, Op: op, Status: adaptersetup.StatusNone}
		if err := r.Validate(op, adaptersetup.Version); err == nil || !strings.Contains(err.Error(), "recorded") {
			t.Errorf("%s without recorded: %v", op, err)
		}
		r.Recorded = ptr(false)
		if err := r.Validate(op, adaptersetup.Version); err != nil {
			t.Errorf("%s with recorded: %v", op, err)
		}
	}
}

// Plan, apply and remove (and the undo that follows it) refuse an instance whose state
// directory is not there before the handler is called; check and refresh find no record
// and answer for it.
func TestCommandRequiresTheStateDirectoryOfOpsThatWrite(t *testing.T) {
	missing, _ := json.Marshal(filepath.Join(t.TempDir(), "never-made"))
	for _, tc := range []struct {
		name, request string
		refused       bool
	}{
		{"plan", `"op":"plan"`, true},
		{"remove", `"op":"remove"`, true},
		{"apply", `"op":"apply"`, true},
		{"undo", `"op":"apply","undo":true`, true},
		{"check", `"op":"check"`, false},
		{"refresh", `"op":"refresh"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &script{}
			code, _, errs := runSetup(t, h, `{"version":1,`+tc.request+`,"state_dir":`+string(missing)+`}`)
			if tc.refused {
				if code != adapterkit.ExitFailed || !strings.Contains(errs, "start the daemon") || h.called != "" {
					t.Errorf("exit %d, stderr %q, handler called %q; want a refusal before the handler", code, errs, h.called)
				}
			} else if code != adapterkit.ExitOK || h.called == "" {
				t.Errorf("exit %d, stderr %q, handler called %q", code, errs, h.called)
			}
		})
	}
}

// A refresh with no record has nothing to report, and one that did an item the record
// does not hold has created it: both are the adapter's bug, whatever the adapter said.
func TestCommandHoldsRefreshToTheRecord(t *testing.T) {
	recorded := func(t *testing.T) string {
		state := t.TempDir()
		rec := adaptersetup.Record{}
		rec.Set(adaptersetup.RecordItem{ID: "a", Kind: "file", Target: "/t/a"})
		if err := adaptersetup.WriteRecord(state, rec); err != nil {
			t.Fatal(err)
		}
		return state
	}
	for _, tc := range []struct {
		name    string
		state   func(*testing.T) string
		refresh adaptersetup.Refresh
		want    string // in the error; empty for a result
	}{
		{"no record, nothing reported", func(t *testing.T) string { return t.TempDir() }, adaptersetup.Refresh{}, ""},
		{"no record, outcomes", func(t *testing.T) string { return t.TempDir() }, adaptersetup.Refresh{Outcomes: []adaptersetup.Outcome{outcome("a", adaptersetup.OutcomeDone)}}, "no record"},
		{"no record, drift", func(t *testing.T) string { return t.TempDir() }, adaptersetup.Refresh{Drift: []adaptersetup.Drift{drift("a")}}, "no record"},
		{"a recorded item rewritten", recorded, adaptersetup.Refresh{Recorded: true, Outcomes: []adaptersetup.Outcome{outcome("a", adaptersetup.OutcomeDone)}}, ""},
		{"an item the record does not hold made", recorded, adaptersetup.Refresh{Recorded: true, Outcomes: []adaptersetup.Outcome{outcome("b", adaptersetup.OutcomeDone)}}, `"b"`},
		{"an item the record does not hold failed", recorded, adaptersetup.Refresh{Recorded: true, Outcomes: []adaptersetup.Outcome{outcome("b", adaptersetup.OutcomeFailed)}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, _ := json.Marshal(tc.state(t))
			code, _, errs := runSetup(t, &script{refresh: tc.refresh}, `{"version":1,"op":"refresh","state_dir":`+string(state)+`}`)
			if tc.want == "" && code != adapterkit.ExitOK || tc.want != "" && (code != adapterkit.ExitFailed || !strings.Contains(errs, tc.want)) {
				t.Errorf("exit %d, stderr %q; want error %q", code, errs, tc.want)
			}
		})
	}
}
