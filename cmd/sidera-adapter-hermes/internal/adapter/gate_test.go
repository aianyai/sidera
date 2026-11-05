package adapter

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban/kanbantest"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The gate tells the daemon what the last verification found, and only when it changes.
// Each step sets what the fake Hermes answers about the notifiers and the toolsets,
// optionally lets a verification interval pass, checks the gate once and says what the
// daemon was told by that check: nothing, that the conditions are cleared, or the
// conditions with their text in it.
func TestGateReportsTheChangesOfWhatItFinds(t *testing.T) {
	const (
		nothing = "nothing"
		cleared = "cleared"
	)
	type step struct {
		name string
		// files are the notifier files of the fake, replacing the ones of the step before:
		// the general answer and the answers of single profiles.
		files map[string]string
		down  bool
		// later lets the verification interval pass before the check.
		later   bool
		wantErr bool
		// tools is the answer of `tools list`, replacing the recording in which both
		// required toolsets are enabled.
		tools string
		// told is nothing, cleared, or the text the first of the conditions holds;
		// also is the text of the second condition, when there are two.
		told, also string
	}
	off := func(profiles ...string) map[string]string {
		m := map[string]string{kanbantest.NotifierFile: "true\n"}
		for _, p := range profiles {
			m[kanbantest.NotifierFileOf(p)] = "false\n"
		}
		return m
	}
	unreadable := func(m map[string]string, profile string) map[string]string {
		m[kanbantest.NotifierFileOf(profile)] = "maybe\n"
		return m
	}
	allOff := map[string]string{kanbantest.NotifierFile: "false\n"}
	allOn := map[string]string{kanbantest.NotifierFile: "true\n"}
	const (
		noKanban  = "  ✗ disabled  kanban  x\n  ✓ enabled  skills  x\n"
		noRows    = "✗ Unknown platform 'x'. Valid: feishu\n"
		kanbanOff = "has not enabled the toolset(s) kanban"
	)

	steps := []step{
		{name: "everything on at the first check", files: allOn, told: nothing},
		{name: "off at the next verification", files: allOff, later: true,
			told: "disabled in the configuration of profiles default, feishu"},
		{name: "on again, not seen before a verification is due", files: allOn, told: nothing},
		{name: "cleared at the verification", files: allOn, later: true, told: cleared},
		{name: "unreadable adds nothing", files: unreadable(off(), "feishu"), later: true, told: nothing},
		{name: "off in one profile while another is unreadable", files: unreadable(off("default"), "feishu"), later: true,
			told: "disabled in the configuration of profile default ("},
		{name: "unreadable does not clear it", files: unreadable(off(), "feishu"), later: true, told: nothing},
		{name: "on everywhere clears it", files: allOn, later: true, told: cleared},
		{name: "off again", files: allOff, later: true, told: "disabled in the configuration of profiles default, feishu"},
		{name: "Hermes not usable takes the place of it", files: allOff, down: true, wantErr: true, told: "hermes kanban is not usable"},
		{name: "back, and the notifier condition returns", files: allOff, told: "disabled in the configuration of profiles default, feishu"},
		{name: "on after the recovery", files: allOn, later: true, told: cleared},

		{name: "toolsets: a toolset is missing at the next verification", files: allOn, tools: noKanban, later: true, told: kanbanOff},
		{name: "toolsets: enabled again, not seen before a verification is due", files: allOn, told: nothing},
		{name: "toolsets: cleared at the verification", files: allOn, later: true, told: cleared},
		{name: "toolsets: missing again", files: allOn, tools: noKanban, later: true, told: kanbanOff},
		{name: "toolsets: an unreadable answer keeps the last one", files: allOn, tools: noRows, later: true, told: nothing},
		{name: "toolsets: the notifier off too, both told", files: allOff, tools: noKanban, later: true,
			told: "disabled in the configuration of profiles default, feishu", also: kanbanOff},
		{name: "toolsets: both cleared at once", files: allOn, later: true, told: cleared},
	}

	ctl := t.TempDir()
	t.Setenv(kanbantest.ControlEnv, ctl)
	board, err := kanban.New(kanban.Config{Executable: kanbantest.Executable(t, kanbantest.ModeBoard), Home: t.TempDir(), Board: "lane"})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_700_000_000, 0)
	var told []adapterproto.AvailabilityParams
	g := &gate{
		board: board, wakeProfiles: []string{"default", "feishu"}, homeProfile: "feishu", homePlatform: "feishu",
		log: slog.New(slog.DiscardHandler),
		now: func() time.Time { return clock }, verifyEvery: time.Hour,
		report: func(_ context.Context, p adapterproto.AvailabilityParams) error {
			told = append(told, p)
			return nil
		},
	}
	for _, st := range steps {
		entries, err := os.ReadDir(ctl)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if err := os.Remove(filepath.Join(ctl, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
		for name, content := range st.files {
			if err := os.WriteFile(filepath.Join(ctl, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if st.down {
			if err := os.WriteFile(filepath.Join(ctl, kanbantest.DownFile), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if st.tools != "" {
			if err := os.WriteFile(filepath.Join(ctl, kanbantest.ToolsFile), []byte(st.tools), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if st.later {
			clock = clock.Add(2 * time.Hour)
		}
		before := len(told)
		if err := g.Check(context.Background()); (err != nil) != st.wantErr {
			t.Fatalf("%s: Check = %v, want an error: %v", st.name, err, st.wantErr)
		}
		got := told[before:]
		switch st.told {
		case nothing:
			if len(got) != 0 {
				t.Errorf("%s: told %+v, want nothing", st.name, got)
			}
		case cleared:
			if len(got) != 1 || len(got[0].Conditions) != 0 {
				t.Errorf("%s: told %+v, want the conditions cleared", st.name, got)
			}
		default:
			want := []string{st.told}
			if st.also != "" {
				want = append(want, st.also)
			}
			ok := len(got) == 1 && len(got[0].Conditions) == len(want)
			for i := 0; ok && i < len(want); i++ {
				c := got[0].Conditions[i]
				ok = c.Condition == adapterproto.ConditionDependencyMissing && strings.Contains(c.Detail, want[i])
			}
			if !ok {
				t.Errorf("%s: told %+v, want dependency_missing conditions holding %q", st.name, got, want)
			}
		}
	}
}

// The probe says which of the two it found about the notifier: that it is off in a
// profile, also when another profile could not be read, and that it could not tell.
func TestProbeSaysWhatItCouldNotReadAboutTheNotifier(t *testing.T) {
	for _, tc := range []struct {
		name   string
		files  map[string]string
		detail string
	}{
		{"nothing can be read", map[string]string{kanbantest.NotifierFile: "maybe\n"}, "could not read whether the Kanban notifier is on"},
		{"a profile is off, another cannot be read", map[string]string{
			kanbantest.NotifierFile: "false\n", kanbantest.NotifierFileOf("feishu"): "maybe\n"}, "disabled in the configuration of profile default ("},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &daemon{directAccess: true}
			h := start(t, options{d: d, settings: map[string]any{"creator_profiles": []string{"feishu"}}})
			for name, content := range tc.files {
				if err := os.WriteFile(filepath.Join(h.hermes, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var res adapterproto.ProbeResult
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := h.peer.Call(ctx, adapterproto.MethodAdapterProbe, adapterproto.ProbeParams{ToolProbes: []adapterproto.ToolProbeRequest{}}, &res); err != nil {
				t.Fatal(err)
			}
			for _, dep := range res.Dependencies {
				if dep.Name != "hermes-notifier" {
					continue
				}
				if dep.State != adapterproto.DependencyMissing || !strings.Contains(dep.Detail, tc.detail) {
					t.Errorf("hermes-notifier = %+v, want missing, holding %q", dep, tc.detail)
				}
				return
			}
			t.Errorf("no hermes-notifier dependency in %+v", res.Dependencies)
		})
	}
}

// Checks that arrive together share one verification: the loops do not read the
// notifiers and the toolsets once each. The fake Hermes holds every command until the
// test lets it go, so that both checks are in flight together.
func TestConcurrentChecksShareOneVerification(t *testing.T) {
	ctl := t.TempDir()
	t.Setenv(kanbantest.ControlEnv, ctl)
	board, err := kanban.New(kanban.Config{Executable: kanbantest.Executable(t, kanbantest.ModeBoard), Home: t.TempDir(), Board: "lane"})
	if err != nil {
		t.Fatal(err)
	}
	g := &gate{
		board: board, wakeProfiles: []string{"default"}, homeProfile: "feishu", homePlatform: "feishu",
		log: slog.New(slog.DiscardHandler), now: time.Now, verifyEvery: time.Hour,
		report: func(context.Context, adapterproto.AvailabilityParams) error { return nil },
	}
	calls := filepath.Join(ctl, kanbantest.CallsFile)
	if err := os.WriteFile(filepath.Join(ctl, kanbantest.BlockFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	check := func() { errs <- g.Check(context.Background()) }
	go check()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(calls); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first check never ran Hermes")
		}
	}
	go check()
	time.Sleep(100 * time.Millisecond) // the second check has arrived and waits
	if err := os.Remove(filepath.Join(ctl, kanbantest.BlockFile)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Errorf("Check = %v", err)
		}
	}
	log, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(log), "tools list"); n != 1 {
		t.Errorf("the toolsets were read %d times, want once:\n%s", n, log)
	}
}
