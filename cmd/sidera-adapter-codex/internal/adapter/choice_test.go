package adapter_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver/appservertest"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// choicePresets are the presets of the model-choice tests: "dev" has defaults and an
// allowlist, "plain" has defaults and no allowlist, and "bare" has an allowlist and
// no defaults.
func choicePresets(t *testing.T, dir string) []adapterproto.PresetConfig {
	allow := map[string][]string{"gpt-5": {"low", "high"}, "gpt-5.1-codex": {"medium", "high"}}
	return []adapterproto.PresetConfig{
		{Name: "dev", Config: presetConfig(t, map[string]any{"cwd": dir, "model": "gpt-5", "effort": "low", "allowed_models": allow})},
		{Name: "plain", Config: presetConfig(t, map[string]any{"cwd": dir, "model": "gpt-5", "effort": "low"})},
		{Name: "bare", Config: presetConfig(t, map[string]any{"cwd": dir, "allowed_models": map[string][]string{"gpt-5": {"low", "high"}}})},
	}
}

// modelAndEffort is what a thread/start, thread/resume or turn/start call carried.
// Whether a member was sent at all is told by its pointer.
type modelAndEffort struct {
	Model  *string `json:"model"`
	Effort *string `json:"effort"`
}

func sent(c appservertest.Call) modelAndEffort { return decode[modelAndEffort](c.Params) }

func (m modelAndEffort) String() string {
	b, _ := json.Marshal(m)
	return string(b)
}

func want(model, effort string) modelAndEffort {
	var m modelAndEffort
	if model != "" {
		m.Model = &model
	}
	if effort != "" {
		m.Effort = &effort
	}
	return m
}

func requireSent(t *testing.T, what string, c appservertest.Call, w modelAndEffort) {
	t.Helper()
	if got := sent(c); got.String() != w.String() {
		t.Fatalf("%s carried %s, want %s", what, got, w)
	}
}

func (h *harness) createChoosing(attempt, endpoint, preset, model string, effort adapterproto.Effort) adapterproto.SessionCreateResult {
	h.t.Helper()
	p := createParams(attempt, endpoint, preset)
	p.Model, p.Effort = model, effort
	var res adapterproto.SessionCreateResult
	h.mustCall(adapterproto.MethodSessionCreate, p, &res)
	return res
}

func (h *harness) sessionCheckpoints() []adapterproto.CheckpointPutParams {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []adapterproto.CheckpointPutParams
	for _, c := range h.checkpoints {
		if c.Scope == adapterproto.ScopeSession {
			out = append(out, c)
		}
	}
	return out
}

// The choice is stored with the session before it is answered created, and it is what
// the thread is started with and every turn asks for. After the adapter restarts it
// comes back from the checkpoint, and the thread is resumed with it and every later
// turn asks for it, even though the preset says otherwise.
func TestChoiceSurvivesARestartAndAResume(t *testing.T) {
	codex := newFakeCodex(t)
	h := newHarness(t, codex)
	h.startWith(choicePresets(t, h.dir))
	got := h.createChoosing(attempt1, sessionEP, "dev", "gpt-5.1-codex", adapterproto.EffortHigh)
	if got.Outcome != adapterproto.CreateCreated {
		t.Fatalf("result = %+v", got)
	}
	requireSent(t, "thread/start", codex.callsOf("thread/start")[0], want("gpt-5.1-codex", ""))
	requireSent(t, "the first turn/start", codex.callsOf("turn/start")[0], want("gpt-5.1-codex", "high"))
	cps := h.sessionCheckpoints()
	if len(cps) != 1 || cps[0].ID != sessionEP || string(cps[0].State) != `{"v":1,"choice":{"model":"gpt-5.1-codex","effort":"high"}}` {
		t.Fatalf("session checkpoints = %+v", cps)
	}

	// The adapter starts again against a server that holds the thread, with the
	// checkpoint the daemon kept, and a preset whose defaults are other ones.
	restarted := newFakeCodex(t)
	restarted.seed("th-1", &fakeTurn{id: "tu-1", status: "completed", clientID: attempt1})
	h2 := newHarness(t, restarted)
	access := adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": token, "SIDERA_HOME": dataDir}}
	res := h2.startWith(choicePresets(t, h2.dir), adapterproto.SessionAttach{
		Endpoint: sessionEP, Preset: "dev", NativeRef: "th-1", ToolAccess: &access, Checkpoint: cps[0].State,
	})
	if len(res.Sessions) != 1 || res.Sessions[0].State != adapterproto.AttachAttached {
		t.Fatalf("start = %+v", res)
	}
	requireSent(t, "thread/resume", restarted.callsOf("thread/resume")[0], want("gpt-5.1-codex", ""))
	if r := h2.deliver(attempt2); r.Stage == adapterproto.PromptRefused {
		t.Fatalf("delivery = %+v", r)
	}
	requireSent(t, "the turn/start after the restart", restarted.callsOf("turn/start")[0], want("gpt-5.1-codex", "high"))
}

// A session whose requester chose nothing follows its preset, with nothing stored,
// before and after a restart.
func TestSessionWithoutAChoiceFollowsThePreset(t *testing.T) {
	codex := newFakeCodex(t)
	h := newHarness(t, codex)
	h.startWith(choicePresets(t, h.dir))
	if got := h.create(attempt1, sessionEP, "dev"); got.Outcome != adapterproto.CreateCreated {
		t.Fatalf("result = %+v", got)
	}
	requireSent(t, "thread/start", codex.callsOf("thread/start")[0], want("gpt-5", ""))
	requireSent(t, "turn/start", codex.callsOf("turn/start")[0], want("gpt-5", "low"))
	if cps := h.sessionCheckpoints(); len(cps) != 0 {
		t.Fatalf("session checkpoints = %+v: a session that chose nothing stores nothing", cps)
	}

	restarted := newFakeCodex(t)
	restarted.seed("th-1", &fakeTurn{id: "tu-1", status: "completed", clientID: attempt1})
	h2 := newHarness(t, restarted)
	access := adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": token, "SIDERA_HOME": dataDir}}
	h2.startWith(choicePresets(t, h2.dir), adapterproto.SessionAttach{Endpoint: sessionEP, Preset: "dev", NativeRef: "th-1", ToolAccess: &access})
	requireSent(t, "thread/resume", restarted.callsOf("thread/resume")[0], want("gpt-5", ""))
	h2.deliver(attempt2)
	requireSent(t, "turn/start", restarted.callsOf("turn/start")[0], want("gpt-5", "low"))
}

// What neither the preset nor the requester gave stays absent and is not sent; what
// the requester left out and the preset states is the preset's.
func TestChoiceFillsOnlyWhatTheyStateAndSendsNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name         string
		preset       string
		model        string
		effort       adapterproto.Effort
		start, turn  modelAndEffort
		storedChoice string
	}{
		{"effort only, the preset has a model", "dev", "", adapterproto.EffortHigh,
			want("gpt-5", ""), want("gpt-5", "high"), `{"model":"gpt-5","effort":"high"}`},
		{"model only, the preset has an effort", "dev", "gpt-5", "",
			want("gpt-5", ""), want("gpt-5", "low"), `{"model":"gpt-5","effort":"low"}`},
		{"model only, the preset has no defaults", "bare", "gpt-5", "",
			want("gpt-5", ""), want("gpt-5", ""), `{"model":"gpt-5"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codex := newFakeCodex(t)
			h := newHarness(t, codex)
			h.startWith(choicePresets(t, h.dir))
			if got := h.createChoosing(attempt1, sessionEP, tc.preset, tc.model, tc.effort); got.Outcome != adapterproto.CreateCreated {
				t.Fatalf("result = %+v", got)
			}
			requireSent(t, "thread/start", codex.callsOf("thread/start")[0], tc.start)
			requireSent(t, "turn/start", codex.callsOf("turn/start")[0], tc.turn)
			cps := h.sessionCheckpoints()
			if len(cps) != 1 || string(cps[0].State) != `{"v":1,"choice":`+tc.storedChoice+`}` {
				t.Fatalf("session checkpoints = %+v", cps)
			}
		})
	}
}

// The pair a creation resolved is stored with the attempt in the same write as the
// thread's id, so that no thread exists that a retry could not tell the pair of.
func TestAttemptCheckpointHoldsTheChoiceWithTheThread(t *testing.T) {
	codex := newFakeCodex(t)
	h := newHarness(t, codex)
	h.startWith(choicePresets(t, h.dir))
	if got := h.createChoosing(attempt1, sessionEP, "dev", "gpt-5.1-codex", adapterproto.EffortMedium); got.Outcome != adapterproto.CreateCreated {
		t.Fatalf("result = %+v", got)
	}
	var attemptCPs []string
	for _, c := range h.checkpoints {
		if c.Scope == adapterproto.ScopeAttempt {
			attemptCPs = append(attemptCPs, string(c.State))
		}
	}
	if len(attemptCPs) != 1 || attemptCPs[0] != `{"v":1,"thread_id":"th-1","choice":{"model":"gpt-5.1-codex","effort":"medium"}}` {
		t.Fatalf("attempt checkpoints = %q", attemptCPs)
	}
}

// A creation that is retried after the adapter restarted finds the pair it resolved
// first in the attempt's checkpoint and keeps it, whatever the preset says now: the
// first prompt already ran with that pair, so a narrowed allowlist must not fail the
// attempt and a changed default must not apply from the second turn on. The session
// stores the same pair.
func TestRetriedCreationKeepsTheChoiceItResolvedFirst(t *testing.T) {
	stored := `{"v":1,"thread_id":"th-9","choice":{"model":"gpt-5.1-codex","effort":"medium"}}`
	for _, tc := range []struct {
		name   string
		preset map[string]any
		model  string
		effort adapterproto.Effort
	}{
		{"the allowlist was narrowed", map[string]any{"model": "gpt-5", "effort": "low", "allowed_models": map[string][]string{"gpt-5": {"low"}}},
			"gpt-5.1-codex", adapterproto.EffortMedium},
		{"the default effort was changed", map[string]any{"model": "gpt-5", "effort": "high", "allowed_models": map[string][]string{"gpt-5.1-codex": {"low", "medium", "high"}}},
			"gpt-5.1-codex", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codex := newFakeCodex(t)
			codex.seed("th-9", &fakeTurn{id: "tu-1", status: "completed", clientID: attempt2})
			h := newHarness(t, codex)
			tc.preset["cwd"] = h.dir
			h.startWith([]adapterproto.PresetConfig{{Name: "dev", Config: presetConfig(t, tc.preset)}})
			p := createParams(attempt2, sessionEP, "dev")
			p.Model, p.Effort = tc.model, tc.effort
			p.Checkpoint = json.RawMessage(stored)
			var got adapterproto.SessionCreateResult
			h.mustCall(adapterproto.MethodSessionCreate, p, &got)
			if got.Outcome != adapterproto.CreateCreated || got.NativeRef != "th-9" {
				t.Fatalf("result = %+v", got)
			}
			if n := len(codex.callsOf("thread/archive")); n != 0 {
				t.Fatalf("%d threads archived", n)
			}
			requireSent(t, "thread/resume", codex.callsOf("thread/resume")[0], want("gpt-5.1-codex", ""))
			cps := h.sessionCheckpoints()
			if len(cps) != 1 || string(cps[0].State) != `{"v":1,"choice":{"model":"gpt-5.1-codex","effort":"medium"}}` {
				t.Fatalf("session checkpoints = %+v", cps)
			}
		})
	}
}

// The same attempt with another model or effort is another request under the same
// identifier.
func TestSameAttemptWithAnotherChoiceConflicts(t *testing.T) {
	codex := newFakeCodex(t)
	h := newHarness(t, codex)
	h.startWith(choicePresets(t, h.dir))
	if got := h.createChoosing(attempt1, sessionEP, "dev", "gpt-5", adapterproto.EffortHigh); got.Outcome != adapterproto.CreateCreated {
		t.Fatalf("result = %+v", got)
	}
	for _, tc := range []struct {
		name   string
		model  string
		effort adapterproto.Effort
	}{
		{"another model", "gpt-5.1-codex", adapterproto.EffortHigh},
		{"another effort", "gpt-5", adapterproto.EffortLow},
		{"no choice", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := createParams(attempt1, sessionEP, "dev")
			p.Model, p.Effort = tc.model, tc.effort
			if code := codeOf(t, h.call(adapterproto.MethodSessionCreate, p, &adapterproto.SessionCreateResult{})); code != adapterproto.CodeAttemptConflict {
				t.Fatalf("code %d", code)
			}
		})
	}
	if got := h.createChoosing(attempt1, sessionEP, "dev", "gpt-5", adapterproto.EffortHigh); got.Outcome != adapterproto.CreateCreated {
		t.Fatalf("the same request again: %+v", got)
	}
}

// A choice the preset does not allow is refused without a thread being started.
func TestRefusedChoiceFailsWithoutStartingAThread(t *testing.T) {
	for _, tc := range []struct {
		name   string
		preset string
		model  string
		effort adapterproto.Effort
	}{
		{"model not listed", "dev", "gpt-6", ""},
		{"effort not listed for the model", "dev", "gpt-5.1-codex", adapterproto.EffortLow},
		{"no allowlist", "plain", "gpt-5", ""},
		{"effort alone without a default model it can be checked against", "bare", "", adapterproto.EffortLow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codex := newFakeCodex(t)
			h := newHarness(t, codex)
			h.startWith(choicePresets(t, h.dir))
			got := h.createChoosing(attempt1, sessionEP, tc.preset, tc.model, tc.effort)
			if got.Outcome != adapterproto.CreateFailed || got.Reason != adapterproto.CreateFailModelChoiceNotAllowed {
				t.Fatalf("result = %+v", got)
			}
			if n := len(codex.callsOf("thread/start")); n != 0 {
				t.Fatalf("%d threads started", n)
			}
			if cps := h.sessionCheckpoints(); len(cps) != 0 {
				t.Fatalf("session checkpoints = %+v", cps)
			}
		})
	}
}

// The probe reports each preset's defaults and allowlist, and only for a preset that
// parsed without a problem.
func TestProbeReportsTheChoiceMembersOfEachPreset(t *testing.T) {
	h := newHarness(t, newFakeCodex(t))
	presets := append(choicePresets(t, h.dir), adapterproto.PresetConfig{
		Name: "broken", Config: presetConfig(t, map[string]any{"cwd": h.dir, "model": "gpt-5", "allowed_models": map[string][]string{"gpt-5": {"max"}}}),
	})
	gone := filepath.Join(h.dir, "gone")
	presets = append(presets, adapterproto.PresetConfig{
		Name: "gone", Config: presetConfig(t, map[string]any{"cwd": gone, "model": "gpt-5", "effort": "low", "allowed_models": map[string][]string{"gpt-5": {"low"}}}),
	})
	h.startWith(presets)
	var res adapterproto.ProbeResult
	h.mustCall(adapterproto.MethodAdapterProbe, adapterproto.ProbeParams{ToolProbes: []adapterproto.ToolProbeRequest{}}, &res)
	byName := map[string]adapterproto.PresetStatus{}
	for _, st := range res.Presets {
		byName[st.Name] = st
	}
	dev := byName["dev"]
	if dev.Model != "gpt-5" || dev.Effort != adapterproto.EffortLow || len(dev.AllowedModels) != 2 ||
		dev.AllowedModels[0].Model != "gpt-5" || len(dev.AllowedModels[0].Efforts) != 2 || dev.AllowedModels[1].Model != "gpt-5.1-codex" {
		t.Fatalf("dev = %+v", dev)
	}
	if plain := byName["plain"]; plain.Model != "gpt-5" || plain.Effort != adapterproto.EffortLow || plain.AllowedModels != nil {
		t.Fatalf("plain = %+v", plain)
	}
	if bare := byName["bare"]; bare.Model != "" || bare.Effort != "" || len(bare.AllowedModels) != 1 {
		t.Fatalf("bare = %+v", bare)
	}
	if broken := byName["broken"]; broken.Usable || len(broken.Problems) == 0 || broken.Model != "" || broken.AllowedModels != nil {
		t.Fatalf("broken = %+v: a preset with a problem reports its problems alone", broken)
	}
	if gone := byName["gone"]; gone.Usable || len(gone.Problems) == 0 || gone.Model != "" || gone.Effort != "" || gone.AllowedModels != nil {
		t.Fatalf("gone = %+v: a preset whose directory is missing reports its problem alone", gone)
	}
}

// A session checkpoint that cannot be read leaves the session unreachable, not run on
// values nobody chose.
func TestUnreadableSessionCheckpointLeavesTheSessionUnreachable(t *testing.T) {
	codex := newFakeCodex(t)
	codex.seed("th-1")
	h := newHarness(t, codex)
	access := adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": token, "SIDERA_HOME": dataDir}}
	res := h.startWith(choicePresets(t, h.dir),
		adapterproto.SessionAttach{Endpoint: sessionEP, Preset: "dev", NativeRef: "th-1", ToolAccess: &access, Checkpoint: json.RawMessage(`{"v":9,"choice":{"model":"gpt-5"}}`)},
	)
	if len(res.Sessions) != 1 || res.Sessions[0].Endpoint != sessionEP || res.Sessions[0].State != adapterproto.AttachUnreachable {
		t.Fatalf("start = %+v", res)
	}
	if n := len(codex.callsOf("thread/resume")); n != 0 {
		t.Fatalf("%d resumes: the session was loaded", n)
	}
}
