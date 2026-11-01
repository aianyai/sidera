package adapter_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/codexcfg"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func ended(raw json.RawMessage, final adapterproto.TurnFinal) bool {
	return decode[adapterproto.TurnEndedParams](raw).Final == final
}

// A session is a thread: it is started with the credentials in its own
// configuration, stored with the daemon before anything else is done to it, named
// after the attempt, and the first prompt is its first turn. The answer comes when
// the prompt is in the record, and the turn is reported with the attempt.
func TestCreateSession(t *testing.T) {
	codex := newFakeCodex(t)
	h := newHarness(t, codex)
	res := h.start()
	if len(res.Sessions) != 0 {
		t.Fatalf("start = %+v", res)
	}
	if raw := h.waitNote(adapterproto.MethodAdapterAvailability, nil); string(raw) != `{"conditions":[]}` {
		t.Fatalf("availability = %s", raw)
	}

	got := h.create(attempt1, sessionEP, "dev")
	if got.Outcome != adapterproto.CreateCreated || got.NativeRef != "th-1" || got.FirstTurnEnded {
		t.Fatalf("result = %+v", got)
	}
	if len(got.Frontends) != 1 || got.Frontends[0] != adapterproto.FrontendMCP {
		t.Fatalf("frontends = %v: a sandboxed preset offers the MCP front end only", got.Frontends)
	}

	start := decode[struct {
		Ephemeral             bool
		Config                map[string]map[string]any
		DeveloperInstructions string
	}](codex.callsOf("thread/start")[0].Params)
	if start.DeveloperInstructions != codexcfg.DeveloperInstructions("") {
		t.Errorf("developer instructions = %q", start.DeveloperInstructions)
	}
	if start.Ephemeral {
		t.Error("the thread was started ephemeral")
	}
	env := start.Config["shell_environment_policy"]["set"].(map[string]any)
	mcp := start.Config["mcp_servers"]["sidera"].(map[string]any)
	if env["SIDERA_TOKEN"] != token || mcp["env"].(map[string]any)["SIDERA_HOME"] != "/home/sidera" || mcp["command"] != "/bin/sidera" {
		t.Errorf("thread configuration = %+v", start.Config)
	}
	if name := decode[struct{ Name string }](codex.callsOf("thread/name/set")[0].Params).Name; name != "sidera "+attempt1 {
		t.Errorf("thread name = %q", name)
	}
	if id := decode[struct{ ClientUserMessageID string }](codex.callsOf("turn/start")[0].Params).ClientUserMessageID; id != attempt1 {
		t.Errorf("the prompt's client id = %q, want the first prompt's attempt", id)
	}
	h.mu.Lock()
	cps := h.checkpoints
	h.mu.Unlock()
	if len(cps) != 1 || cps[0].Scope != adapterproto.ScopeAttempt || cps[0].ID != attempt1 || string(cps[0].State) != `{"v":1,"thread_id":"th-1"}` {
		t.Fatalf("checkpoints = %+v: the thread is stored once, without credentials", cps)
	}

	started := decode[adapterproto.TurnStartedParams](h.waitNote(adapterproto.MethodTurnStarted, nil))
	if started.Endpoint != sessionEP || started.Attempt != attempt1 || started.Turn == "" {
		t.Fatalf("turn.started = %+v", started)
	}
	codex.end("th-1", string(started.Turn), "completed")
	h.waitNote(adapterproto.MethodTurnEnded, func(r json.RawMessage) bool { return ended(r, adapterproto.TurnCompleted) })

	// The attempt is idempotent: a repeat gets the answer and starts nothing.
	again := h.create(attempt1, sessionEP, "dev")
	if again.NativeRef != got.NativeRef || len(codex.callsOf("thread/start")) != 1 {
		t.Fatalf("repeat = %+v, %d threads", again, len(codex.callsOf("thread/start")))
	}
	other := createParams(attempt1, sessionEP, "dev")
	other.FirstPrompt.Text = "different"
	if code := codeOf(t, h.call(adapterproto.MethodSessionCreate, other, &adapterproto.SessionCreateResult{})); code != adapterproto.CodeAttemptConflict {
		t.Fatalf("same attempt, other parameters: code %d", code)
	}
}

// An attempt that was stored and whose prompt is already in the thread's record,
// with its turn over, is answered from the record, starts no second turn, and says
// that the turn is over.
// The events of a turn can come well after the answer to its start, while the
// thread's MCP servers start. The creation must not take that for a turn that did
// not start and put the first prompt into the thread a second time.
func TestCreateStartsOneTurnWhileTheEventsOfItAreStillComing(t *testing.T) {
	codex := newFakeCodex(t)
	codex.slowStart = true
	h := newHarness(t, codex)
	h.start()
	got := h.create(attempt1, sessionEP, "dev")
	if got.Outcome != adapterproto.CreateCreated {
		t.Fatalf("result = %+v", got)
	}
	if n := len(codex.callsOf("turn/start")); n != 1 {
		t.Fatalf("%d turn starts, want 1: the first prompt went into the thread twice", n)
	}
}

func TestCreateAfterARestartFindsThePromptInTheRecord(t *testing.T) {
	codex := newFakeCodex(t)
	codex.seed("th-9", &fakeTurn{id: "tu-1", status: "completed", clientID: attempt2})
	h := newHarness(t, codex)
	h.start()
	p := createParams(attempt2, sessionEP, "dev")
	p.Checkpoint = json.RawMessage(`{"v":1,"thread_id":"th-9"}`)
	var got adapterproto.SessionCreateResult
	h.mustCall(adapterproto.MethodSessionCreate, p, &got)
	if got.Outcome != adapterproto.CreateCreated || got.NativeRef != "th-9" || !got.FirstTurnEnded {
		t.Fatalf("result = %+v", got)
	}
	if n := len(codex.callsOf("turn/start")) + len(codex.callsOf("thread/start")); n != 0 {
		t.Fatalf("%d starts: the prompt was already delivered", n)
	}
	resumes := codex.callsOf("thread/resume")
	if len(resumes) != 1 {
		t.Fatal("the stored thread was not resumed")
	}
	if di := decode[struct{ DeveloperInstructions string }](resumes[0].Params).DeveloperInstructions; di != codexcfg.DeveloperInstructions("") {
		t.Fatalf("developer instructions of the resume = %q", di)
	}
}

// A stored thread the server no longer has was never written: the attempt starts
// over with a new thread and stores that one.
func TestCreateStartsOverWhenTheStoredThreadIsGone(t *testing.T) {
	codex := newFakeCodex(t)
	h := newHarness(t, codex)
	h.start()
	p := createParams(attempt2, sessionEP, "dev")
	p.Checkpoint = json.RawMessage(`{"v":1,"thread_id":"th-lost"}`)
	var got adapterproto.SessionCreateResult
	h.mustCall(adapterproto.MethodSessionCreate, p, &got)
	if got.Outcome != adapterproto.CreateCreated || got.NativeRef != "th-1" {
		t.Fatalf("result = %+v", got)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := len(h.checkpoints); n != 1 || string(h.checkpoints[0].State) != `{"v":1,"thread_id":"th-1"}` {
		t.Fatalf("checkpoints = %+v", h.checkpoints)
	}
}

func TestCreateRefusals(t *testing.T) {
	t.Run("preset", func(t *testing.T) {
		h := newHarness(t, newFakeCodex(t))
		h.start()
		if got := h.create(attempt1, sessionEP, "nope"); got.Outcome != adapterproto.CreateFailed || got.Reason != adapterproto.CreateFailPresetUnknown {
			t.Fatalf("unknown preset: %+v", got)
		}
	})
	t.Run("account", func(t *testing.T) {
		codex := newFakeCodex(t)
		codex.signedIn = false
		h := newHarness(t, codex)
		h.start()
		if got := h.create(attempt1, sessionEP, "dev"); got.Reason != adapterproto.CreateFailRuntimeAuth {
			t.Fatalf("no account: %+v", got)
		}
		raw := h.waitNote(adapterproto.MethodAdapterAvailability, nil)
		if c := decode[adapterproto.AvailabilityParams](raw).Conditions; len(c) != 1 || c[0].Condition != adapterproto.ConditionRuntimeAuth {
			t.Fatalf("availability = %s", raw)
		}
		if len(codex.callsOf("thread/start")) != 0 {
			t.Fatal("a thread was started without an account")
		}
	})
	t.Run("server", func(t *testing.T) {
		codex := newFakeCodex(t)
		h := newHarness(t, codex)
		h.mgr.err = context.DeadlineExceeded
		res := h.start(adapterproto.SessionAttach{
			Endpoint: sessionEP, Preset: "dev", NativeRef: "th-1", ToolAccess: adapterproto.ToolAccess{Env: map[string]string{}},
		})
		if len(res.Sessions) != 1 || res.Sessions[0].State != adapterproto.AttachUnreachable {
			t.Fatalf("start = %+v", res)
		}
		if got := h.create(attempt1, otherEP, "dev"); got.Reason != adapterproto.CreateFailNativeUnavailable {
			t.Fatalf("no server: %+v", got)
		}
		raw := h.waitNote(adapterproto.MethodAdapterAvailability, nil)
		if c := decode[adapterproto.AvailabilityParams](raw).Conditions; len(c) != 1 || c[0].Condition != adapterproto.ConditionNativeUnavailable {
			t.Fatalf("availability = %s", raw)
		}
	})
}

// adapter.start resumes each session with its credentials: an attached one reports
// whether a turn runs, one whose record is gone is lost.
func TestStartAttachesSessions(t *testing.T) {
	codex := newFakeCodex(t)
	codex.seed("th-1", &fakeTurn{id: "tu-1", status: "inProgress", clientID: attempt1})
	h := newHarness(t, codex)
	access := adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": token, "SIDERA_HOME": "/home/sidera"}}
	res := h.start(
		adapterproto.SessionAttach{Endpoint: sessionEP, Preset: "dev", NativeRef: "th-1", ToolAccess: access},
		adapterproto.SessionAttach{Endpoint: otherEP, Preset: "dev", NativeRef: "th-gone", ToolAccess: access},
	)
	if len(res.Sessions) != 2 ||
		res.Sessions[0].State != adapterproto.AttachAttached || !res.Sessions[0].TurnActive ||
		res.Sessions[1].State != adapterproto.AttachLost {
		t.Fatalf("start = %+v", res)
	}
	var cfg struct {
		Config                map[string]map[string]any
		DeveloperInstructions string
	}
	for _, c := range codex.callsOf("thread/resume") {
		if decode[struct{ ThreadID string }](c.Params).ThreadID == "th-1" {
			cfg = decode[struct {
				Config                map[string]map[string]any
				DeveloperInstructions string
			}](c.Params)
		}
	}
	if cfg.DeveloperInstructions != codexcfg.DeveloperInstructions("") {
		t.Fatalf("resume developer instructions = %q: the server keeps none, so they are sent again", cfg.DeveloperInstructions)
	}
	if cfg.Config["shell_environment_policy"]["set"].(map[string]any)["SIDERA_TOKEN"] != token {
		t.Fatalf("resume configuration = %+v: the server keeps none, so it is sent again", cfg)
	}
	// The running turn ends: it is reported although the adapter never saw it start.
	codex.end("th-1", "tu-1", "completed")
	h.waitNote(adapterproto.MethodTurnEnded, func(r json.RawMessage) bool { return decode[adapterproto.TurnEndedParams](r).Turn == "tu-1" })
}

// A delivered prompt is accepted, and reported persisted when it reaches the record;
// a session that runs a turn refuses another prompt; an attempt asked again is
// answered again.
func TestDeliverPrompt(t *testing.T) {
	codex := newFakeCodex(t)
	h := newHarness(t, codex)
	h.start()
	h.create(attempt1, sessionEP, "dev")
	first := decode[adapterproto.TurnStartedParams](h.waitNote(adapterproto.MethodTurnStarted, nil))

	busy := adapterproto.PromptDeliverParams{Endpoint: sessionEP, Attempt: attempt2, Text: "next"}
	var res adapterproto.PromptDeliverResult
	h.mustCall(adapterproto.MethodPromptDeliver, busy, &res)
	if res.Stage != adapterproto.PromptRefused || res.Reason != adapterproto.RefusalBusy {
		t.Fatalf("while a turn runs: %+v", res)
	}
	codex.end("th-1", string(first.Turn), "completed")
	h.waitNote(adapterproto.MethodTurnEnded, nil)

	codex.mu.Lock()
	codex.lateItem = true
	codex.mu.Unlock()
	h.mustCall(adapterproto.MethodPromptDeliver, busy, &res)
	if res.Stage != adapterproto.PromptAccepted {
		t.Fatalf("idle session: %+v", res)
	}
	h.waitNote(adapterproto.MethodPromptPersisted, func(r json.RawMessage) bool {
		return decode[adapterproto.PromptPersistedParams](r).Attempt == attempt2
	})
	started := decode[adapterproto.TurnStartedParams](h.waitNote(adapterproto.MethodTurnStarted, func(r json.RawMessage) bool {
		return decode[adapterproto.TurnStartedParams](r).Attempt == attempt2
	}))
	h.mustCall(adapterproto.MethodPromptDeliver, busy, &res)
	if res.Stage != adapterproto.PromptPersisted {
		t.Fatalf("the same attempt after it was persisted: %+v", res)
	}
	changed := busy
	changed.Text = "other text"
	if code := codeOf(t, h.call(adapterproto.MethodPromptDeliver, changed, &res)); code != adapterproto.CodeAttemptConflict {
		t.Fatalf("same attempt, other text: code %d", code)
	}
	if code := codeOf(t, h.call(adapterproto.MethodPromptDeliver, adapterproto.PromptDeliverParams{Endpoint: otherEP, Attempt: attempt3, Text: "x"}, &res)); code != adapterproto.CodeUnknownEndpoint {
		t.Fatalf("unknown endpoint: code %d", code)
	}

	// Reconcile: the delivered attempt is found, an attempt never made is absent
	// once the turn is over and the record has been read to its first turn.
	var rec adapterproto.ReconcileResult
	h.mustCall(adapterproto.MethodPromptReconcile, adapterproto.ReconcileParams{Endpoint: sessionEP, Attempt: attempt2}, &rec)
	if rec.Verdict != adapterproto.VerdictFound || rec.Turn == nil || rec.Turn.State != adapterproto.TurnStateRunning {
		t.Fatalf("reconcile of a running turn: %+v", rec)
	}
	codex.end("th-1", string(started.Turn), "completed")
	h.waitNote(adapterproto.MethodTurnEnded, func(r json.RawMessage) bool { return decode[adapterproto.TurnEndedParams](r).Turn == started.Turn })
	h.mustCall(adapterproto.MethodPromptReconcile, adapterproto.ReconcileParams{Endpoint: sessionEP, Attempt: attempt3}, &rec)
	if rec.Verdict != adapterproto.VerdictProvablyAbsent {
		t.Fatalf("reconcile of an attempt never made: %+v", rec)
	}

	// A record in a form the adapter does not read proves nothing: even the
	// delivered attempt is not found, and the answer must not be absence.
	codex.mu.Lock()
	codex.otherRecordForm = true
	codex.mu.Unlock()
	h.mustCall(adapterproto.MethodPromptReconcile, adapterproto.ReconcileParams{Endpoint: sessionEP, Attempt: attempt2}, &rec)
	if rec.Verdict != adapterproto.VerdictIndeterminate || rec.Grounds.Code != adapterproto.GroundsRecordUnreadable {
		t.Fatalf("reconcile over a record in another form: %+v", rec)
	}
}

// After the connection to the App Server ends, the adapter says so, reaches the
// server again, resumes the sessions with their credentials and reports the turn
// that ended while it was away.
func TestReconnect(t *testing.T) {
	codex := newFakeCodex(t)
	h := newHarness(t, codex)
	h.start()
	h.create(attempt1, sessionEP, "dev")
	first := decode[adapterproto.TurnStartedParams](h.waitNote(adapterproto.MethodTurnStarted, nil))
	resumes := len(codex.callsOf("thread/resume"))

	codex.srv.DropConnections()
	codex.endQuietly("th-1", string(first.Turn), "interrupted")

	h.waitNote(adapterproto.MethodAdapterAvailability, func(r json.RawMessage) bool {
		c := decode[adapterproto.AvailabilityParams](r).Conditions
		return len(c) == 1 && c[0].Condition == adapterproto.ConditionNativeUnavailable
	})
	h.waitNote(adapterproto.MethodTurnEnded, func(r json.RawMessage) bool { return ended(r, adapterproto.TurnInterrupted) })
	if got := codex.callsOf("thread/resume"); len(got) != resumes+1 || !strings.Contains(string(got[resumes].Params), token) {
		t.Fatalf("resumes = %d, want one more with the credentials", len(got))
	}
	// The snapshot is empty again.
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw := h.lastAvailability()
		if len(decode[adapterproto.AvailabilityParams](raw).Conditions) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("availability never cleared: %s", raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Requests of the server are answered from the preset at once and reported.
func TestServerRequests(t *testing.T) {
	codex := newFakeCodex(t)
	h := newHarness(t, codex)
	h.start()
	denies := string(h.create(attempt1, sessionEP, "dev").NativeRef)
	approves := string(h.create(attempt2, otherEP, "yolo").NativeRef)
	ask := func(thread, method string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		raw, err := codex.srv.Request(ctx, method, map[string]string{"threadId": thread})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if got := ask(denies, "item/commandExecution/requestApproval"); !strings.Contains(got, "decline") {
		t.Errorf("dev preset: %s", got)
	}
	if got := ask(approves, "item/commandExecution/requestApproval"); !strings.Contains(got, "accept") {
		t.Errorf("yolo preset: %s", got)
	}
	ask(denies, "item/tool/requestUserInput")
	handled := func(ep, method string, disp adapterproto.Disposition) {
		h.waitNote(adapterproto.MethodNativeRequestHandled, func(r json.RawMessage) bool {
			p := decode[adapterproto.NativeRequestHandledParams](r)
			return string(p.Endpoint) == ep && p.Method == method && p.Disposition == disp
		})
	}
	handled(sessionEP, "item/commandExecution/requestApproval", adapterproto.DispositionRejectedByPreset)
	handled(otherEP, "item/commandExecution/requestApproval", adapterproto.DispositionPreset)
	handled(sessionEP, "item/tool/requestUserInput", adapterproto.DispositionRejectedByPreset)
}

// A session is closed by unloading and archiving its thread; one that runs a turn
// is not closed yet; a thread that is gone is as closed as one that was archived.
func TestCloseSession(t *testing.T) {
	codex := newFakeCodex(t)
	h := newHarness(t, codex)
	h.start()
	h.create(attempt1, sessionEP, "dev")
	first := decode[adapterproto.TurnStartedParams](h.waitNote(adapterproto.MethodTurnStarted, nil))

	var res adapterproto.CloseResult
	h.mustCall(adapterproto.MethodSessionClose, adapterproto.CloseParams{Endpoint: sessionEP}, &res)
	if res.Outcome != adapterproto.CloseUnconfirmed || res.Reason != adapterproto.CloseTurnStillRunning {
		t.Fatalf("with a turn running: %+v", res)
	}
	codex.end("th-1", string(first.Turn), "completed")
	h.waitNote(adapterproto.MethodTurnEnded, nil)
	h.mustCall(adapterproto.MethodSessionClose, adapterproto.CloseParams{Endpoint: sessionEP}, &res)
	if res.Outcome != adapterproto.CloseClosed || len(codex.callsOf("thread/archive")) != 1 {
		t.Fatalf("idle: %+v", res)
	}
	// Closed again, now by reference, the adapter no longer holds the endpoint.
	h.mustCall(adapterproto.MethodSessionClose, adapterproto.CloseParams{Endpoint: sessionEP, NativeRef: "th-1"}, &res)
	if res.Outcome != adapterproto.CloseClosed || len(codex.callsOf("thread/archive")) != 2 {
		t.Fatalf("by reference: %+v", res)
	}
	h.mustCall(adapterproto.MethodSessionClose, adapterproto.CloseParams{Endpoint: sessionEP, NativeRef: "th-gone"}, &res)
	if res.Outcome != adapterproto.CloseClosed {
		t.Fatalf("a thread that is gone: %+v", res)
	}
	if code := codeOf(t, h.call(adapterproto.MethodSessionClose, adapterproto.CloseParams{Endpoint: otherEP}, &res)); code != adapterproto.CodeUnknownEndpoint {
		t.Fatalf("unknown endpoint without a reference: code %d", code)
	}
}
