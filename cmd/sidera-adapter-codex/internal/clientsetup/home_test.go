package clientsetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adaptersetup"
)

// What setup made lives in one Codex home, and what it made in one is not in another. These
// tests set up in one home and then run the other operations with an environment that
// names another, or the default one: they must act on the home the items were made in.

func (r *rig) targetOf(id string) string {
	r.t.Helper()
	it, ok := r.record().Find(id)
	if !ok {
		r.t.Fatalf("%s is not recorded", id)
	}
	return it.Target
}

func TestTheCodexHomeIsRecordedInTheTargetOfTheItemsInCodex(t *testing.T) {
	for _, c := range []struct{ name, home, recorded string }{
		{"named by the variable", "/h1", "CODEX_HOME=/h1"},
		{"the default", "", "the default Codex home"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.useHome(c.home)
			plan := r.run(r.req(adaptersetup.OpPlan))
			for _, it := range plan.Items {
				if inCodex := it.ID != sourceID; inCodex != strings.Contains(it.Target, c.recorded) {
					t.Errorf("planned %s has target %q, want it to name %q: %v", it.ID, it.Target, c.recorded, inCodex)
				}
			}
			r.setUp()
			for _, id := range []string{marketplID, pluginItem} {
				if got := r.targetOf(id); !strings.Contains(got, c.recorded) {
					t.Errorf("%s is recorded with target %q, want it to name %q", id, got, c.recorded)
				}
			}
			if got := r.targetOf(sourceID); got != r.dir() {
				t.Errorf("the source is recorded with target %q, want %q", got, r.dir())
			}
		})
	}
}

func TestCodexIsRunInTheHomeTheEnvironmentNames(t *testing.T) {
	rel := filepath.Join("rel", "home")
	abs, err := filepath.Abs(rel)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, env, wantHome string
		set                 bool
	}{
		{"named", "/h1", "/h1", true},
		{"default", "", "", false},
		// Setup runs Codex in the state directory, so a relative home would be another
		// directory there than where the person named it.
		{"relative", rel, abs, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.useHome(c.env)
			r.run(r.req(adaptersetup.OpPlan))
			if got := envValue(r.clientEnv, homeVar); got != c.wantHome || hasVar(r.clientEnv, homeVar) != c.set {
				t.Errorf("Codex ran with CODEX_HOME %q (set %v), want %q (set %v)", got, hasVar(r.clientEnv, homeVar), c.wantHome, c.set)
			}
		})
	}
}

// otherHomes are the environments that name another Codex home than the one "/h1" of the
// setup: another one named, and the default one.
var otherHomes = []struct{ name, home string }{
	{"another home", "/h2"},
	{"the default home", ""},
}

func TestUndoActsOnTheHomeSetupRecordedWhateverTheEnvironmentNames(t *testing.T) {
	for _, o := range otherHomes {
		t.Run(o.name, func(t *testing.T) {
			r := newRig(t)
			r.useHome("/h1")
			r.setUp()
			recorded := r.fakeFor("/h1")
			r.useHome(o.home)
			res := r.run(undoReq(r, sourceID, pluginItem, marketplID))
			want(t, "outcomes", outcomeIDs(res.Outcomes), []string{pluginItem + ":done", marketplID + ":done", sourceID + ":done"})
			if recorded.installed || len(recorded.marketplaces) != 0 {
				t.Errorf("the recorded home still holds the plugin (%v) or its marketplace (%v)", recorded.installed, recorded.marketplaces)
			}
			if other := r.fakeFor(o.home); other.called("remove-plugin")+other.called("remove-marketplace") > 0 {
				t.Errorf("undo touched the other home: %v", other.calls)
			}
			if got := envValue(r.clientEnv, homeVar); got != "/h1" {
				t.Errorf("Codex ran with CODEX_HOME %q, want the recorded /h1", got)
			}
			for _, out := range res.Outcomes {
				if !strings.Contains(out.Message, "CODEX_HOME=/h1") {
					t.Errorf("outcome %s says %q, want it to say that it acted on the recorded home", out.ID, out.Message)
				}
			}
			if r.record() != nil {
				t.Error("the record is still there")
			}
		})
	}
}

func TestUndoOfTheDefaultHomeRunsCodexWithoutTheVariable(t *testing.T) {
	r := newRig(t)
	r.setUp()
	r.useHome("/h2")
	res := r.run(undoReq(r, pluginItem, marketplID, sourceID))
	if r.f.installed || len(r.f.marketplaces) != 0 {
		t.Errorf("the default home still holds the plugin (%v) or its marketplace (%v)", r.f.installed, r.f.marketplaces)
	}
	if hasVar(r.clientEnv, homeVar) {
		t.Errorf("Codex ran with %v, want CODEX_HOME removed for the default home", r.clientEnv)
	}
	if !strings.Contains(res.Outcomes[0].Message, "default Codex home") {
		t.Errorf("outcome says %q, want it to say that it acted on the recorded default home", res.Outcomes[0].Message)
	}
}

func TestUndoInTheHomeItWasMadeInSaysNothingOfTheHome(t *testing.T) {
	r := newRig(t)
	r.useHome("/h1")
	r.setUp()
	for _, out := range r.run(undoReq(r, pluginItem, marketplID, sourceID)).Outcomes {
		if out.Message != "" {
			t.Errorf("outcome %s says %q", out.ID, out.Message)
		}
	}
}

func TestCheckLooksInTheRecordedHomeAndSaysSoWhenTheEnvironmentNamesAnother(t *testing.T) {
	for _, o := range otherHomes {
		t.Run(o.name, func(t *testing.T) {
			r := newRig(t)
			r.useHome("/h1")
			r.setUp()
			r.useHome(o.home)
			res := r.run(r.req(adaptersetup.OpCheck))
			if res.Status != adaptersetup.StatusOK {
				t.Errorf("check = %s, drift %+v; the recorded home holds everything", res.Status, res.Drift)
			}
			if got := envValue(r.clientEnv, homeVar); got != "/h1" {
				t.Errorf("Codex ran with CODEX_HOME %q, want the recorded /h1", got)
			}
			if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "CODEX_HOME=/h1") {
				t.Errorf("notes = %q, want one that names the recorded home", res.Notes)
			}
		})
	}
}

func TestCheckInTheRecordedHomeHasNoNote(t *testing.T) {
	r := newRig(t)
	r.useHome("/h1")
	r.setUp()
	if res := r.run(r.req(adaptersetup.OpCheck)); len(res.Notes) != 0 {
		t.Errorf("notes = %q", res.Notes)
	}
}

func TestRefreshWorksInTheRecordedHomeAndSaysSo(t *testing.T) {
	r := newRig(t)
	r.useHome("/h1")
	r.setUp()
	recorded := r.fakeFor("/h1")
	r.useHome("")
	r.h.Version = "1.0.1"
	res := r.run(r.req(adaptersetup.OpRefresh))
	want(t, "outcomes", outcomeIDs(res.Outcomes), []string{sourceID + ":done", pluginItem + ":done"})
	if recorded.version != "1.0.1" {
		t.Errorf("the recorded home has the plugin at %q, want 1.0.1", recorded.version)
	}
	if other := r.fakeFor(""); other.called("add-plugin") > 0 {
		t.Errorf("refresh installed in the other home: %v", other.calls)
	}
	for _, out := range res.Outcomes {
		if !strings.Contains(out.Message, "CODEX_HOME=/h1") {
			t.Errorf("outcome %s says %q, want it to name the recorded home", out.ID, out.Message)
		}
	}
}

// A plan and an apply act on the home the environment names. They refuse when setup was
// made in another: both would write the same source directory, which the other home's
// marketplace points at.
func TestPlanRefusesWhenSetupWasMadeInAnotherHome(t *testing.T) {
	for _, o := range otherHomes {
		t.Run(o.name, func(t *testing.T) {
			r := newRig(t)
			r.useHome("/h1")
			r.setUp()
			r.useHome(o.home)
			for _, op := range []adaptersetup.Op{adaptersetup.OpPlan, adaptersetup.OpApply} {
				_, err := adaptersetup.Handle(t.Context(), r.h, r.req(op, sourceID, marketplID, pluginItem))
				if err == nil {
					t.Fatalf("%s went ahead", op)
				}
				text := err.Error()
				if !strings.Contains(text, "CODEX_HOME=/h1") || !strings.Contains(text, "--remove") {
					t.Errorf("%s says %q, want it to name the recorded home and to say to remove it first", op, text)
				}
			}
			if other := r.fakeFor(o.home); other.called("add-marketplace")+other.called("add-plugin") > 0 {
				t.Errorf("setup changed the other home: %v", other.calls)
			}
			if _, err := os.Stat(r.dir()); err != nil {
				t.Errorf("the source directory of the recorded setup: %v", err)
			}
		})
	}
}

func TestPlanInTheRecordedHomeIsNotRefused(t *testing.T) {
	r := newRig(t)
	r.useHome("/h1")
	r.setUp()
	r.run(r.req(adaptersetup.OpPlan))
}
