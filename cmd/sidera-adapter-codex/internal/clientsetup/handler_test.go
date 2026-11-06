package clientsetup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// rig is one instance whose setup runs against a fake Codex, through adaptersetup.Handle
// so that every result is also held to the contract.
type rig struct {
	t       *testing.T
	h       Handler
	f       *fakeCodex
	state   string
	program string
	home    string
	// homeIsDefault says whether the data directory is the default one.
	homeIsDefault bool
	config        map[string]json.RawMessage
	// environ is the environment of the setup program. Each Codex home has a Codex of its
	// own: fakes holds them by the value of CODEX_HOME, "" for the default home, and f is
	// the one of the home the rig starts in.
	environ []string
	fakes   map[string]*fakeCodex
	// clientProgram, clientDir and clientEnv are what the last client was made for.
	clientProgram, clientDir string
	clientEnv                []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	program := filepath.Join(dir, "sidera")
	if err := os.WriteFile(program, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, state: state, program: program, home: filepath.Join(dir, "home"), homeIsDefault: true,
		environ: []string{"PATH=/usr/bin"}, fakes: map[string]*fakeCodex{}}
	r.f = r.fakeFor("")
	r.h = Handler{Version: "1.0.0", Skill: []byte("skill one"), Environ: r.environ, NewClient: func(program, dir string, env []string) client {
		r.clientProgram, r.clientDir, r.clientEnv = program, dir, env
		return r.fakeFor(envValue(env, homeVar))
	}}
	return r
}

// fakeFor is the Codex of the Codex home, made when it is first asked for.
func (r *rig) fakeFor(home string) *fakeCodex {
	if r.fakes[home] == nil {
		r.fakes[home] = newFake()
	}
	return r.fakes[home]
}

// useHome makes the setup program's environment name the Codex home, the default one
// when home is empty.
func (r *rig) useHome(home string) {
	r.environ = []string{"PATH=/usr/bin"}
	if home != "" {
		r.environ = append(r.environ, homeVar+"="+home)
	}
	r.h.Environ = r.environ
}

// envValue is the value of the variable in the environment, "" when it is not there.
func envValue(env []string, name string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}
	return ""
}

// hasVar says whether the environment holds the variable at all.
func hasVar(env []string, name string) bool {
	return slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, name+"=") })
}

func (r *rig) req(op adaptersetup.Op, accepted ...string) adaptersetup.Request {
	return adaptersetup.Request{Version: adaptersetup.Version, Op: op, Home: r.home, HomeIsDefault: r.homeIsDefault,
		Instance: "codex", StateDir: r.state, Config: r.config, SideraProgram: r.program, Accepted: accepted}
}

func (r *rig) run(req adaptersetup.Request) adaptersetup.Result {
	r.t.Helper()
	res, err := adaptersetup.Handle(context.Background(), r.h, req)
	if err != nil {
		r.t.Fatalf("%s: %v", req.Op, err)
	}
	return res
}

func ids[T any](list []T, id func(T) string) []string {
	var out []string
	for _, x := range list {
		out = append(out, id(x))
	}
	return out
}

func itemIDs(items []adaptersetup.Item) []string {
	return ids(items, func(i adaptersetup.Item) string { return i.ID })
}
func driftIDs(d []adaptersetup.Drift) []string {
	return ids(d, func(i adaptersetup.Drift) string { return i.ID + ":" + string(i.Problem) })
}
func outcomeIDs(o []adaptersetup.Outcome) []string {
	return ids(o, func(i adaptersetup.Outcome) string { return i.ID + ":" + string(i.Status) })
}

func want(t *testing.T, what string, got, wanted []string) {
	t.Helper()
	if !slices.Equal(got, wanted) {
		t.Errorf("%s = %v, want %v", what, got, wanted)
	}
}

// setUp plans and applies everything, as a person who answers yes to every question.
func (r *rig) setUp() {
	r.t.Helper()
	plan := r.run(r.req(adaptersetup.OpPlan))
	res := r.run(r.req(adaptersetup.OpApply, itemIDs(plan.Items)...))
	if res.Status != adaptersetup.StatusOK {
		r.t.Fatalf("set up: %+v", res)
	}
}

func (r *rig) record() *adaptersetup.Record {
	r.t.Helper()
	rec, err := adaptersetup.ReadRecord(r.state)
	if err != nil {
		r.t.Fatal(err)
	}
	return rec
}

func (r *rig) dir() string { return filepath.Join(r.state, "setup", "codex", "marketplace") }

func TestPlanOfAnInstanceWithNothing(t *testing.T) {
	r := newRig(t)
	plan := r.run(r.req(adaptersetup.OpPlan))
	want(t, "items", itemIDs(plan.Items), []string{sourceID, marketplID, pluginItem})
	for _, it := range plan.Items {
		wantConsent := it.ID != sourceID
		if it.Consent != wantConsent || it.Action != adaptersetup.ActionCreate {
			t.Errorf("%s: consent %v action %s", it.ID, it.Consent, it.Action)
		}
	}
	want(t, "marketplace requires", plan.Items[1].Requires, []string{sourceID})
	want(t, "plugin requires", plan.Items[2].Requires, []string{sourceID, marketplID})
	if _, err := os.Stat(r.dir()); err == nil {
		t.Error("a plan wrote the source files")
	}
	if len(r.f.calls) > 0 && r.f.called("add-marketplace")+r.f.called("add-plugin") > 0 {
		t.Errorf("a plan changed Codex: %v", r.f.calls)
	}
}

func TestSetUpInstallsThePluginOnceAndPlansNothingAfter(t *testing.T) {
	r := newRig(t)
	r.setUp()
	if !r.f.installed || !r.f.enabled || r.f.marketplaces[marketplaceName] != r.dir() {
		t.Fatalf("codex = %+v", r.f)
	}
	s := r.f.pluginServer()
	if s.Command != r.program || !slices.Equal(s.Args, []string{"mcp"}) || s.Approval != "approve" {
		t.Errorf("the plugin's server = %+v", s)
	}
	rec := r.record()
	if rec == nil || len(rec.Items) != 3 || rec.SideraProgram != r.program || rec.AdapterVersion != "1.0.0" {
		t.Fatalf("record = %+v", rec)
	}
	want(t, "second plan", itemIDs(r.run(r.req(adaptersetup.OpPlan)).Items), nil)
	if check := r.run(r.req(adaptersetup.OpCheck)); check.Status != adaptersetup.StatusOK {
		t.Errorf("check = %+v", check)
	}
}

func TestTheServerIsStartedWithTheHomeOnlyWhenItIsNotTheDefault(t *testing.T) {
	r := newRig(t)
	r.homeIsDefault = false
	r.setUp()
	want(t, "args", r.f.pluginServer().Args, []string{"--home", r.home, "mcp"})
}

func TestTheInstancesOwnProgramWinsOverTheOneThatRanSetup(t *testing.T) {
	r := newRig(t)
	own := filepath.Join(t.TempDir(), "own-sidera")
	os.WriteFile(own, nil, 0o755)
	r.config = map[string]json.RawMessage{"sidera_program": json.RawMessage(`"` + filepath.ToSlash(own) + `"`)}
	r.setUp()
	if got := r.f.pluginServer().Command; got != filepath.ToSlash(own) {
		t.Errorf("command = %q, want %q", got, own)
	}
}

func TestPlanRefusesAMarketplaceThatPointsElsewhere(t *testing.T) {
	r := newRig(t)
	other := t.TempDir()
	os.MkdirAll(filepath.Join(other, ".agents", "plugins"), 0o755)
	os.WriteFile(filepath.Join(other, marketplaceFile), []byte("{}"), 0o644)
	r.f.marketplaces[marketplaceName] = other
	_, err := adaptersetup.Handle(context.Background(), r.h, r.req(adaptersetup.OpPlan))
	if err == nil || !strings.Contains(err.Error(), other) {
		t.Fatalf("err = %v", err)
	}
	if r.f.called("add-marketplace") > 0 {
		t.Error("the marketplace was re-pointed")
	}
}

func TestAnExistingMarketplaceAndPluginOfOursAreAdoptedNotMadeAgain(t *testing.T) {
	r := newRig(t)
	r.setUp()
	if err := os.Remove(adaptersetup.RecordPath(r.state)); err != nil { // a record that was lost
		t.Fatal(err)
	}
	r.f.calls = nil
	plan := r.run(r.req(adaptersetup.OpPlan))
	// The files in the state directory are written again, unasked; what is in Codex is
	// only taken over, with consent.
	want(t, "items", itemIDs(plan.Items), []string{sourceID, marketplID, pluginItem})
	for _, it := range plan.Items {
		if it.ID == sourceID {
			if it.Action != adaptersetup.ActionUpdate || it.Consent {
				t.Errorf("%s: %s consent %v", it.ID, it.Action, it.Consent)
			}
		} else if it.Action != adaptersetup.ActionAdopt || !it.Consent {
			t.Errorf("%s: %s consent %v", it.ID, it.Action, it.Consent)
		}
	}
	r.run(r.req(adaptersetup.OpApply, itemIDs(plan.Items)...))
	if r.f.called("add-marketplace") != 0 {
		t.Error("an adopted marketplace was added again")
	}
	if len(r.record().Items) != 3 {
		t.Errorf("record = %+v", r.record())
	}
}

func TestAPersonsOwnServerThatHidesThePluginIsOfferedForRemoval(t *testing.T) {
	cases := []struct {
		name     string
		user     *server
		offered  bool
		afterSet bool // the plugin is installed before the plan
	}{
		{"same program without the approval mode", &server{Found: true, Enabled: true, Command: "PROGRAM", Args: []string{"mcp"}}, true, false},
		{"another program", &server{Found: true, Enabled: true, Command: "/elsewhere/sidera", Args: []string{"mcp"}, Approval: "approve"}, true, false},
		{"disabled", &server{Found: true, Enabled: false, Command: "PROGRAM", Args: []string{"mcp"}, Approval: "approve"}, true, false},
		{"the same as the plugin's", &server{Found: true, Enabled: true, Command: "PROGRAM", Args: []string{"mcp"}, Approval: "approve"}, false, false},
		{"none", nil, false, false},
		{"installed plugin and a hiding server", &server{Found: true, Enabled: true, Command: "/elsewhere/sidera", Args: []string{"mcp"}}, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			if c.afterSet {
				r.setUp()
			}
			if c.user != nil {
				u := *c.user
				if u.Command == "PROGRAM" {
					u.Command = r.program
				}
				r.f.user = &u
			}
			plan := r.run(r.req(adaptersetup.OpPlan))
			var got *adaptersetup.Item
			for i, it := range plan.Items {
				if it.ID == mcpServerID {
					got = &plan.Items[i]
				}
			}
			if (got != nil) != c.offered {
				t.Fatalf("items = %v, offered %v", itemIDs(plan.Items), c.offered)
			}
			if got != nil {
				if got.Action != adaptersetup.ActionRemove || !got.Consent || len(got.Requires) != 0 {
					t.Errorf("item = %+v", *got)
				}
				res := r.run(r.req(adaptersetup.OpApply, mcpServerID))
				want(t, "outcomes", outcomeIDs(res.Outcomes), []string{mcpServerID + ":done"})
				if r.f.user != nil {
					t.Error("the server is still configured")
				}
				if rec := r.record(); rec != nil {
					if _, recorded := rec.Find(mcpServerID); recorded {
						t.Error("a server setup did not make was recorded")
					}
				}
			}
		})
	}
}

func TestApplyDoesWhatWasAcceptedInTheOrderOfThePlan(t *testing.T) {
	r := newRig(t)
	res := r.run(r.req(adaptersetup.OpApply, pluginItem, marketplID, sourceID, "stray"))
	want(t, "outcomes", outcomeIDs(res.Outcomes), []string{"stray:skipped", sourceID + ":done", marketplID + ":done", pluginItem + ":done"})
}

func TestAnItemWhoseRequiredItemFailedIsNotDone(t *testing.T) {
	r := newRig(t)
	r.f.failing["add-marketplace"] = true
	res := r.run(r.req(adaptersetup.OpApply, sourceID, marketplID, pluginItem))
	want(t, "outcomes", outcomeIDs(res.Outcomes), []string{sourceID + ":done", marketplID + ":failed", pluginItem + ":skipped"})
	if res.Status != adaptersetup.StatusError || r.f.called("add-plugin") != 0 {
		t.Errorf("status %s, add-plugin called %d times", res.Status, r.f.called("add-plugin"))
	}
	if rec := r.record(); rec == nil || len(rec.Items) != 1 || rec.Items[0].ID != sourceID {
		t.Errorf("record = %+v", rec)
	}
}

func TestACodexThatOffersNoServerFailsTheInstallButKeepsItInTheRecord(t *testing.T) {
	r := newRig(t)
	r.f.noServer = true
	plan := r.run(r.req(adaptersetup.OpPlan))
	res := r.run(r.req(adaptersetup.OpApply, itemIDs(plan.Items)...))
	if res.Status != adaptersetup.StatusError || !strings.Contains(res.Outcomes[2].Message, "does not offer its sidera MCP server") {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := r.record().Find(pluginItem); !ok {
		t.Error("an installed plugin is not recorded, so it could not be undone")
	}
}

func TestCheck(t *testing.T) {
	setUpThen := func(change func(r *rig)) func(t *testing.T) *rig {
		return func(t *testing.T) *rig {
			r := newRig(t)
			r.setUp()
			change(r)
			return r
		}
	}
	cases := []struct {
		name  string
		build func(t *testing.T) *rig
		drift []string
	}{
		{"no record lists what setup would make", func(t *testing.T) *rig { return newRig(t) },
			[]string{sourceID + ":needs_setup", marketplID + ":needs_setup", pluginItem + ":needs_setup"}},
		{"as set up", setUpThen(func(*rig) {}), nil},
		{"the person disabled the plugin", setUpThen(func(r *rig) { r.f.enabled = false }),
			[]string{pluginItem + ":broken"}},
		{"the plugin was uninstalled", setUpThen(func(r *rig) { r.f.installed = false }),
			[]string{pluginItem + ":missing"}},
		{"the marketplace was removed", setUpThen(func(r *rig) { delete(r.f.marketplaces, marketplaceName) }),
			[]string{marketplID + ":missing"}},
		{"the source files are gone: Codex cannot list, the plugin runs from its cache", setUpThen(func(r *rig) { os.RemoveAll(r.dir()) }),
			[]string{sourceID + ":missing", marketplID + ":broken"}},
		{"a source file was edited", setUpThen(func(r *rig) {
			os.WriteFile(filepath.Join(r.dir(), filepath.FromSlash(pluginSkill)), []byte("mine"), 0o600)
		}),
			[]string{sourceID + ":changed"}},
		{"someone installed another version of the plugin", setUpThen(func(r *rig) { r.f.version = "9.9.9" }),
			[]string{pluginItem + ":changed"}},
		{"a server of the person's hides the plugin's", setUpThen(func(r *rig) {
			r.f.user = &server{Found: true, Enabled: true, Command: "/elsewhere", Args: []string{"mcp"}}
		}), []string{mcpServerID + ":broken"}},
		{"Codex stopped offering the server", setUpThen(func(r *rig) { r.f.noServer = true }),
			[]string{pluginItem + ":broken"}},
		{"the sidera program is gone", setUpThen(func(r *rig) { os.Remove(r.program) }),
			[]string{programID + ":broken"}},
		{"Codex cannot be run", setUpThen(func(r *rig) { r.f.down = true }),
			[]string{clientID + ":broken"}},
		{"the adapter was updated", setUpThen(func(r *rig) { r.h.Version = "2.0.0" }),
			[]string{sourceID + ":stale", pluginItem + ":stale"}},
		{"the sidera program moved", setUpThen(func(r *rig) {
			moved := r.program + "-moved"
			os.Rename(r.program, moved)
			r.program = moved
		}), []string{sourceID + ":stale", pluginItem + ":stale"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := c.build(t)
			res := r.run(r.req(adaptersetup.OpCheck))
			want(t, "drift", driftIDs(res.Drift), c.drift)
		})
	}
}

func TestCheckOfAnInstanceWithNoRecordDoesNotNeedCodex(t *testing.T) {
	r := newRig(t)
	r.f.down = true
	res := r.run(r.req(adaptersetup.OpCheck))
	if res.Status != adaptersetup.StatusDrift || *res.Recorded || len(r.f.calls) != 0 {
		t.Errorf("result %+v, calls %v", res, r.f.calls)
	}
}

func TestRefresh(t *testing.T) {
	type refreshCase struct {
		name     string
		change   func(r *rig)
		status   adaptersetup.Status
		outcomes []string
		drift    []string
		adds     int // plugin installs the refresh made
		skill    string
	}
	cases := []refreshCase{
		{"nothing to do", func(*rig) {}, adaptersetup.StatusRefreshed, nil, nil, 0, "skill one"},
		{"the adapter was updated", func(r *rig) { r.h.Version, r.h.Skill = "2.0.0", []byte("skill two") },
			adaptersetup.StatusRefreshed, []string{sourceID + ":done", pluginItem + ":done"}, nil, 1, "skill two"},
		{"the sidera program moved", func(r *rig) {
			moved := r.program + "-moved"
			os.Rename(r.program, moved)
			r.program = moved
		}, adaptersetup.StatusRefreshed, []string{sourceID + ":done", pluginItem + ":done"}, nil, 1, "skill one"},
		{"updated while the person had disabled the plugin", func(r *rig) {
			r.h.Version, r.h.Skill = "2.0.0", []byte("skill two")
			r.f.enabled = false
		}, adaptersetup.StatusDrift, []string{sourceID + ":done", pluginItem + ":skipped"}, []string{pluginItem + ":broken"}, 0, "skill two"},
		{"updated after the person removed the plugin", func(r *rig) {
			r.h.Version = "2.0.0"
			r.f.installed = false
		}, adaptersetup.StatusDrift, []string{sourceID + ":done"}, []string{pluginItem + ":missing"}, 0, "skill one"},
		{"updated after the person removed the marketplace", func(r *rig) {
			r.h.Version = "2.0.0"
			delete(r.f.marketplaces, marketplaceName)
		}, adaptersetup.StatusDrift, []string{sourceID + ":done", pluginItem + ":skipped"}, []string{marketplID + ":missing"}, 0, "skill one"},
		{"updated after the person edited the source", func(r *rig) {
			r.h.Version = "2.0.0"
			os.WriteFile(filepath.Join(r.dir(), filepath.FromSlash(pluginSkill)), []byte("mine"), 0o600)
		}, adaptersetup.StatusDrift, []string{pluginItem + ":skipped"}, []string{sourceID + ":changed"}, 0, "mine"},
		{"updated while the source files are gone", func(r *rig) {
			r.h.Version = "2.0.0"
			os.RemoveAll(r.dir())
		}, adaptersetup.StatusDrift, []string{pluginItem + ":skipped"}, []string{sourceID + ":missing", marketplID + ":broken"}, 0, ""},
		{"updated and the install fails", func(r *rig) {
			r.h.Version = "2.0.0"
			r.f.failing["add-plugin"] = true
		}, adaptersetup.StatusError, []string{sourceID + ":done", pluginItem + ":failed"}, nil, 1, "skill one"},
		{"Codex cannot be run", func(r *rig) { r.h.Version = "2.0.0"; r.f.down = true },
			adaptersetup.StatusDrift, nil, []string{clientID + ":broken"}, 0, "skill one"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.setUp()
			c.change(r)
			before := r.f.called("add-plugin")
			res := r.run(r.req(adaptersetup.OpRefresh))
			if res.Status != c.status {
				t.Errorf("status = %s, want %s: %+v", res.Status, c.status, res)
			}
			want(t, "outcomes", outcomeIDs(res.Outcomes), c.outcomes)
			want(t, "drift", driftIDs(res.Drift), c.drift)
			if got := r.f.called("add-plugin") - before; got != c.adds {
				t.Errorf("plugin installs = %d, want %d", got, c.adds)
			}
			if c.skill != "" {
				got, _ := os.ReadFile(filepath.Join(r.dir(), filepath.FromSlash(pluginSkill)))
				if string(got) != c.skill {
					t.Errorf("skill = %q, want %q", got, c.skill)
				}
			}
		})
	}
}

func TestRefreshRecordsWhatItDidAndThenFindsNothingLeft(t *testing.T) {
	r := newRig(t)
	r.setUp()
	r.h.Version, r.h.Skill = "2.0.0", []byte("skill two")
	r.run(r.req(adaptersetup.OpRefresh))
	rec := r.record()
	if rec.AdapterVersion != "2.0.0" || len(rec.Items) != 3 {
		t.Errorf("record = %+v", rec)
	}
	if v := r.f.version; v != "2.0.0" {
		t.Errorf("installed version = %s", v)
	}
	res := r.run(r.req(adaptersetup.OpRefresh))
	if res.Status != adaptersetup.StatusRefreshed || len(res.Outcomes) != 0 {
		t.Errorf("second refresh = %+v", res)
	}
	want(t, "check", driftIDs(r.run(r.req(adaptersetup.OpCheck)).Drift), nil)
}

func TestRefreshWithNoRecordIsNoneAndTouchesNothing(t *testing.T) {
	r := newRig(t)
	res := r.run(r.req(adaptersetup.OpRefresh))
	if res.Status != adaptersetup.StatusNone || len(r.f.calls) != 0 {
		t.Errorf("status %s, calls %v", res.Status, r.f.calls)
	}
}

func TestRemoveListsRecordedItemsInTheOrderOfUndoing(t *testing.T) {
	r := newRig(t)
	if got := r.run(r.req(adaptersetup.OpRemove)).Items; len(got) != 0 {
		t.Errorf("items without a record = %v", got)
	}
	r.setUp()
	res := r.run(r.req(adaptersetup.OpRemove))
	want(t, "items", itemIDs(res.Items), []string{pluginItem, marketplID, sourceID})
	for i, wantConsent := range []bool{true, true, false} {
		if res.Items[i].Consent != wantConsent || res.Items[i].Action != adaptersetup.ActionRemove {
			t.Errorf("%s: %+v", res.Items[i].ID, res.Items[i])
		}
	}
	want(t, "marketplace requires", res.Items[1].Requires, []string{pluginItem})
	want(t, "source requires", res.Items[2].Requires, []string{marketplID})
}

func undoReq(r *rig, accepted ...string) adaptersetup.Request {
	req := r.req(adaptersetup.OpApply, accepted...)
	req.Undo = true
	return req
}

func TestUndoRemovesEverythingSetupMadeAndTheRecord(t *testing.T) {
	r := newRig(t)
	r.setUp()
	// The ids arrive in any order; the undoing follows the listing.
	res := r.run(undoReq(r, sourceID, pluginItem, marketplID))
	want(t, "outcomes", outcomeIDs(res.Outcomes), []string{pluginItem + ":done", marketplID + ":done", sourceID + ":done"})
	if r.f.installed || len(r.f.marketplaces) != 0 {
		t.Errorf("codex = %+v", r.f)
	}
	if _, err := os.Stat(filepath.Join(r.state, "setup", "codex")); !os.IsNotExist(err) {
		t.Errorf("the source directory is still there: %v", err)
	}
	if r.record() != nil {
		t.Error("the record is still there")
	}
}

func TestUndoLeavesWhatItDidNotUndoRecorded(t *testing.T) {
	cases := []struct {
		name     string
		change   func(r *rig)
		accepted []string
		outcomes []string
		left     []string
	}{
		{"the plugin cannot be removed: nothing after it is", func(r *rig) { r.f.failing["remove-plugin"] = true },
			[]string{pluginItem, marketplID, sourceID}, []string{pluginItem + ":failed", marketplID + ":skipped", sourceID + ":skipped"}, []string{sourceID, marketplID, pluginItem}},
		{"the marketplace is somebody else's now", func(r *rig) { r.f.marketplaces[marketplaceName] = "/elsewhere" },
			[]string{pluginItem, marketplID}, []string{pluginItem + ":done", marketplID + ":skipped"}, []string{sourceID, marketplID}},
		{"already uninstalled and already removed", func(r *rig) { r.f.installed = false; delete(r.f.marketplaces, marketplaceName) },
			[]string{pluginItem, marketplID}, []string{pluginItem + ":done", marketplID + ":done"}, []string{sourceID}},
		{"only what was accepted", func(*rig) {},
			[]string{pluginItem, "stray"}, []string{"stray:skipped", pluginItem + ":done"}, []string{sourceID, marketplID}},
		{"the record names another directory", func(r *rig) {
			rec := r.record()
			it, _ := rec.Find(sourceID)
			it.Target = filepath.Join(r.state, "other")
			rec.Set(it)
			adaptersetup.WriteRecord(r.state, *rec)
		}, []string{sourceID}, []string{sourceID + ":skipped"}, []string{sourceID, marketplID, pluginItem}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.setUp()
			c.change(r)
			res := r.run(undoReq(r, c.accepted...))
			want(t, "outcomes", outcomeIDs(res.Outcomes), c.outcomes)
			left := ids(r.record().Items, func(i adaptersetup.RecordItem) string { return i.ID })
			slices.Sort(left)
			wantLeft := slices.Clone(c.left)
			slices.Sort(wantLeft)
			want(t, "recorded", left, wantLeft)
		})
	}
}

func TestEveryRefusedConfigurationIsAnError(t *testing.T) {
	r := newRig(t)
	r.config = map[string]json.RawMessage{"codex_program": json.RawMessage(`7`)}
	if _, err := adaptersetup.Handle(context.Background(), r.h, r.req(adaptersetup.OpPlan)); err == nil {
		t.Error("a codex_program that is not a string was taken")
	}
}

// Codex names a directory by its real path, including one that is gone.
func TestSameDirComparesRealPathsEvenForADirectoryThatIsGone(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("links are not available:", err)
	}
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{filepath.Join(link, "gone", "dir"), filepath.Join(real, "gone", "dir"), true},
		{filepath.Join(link, "gone"), filepath.Join(real, "other"), false},
		{link, real, true},
	} {
		if got := sameDir(c.a, c.b); got != c.same {
			t.Errorf("sameDir(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

// Codex reports an unreadable marketplace by name. One that is somebody else's is not
// ours gone: it must not be taken for our source, or a refresh would install the
// plugin over a person's decision, and an undo would remove a marketplace unseen.
func TestSomeoneElsesUnreadableMarketplaceIsNotOurSourceGone(t *testing.T) {
	broken := func(r *rig) { r.f.marketplaces["foo"] = filepath.Join(r.state, "no-such-marketplace") }

	t.Run("check says Codex cannot list, not that our source is gone", func(t *testing.T) {
		r := newRig(t)
		r.setUp()
		broken(r)
		want(t, "drift", driftIDs(r.run(r.req(adaptersetup.OpCheck)).Drift), []string{clientID + ":broken"})
	})
	t.Run("refresh leaves a disabled plugin disabled", func(t *testing.T) {
		r := newRig(t)
		r.setUp()
		r.h.Version, r.h.Skill = "2.0.0", []byte("skill two")
		r.f.enabled = false
		broken(r)
		before := r.f.called("add-plugin")
		res := r.run(r.req(adaptersetup.OpRefresh))
		if got := r.f.called("add-plugin") - before; got != 0 {
			t.Errorf("the plugin was installed %d times while its state is not known", got)
		}
		want(t, "outcomes", outcomeIDs(res.Outcomes), []string{sourceID + ":done", pluginItem + ":skipped"})
		want(t, "drift", driftIDs(res.Drift), []string{clientID + ":broken"})
	})
	t.Run("plan does not adopt a marketplace that may not exist", func(t *testing.T) {
		r := newRig(t)
		broken(r)
		_, err := adaptersetup.Handle(context.Background(), r.h, r.req(adaptersetup.OpPlan))
		if err == nil || !strings.Contains(err.Error(), "foo") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("undo does not remove a marketplace whose source it cannot see", func(t *testing.T) {
		r := newRig(t)
		r.setUp()
		broken(r)
		res := r.run(undoReq(r, pluginItem, marketplID, sourceID))
		want(t, "outcomes", outcomeIDs(res.Outcomes), []string{pluginItem + ":done", marketplID + ":skipped", sourceID + ":skipped"})
		if r.f.called("remove-marketplace") != 0 {
			t.Error("the marketplace was removed")
		}
	})
}

func TestAnErrorNamingOurMarketplaceElsewhereIsNotOurSourceGone(t *testing.T) {
	r := newRig(t)
	r.setUp()
	// sidera was re-pointed at a directory that has no manifest.
	r.f.marketplaces[marketplaceName] = filepath.Join(r.state, "elsewhere")
	if _, err := adaptersetup.Handle(context.Background(), r.h, r.req(adaptersetup.OpPlan)); err == nil || !strings.Contains(err.Error(), "elsewhere") {
		t.Errorf("plan err = %v", err)
	}
	want(t, "drift", driftIDs(r.run(r.req(adaptersetup.OpCheck)).Drift), []string{marketplID + ":changed"})
	res := r.run(undoReq(r, pluginItem, marketplID, sourceID))
	want(t, "outcomes", outcomeIDs(res.Outcomes), []string{pluginItem + ":done", marketplID + ":skipped", sourceID + ":skipped"})
}

func TestUndoRemovesOurMarketplaceWhoseFilesAreGone(t *testing.T) {
	r := newRig(t)
	r.setUp()
	os.RemoveAll(r.dir())
	res := r.run(undoReq(r, pluginItem, marketplID, sourceID))
	want(t, "outcomes", outcomeIDs(res.Outcomes), []string{pluginItem + ":done", marketplID + ":done", sourceID + ":done"})
	if len(r.f.marketplaces) != 0 || r.f.installed {
		t.Errorf("codex = %+v", r.f)
	}
}

func TestUpdatingAPluginWhoseStateIsNotKnownWarnsThatItEnablesIt(t *testing.T) {
	r := newRig(t)
	r.setUp()
	os.RemoveAll(r.dir()) // our source is gone: Codex cannot say whether the plugin is disabled
	plan := r.run(r.req(adaptersetup.OpPlan))
	for _, it := range plan.Items {
		if it.ID == pluginItem && !strings.Contains(it.Summary, "if it is disabled now, installing it enables it again") {
			t.Errorf("plugin summary = %q", it.Summary)
		}
	}
}

func TestADisabledPluginIsReportedWithWhatToDoAboutIt(t *testing.T) {
	r := newRig(t)
	r.setUp()
	r.f.enabled = false
	d := r.run(r.req(adaptersetup.OpCheck)).Drift
	if len(d) != 1 || !strings.Contains(d[0].Summary, "enable it in Codex") || !strings.Contains(d[0].Summary, "`sidera adapter setup codex --remove`") {
		t.Errorf("drift = %+v", d)
	}
}

// What Codex cannot answer does not stop the checks that need no answer from it.
func TestACheckThatCannotAskCodexStillChecksTheRest(t *testing.T) {
	r := newRig(t)
	r.setUp()
	r.h.Version = "2.0.0"
	os.Remove(r.program)
	r.f.down = true
	want(t, "drift", driftIDs(r.run(r.req(adaptersetup.OpCheck)).Drift),
		[]string{sourceID + ":stale", pluginItem + ":stale", clientID + ":broken", programID + ":broken"})
	// A refresh that cannot ask Codex changes nothing.
	res := r.run(r.req(adaptersetup.OpRefresh))
	want(t, "refresh outcomes", outcomeIDs(res.Outcomes), nil)
	want(t, "refresh drift", driftIDs(res.Drift), []string{clientID + ":broken", programID + ":broken"})
}

func TestASourceDirectoryThatCannotBeReadIsNotACodexFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a directory cannot be made unreadable here")
	}
	r := newRig(t)
	r.setUp()
	locked := filepath.Join(r.dir(), "plugins", "sidera", "skills")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	d := r.run(r.req(adaptersetup.OpCheck)).Drift
	want(t, "drift", driftIDs(d), []string{sourceID + ":broken"})
	if strings.Contains(d[0].Summary, "cannot ask Codex") {
		t.Errorf("summary = %q", d[0].Summary)
	}
}

func TestARemovalThatLeavesAHidingServerListedFails(t *testing.T) {
	r := newRig(t)
	r.setUp()
	r.f.user = &server{Found: true, Enabled: true, Command: "/elsewhere/sidera", Args: []string{"mcp"}}
	r.f.userStays = true // it comes from a project's configuration, not config.toml
	res := r.run(r.req(adaptersetup.OpApply, mcpServerID))
	want(t, "outcomes", outcomeIDs(res.Outcomes), []string{mcpServerID + ":failed"})
	if res.Status != adaptersetup.StatusError || !strings.Contains(res.Outcomes[0].Message, "still offers") {
		t.Errorf("result = %+v", res)
	}
}

func TestARelativeProgramIsWrittenAsAnAbsolutePathAndRecorded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a program without an extension is not found on Windows")
	}
	r := newRig(t)
	dir := filepath.Dir(r.program)
	t.Chdir(dir)
	r.config = map[string]json.RawMessage{"sidera_program": json.RawMessage(`"./sidera"`)}
	r.setUp()
	got := r.f.pluginServer().Command
	if !filepath.IsAbs(got) || realPath(t, got) != realPath(t, r.program) {
		t.Errorf("command = %q, want %q", got, r.program)
	}
	// The record keeps the program the items name, not the one that ran setup.
	if rec := r.record(); rec.SideraProgram != got {
		t.Errorf("recorded program = %q, want %q", rec.SideraProgram, got)
	}
	want(t, "check", driftIDs(r.run(r.req(adaptersetup.OpCheck)).Drift), nil)
	if r.clientDir != r.state {
		t.Errorf("Codex was run in %s, not in the state directory %s", r.clientDir, r.state)
	}
}

func TestTrimVerbatim(t *testing.T) {
	for in, want := range map[string]string{
		`\\?\C:\Users\a\state`:       `C:\Users\a\state`,
		`\\?\UNC\server\share\state`: `\\server\share\state`,
		`C:\Users\a\state`:           `C:\Users\a\state`,
		`/home/a/state`:              `/home/a/state`,
	} {
		if got := trimVerbatim(in); got != want {
			t.Errorf("trimVerbatim(%q) = %q, want %q", in, got, want)
		}
	}
}

// A person who declined an item is not told to run setup for it, is asked again when
// they run it, and a rewrite of the record keeps what they declined.
func TestWhatThePersonDeclinedIsNotDriftAndIsOfferedAgain(t *testing.T) {
	r := newRig(t)
	apply := r.req(adaptersetup.OpApply, sourceID)
	apply.Declined = []string{marketplID, pluginItem}
	r.run(apply)
	if rec := r.record(); !rec.Installed() || !slices.Equal(rec.Declined, []string{marketplID, pluginItem}) {
		t.Fatalf("record = %+v", rec)
	}
	check := r.run(r.req(adaptersetup.OpCheck))
	if *check.Recorded != true || len(check.Drift) != 0 {
		t.Errorf("check = %+v", check)
	}
	want(t, "plan", itemIDs(r.run(r.req(adaptersetup.OpPlan)).Items), []string{marketplID, pluginItem})
	// The adapter was updated: the refresh rewrites the record, and the declines stay.
	r.h.Version, r.h.Skill = "2.0.0", []byte("skill two")
	r.run(r.req(adaptersetup.OpRefresh))
	if rec := r.record(); rec.AdapterVersion != "2.0.0" || !slices.Equal(rec.Declined, []string{marketplID, pluginItem}) {
		t.Errorf("record after the refresh = %+v", rec)
	}
}

func TestADeclinedRemovalOfAHidingServerIsNotDriftAndIsOfferedAgain(t *testing.T) {
	r := newRig(t)
	r.setUp()
	r.f.user = &server{Found: true, Enabled: true, Command: "/elsewhere/sidera", Args: []string{"mcp"}}
	want(t, "drift before", driftIDs(r.run(r.req(adaptersetup.OpCheck)).Drift), []string{mcpServerID + ":broken"})
	apply := r.req(adaptersetup.OpApply)
	apply.Declined = []string{mcpServerID}
	r.run(apply)
	want(t, "drift after", driftIDs(r.run(r.req(adaptersetup.OpCheck)).Drift), nil)
	want(t, "plan", itemIDs(r.run(r.req(adaptersetup.OpPlan)).Items), []string{mcpServerID})
}

func TestEverythingDeclinedLeavesNoRecordedSetup(t *testing.T) {
	r := newRig(t)
	apply := r.req(adaptersetup.OpApply)
	apply.Declined = []string{marketplID, pluginItem}
	r.run(apply)
	r.f.calls = nil
	check := r.run(r.req(adaptersetup.OpCheck))
	if *check.Recorded || len(r.f.calls) != 0 {
		t.Errorf("check = %+v, calls %v", check, r.f.calls)
	}
	want(t, "drift", driftIDs(check.Drift), []string{sourceID + ":needs_setup"})
	if res := r.run(r.req(adaptersetup.OpRefresh)); res.Status != adaptersetup.StatusNone {
		t.Errorf("refresh = %+v", res)
	}
	want(t, "remove", itemIDs(r.run(r.req(adaptersetup.OpRemove)).Items), nil)
}
