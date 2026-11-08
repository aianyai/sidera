package runtime

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

const attempt3 = "55555555-5555-4555-8555-555555555555"

// argvFlag returns the value after flag in a launch's arguments, and whether the flag is there.
func argvFlag(argv []string, flag string) (string, bool) {
	i := slices.Index(argv, flag)
	if i < 0 || i+1 >= len(argv) {
		return "", false
	}
	return argv[i+1], true
}

// wantChoice fails unless a launch passes exactly the model and the effort; an
// empty one means the flag is absent.
func wantChoice(t *testing.T, argv []string, model, effort string) {
	t.Helper()
	for flag, want := range map[string]string{"--model": model, "--effort": effort} {
		got, ok := argvFlag(argv, flag)
		if got != want || ok != (want != "") {
			t.Errorf("%s = %q (present %v), want %q in %q", flag, got, ok, want, argv)
		}
	}
}

// checkpointMember reads one top-level member of a stored checkpoint.
func checkpointMember(t *testing.T, w *world, key, member string) (json.RawMessage, bool) {
	t.Helper()
	raw, ok := w.checkpoint(key)
	if !ok {
		t.Fatalf("no checkpoint %s", key)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	v, ok := m[member]
	return v, ok
}

// chooseParams is a creation on a preset, with the model and effort the requester chose.
func chooseParams(preset, model string, effort adapterproto.Effort) adapterproto.SessionCreateParams {
	p := createParams(attempt1, prompt1)
	p.Preset, p.Model, p.Effort = preset, model, effort
	return p
}

// setPresetChoice rewrites the model and effort of a preset, as an owner who
// rebuilds the preset does.
func setPresetChoice(app *App, preset, model, effort string) {
	app.mu.Lock()
	defer app.mu.Unlock()
	app.presets[preset]["model"], app.presets[preset]["effort"] = json.RawMessage(`"`+model+`"`), json.RawMessage(`"`+effort+`"`)
}

// The model and effort a requester chose are the session's own: they are in
// every turn's arguments, across a resume, a change of the preset and a restart
// of the adapter, and the owner's preset does not touch them.
func TestAChosenModelAndEffortStayWithTheSession(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	w.onLaunch = claudeWrites(2 * time.Millisecond)

	res, err := app.CreateSession(ctxT(t), chooseParams("choosy", "opus", adapterproto.EffortMax))
	if err != nil || res.Outcome != adapterproto.CreateCreated {
		t.Fatalf("create: %+v %v", res, err)
	}
	session := string(res.NativeRef)
	wantChoice(t, w.launched()[0].Argv, "opus", "max")
	w.waitEvents("started " + prompt1)
	w.exit(w.lastProc(), resultOK)
	w.waitEvents("started "+prompt1, "ended "+prompt1+" completed")

	for _, key := range []string{"attempt/" + attempt1, "session/session-1"} {
		choice, ok := checkpointMember(t, w, key, "choice")
		if !ok || string(choice) != `{"model":"opus","effort":"max"}` {
			t.Fatalf("%s: choice %s (present %v)", key, choice, ok)
		}
	}

	// The owner rebuilds the preset; the session keeps what it was created with.
	setPresetChoice(app, "choosy", "haiku", "low")
	if _, err := app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: attempt2, Text: "more"}); err != nil {
		t.Fatal(err)
	}
	wantChoice(t, w.launched()[1].Argv, "opus", "max")
	w.appendRecord(filepath.Join(recordDir, session+".jsonl"), userLine(session, attempt2))
	w.waitEvents("started "+prompt1, "ended "+prompt1+" completed", "persisted "+attempt2, "started "+attempt2)
	w.exit(w.lastProc(), resultOK)
	w.waitEvents("started "+prompt1, "ended "+prompt1+" completed", "persisted "+attempt2, "started "+attempt2, "ended "+attempt2+" completed")

	// A restarted adapter takes the session back from its checkpoint.
	cp, _ := w.checkpoint("session/session-1")
	restarted, _ := newApp(t, w, adapterproto.SessionAttach{
		Endpoint: "session-1", Preset: "choosy", NativeRef: adapterproto.NativeRef(session), Checkpoint: cp,
		ToolAccess: &adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": token, "SIDERA_HOME": toolHome}},
	})
	if res, err := restarted.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: attempt3, Text: "again"}); err != nil || res.Stage != adapterproto.PromptAccepted {
		t.Fatalf("deliver: %+v %v", res, err)
	}
	wantChoice(t, w.launched()[2].Argv, "opus", "max")
}

// A session whose requester chose nothing follows the preset as it is now, and a
// value neither the preset nor the requester gave is not passed.
func TestASessionWithoutAChoiceFollowsThePreset(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	w.onLaunch = claudeWrites(2 * time.Millisecond)

	res, err := app.CreateSession(ctxT(t), chooseParams("choosy", "", ""))
	if err != nil || res.Outcome != adapterproto.CreateCreated {
		t.Fatalf("create: %+v %v", res, err)
	}
	wantChoice(t, w.launched()[0].Argv, "sonnet", "high")
	w.waitEvents("started " + prompt1)
	w.exit(w.lastProc(), resultOK)
	w.waitEvents("started "+prompt1, "ended "+prompt1+" completed")
	for _, key := range []string{"attempt/" + attempt1, "session/session-1"} {
		if choice, ok := checkpointMember(t, w, key, "choice"); ok {
			t.Fatalf("%s holds a choice nobody made: %s", key, choice)
		}
	}

	setPresetChoice(app, "choosy", "haiku", "low")
	if _, err := app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: attempt2, Text: "more"}); err != nil {
		t.Fatal(err)
	}
	wantChoice(t, w.launched()[1].Argv, "haiku", "low")
}

// A model chosen on a preset that sets no effort leaves the effort to Claude Code.
func TestAChosenModelWithoutAPresetEffortPassesNoEffort(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	w.onLaunch = claudeWrites(2 * time.Millisecond)
	res, err := app.CreateSession(ctxT(t), chooseParams("lean", "opus", ""))
	if err != nil || res.Outcome != adapterproto.CreateCreated {
		t.Fatalf("create: %+v %v", res, err)
	}
	wantChoice(t, w.launched()[0].Argv, "opus", "")
	if choice, _ := checkpointMember(t, w, "attempt/"+attempt1, "choice"); string(choice) != `{"model":"opus"}` {
		t.Fatalf("choice %s", choice)
	}
}

// A choice the preset does not allow is refused before anything is stored or started.
func TestACreateWithAChoiceThePresetDoesNotAllowFails(t *testing.T) {
	tests := []struct {
		name   string
		preset string
		model  string
		effort adapterproto.Effort
	}{
		{"a model that is not listed", "choosy", "haiku", ""},
		{"an effort the model does not list", "choosy", "sonnet", adapterproto.EffortMax},
		{"a preset without an allowlist", "dev", "opus", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			app, _ := newApp(t, w)
			res, err := app.CreateSession(ctxT(t), chooseParams(tc.preset, tc.model, tc.effort))
			if err != nil {
				t.Fatal(err)
			}
			mustValid(t, res)
			if res.Outcome != adapterproto.CreateFailed || res.Reason != adapterproto.CreateFailModelChoiceNotAllowed || w.launchCount() != 0 || len(w.putKeys()) != 0 {
				t.Fatalf("result %+v, %d launches, puts %v", res, w.launchCount(), w.putKeys())
			}
		})
	}
}

// The same attempt asked again with another model or effort is another request,
// also by an adapter that knows the attempt only from its checkpoint.
func TestAnAttemptRepeatedWithAnotherChoiceConflicts(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	w.onLaunch = claudeWrites(2 * time.Millisecond)
	if _, err := app.CreateSession(ctxT(t), chooseParams("choosy", "opus", adapterproto.EffortMax)); err != nil {
		t.Fatal(err)
	}
	cp, _ := w.checkpoint("attempt/" + attempt1)

	tests := []struct {
		name   string
		model  string
		effort adapterproto.Effort
		ok     bool
	}{
		{"the same choice", "opus", adapterproto.EffortMax, true},
		{"another model", "sonnet", adapterproto.EffortMax, false},
		{"another effort", "opus", adapterproto.EffortHigh, false},
		{"no choice", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			restarted, _ := newApp(t, w)
			p := chooseParams("choosy", tc.model, tc.effort)
			p.Checkpoint = cp
			_, err := restarted.CreateSession(ctxT(t), p)
			if tc.ok != (err == nil) || (err != nil && !strings.Contains(err.Error(), "other parameters")) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// The probe reports each preset's default model and effort and its allowlist.
func TestProbeReportsTheModelChoiceOfEachPreset(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	res, err := app.Probe(ctxT(t), adapterproto.ProbeParams{ToolProbes: []adapterproto.ToolProbeRequest{}})
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, res)
	got := map[string]adapterproto.PresetStatus{}
	for _, p := range res.Presets {
		got[p.Name] = p
	}
	choosy := got["choosy"]
	wantAllowed := []adapterproto.ModelAllowance{
		{Model: "opus", Efforts: []adapterproto.Effort{adapterproto.EffortHigh, adapterproto.EffortMax}},
		{Model: "sonnet", Efforts: []adapterproto.Effort{adapterproto.EffortLow, adapterproto.EffortHigh}},
	}
	same := func(a, b adapterproto.ModelAllowance) bool {
		return a.Model == b.Model && slices.Equal(a.Efforts, b.Efforts)
	}
	if choosy.Model != "sonnet" || choosy.Effort != adapterproto.EffortHigh || !slices.EqualFunc(choosy.AllowedModels, wantAllowed, same) {
		t.Fatalf("choosy %+v", choosy)
	}
	if dev := got["dev"]; dev.Model != "" || dev.Effort != "" || dev.AllowedModels != nil {
		t.Fatalf("dev %+v", dev)
	}
}

// A stored choice ends up in the arguments of a turn process, so one that could
// not have come from a resolved choice makes the session unreachable.
func TestASessionWhoseStoredChoiceIsNotAChoiceIsUnreachable(t *testing.T) {
	const session = "6ee62271-42c1-4558-89cf-0e32c50e0c1b"
	for name, choice := range map[string]string{
		"a model that looks like a flag": `{"model":"--dangerously-skip-permissions"}`,
		"an effort off the scale":        `{"effort":"ultracode"}`,
		"a choice of nothing":            `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			cp := `{"v":1,"session":"` + session + `","cwd":` + strconvQuote(workDir) + `,"record_dir":` + strconvQuote(recordDir) + `,"choice":` + choice + `}`
			_, res := newApp(t, w, adapterproto.SessionAttach{
				Endpoint: "session-1", Preset: "choosy", NativeRef: session, Checkpoint: json.RawMessage(cp),
				ToolAccess: &adapterproto.ToolAccess{Env: map[string]string{}},
			})
			if res.Sessions[0].State != adapterproto.AttachUnreachable {
				t.Fatalf("session state %+v", res.Sessions[0])
			}
		})
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

// A checkpoint an earlier adapter stored is read and written back byte for byte: the
// daemon keeps checkpoints across an upgrade of the adapter, so the form of the
// choice in them must not move.
func TestStoredChoiceKeepsItsBytes(t *testing.T) {
	const session = "6ee62271-42c1-4558-89cf-0e32c50e0c1b"
	for _, choice := range []string{`{"model":"opus","effort":"high"}`, `{"model":"opus"}`, `{"effort":"low"}`} {
		stored := `{"v":1,"session":"` + session + `","cwd":"/work","record_dir":"/records","choice":` + choice + `}`
		cp, err := decodeSessionCheckpoint(json.RawMessage(stored))
		if err != nil {
			t.Fatalf("decode %s: %v", stored, err)
		}
		again, err := json.Marshal(cp)
		if err != nil || string(again) != stored {
			t.Errorf("session checkpoint written back as %s (%v), stored was %s", again, err, stored)
		}
	}
}
