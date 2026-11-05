package adapter

import (
	"context"
	"errors"
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
// Each step sets what the fake Hermes answers, optionally lets a verification interval
// pass, checks the gate once and says what the daemon was told by that check: nothing,
// that the condition is cleared, or a condition with the text in it.
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
		// told is nothing, cleared, or the text the new condition holds.
		told string
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
		board: board, wakeProfiles: []string{"default", "feishu"}, log: slog.New(slog.DiscardHandler),
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
			if len(got) != 1 || len(got[0].Conditions) != 1 ||
				got[0].Conditions[0].Condition != adapterproto.ConditionDependencyMissing ||
				!strings.Contains(got[0].Conditions[0].Detail, st.told) {
				t.Errorf("%s: told %+v, want one dependency_missing holding %q", st.name, got, st.told)
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

// A check that finds a verification in progress waits for it and takes its result, so
// that the loops do not read the notifier together. The executable here does not exist:
// a check that verified on its own would fail with another error.
func TestVerificationInProgressIsShared(t *testing.T) {
	board, err := kanban.New(kanban.Config{Executable: filepath.Join(t.TempDir(), "no-hermes"), Home: t.TempDir(), Board: "lane"})
	if err != nil {
		t.Fatal(err)
	}
	g := &gate{
		board: board, wakeProfiles: []string{"default"}, log: slog.New(slog.DiscardHandler),
		now: time.Now, verifyEvery: time.Hour,
		report: func(context.Context, adapterproto.AvailabilityParams) error { return nil },
	}
	want := errors.New("the answer of the verification in progress")
	v := &verification{done: make(chan struct{}), err: want}
	close(v.done)
	g.verifying = v
	if got := g.Check(context.Background()); got != want {
		t.Errorf("got %v, want the result of the verification in progress", got)
	}
}
