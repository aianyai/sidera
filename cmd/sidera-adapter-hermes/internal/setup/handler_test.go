package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// fakeHermes answers the reads setup makes of Hermes. It has no method that changes
// Hermes's configuration, as the Hermes interface has none.
type fakeHermes struct {
	root        string
	toolsets    map[string]kanban.ToolsetReading // by profile + "/" + platform
	notifierOff []string
	failConfig  map[string]bool
}

func (h *fakeHermes) ConfigFile(_ context.Context, profile string) (string, error) {
	if h.failConfig[profile] {
		return "", errors.New("hermes: no such profile")
	}
	return filepath.Join(h.root, profile, "config.yaml"), nil
}

func (h *fakeHermes) Toolsets(_ context.Context, profile, platform string) (kanban.ToolsetReading, error) {
	r, ok := h.toolsets[profile+"/"+platform]
	if !ok {
		return kanban.ToolsetReading{Enabled: []string{"kanban", "skills"}}, nil
	}
	return r, nil
}

func (h *fakeHermes) NotifierDisabled(context.Context, []string) ([]string, error) {
	return h.notifierOff, nil
}

// rig is one instance of the adapter with a fake Hermes and a state directory.
type rig struct {
	t        *testing.T
	hermes   *fakeHermes
	handler  Handler
	stateDir string
	config   map[string]json.RawMessage
	options  []string
	declined []string // sent with an apply, as the CLI sends the items the person said no to
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, hermes: &fakeHermes{root: filepath.Join(root, "profiles")}, stateDir: stateDir, config: map[string]json.RawMessage{}}
	for k, v := range map[string]any{
		config.KeyHermesHome:   filepath.Join(root, "hermes"),
		config.KeyBoard:        "lane",
		config.KeyHomeProfile:  "default",
		config.KeyHomePlatform: "feishu",
		config.KeyHomeChatID:   "oc_1",
		config.KeyAliases:      []string{"codex=nodeb/codex;modes=message"},
	} {
		r.set(k, v)
	}
	r.handler = Handler{NewHermes: func(config.Config) Hermes { return r.hermes }}
	return r
}

func (r *rig) set(key string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		r.t.Fatal(err)
	}
	r.config[key] = raw
}

// do runs one op through the SDK helper, as the setup command does.
func (r *rig) do(op adaptersetup.Op, accepted []string, undo bool) adaptersetup.Result {
	r.t.Helper()
	res, err := adaptersetup.Handle(context.Background(), r.handler, adaptersetup.Request{
		Version: adaptersetup.Version, Op: op, Instance: "hermes", StateDir: r.stateDir, Config: r.config,
		SideraProgram: "/usr/bin/sidera", Options: r.options, Accepted: accepted, Declined: r.declined, Undo: undo,
	})
	if err != nil {
		r.t.Fatalf("%s: %v", op, err)
	}
	return res
}

func (r *rig) skillFile(profile string) string {
	return filepath.Join(r.hermes.root, profile, "skills", "sidera", "SKILL.md")
}

func (r *rig) wantedSkill() string {
	r.t.Helper()
	cfg, err := config.Decode(r.config)
	if err != nil {
		r.t.Fatal(err)
	}
	skill, err := Skill(cfg.Board, cfg.Aliases)
	if err != nil {
		r.t.Fatal(err)
	}
	return string(skill)
}

func (r *rig) write(profile, content string) {
	r.t.Helper()
	p := r.skillFile(profile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) read(profile string) (string, bool) {
	data, err := os.ReadFile(r.skillFile(profile))
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return string(data), true
}

func (r *rig) record() *adaptersetup.Record {
	r.t.Helper()
	rec, err := adaptersetup.ReadRecord(r.stateDir)
	if err != nil {
		r.t.Fatal(err)
	}
	return rec
}

// setupAll plans and applies everything for the profiles of the options.
func (r *rig) setupAll() {
	r.t.Helper()
	var ids []string
	for _, it := range r.do(adaptersetup.OpPlan, nil, false).Items {
		ids = append(ids, it.ID)
	}
	if res := r.do(adaptersetup.OpApply, ids, false); res.Status != adaptersetup.StatusOK {
		r.t.Fatalf("apply: %+v", res)
	}
}

func itemActions(res adaptersetup.Result) map[string]adaptersetup.Action {
	out := map[string]adaptersetup.Action{}
	for _, it := range res.Items {
		out[it.ID] = it.Action
	}
	return out
}

// What a plan offers for a profile depends on the file there and on the record: the
// Skill is made, taken into the record when it is already as wanted, replaced when it is
// not, and left out when it is current. Everything touching the client is asked.
func TestPlanOffersWhatTheFileAndTheRecordCall(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(r *rig)
		want  adaptersetup.Action // "" for no item
		in    string              // a phrase of the summary
	}{
		{"no file", func(r *rig) {}, adaptersetup.ActionCreate, "give the agents of profile work"},
		{"a file as wanted that setup did not place (an earlier release)", func(r *rig) { r.write("work", r.wantedSkill()) },
			adaptersetup.ActionAdopt, "already have"},
		{"a different file that setup did not place", func(r *rig) { r.write("work", "someone else's") },
			adaptersetup.ActionUpdate, "which setup did not write, discarding its content (no copy is kept; --yes accepts this)"},
		{"a file setup placed, current", func(r *rig) { r.setupAll() }, "", ""},
		{"a file setup placed, now out of date", func(r *rig) {
			r.setupAll()
			r.set(config.KeyBoard, "other")
		}, adaptersetup.ActionUpdate, "up to date"},
		{"a file setup placed, edited since", func(r *rig) {
			r.setupAll()
			r.write("work", "edited")
		}, adaptersetup.ActionUpdate, "changed since setup wrote it, discarding that change (no copy is kept; --yes accepts this)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.options = []string{"--profile", "work"}
			tc.setup(r)
			res := r.do(adaptersetup.OpPlan, nil, false)
			if tc.want == "" {
				if len(res.Items) != 0 {
					t.Fatalf("items = %+v, want none", res.Items)
				}
				return
			}
			if len(res.Items) != 1 {
				t.Fatalf("items = %+v, want one", res.Items)
			}
			it := res.Items[0]
			if it.ID != "skill:work" || it.Action != tc.want || !it.Consent || it.Target != r.skillFile("work") {
				t.Errorf("item = %+v, want skill:work %s with consent at %s", it, tc.want, r.skillFile("work"))
			}
			if !strings.Contains(it.Summary, tc.in) {
				t.Errorf("summary %q lacks %q", it.Summary, tc.in)
			}
		})
	}
}

func TestOptionsChooseTheProfilesAndTheDefaultIsTheHomeProfile(t *testing.T) {
	for _, tc := range []struct {
		options []string
		want    []string
	}{
		{nil, []string{"skill:default"}},
		{[]string{"--profile", "work", "--profile=alpha", "--profile", "work"}, []string{"skill:alpha", "skill:work"}},
	} {
		r := newRig(t)
		r.options = tc.options
		var got []string
		for _, it := range r.do(adaptersetup.OpPlan, nil, false).Items {
			got = append(got, it.ID)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("options %v: items %v, want %v", tc.options, got, tc.want)
		}
	}
}

func TestOptionsAreRefusedWhenHermesWouldNotTakeThem(t *testing.T) {
	for _, options := range [][]string{{"--profile", "-x"}, {"--profile", "a b"}, {"--bogus"}, {"extra"}, {"--profile"}} {
		r := newRig(t)
		_, err := adaptersetup.Handle(context.Background(), r.handler, adaptersetup.Request{
			Version: adaptersetup.Version, Op: adaptersetup.OpPlan, StateDir: r.stateDir, Config: r.config, Options: options,
		})
		if err == nil {
			t.Errorf("options %v: accepted", options)
		}
	}
}

// Placing the Skill writes the file and records its digest; adopting records without
// touching the file; an id the plan no longer lists is skipped.
func TestApplyPlacesAdoptsAndRecords(t *testing.T) {
	r := newRig(t)
	r.options = []string{"--profile", "alpha", "--profile", "beta"}
	r.write("beta", r.wantedSkill())
	if err := os.Chtimes(r.skillFile("beta"), mtime, mtime); err != nil {
		t.Fatal(err)
	}

	plan := r.do(adaptersetup.OpPlan, nil, false)
	if got := itemActions(plan); got["skill:alpha"] != adaptersetup.ActionCreate || got["skill:beta"] != adaptersetup.ActionAdopt {
		t.Fatalf("plan = %v", got)
	}
	res := r.do(adaptersetup.OpApply, []string{"skill:alpha", "skill:beta", "skill:delta"}, false)
	status := map[string]adaptersetup.OutcomeStatus{}
	for _, o := range res.Outcomes {
		status[o.ID] = o.Status
	}
	for id, want := range map[string]adaptersetup.OutcomeStatus{"skill:alpha": adaptersetup.OutcomeDone, "skill:beta": adaptersetup.OutcomeDone, "skill:delta": adaptersetup.OutcomeSkipped} {
		if status[id] != want {
			t.Errorf("outcome of %s = %q, want %q (%+v)", id, status[id], want, res.Outcomes)
		}
	}
	if got, _ := r.read("alpha"); got != r.wantedSkill() {
		t.Errorf("the Skill of alpha is\n%s", got)
	}
	if info, err := os.Stat(r.skillFile("beta")); err != nil || !info.ModTime().Equal(mtime) {
		t.Errorf("adopting rewrote the file: %v, %v", info, err)
	}
	rec := r.record()
	for _, p := range []string{"alpha", "beta"} {
		it, ok := rec.Find("skill:" + p)
		if !ok || it.Digest != adaptersetup.Digest([]byte(r.wantedSkill())) || it.Target != r.skillFile(p) || it.Kind != "skill" {
			t.Errorf("record of %s = %+v, %v", p, it, ok)
		}
	}
	if rec.SideraProgram != "/usr/bin/sidera" || rec.AdapterVersion == "" {
		t.Errorf("record = %+v", rec)
	}
}

// A profile Hermes does not know fails the plan with Hermes's reason, naming nothing else.
func TestPlanFailsForAProfileHermesDoesNotKnow(t *testing.T) {
	r := newRig(t)
	r.options = []string{"--profile", "gamma"}
	r.hermes.failConfig = map[string]bool{"gamma": true}
	_, err := adaptersetup.Handle(context.Background(), r.handler, adaptersetup.Request{
		Version: adaptersetup.Version, Op: adaptersetup.OpPlan, StateDir: r.stateDir, Config: r.config, Options: r.options,
	})
	if err == nil || !strings.Contains(err.Error(), "no such profile") {
		t.Errorf("err = %v", err)
	}
}

func TestApplyFailsAProfileItCannotWriteAndRecordsNothingForIt(t *testing.T) {
	r := newRig(t)
	r.options = []string{"--profile", "work"}
	if runtime.GOOS == "windows" {
		t.Skip("directory modes do not stop a write on Windows")
	}
	// A profile directory in which nothing can be made.
	profileDir := filepath.Join(r.hermes.root, "work")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(profileDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(profileDir, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	res := r.do(adaptersetup.OpApply, []string{"skill:work"}, false)
	if res.Status != adaptersetup.StatusError || len(res.Outcomes) != 1 || res.Outcomes[0].Status != adaptersetup.OutcomeFailed {
		t.Fatalf("result = %+v", res)
	}
	requireNothingRecorded(t, r)
}

// An apply that did nothing (an id the plan no longer lists) leaves no record either.
func TestApplyThatDidNothingRecordsNothing(t *testing.T) {
	r := newRig(t)
	if res := r.do(adaptersetup.OpApply, []string{"skill:gone"}, false); res.Outcomes[0].Status != adaptersetup.OutcomeSkipped {
		t.Fatalf("result = %+v", res)
	}
	requireNothingRecorded(t, r)
}

// requireNothingRecorded asserts that no record exists and that the check `adapter remove`
// makes does not see one, so that the instance can be removed.
func requireNothingRecorded(t *testing.T, r *rig) {
	t.Helper()
	if rec := r.record(); rec != nil {
		t.Errorf("a record exists: %+v", rec)
	}
	if res := r.do(adaptersetup.OpCheck, nil, false); *res.Recorded {
		t.Errorf("check says recorded: %+v", res)
	}
}

// A refresh rewrites what it recorded and that is out of date; what the user changed or
// deleted is drift it leaves alone; with no record it does nothing.
func TestRefreshRewritesOnlyWhatItRecordedAndIsStale(t *testing.T) {
	t.Run("no record", func(t *testing.T) {
		r := newRig(t)
		res := r.do(adaptersetup.OpRefresh, nil, false)
		if res.Status != adaptersetup.StatusNone || *res.Recorded || len(res.Outcomes)+len(res.Drift) != 0 {
			t.Errorf("result = %+v", res)
		}
		if _, ok := r.read("default"); ok {
			t.Error("refresh made a file")
		}
	})
	t.Run("stale, changed and deleted", func(t *testing.T) {
		r := newRig(t)
		r.options = []string{"--profile", "stale", "--profile", "edited", "--profile", "gone", "--profile", "fine"}
		r.setupAll()
		r.options = nil // update refreshes with no options: the record names the profiles
		old := r.wantedSkill()
		r.set(config.KeyBoard, "other")
		r.write("edited", "my own edit")
		if err := os.Remove(r.skillFile("gone")); err != nil {
			t.Fatal(err)
		}
		// fine is current under the new board only if rewritten; make it current by hand.
		res := r.do(adaptersetup.OpRefresh, nil, false)
		done := map[string]bool{}
		for _, o := range res.Outcomes {
			done[o.ID] = o.Status == adaptersetup.OutcomeDone
		}
		if !done["skill:stale"] || !done["skill:fine"] || done["skill:edited"] || done["skill:gone"] {
			t.Errorf("outcomes = %+v", res.Outcomes)
		}
		problems := map[string]adaptersetup.Problem{}
		for _, d := range res.Drift {
			problems[d.ID] = d.Problem
		}
		if problems["skill:edited"] != adaptersetup.ProblemChanged || problems["skill:gone"] != adaptersetup.ProblemMissing || len(problems) != 2 {
			t.Errorf("drift = %+v", res.Drift)
		}
		if res.Status != adaptersetup.StatusDrift {
			t.Errorf("status = %q", res.Status)
		}
		if got, _ := r.read("stale"); got == old || got != r.wantedSkill() {
			t.Error("the stale Skill was not rewritten")
		}
		if got, _ := r.read("edited"); got != "my own edit" {
			t.Error("the user's edit was overwritten")
		}
		if _, ok := r.read("gone"); ok {
			t.Error("a deleted Skill was made again")
		}
	})
	t.Run("current", func(t *testing.T) {
		r := newRig(t)
		r.setupAll()
		res := r.do(adaptersetup.OpRefresh, nil, false)
		if res.Status != adaptersetup.StatusRefreshed || len(res.Outcomes) != 0 {
			t.Errorf("result = %+v", res)
		}
	})
}

// A check says the same as a refresh would find, and with no record lists what setup
// would make. Hermes is read only when there is a record, and what it says (a toolset an
// agent lacks, a notifier that is off) is a problem of the Skill that cannot work.
func TestCheckListsDriftAndWhatHermesLacks(t *testing.T) {
	t.Run("no record", func(t *testing.T) {
		r := newRig(t)
		r.hermes.toolsets = map[string]kanban.ToolsetReading{"default/feishu": {Enabled: []string{"web"}, Missing: []string{"kanban"}}}
		res := r.do(adaptersetup.OpCheck, nil, false)
		if *res.Recorded || res.Status != adaptersetup.StatusDrift || len(res.Drift) != 1 || res.Drift[0].Problem != adaptersetup.ProblemNeedsSetup || res.Drift[0].ID != "skill:default" {
			t.Errorf("result = %+v", res)
		}
	})
	t.Run("recorded and healthy", func(t *testing.T) {
		r := newRig(t)
		r.setupAll()
		if res := r.do(adaptersetup.OpCheck, nil, false); res.Status != adaptersetup.StatusOK || len(res.Drift) != 0 || len(res.Notes) != 0 {
			t.Errorf("result = %+v", res)
		}
	})
	t.Run("recorded, stale, and the agent lacks kanban on a platform named", func(t *testing.T) {
		r := newRig(t)
		r.setupAll()
		r.set(config.KeyBoard, "other")
		r.options = []string{"--platform", "telegram"}
		r.hermes.toolsets = map[string]kanban.ToolsetReading{"default/telegram": {Enabled: []string{"web", "skills"}, Missing: []string{"kanban"}}}
		r.hermes.notifierOff = []string{"default"}
		res := r.do(adaptersetup.OpCheck, nil, false)
		got := map[string]adaptersetup.Problem{}
		text := ""
		for _, d := range res.Drift {
			got[d.ID] = d.Problem
			text += d.Summary + "\n"
		}
		want := map[string]adaptersetup.Problem{"skill:default": adaptersetup.ProblemStale, "toolsets:default:telegram": adaptersetup.ProblemBroken, "notifier:default": adaptersetup.ProblemBroken}
		for id, p := range want {
			if got[id] != p {
				t.Errorf("drift %s = %q, want %q (%+v)", id, got[id], p, res.Drift)
			}
		}
		for _, s := range []string{"platform_toolsets.telegram", "add it as [web, skills, kanban]", "hermes -p default config set kanban.notify_in_gateway true"} {
			if !strings.Contains(text, s) {
				t.Errorf("the drift lacks %q:\n%s", s, text)
			}
		}
		if len(res.Notes) != 0 {
			t.Errorf("a check repeats its drift in notes: %v", res.Notes)
		}
	})
}

// A check reads the toolsets of the home conversation's agent as well as the profiles and
// platforms setup was run with: that is the agent whose delegation the gate watches. Cron
// is read only when setup was run with it, since it is the one platform an owner may not
// use at all.
func TestCheckReadsTheHomeAgentAndTheRememberedPlatforms(t *testing.T) {
	gap := kanban.ToolsetReading{Enabled: []string{"web"}, Missing: []string{"kanban"}}
	toolsets := map[string]kanban.ToolsetReading{
		"default/feishu": gap, "work/feishu": gap, "work/telegram": gap, "default/cron": gap, "work/cron": gap,
	}
	drift := func(r *rig) []string {
		var ids []string
		for _, d := range r.do(adaptersetup.OpCheck, nil, false).Drift {
			ids = append(ids, d.ID)
		}
		slices.Sort(ids)
		return ids
	}
	r := newRig(t)
	r.options = []string{"--profile", "work", "--platform", "telegram"}
	r.setupAll()
	r.hermes.toolsets = toolsets
	r.options = nil // `--check` with no options reads what setup was run with
	got := drift(r)
	want := []string{"toolsets:default:feishu", "toolsets:work:feishu", "toolsets:work:telegram"}
	if !slices.Equal(got, want) {
		t.Errorf("drift = %v, want %v (no cron, which was not named)", got, want)
	}

	r = newRig(t)
	r.options = []string{"--platform", "cron"}
	r.setupAll()
	r.hermes.toolsets = toolsets
	r.options = nil
	got = drift(r)
	want = []string{"toolsets:default:cron", "toolsets:default:feishu"}
	if !slices.Equal(got, want) {
		t.Errorf("drift = %v, want %v (cron was named)", got, want)
	}

	// A run of setup adds to what was remembered, and removing the setup forgets it.
	r = newRig(t)
	r.options = []string{"--platform", "telegram"}
	r.setupAll()
	r.options = []string{"--platform", "irc"}
	r.set(config.KeyBoard, "other") // so that the second run has an item to apply
	r.setupAll()
	r.hermes.toolsets = map[string]kanban.ToolsetReading{"default/telegram": gap, "default/irc": gap}
	r.options = nil
	if got, want := drift(r), []string{"toolsets:default:irc", "toolsets:default:telegram"}; !slices.Equal(got, want) {
		t.Errorf("drift = %v, want %v", got, want)
	}
	r.do(adaptersetup.OpApply, []string{"skill:default"}, true)
	if _, err := os.Stat(filepath.Join(r.stateDir, "setup", "platforms.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the remembered platforms outlive the setup: %v", err)
	}
}

// A plan carries what an agent still lacks as notes, so that it is read even when nothing
// is planned, and a person who declines the items has read it. A platform that has both
// toolsets is not named, nor is a platform that was not asked for; cron is always advised.
func TestPlanSaysWhichToolsetsToEnableByHandInNotes(t *testing.T) {
	const platform = "irc"
	r := newRig(t)
	r.options = []string{"--platform", platform}
	r.hermes.toolsets = map[string]kanban.ToolsetReading{
		"default/" + platform: {Enabled: []string{"web", "skills"}, Missing: []string{"kanban"}},
		"default/cron":        {Enabled: []string{"web"}, Missing: []string{"kanban", "skills"}},
		"default/feishu":      {Enabled: []string{"web"}, Missing: []string{"kanban"}},
	}
	for _, setUp := range []bool{false, true} {
		if setUp {
			r.setupAll() // nothing is left to plan
		}
		res := r.do(adaptersetup.OpPlan, nil, false)
		if (len(res.Items) != 0) == setUp {
			t.Fatalf("setUp %v: items = %+v", setUp, res.Items)
		}
		for _, it := range res.Items {
			if strings.Contains(it.Summary, "toolset") {
				t.Errorf("the summary of an item carries advice: %s", it.Summary)
			}
		}
		notes := strings.Join(res.Notes, "\n")
		cfg := filepath.Join(r.hermes.root, "default", "config.yaml")
		for _, want := range []string{
			"platform irc has not enabled the toolset(s) kanban",
			"platform cron has not enabled the toolset(s) kanban, skills",
			"edit " + cfg + " by hand",
			"platform_toolsets.irc holds kanban, and agent.disabled_toolsets does not",
			"add it as [web, skills, kanban]",
			"add it as [web, kanban, skills]",
		} {
			if !strings.Contains(notes, want) {
				t.Errorf("setUp %v: the notes lack %q:\n%s", setUp, want, notes)
			}
		}
		if strings.Contains(notes, "[hermes-") || strings.Contains(notes, "platform feishu") {
			t.Errorf("the notes name a composite toolset or a platform that was not asked for:\n%s", notes)
		}
	}
}

func TestPlanWarnsOfAProfileWithTheNotifierOff(t *testing.T) {
	for _, tc := range []struct {
		name string
		off  []string
		want []string
	}{
		{"on everywhere", nil, nil},
		{"the default off", []string{"default"}, []string{"the Kanban notifier is off in profile default (kanban.notify_in_gateway is false)", "hermes -p default config set kanban.notify_in_gateway true"}},
		{"another profile off", []string{"work"}, []string{"off in profile work (kanban.notify_in_gateway is false): a gateway started from it", "hermes -p work config set kanban.notify_in_gateway true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.options = []string{"--profile", "work"}
			r.hermes.notifierOff = tc.off
			notes := strings.Join(r.do(adaptersetup.OpPlan, nil, false).Notes, "\n")
			for _, want := range tc.want {
				if !strings.Contains(notes, want) {
					t.Errorf("the notes lack %q:\n%s", want, notes)
				}
			}
			if len(tc.want) == 0 && notes != "" {
				t.Errorf("notes although the notifier is on:\n%s", notes)
			}
		})
	}
}

// An item the person declined is not drift of a check or a refresh, and a plan offers it
// again so that they can change their mind.
func TestADeclinedSkillIsNotDriftAndIsOfferedAgain(t *testing.T) {
	r := newRig(t)
	r.options = []string{"--profile", "work", "--profile", "alpha"}
	r.declined = []string{"skill:alpha"}
	r.do(adaptersetup.OpApply, []string{"skill:work"}, false)
	r.declined = nil
	r.options = []string{"--profile", "alpha"} // the profiles of the record are added: work, alpha

	if res := r.do(adaptersetup.OpCheck, nil, false); res.Status != adaptersetup.StatusOK || len(res.Drift) != 0 || !*res.Recorded {
		t.Errorf("check = %+v", res)
	}
	if res := r.do(adaptersetup.OpRefresh, nil, false); res.Status != adaptersetup.StatusRefreshed || len(res.Drift) != 0 {
		t.Errorf("refresh = %+v", res)
	}
	plan := r.do(adaptersetup.OpPlan, nil, false)
	if len(plan.Items) != 1 || plan.Items[0].ID != "skill:alpha" || plan.Items[0].Action != adaptersetup.ActionCreate {
		t.Errorf("plan = %+v", plan.Items)
	}
	if rec := r.record(); rec == nil || !rec.IsDeclined("skill:alpha") || len(rec.Items) != 1 {
		t.Errorf("record = %+v", rec)
	}

	// Declining everything leaves a record of the declines alone: nothing is installed.
	r = newRig(t)
	r.declined = []string{"skill:default"}
	r.do(adaptersetup.OpApply, nil, false)
	res := r.do(adaptersetup.OpCheck, nil, false)
	if *res.Recorded || res.Status != adaptersetup.StatusNone || len(res.Drift) != 0 {
		t.Errorf("check = %+v", res)
	}
	if res := r.do(adaptersetup.OpRefresh, nil, false); *res.Recorded || res.Status != adaptersetup.StatusNone {
		t.Errorf("refresh = %+v", res)
	}
}

// `-- --help` answers with the options alone, not with the flag package's wording.
func TestHelpOptionPrintsTheOptionsText(t *testing.T) {
	r := newRig(t)
	_, err := adaptersetup.Handle(context.Background(), r.handler, adaptersetup.Request{
		Version: adaptersetup.Version, Op: adaptersetup.OpPlan, StateDir: r.stateDir, Config: r.config, Options: []string{"--help"},
	})
	if err == nil || err.Error() != optionsHelp {
		t.Errorf("err = %v", err)
	}
}

// A Skill that is a link into the owner's dotfiles stays a link: the file it leads to is
// written.
func TestApplyWritesThroughALinkedSkill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links need privileges on Windows")
	}
	r := newRig(t)
	r.options = []string{"--profile", "work"}
	target := filepath.Join(t.TempDir(), "dotfiles-skill.md")
	if err := os.WriteFile(target, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(r.skillFile("work")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, r.skillFile("work")); err != nil {
		t.Fatal(err)
	}
	r.do(adaptersetup.OpApply, []string{"skill:work"}, false)
	if info, err := os.Lstat(r.skillFile("work")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v, %v", info, err)
	}
	if got, _ := os.ReadFile(target); string(got) != r.wantedSkill() {
		t.Errorf("the file the link leads to holds\n%s", got)
	}
}

// Undoing deletes the file only while it is what setup wrote; a file someone changed is
// left and no longer recorded, so that the instance can still be removed; a file already
// gone is done. The record goes with the last item.
func TestUndoDeletesOnlyWhatSetupWrote(t *testing.T) {
	r := newRig(t)
	r.options = []string{"--profile", "mine", "--profile", "edited", "--profile", "gone"}
	r.setupAll()
	r.write("edited", "my own edit")
	if err := os.Remove(r.skillFile("gone")); err != nil {
		t.Fatal(err)
	}
	r.options = nil

	listing := r.do(adaptersetup.OpRemove, nil, false)
	if len(listing.Items) != 3 {
		t.Fatalf("remove listing = %+v", listing.Items)
	}
	for _, it := range listing.Items {
		if it.Action != adaptersetup.ActionRemove || !it.Consent {
			t.Errorf("item = %+v", it)
		}
		if changed := strings.Contains(it.Summary, "changed since setup wrote it"); changed != (it.ID == "skill:edited") {
			t.Errorf("summary of %s = %q", it.ID, it.Summary)
		}
	}
	res := r.do(adaptersetup.OpApply, []string{"skill:mine", "skill:edited", "skill:gone", "skill:never"}, true)
	status := map[string]adaptersetup.OutcomeStatus{}
	for _, o := range res.Outcomes {
		status[o.ID] = o.Status
	}
	want := map[string]adaptersetup.OutcomeStatus{"skill:mine": adaptersetup.OutcomeDone, "skill:edited": adaptersetup.OutcomeSkipped, "skill:gone": adaptersetup.OutcomeDone, "skill:never": adaptersetup.OutcomeSkipped}
	for id, w := range want {
		if status[id] != w {
			t.Errorf("outcome of %s = %q, want %q", id, status[id], w)
		}
	}
	if _, ok := r.read("mine"); ok {
		t.Error("the Skill setup wrote was not deleted")
	}
	if _, err := os.Stat(filepath.Dir(r.skillFile("mine"))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the empty skills/sidera directory was left: %v", err)
	}
	if got, _ := r.read("edited"); got != "my own edit" {
		t.Error("an edited file was deleted")
	}
	if r.record() != nil {
		t.Errorf("the record is left: %+v", r.record())
	}
}

func TestRemoveWithNoRecordListsNothing(t *testing.T) {
	r := newRig(t)
	if res := r.do(adaptersetup.OpRemove, nil, false); len(res.Items) != 0 {
		t.Errorf("items = %+v", res.Items)
	}
}

func TestSkillListsEveryAliasWithItsModesPresetsAndTheBoard(t *testing.T) {
	aliases, err := config.ParseAliases([]string{
		"Node-B/Codex=nodeb/codex;modes=new_session,message;presets=work,review",
		"ops=nodec/ops;modes=message",
	})
	if err != nil {
		t.Fatal(err)
	}
	skill, err := Skill("e2e-board", aliases)
	if err != nil {
		t.Fatal(err)
	}
	text := string(skill)
	for _, want := range []string{
		"- `node-b/codex`: `new_session`, `message`; presets: `work`, `review`\n",
		"- `ops`: `message`\n",
		"name: sidera\n",
		"`board: \"e2e-board\"`",
		"- `board`: `e2e-board`.\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("skill lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "nodeb/codex") || strings.Contains(text, "nodec/ops") {
		t.Error("the Skill shows an address: a card names an alias, never an address")
	}
}

var mtime = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
