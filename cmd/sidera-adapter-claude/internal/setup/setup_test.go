package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
	"github.com/aianyai/sidera/sdk/go/guidance"
)

func TestPlanOfAnInstanceThatHasNothing(t *testing.T) {
	r := newRig(t)
	res := r.mustRun(adaptersetup.OpPlan)
	equal(t, "items", ids(res.Items), []string{idSource, idMarketplace, idPlugin, idMCP, idPermission})
	want := map[string]struct {
		consent  bool
		requires []string
	}{
		idSource:      {false, nil},
		idMarketplace: {true, []string{idSource}},
		idPlugin:      {true, []string{idMarketplace}},
		idMCP:         {true, nil},
		idPermission:  {true, nil},
	}
	for _, it := range res.Items {
		w := want[it.ID]
		if it.Consent != w.consent || !slices.Equal(it.Requires, w.requires) || it.Action != adaptersetup.ActionCreate {
			t.Errorf("%s: consent %v requires %v action %s", it.ID, it.Consent, it.Requires, it.Action)
		}
	}
}

// What the server is registered with: the program that ran setup, or the instance's
// sidera_path; the data directory only when it is not the default.
func TestTheRegistrationNamesTheProgramAndTheHomeOnlyWhenNotDefault(t *testing.T) {
	other := filepath.Join(t.TempDir(), programName("other-sidera"))
	if err := os.WriteFile(other, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		homeDefault bool
		sideraPath  string
		wantCommand func(r *rig) string
		wantArgs    func(r *rig) string
	}{
		{"default home", true, "", func(r *rig) string { return r.program }, func(*rig) string { return "mcp" }},
		{"other home", false, "", func(r *rig) string { return r.program }, func(r *rig) string { return "--home " + r.home + " mcp" }},
		{"sidera_path wins", true, other, func(*rig) string { return other }, func(*rig) string { return "mcp" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.homeDef = tc.homeDefault
			if tc.sideraPath != "" {
				r.values["sidera_path"], _ = json.Marshal(tc.sideraPath)
			}
			r.setUp()
			if got := r.claude.server; got == nil || got.Command != tc.wantCommand(r) || got.Args != tc.wantArgs(r) {
				t.Errorf("registered %+v, want %q %q", got, tc.wantCommand(r), tc.wantArgs(r))
			}
		})
	}
}

func TestApplyDoesWhatWasAcceptedAndRecordsIt(t *testing.T) {
	r := newRig(t)
	r.setUp()

	// Claude Code has all of it.
	if r.claude.server == nil || r.claude.plugin == nil || len(r.claude.marketplaces) != 1 || r.claude.marketplaces[0].Path != r.srcDir() {
		t.Fatalf("claude holds %+v %+v %+v", r.claude.server, r.claude.plugin, r.claude.marketplaces)
	}
	// The source is the plugin, with the Skill as one file, for the owner alone.
	skill, err := os.ReadFile(filepath.Join(r.srcDir(), "plugins", "sidera", "skills", guidance.SkillName, "SKILL.md"))
	if err != nil || string(skill) != string(guidance.Skill()) {
		t.Errorf("SKILL.md: %v, same as the SDK's text: %v", err, string(skill) == string(guidance.Skill()))
	}
	var plugin struct{ Name, Version string }
	data, _ := os.ReadFile(filepath.Join(r.srcDir(), "plugins", "sidera", ".claude-plugin", "plugin.json"))
	if err := json.Unmarshal(data, &plugin); err != nil || plugin.Name != "sidera" || plugin.Version != r.version {
		t.Errorf("plugin.json %s: %v", data, err)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{r.srcDir(), filepath.Join(r.srcDir(), ".claude-plugin"), filepath.Join(r.srcDir(), ".claude-plugin", "marketplace.json")} {
			info, err := os.Stat(p)
			if err != nil || info.Mode().Perm()&0o077 != 0 {
				t.Errorf("%s: %v %v is readable by others", p, err, info.Mode())
			}
		}
	}
	// The rule is in settings.json.
	if got := r.readSettings(); !strings.Contains(got, `"mcp__sidera"`) {
		t.Errorf("settings.json %s", got)
	}
	// Everything is recorded.
	rec := r.record()
	for _, id := range planOrder {
		if _, ok := rec.Find(id); !ok {
			t.Errorf("%s is not recorded", id)
		}
	}
	// A second plan has nothing to do, and a check finds nothing wrong.
	if items := r.mustRun(adaptersetup.OpPlan).Items; len(items) != 0 {
		t.Errorf("second plan: %v", ids(items))
	}
	if res := r.mustRun(adaptersetup.OpCheck); res.Status != adaptersetup.StatusOK || len(res.Drift) != 0 {
		t.Errorf("check: %s %+v", res.Status, res.Drift)
	}
}

func TestApplyDoesOnlyWhatWasAcceptedInThePlansOrder(t *testing.T) {
	r := newRig(t)
	res := r.mustRun(adaptersetup.OpApply, idMCP, idSource, "nonsense")
	equal(t, "done", outcomeIDs(res.Outcomes, adaptersetup.OutcomeDone), []string{idSource, idMCP})
	equal(t, "skipped", outcomeIDs(res.Outcomes, adaptersetup.OutcomeSkipped), []string{"nonsense"})
	if r.claude.plugin != nil || len(r.claude.marketplaces) != 0 {
		t.Error("the marketplace and the plugin were declined")
	}
	if _, err := os.Stat(r.settings()); err == nil {
		t.Error("settings.json was written although the rule was declined")
	}
	rec := r.record()
	if _, ok := rec.Find(idMarketplace); ok || len(rec.Items) != 2 {
		t.Errorf("record %+v", rec.Items)
	}
}

func TestAFailedItemIsReportedAndNotRecorded(t *testing.T) {
	r := newRig(t)
	r.claude.fail["marketplace add"] = errors.New("boom")
	res := r.mustRun(adaptersetup.OpApply, idSource, idMarketplace, idMCP)
	if res.Status != adaptersetup.StatusError {
		t.Errorf("status %s", res.Status)
	}
	equal(t, "failed", outcomeIDs(res.Outcomes, adaptersetup.OutcomeFailed), []string{idMarketplace})
	for _, o := range res.Outcomes {
		if o.ID == idMarketplace && !strings.Contains(o.Message, "boom") {
			t.Errorf("message %q", o.Message)
		}
	}
	if _, ok := r.record().Find(idMarketplace); ok {
		t.Error("a failed item was recorded")
	}
	if _, ok := r.record().Find(idMCP); !ok {
		t.Error("an item that was done is not recorded")
	}
}

// What was already there: a registration that is what setup would register is adopted,
// a different one is replaced (with consent), a rule that was already allowed is left
// alone and never recorded, so that removing setup does not remove it.
func TestWhatWasAlreadyThere(t *testing.T) {
	t.Run("the same registration is adopted, a different one replaced", func(t *testing.T) {
		r := newRig(t)
		r.claude.server = &MCPServer{Command: r.program, Args: "mcp"}
		it := findItem(t, r.mustRun(adaptersetup.OpPlan).Items, idMCP)
		if it.Action != adaptersetup.ActionAdopt || !it.Consent {
			t.Errorf("same: %+v", it)
		}
		r.claude.server = &MCPServer{Command: "/old/sidera", Args: "mcp"}
		it = findItem(t, r.mustRun(adaptersetup.OpPlan).Items, idMCP)
		if it.Action != adaptersetup.ActionUpdate || !it.Consent {
			t.Errorf("different: %+v", it)
		}
		r.claude.calls = nil
		r.mustRun(adaptersetup.OpApply, idMCP)
		equal(t, "calls", r.claude.calls[len(r.claude.calls)-2:], []string{"mcp remove", "mcp add"})
		if r.claude.server.Command != r.program {
			t.Errorf("registered %+v", r.claude.server)
		}
	})
	t.Run("a rule that was there is not setup's", func(t *testing.T) {
		r := newRig(t)
		before := `{"permissions": {"allow": ["Bash(ls)", "mcp__sidera"]}}` + "\n"
		r.writeSettings(before)
		r.setUp()
		if got := r.readSettings(); got != before {
			t.Errorf("settings.json changed: %s", got)
		}
		if _, ok := r.record().Find(idPermission); ok {
			t.Error("a rule the person had was recorded")
		}
		if res := r.mustRun(adaptersetup.OpCheck); res.Status != adaptersetup.StatusOK {
			t.Errorf("check %s %+v", res.Status, res.Drift)
		}
	})
	t.Run("an installed plugin of the same version is adopted", func(t *testing.T) {
		r := newRig(t)
		r.claude.marketplaces = []Marketplace{{Name: marketplaceName, Path: r.srcDir()}}
		r.claude.plugin = &Plugin{ID: pluginID, Version: r.version, Enabled: true}
		items := r.mustRun(adaptersetup.OpPlan).Items
		if findItem(t, items, idMarketplace).Action != adaptersetup.ActionAdopt || findItem(t, items, idPlugin).Action != adaptersetup.ActionAdopt {
			t.Errorf("items %+v", items)
		}
	})
}

func findItem(t *testing.T, items []adaptersetup.Item, id string) adaptersetup.Item {
	t.Helper()
	for _, it := range items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no item %s in %v", id, ids(items))
	return adaptersetup.Item{}
}

// What setup refuses, and says why, without changing anything.
func TestAPlanRefusesWhatItCannotBuildOn(t *testing.T) {
	tests := []struct {
		name  string
		setup func(r *rig)
		want  string
	}{
		{"a marketplace of that name elsewhere", func(r *rig) {
			r.claude.marketplaces = []Marketplace{{Name: marketplaceName, Path: "/somewhere/else"}}
		}, "does not re-point"},
		{"a settings file with a comment", func(r *rig) { r.writeSettings("{\n// no\n}\n") }, "ignores a settings file"},
		{"a settings file that is not an object", func(r *rig) { r.writeSettings("[]") }, "settings.json"},
		{"permissions of the wrong type", func(r *rig) { r.writeSettings(`{"permissions": []}`) }, "permissions"},
		{"allow of the wrong type", func(r *rig) { r.writeSettings(`{"permissions": {"allow": "mcp__sidera"}}`) }, "permissions.allow"},
		{"a server in another scope", func(r *rig) { r.claude.fail["mcp get"] = errors.New("exists in the project scope") }, "project scope"},
		{"claude cannot be run", func(r *rig) { r.clientIs = errors.New("claude not found") }, "claude not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			tc.setup(r)
			_, err := r.run(adaptersetup.OpPlan)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("plan error %v, want it to say %q", err, tc.want)
			}
			if _, err := r.run(adaptersetup.OpApply, idSource, idPermission); err == nil {
				t.Error("apply went on")
			}
			if _, err := os.Stat(r.srcDir()); err == nil {
				t.Error("the source was written")
			}
			for _, c := range r.claude.calls {
				if strings.HasSuffix(c, " add") || c == "plugin install" {
					t.Errorf("claude was changed: %v", r.claude.calls)
				}
			}
		})
	}
}

// The rule goes into what the person has in settings.json without losing any of it.
func TestThePermissionRuleIsAddedToWhatIsThere(t *testing.T) {
	r := newRig(t)
	r.writeSettings(`{"model": "opus", "permissions": {"allow": ["Bash(ls)"], "deny": ["Bash(rm *)"]}}` + "\n")
	r.mustRun(adaptersetup.OpApply, idPermission)
	var got struct {
		Model       string
		Permissions struct{ Allow, Deny []string }
	}
	if err := json.Unmarshal([]byte(r.readSettings()), &got); err != nil {
		t.Fatal(err)
	}
	equal(t, "allow", got.Permissions.Allow, []string{"Bash(ls)", "mcp__sidera"})
	equal(t, "deny", got.Permissions.Deny, []string{"Bash(rm *)"})
	if got.Model != "opus" {
		t.Errorf("model %q", got.Model)
	}
}

func TestRefresh(t *testing.T) {
	t.Run("no record is none", func(t *testing.T) {
		r := newRig(t)
		res := r.mustRun(adaptersetup.OpRefresh)
		if res.Status != adaptersetup.StatusNone || len(r.claude.calls) != 0 {
			t.Errorf("%s, calls %v", res.Status, r.claude.calls)
		}
	})
	t.Run("nothing to do changes nothing", func(t *testing.T) {
		r := newRig(t)
		r.setUp()
		res := r.mustRun(adaptersetup.OpRefresh)
		if res.Status != adaptersetup.StatusRefreshed || len(res.Outcomes) != 0 {
			t.Errorf("%s %+v", res.Status, res.Outcomes)
		}
		for _, c := range r.claude.calls {
			if c != "marketplace list" && c != "plugin list" && c != "mcp get" {
				t.Errorf("refresh asked claude to %s", c)
			}
		}
	})
	t.Run("a new version rewrites the source and updates the plugin", func(t *testing.T) {
		r := newRig(t)
		r.setUp()
		r.version = "1.1.0"
		res := r.mustRun(adaptersetup.OpRefresh)
		equal(t, "done", outcomeIDs(res.Outcomes, adaptersetup.OutcomeDone), []string{idSource, idPlugin})
		if got := r.claude.plugin.Version; got != "1.1.0" {
			t.Errorf("plugin %s", got)
		}
		if calls := strings.Join(r.claude.calls, ","); !strings.Contains(calls, "marketplace update,plugin update") {
			t.Errorf("calls %s", calls)
		}
		if r.record().AdapterVersion != "1.1.0" {
			t.Errorf("record %+v", r.record())
		}
		if res := r.mustRun(adaptersetup.OpCheck); res.Status != adaptersetup.StatusOK {
			t.Errorf("check %s %+v", res.Status, res.Drift)
		}
	})
	t.Run("a moved program replaces the registration", func(t *testing.T) {
		r := newRig(t)
		r.setUp()
		r.program = filepath.Join(filepath.Dir(r.program), "sidera2")
		if err := os.WriteFile(r.program, nil, 0o755); err != nil {
			t.Fatal(err)
		}
		res := r.mustRun(adaptersetup.OpRefresh)
		equal(t, "done", outcomeIDs(res.Outcomes, adaptersetup.OutcomeDone), []string{idMCP})
		if r.claude.server.Command != r.program || r.record().SideraProgram != r.program {
			t.Errorf("server %+v record %+v", r.claude.server, r.record())
		}
	})
	t.Run("what the person removed or changed is drift, never put back", func(t *testing.T) {
		r := newRig(t)
		r.setUp()
		r.claude.server = nil
		r.claude.plugin = nil
		r.writeSettings("{}\n")
		if err := os.WriteFile(filepath.Join(r.srcDir(), ".claude-plugin", "marketplace.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		r.version = "9.9.9"
		res := r.mustRun(adaptersetup.OpRefresh)
		if res.Status != adaptersetup.StatusDrift || len(res.Outcomes) != 0 {
			t.Errorf("%s %+v", res.Status, res.Outcomes)
		}
		got := driftOf(res)
		for id, want := range map[string]adaptersetup.Problem{idMCP: adaptersetup.ProblemMissing, idPlugin: adaptersetup.ProblemMissing, idPermission: adaptersetup.ProblemMissing, idSource: adaptersetup.ProblemChanged} {
			if got[id] != want {
				t.Errorf("%s: %q, want %q", id, got[id], want)
			}
		}
		if r.claude.server != nil || r.claude.plugin != nil {
			t.Error("a refresh made what was gone")
		}
	})
	t.Run("a failed update is an outcome and is tried again", func(t *testing.T) {
		r := newRig(t)
		r.setUp()
		r.version = "1.1.0"
		r.claude.fail["plugin update"] = errors.New("boom")
		res := r.mustRun(adaptersetup.OpRefresh)
		if res.Status != adaptersetup.StatusError {
			t.Fatalf("status %s", res.Status)
		}
		equal(t, "failed", outcomeIDs(res.Outcomes, adaptersetup.OutcomeFailed), []string{idPlugin})
		delete(r.claude.fail, "plugin update")
		if res := r.mustRun(adaptersetup.OpRefresh); len(outcomeIDs(res.Outcomes, adaptersetup.OutcomeDone)) != 1 || r.claude.plugin.Version != "1.1.0" {
			t.Errorf("second refresh %+v, plugin %s", res.Outcomes, r.claude.plugin.Version)
		}
	})
}

func TestCheck(t *testing.T) {
	t.Run("without a record it says what setup would make", func(t *testing.T) {
		r := newRig(t)
		res := r.mustRun(adaptersetup.OpCheck)
		if res.Status != adaptersetup.StatusDrift || *res.Recorded {
			t.Fatalf("%s recorded %v", res.Status, *res.Recorded)
		}
		for _, id := range planOrder {
			if driftOf(res)[id] != adaptersetup.ProblemNeedsSetup {
				t.Errorf("%s: %q", id, driftOf(res)[id])
			}
		}
	})
	tests := []struct {
		name   string
		break_ func(r *rig)
		id     string
		want   adaptersetup.Problem
	}{
		{"plugin disabled", func(r *rig) { r.claude.plugin.Enabled = false }, idPlugin, adaptersetup.ProblemBroken},
		{"plugin source lost", func(r *rig) { r.claude.plugin.Errors = []string{"Marketplace sidera failed to load: cache-miss"} }, idPlugin, adaptersetup.ProblemBroken},
		{"source directory gone", func(r *rig) { os.RemoveAll(r.srcDir()) }, idSource, adaptersetup.ProblemMissing},
		{"source file edited", func(r *rig) {
			os.WriteFile(filepath.Join(r.srcDir(), "plugins", "sidera", "skills", "sidera", "SKILL.md"), []byte("mine"), 0o600)
		}, idSource, adaptersetup.ProblemChanged},
		{"registration changed", func(r *rig) { r.claude.server.Args = "other" }, idMCP, adaptersetup.ProblemChanged},
		{"rule removed", func(r *rig) { r.writeSettings(`{"permissions": {"allow": []}}`) }, idPermission, adaptersetup.ProblemMissing},
		{"settings.json invalid", func(r *rig) { r.writeSettings(`{"permissions": {"allow": ["mcp__sidera"],}}`) }, idPermission, adaptersetup.ProblemBroken},
		{"marketplace re-pointed", func(r *rig) { r.claude.marketplaces[0].Path = "/elsewhere" }, idMarketplace, adaptersetup.ProblemBroken},
		{"program gone", func(r *rig) { os.Remove(r.program) }, "program", adaptersetup.ProblemBroken},
		{"claude cannot be run", func(r *rig) { r.clientIs = errors.New("not found") }, "claude", adaptersetup.ProblemBroken},
		{"claude fails", func(r *rig) { r.claude.fail["plugin list"] = errors.New("exit 1") }, idPlugin, adaptersetup.ProblemBroken},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.setUp()
			tc.break_(r)
			res, err := r.run(adaptersetup.OpCheck)
			if err != nil {
				t.Fatalf("a check that finds a problem is not an error: %v", err)
			}
			if res.Status != adaptersetup.StatusDrift || !*res.Recorded || driftOf(res)[tc.id] != tc.want {
				t.Errorf("%s recorded %v drift %v, want %s %s", res.Status, *res.Recorded, driftOf(res), tc.id, tc.want)
			}
		})
	}
}

func TestRemoveListsTheRecordInTheOrderItIsUndone(t *testing.T) {
	r := newRig(t)
	if items := r.mustRun(adaptersetup.OpRemove).Items; len(items) != 0 {
		t.Errorf("without a record: %v", ids(items))
	}
	r.setUp()
	items := r.mustRun(adaptersetup.OpRemove).Items
	equal(t, "items", ids(items), []string{idPlugin, idMarketplace, idSource, idMCP, idPermission})
	for _, it := range items {
		if it.Action != adaptersetup.ActionRemove || it.Consent != (it.ID != idSource) {
			t.Errorf("%s: %s consent %v", it.ID, it.Action, it.Consent)
		}
	}
	equal(t, "marketplace requires", findItem(t, items, idMarketplace).Requires, []string{idPlugin})
	equal(t, "source requires", findItem(t, items, idSource).Requires, []string{idMarketplace, idPlugin})
}

func TestUndo(t *testing.T) {
	t.Run("undoes everything and the record goes", func(t *testing.T) {
		r := newRig(t)
		r.writeSettings(`{"permissions": {"allow": ["Bash(ls)"]}}`)
		r.setUp()
		res := r.undo(ids(r.mustRun(adaptersetup.OpRemove).Items)...)
		if res.Status != adaptersetup.StatusOK || len(outcomeIDs(res.Outcomes, adaptersetup.OutcomeDone)) != 5 {
			t.Fatalf("%s %+v", res.Status, res.Outcomes)
		}
		if r.claude.server != nil || r.claude.plugin != nil || len(r.claude.marketplaces) != 0 {
			t.Error("claude still holds something")
		}
		if _, err := os.Stat(r.srcDir()); err == nil {
			t.Error("the source is still there")
		}
		if got := r.readSettings(); strings.Contains(got, "mcp__sidera") || !strings.Contains(got, "Bash(ls)") {
			t.Errorf("settings.json %s", got)
		}
		if r.record() != nil {
			t.Error("the record is still there")
		}
	})
	t.Run("a rule that was the person's stays", func(t *testing.T) {
		r := newRig(t)
		r.writeSettings(`{"permissions": {"allow": ["mcp__sidera"]}}`)
		r.setUp()
		items := r.mustRun(adaptersetup.OpRemove).Items
		if hasItem(items, idPermission) {
			t.Fatal("the rule was listed for removal")
		}
		r.undo(ids(items)...)
		if !strings.Contains(r.readSettings(), "mcp__sidera") {
			t.Error("the rule was removed")
		}
	})
	t.Run("what was changed since is left and forgotten", func(t *testing.T) {
		r := newRig(t)
		r.setUp()
		r.claude.server.Command = "/mine/sidera"
		r.claude.marketplaces[0].Path = "/mine"
		res := r.undo(idMCP, idMarketplace)
		equal(t, "skipped", outcomeIDs(res.Outcomes, adaptersetup.OutcomeSkipped), []string{idMarketplace, idMCP})
		if r.claude.server == nil || len(r.claude.marketplaces) != 1 {
			t.Error("something that was not setup's was removed")
		}
		for _, id := range []string{idMCP, idMarketplace} {
			if _, ok := r.record().Find(id); ok {
				t.Errorf("%s is still recorded", id)
			}
		}
	})
	t.Run("what is already gone is done", func(t *testing.T) {
		r := newRig(t)
		r.setUp()
		r.claude.server, r.claude.plugin = nil, nil
		res := r.undo(idMCP, idPlugin)
		equal(t, "done", outcomeIDs(res.Outcomes, adaptersetup.OutcomeDone), []string{idPlugin, idMCP})
	})
	t.Run("a failure keeps the item recorded", func(t *testing.T) {
		r := newRig(t)
		r.setUp()
		r.claude.fail["plugin uninstall"] = errors.New("boom")
		res := r.undo(idPlugin)
		if res.Status != adaptersetup.StatusError {
			t.Errorf("status %s", res.Status)
		}
		if _, ok := r.record().Find(idPlugin); !ok {
			t.Error("a plugin that is still installed is no longer recorded")
		}
	})
	t.Run("claude missing fails what needs it, not the files", func(t *testing.T) {
		r := newRig(t)
		r.setUp()
		r.clientIs = errors.New("claude not found")
		res := r.undo(idPlugin, idSource, idPermission)
		equal(t, "failed", outcomeIDs(res.Outcomes, adaptersetup.OutcomeFailed), []string{idPlugin})
		equal(t, "done", outcomeIDs(res.Outcomes, adaptersetup.OutcomeDone), []string{idSource, idPermission})
	})
}

// The configuration directory is part of what setup recorded: undo, check and refresh act
// on it, whatever the environment of that run names. A plan and an apply act on the
// environment's, because that is what the person is setting up now, and refuse while
// items are recorded in another (TestAPlanRefusesAnotherConfigurationThanTheRecorded).
func TestItemsAreActedOnInTheConfigurationTheyWereMadeIn(t *testing.T) {
	other := filepath.Join(t.TempDir(), "other-claude")
	// The default configuration is not the same as the same directory named explicitly.
	for _, tc := range []struct {
		name      string
		configEnv func(r *rig) string
		wantEnv   func(r *rig) (string, bool)
	}{
		{"named by CLAUDE_CONFIG_DIR", func(r *rig) string { return r.config }, func(r *rig) (string, bool) { return r.config, true }},
		{"the default", func(r *rig) string { return "" }, func(*rig) (string, bool) { return "", false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			if tc.configEnv(r) == "" {
				r.config = filepath.Join(r.userHome, ".claude")
			}
			r.configEnv = tc.configEnv(r)
			r.setUp()
			wantValue, wantSet := tc.wantEnv(r)
			gotValue, gotSet := envValue(r.clientEnv, "CLAUDE_CONFIG_DIR")
			if gotValue != wantValue || gotSet != wantSet {
				t.Fatalf("setup ran claude with CLAUDE_CONFIG_DIR %q (set %v), want %q (set %v)", gotValue, gotSet, wantValue, wantSet)
			}

			// Now the environment names another directory.
			r.configEnv = other
			for _, op := range []adaptersetup.Op{adaptersetup.OpCheck, adaptersetup.OpRefresh} {
				res := r.mustRun(op)
				if gotValue, gotSet := envValue(r.clientEnv, "CLAUDE_CONFIG_DIR"); gotValue != wantValue || gotSet != wantSet {
					t.Errorf("%s ran claude with CLAUDE_CONFIG_DIR %q (set %v), want the recorded %q (set %v)", op, gotValue, gotSet, wantValue, wantSet)
				}
				if op == adaptersetup.OpCheck && (res.Status != adaptersetup.StatusOK || len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "recorded")) {
					t.Errorf("check: %s, drift %+v, notes %q; want ok with a note that says the recorded configuration was used", res.Status, res.Drift, res.Notes)
				}
			}

			// The rule is removed from the recorded settings.json, and the outcome says so.
			res := r.undo(ids(r.mustRun(adaptersetup.OpRemove).Items)...)
			if gotValue, gotSet := envValue(r.clientEnv, "CLAUDE_CONFIG_DIR"); gotValue != wantValue || gotSet != wantSet {
				t.Errorf("undo ran claude with CLAUDE_CONFIG_DIR %q (set %v), want %q (set %v)", gotValue, gotSet, wantValue, wantSet)
			}
			if strings.Contains(r.readSettings(), "mcp__sidera") {
				t.Error("the rule is still in the recorded settings.json")
			}
			for _, o := range res.Outcomes {
				if o.Status != adaptersetup.OutcomeDone || !strings.Contains(o.Message, "recorded") {
					t.Errorf("outcome %+v: want done, saying the recorded configuration was used", o)
				}
			}
		})
	}
}

// What the person declined is not drift, and a plan offers it again.
func TestADeclinedItemIsNotDriftButIsOfferedAgain(t *testing.T) {
	r := newRig(t)
	plan := r.mustRun(adaptersetup.OpPlan)
	var accepted []string
	for _, id := range ids(plan.Items) {
		if id != idMCP {
			accepted = append(accepted, id)
		}
	}
	r.declined = []string{idMCP}
	if res := r.mustRun(adaptersetup.OpApply, accepted...); res.Status != adaptersetup.StatusOK {
		t.Fatalf("apply: %+v", res.Outcomes)
	}
	r.declined = nil
	if r.claude.server != nil {
		t.Fatal("the declined registration was made")
	}

	if res := r.mustRun(adaptersetup.OpCheck); res.Status != adaptersetup.StatusOK || !*res.Recorded {
		t.Errorf("check: %s %+v", res.Status, res.Drift)
	}
	r.version = "1.1.0"
	res := r.mustRun(adaptersetup.OpRefresh)
	if res.Status != adaptersetup.StatusRefreshed || len(driftOf(res)) != 0 || r.claude.server != nil {
		t.Errorf("refresh: %s drift %+v, server %+v", res.Status, res.Drift, r.claude.server)
	}
	if it := findItem(t, r.mustRun(adaptersetup.OpPlan).Items, idMCP); it.Action != adaptersetup.ActionCreate {
		t.Errorf("plan: %+v", it)
	}
}

// A record that holds only declines records nothing setup did.
func TestARecordOfOnlyDeclinesIsNoRecord(t *testing.T) {
	r := newRig(t)
	r.declined = ids(r.mustRun(adaptersetup.OpPlan).Items)
	r.mustRun(adaptersetup.OpApply)
	r.declined = nil
	res := r.mustRun(adaptersetup.OpCheck)
	if *res.Recorded || res.Status != adaptersetup.StatusNone {
		t.Errorf("check: %s recorded %v drift %+v", res.Status, *res.Recorded, res.Drift)
	}
	r.claude.calls = nil
	if res := r.mustRun(adaptersetup.OpRefresh); res.Status != adaptersetup.StatusNone || len(r.claude.calls) != 0 {
		t.Errorf("refresh: %s, claude was asked %v", res.Status, r.claude.calls)
	}
}

// Claude Code that cannot be started is read from the error of the first call.
func TestAClaudeThatCannotBeStartedIsOneProblem(t *testing.T) {
	r := newRig(t)
	r.setUp()
	r.claude.fail["marketplace list"] = &adaptersetup.NotStartedError{Program: "claude", Err: os.ErrNotExist}
	res := r.mustRun(adaptersetup.OpCheck)
	if got := driftOf(res); len(got) != 1 || got["claude"] != adaptersetup.ProblemBroken {
		t.Errorf("check: %+v", res.Drift)
	}
}

// A sidera program that cannot be found is something to report, not a failure of the
// check, and a refresh does not register it.
func TestASideraPathThatCannotBeFoundIsReportedAsBroken(t *testing.T) {
	r := newRig(t)
	r.setUp()
	r.values["sidera_path"], _ = json.Marshal(filepath.Join(t.TempDir(), "no-such-sidera"))

	res := r.mustRun(adaptersetup.OpCheck)
	if res.Status != adaptersetup.StatusDrift || driftOf(res)["program"] != adaptersetup.ProblemBroken {
		t.Errorf("check: %s %+v", res.Status, res.Drift)
	}
	before := *r.claude.server
	res = r.mustRun(adaptersetup.OpRefresh)
	if len(res.Outcomes) != 0 || driftOf(res)["program"] != adaptersetup.ProblemBroken || *r.claude.server != before {
		t.Errorf("refresh: %+v %+v, server %+v", res.Outcomes, res.Drift, r.claude.server)
	}
	if _, err := r.run(adaptersetup.OpPlan); err == nil || !strings.Contains(err.Error(), "sidera_path") {
		t.Errorf("plan error %v, want it to refuse naming sidera_path", err)
	}
}

// Claude Code that cannot be run is one problem, not a missing marketplace, plugin and
// registration.
func TestWhenClaudeCannotBeRunItsItemsAreUnseenNotMissing(t *testing.T) {
	r := newRig(t)
	r.setUp()
	r.clientIs = errors.New("not found")
	for _, op := range []adaptersetup.Op{adaptersetup.OpCheck, adaptersetup.OpRefresh} {
		res := r.mustRun(op)
		got := driftOf(res)
		if len(got) != 1 || got["claude"] != adaptersetup.ProblemBroken {
			t.Errorf("%s: drift %+v, want one broken entry for the client", op, res.Drift)
		}
	}
}

// A plugin installed from a marketplace that is not setup's is not setup's to uninstall.
func TestUndoLeavesAPluginWhoseMarketplaceIsAnothers(t *testing.T) {
	r := newRig(t)
	r.setUp()
	r.claude.marketplaces[0].Path = "/someone/elses"
	res := r.undo(idPlugin)
	equal(t, "skipped", outcomeIDs(res.Outcomes, adaptersetup.OutcomeSkipped), []string{idPlugin})
	if r.claude.plugin == nil {
		t.Error("the plugin was uninstalled")
	}
	if _, ok := r.record().Find(idPlugin); ok {
		t.Error("the plugin is still recorded")
	}
}

// An installed copy that is behind the source Claude Code reads is stale, whatever the
// record says.
func TestAPluginBehindItsSourceIsStale(t *testing.T) {
	r := newRig(t)
	r.setUp()
	r.claude.plugin.SourceVersion = "1.0.1"
	res := r.mustRun(adaptersetup.OpCheck)
	if driftOf(res)[idPlugin] != adaptersetup.ProblemStale {
		t.Errorf("check: %+v", res.Drift)
	}
	if it := findItem(t, r.mustRun(adaptersetup.OpPlan).Items, idPlugin); it.Action != adaptersetup.ActionUpdate {
		t.Errorf("plan: %+v", it)
	}
	r.claude.version = func() string { return "1.0.1" }
	res = r.mustRun(adaptersetup.OpRefresh)
	equal(t, "done", outcomeIDs(res.Outcomes, adaptersetup.OutcomeDone), []string{idPlugin})
}

// Two configurations of Claude Code would share the one marketplace directory in the
// instance's state directory, so removing the setup of either would break the other's
// plugin silently. A plan, and so an apply, refuses to start a setup in another
// configuration than the recorded one, and says how to get out of it.
func TestAPlanRefusesAnotherConfigurationThanTheRecorded(t *testing.T) {
	other := filepath.Join(t.TempDir(), "other-claude")
	for _, tc := range []struct {
		name string
		// asRecorded makes the rig's setup be made in the configuration; named says how
		// the refusal calls it.
		asRecorded func(r *rig)
		named      string
	}{
		{"named by CLAUDE_CONFIG_DIR", func(r *rig) {}, "CLAUDE_CONFIG_DIR="},
		{"the default", func(r *rig) {
			r.config = filepath.Join(r.userHome, ".claude")
			r.configEnv = ""
		}, "the default configuration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			tc.asRecorded(r)
			r.setUp()
			recorded := r.record()

			r.configEnv = other
			r.claude.calls = nil
			_, err := r.run(adaptersetup.OpPlan)
			if err == nil {
				t.Fatal("plan in another configuration did not refuse")
			}
			for _, want := range []string{tc.named, r.config, "--remove"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("plan error %q does not say %q", err, want)
				}
			}
			if _, err := r.run(adaptersetup.OpApply, planOrder...); err == nil {
				t.Error("apply in another configuration did not refuse")
			}
			if got := r.record(); !reflect.DeepEqual(got.Items, recorded.Items) {
				t.Errorf("the record changed: %+v, was %+v", got.Items, recorded.Items)
			}
			for _, call := range r.claude.calls {
				if strings.Contains(call, "add") || strings.Contains(call, "install") || strings.Contains(call, "update") || strings.Contains(call, "remove") {
					t.Errorf("claude was asked to change something: %v", r.claude.calls)
				}
			}

			// The recorded configuration is still planned in, and once the setup is
			// removed another one may be made.
			res := r.undo(ids(r.mustRun(adaptersetup.OpRemove).Items)...)
			if res.Status != adaptersetup.StatusOK {
				t.Fatalf("undo: %+v", res.Outcomes)
			}
			if _, err := r.run(adaptersetup.OpPlan); err != nil {
				t.Errorf("plan after the setup was removed: %v", err)
			}
		})
	}
}

// A record of declines only holds no item, so it does not tie the instance to the
// configuration it was made in.
func TestADeclinedRecordDoesNotTieTheConfiguration(t *testing.T) {
	r := newRig(t)
	r.declined = ids(r.mustRun(adaptersetup.OpPlan).Items)
	r.mustRun(adaptersetup.OpApply)
	r.declined = nil
	r.configEnv = filepath.Join(t.TempDir(), "other-claude")
	if _, err := r.run(adaptersetup.OpPlan); err != nil {
		t.Errorf("plan: %v", err)
	}
}

// Without a record of something setup did, a check looks at nothing: Claude Code is not
// run, and what it would have said (it cannot be run) is not reported for an instance
// that never ran setup.
func TestACheckWithoutARecordDoesNotRunClaude(t *testing.T) {
	r := newRig(t)
	r.clientIs = errors.New("not found")
	res := r.mustRun(adaptersetup.OpCheck)
	if *res.Recorded || res.Status != adaptersetup.StatusDrift {
		t.Fatalf("check: %s recorded %v", res.Status, *res.Recorded)
	}
	for _, d := range res.Drift {
		if d.Problem != adaptersetup.ProblemNeedsSetup {
			t.Errorf("%s: %s, want only needs_setup", d.ID, d.Problem)
		}
	}
	if len(res.Drift) != len(planOrder) {
		t.Errorf("drift %v, want one needs_setup per item", driftOf(res))
	}
	r.clientIs = nil
	r.mustRun(adaptersetup.OpCheck)
	if len(r.claude.calls) != 0 {
		t.Errorf("claude was asked %v", r.claude.calls)
	}
}

// The record names the program the registration names: the instance's sidera_path wins
// over the program that ran setup.
func TestTheRecordKeepsTheProgramTheRegistrationNames(t *testing.T) {
	r := newRig(t)
	configured := filepath.Join(t.TempDir(), programName("configured-sidera"))
	if err := os.WriteFile(configured, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.values["sidera_path"], _ = json.Marshal(configured)
	r.setUp()
	if got := r.record().SideraProgram; got != configured {
		t.Errorf("record names the program %q, want %q", got, configured)
	}
}

// A configuration directory may hold the words the target is split at; what was
// recorded must still be read back as the configuration it was made in.
func TestTheConfigurationIsReadBackFromATargetWhoseDirectoryHasInInIt(t *testing.T) {
	for _, cfg := range []configuration{
		{dir: "/work/team in prod/claude", fromEnv: true},
		{dir: "/work/team in prod/claude"},
	} {
		for _, what := range []string{pluginID, serverName + " (user scope)", cfg.settings()} {
			got, ok := configurationOf(cfg.target(what))
			if !ok || got != cfg {
				t.Errorf("target %q read as %+v (ok %v), want %+v", cfg.target(what), got, ok, cfg)
			}
		}
	}
}
