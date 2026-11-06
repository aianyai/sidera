package update

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// current puts the programs, the daemon and the adapters at the target, so that a run
// has nothing to install.
func (r *rig) current() {
	r.cfg.Installed.Version = newVersion
	r.node.version = newVersion
	for i := range r.node.adapters {
		r.node.adapters[i].Version = newVersion
	}
}

// Every status the step can report, what it carries, and whether it fails the run.
func TestRunReportsTheRefreshOfEachInstance(t *testing.T) {
	r := newRig(t)
	r.release(newVersion)
	r.current()
	r.node.instances = []Instance{
		{ID: "work", Adapter: "claude"}, {ID: "idle", Adapter: "codex"}, {ID: "moved", Adapter: "codex"},
		{ID: "chat", Adapter: "console"}, {ID: "stuck", Adapter: "hermes"}, {ID: "broken", Adapter: "hermes"},
	}
	r.node.setups = map[string]SetupReport{
		"work":   {Status: SetupRefreshed, Items: []SetupItem{{ID: "skill", Status: "done"}}},
		"moved":  {Status: SetupDrift, Drift: []SetupDifference{{ID: "mcp", Problem: "missing", Summary: "the registration is gone"}}, Fix: "sidera adapter setup moved"},
		"chat":   {Status: SetupSkipped, Message: "the adapter declares no setup"},
		"stuck":  {Status: SetupError, Message: "1 item failed", Items: []SetupItem{{ID: "plugin", Status: "failed", Message: "client exited 1"}}},
		"broken": {},
	}
	r.node.setupErr = map[string]error{"broken": errors.New("the setup program did not start")}

	rep, err := Run(context.Background(), r.cfg, "")
	if err == nil || !strings.Contains(err.Error(), "stuck") || !strings.Contains(err.Error(), "broken") || strings.Contains(err.Error(), "moved") {
		t.Fatalf("Run = %v, want the failures of stuck and broken, and not the drift of moved", err)
	}
	want := []SetupReport{
		{Instance: "work", Adapter: "claude", Status: SetupRefreshed, Items: []SetupItem{{ID: "skill", Status: "done"}}},
		{Instance: "idle", Adapter: "codex", Status: SetupNone},
		{Instance: "moved", Adapter: "codex", Status: SetupDrift, Drift: []SetupDifference{{ID: "mcp", Problem: "missing", Summary: "the registration is gone"}}, Fix: "sidera adapter setup moved"},
		{Instance: "chat", Adapter: "console", Status: SetupSkipped, Message: "the adapter declares no setup"},
		{Instance: "stuck", Adapter: "hermes", Status: SetupError, Message: "1 item failed", Items: []SetupItem{{ID: "plugin", Status: "failed", Message: "client exited 1"}}},
		{Instance: "broken", Adapter: "hermes", Status: SetupError, Message: "the setup program did not start"},
	}
	if !reflect.DeepEqual(rep.Setup, want) {
		t.Errorf("setup = %+v\nwant   %+v", rep.Setup, want)
	}
	if rep.AlreadyCurrent {
		t.Error("a run that refreshed and failed is said to have had nothing to do")
	}
	if !rep.Acted() {
		t.Error("a failed refresh is not worth showing")
	}
}

// The step is triggered by what the adapter finds, not by a package having been
// replaced: a run with nothing to install still asks every instance, downloads nothing,
// and says it was not idle when an instance was refreshed, so that an update cut short
// between installing and refreshing is finished by the next run. Drift, which a run does
// not change, keeps it idle.
func TestRunRefreshesWhenNothingIsToBeInstalled(t *testing.T) {
	for _, tc := range []struct {
		name        string
		setups      map[string]SetupReport
		wantCurrent bool
	}{
		{"nothing to refresh", nil, true},
		{"a record that is up to date", map[string]SetupReport{"work": {Status: SetupRefreshed}}, true},
		{"one is refreshed", map[string]SetupReport{"work": {Status: SetupRefreshed, Items: []SetupItem{{ID: "skill", Status: "done"}}}}, false},
		// Drift is something only a person settles: it neither fails the run nor makes it busy.
		{"drift", map[string]SetupReport{"work": {Status: SetupDrift, Drift: []SetupDifference{{ID: "mcp", Problem: "changed"}}, Fix: "sidera adapter setup work"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.release(newVersion)
			r.current()
			r.node.instances = []Instance{{ID: "work", Adapter: "claude"}, {ID: "chat", Adapter: "console"}}
			r.node.setups = tc.setups
			rep, err := Run(context.Background(), r.cfg, "")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(r.node.steps, []string{"refresh work", "refresh chat"}) {
				t.Errorf("steps = %v, want both instances asked", r.node.steps)
			}
			if rep.AlreadyCurrent != tc.wantCurrent {
				t.Errorf("already_current = %v, want %v", rep.AlreadyCurrent, tc.wantCurrent)
			}
			if r.repo.hit("checksums") != 0 || len(r.plans) != 0 {
				t.Error("a run with nothing to install asked the network or the person")
			}
		})
	}
}

// The refresh is made by the program of the target, on the target's packages, so it
// comes after the daemon is restarted and after every adapter is installed, and also
// after an installation that failed (the other instances are still refreshed).
func TestRunRefreshesAfterTheInstalls(t *testing.T) {
	r := newRig(t)
	r.release(newVersion)
	r.node.instances = []Instance{{ID: "work", Adapter: "claude"}}
	r.node.installErr = map[string]error{"console": errors.New("in use")}
	rep, err := Run(context.Background(), r.cfg, "")
	if err == nil || !strings.Contains(err.Error(), "install adapter console") {
		t.Fatalf("Run = %v", err)
	}
	if want := []string{"restart", "install claude", "install console", "refresh work"}; !reflect.DeepEqual(r.node.steps, want) {
		t.Errorf("steps = %v, want %v", r.node.steps, want)
	}
	if len(rep.Setup) != 1 || len(r.plans) != 1 || !r.plans[0].RefreshSetup {
		t.Errorf("setup %+v, plans %+v: the plan must say the setup is refreshed", rep.Setup, r.plans)
	}
}

// Nothing is refreshed by an old daemon, or when no daemon answers.
func TestRunDoesNotRefreshWithoutADaemonOfTheTarget(t *testing.T) {
	t.Run("the restart fails", func(t *testing.T) {
		r := newRig(t)
		r.release(newVersion)
		r.node.instances = []Instance{{ID: "work", Adapter: "claude"}}
		r.node.restartErr = errors.New("no")
		if _, err := Run(context.Background(), r.cfg, ""); err == nil {
			t.Fatal("Run succeeded")
		}
		if want := []string{"restart"}; !reflect.DeepEqual(r.node.steps, want) {
			t.Errorf("steps = %v, want %v", r.node.steps, want)
		}
	})
	t.Run("no daemon runs", func(t *testing.T) {
		r := newRig(t)
		r.release(newVersion)
		r.node.running = false
		r.node.instances = []Instance{{ID: "work", Adapter: "claude"}}
		rep, err := Run(context.Background(), r.cfg, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(r.node.steps) != 0 || len(rep.Setup) != 0 || r.plans[0].RefreshSetup {
			t.Errorf("steps %v, setup %+v, plan %+v: with no daemon nothing is asked", r.node.steps, rep.Setup, r.plans[0])
		}
	})
}
