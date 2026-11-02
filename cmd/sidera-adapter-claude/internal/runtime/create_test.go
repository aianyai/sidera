package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/create"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func mustValid(t *testing.T, m adapterproto.Message) {
	t.Helper()
	if err := m.Validate(); err != nil {
		t.Fatalf("not a valid message: %v", err)
	}
}

// A first turn that started but cannot be identified runs with the prompt, so the
// request is an error and not a failure; asked again, the attempt finds the
// process by the uuid on its command line and starts no second one.
func TestCreateWithAStartedProcessThatCannotBeIdentifiedIsRetriedWithoutASecondStart(t *testing.T) {
	w := newWorld(t)
	w.onLaunch = claudeWrites(5 * time.Millisecond)
	w.setUnidentified(true)
	app, _ := newApp(t, w)

	res, err := app.CreateSession(ctxT(t), createParams(attempt1, prompt1))
	if err == nil || res.Outcome == adapterproto.CreateFailed {
		t.Fatalf("result %+v, err %v: want an error and no failure", res, err)
	}
	w.setUnidentified(false)
	res, err = app.CreateSession(ctxT(t), createParams(attempt1, prompt1))
	if err != nil || res.Outcome != adapterproto.CreateCreated {
		t.Fatalf("retry: %+v %v", res, err)
	}
	if w.launchCount() != 1 {
		t.Fatalf("%d processes started, want the one of the first request", w.launchCount())
	}
}

func TestCreateAStartedSessionAndReportItsTurn(t *testing.T) {
	w := newWorld(t)
	w.onLaunch = claudeWrites(5 * time.Millisecond)
	app, _ := newApp(t, w)

	res, err := app.CreateSession(ctxT(t), createParams(attempt1, prompt1))
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, res)
	if res.Outcome != adapterproto.CreateCreated || bool(res.FirstTurnEnded) || res.Process == nil || !slices.Equal(res.Frontends, frontends) {
		t.Fatalf("result %+v", res)
	}

	// One process, started the way the design says.
	if w.launchCount() != 1 {
		t.Fatalf("%d processes started", w.launchCount())
	}
	l := w.launched()[0]
	session := string(res.NativeRef)
	if sessionOf(l) != session || !slices.Contains(l.Argv, "--session-id") || slices.Contains(l.Argv, "--resume") {
		t.Fatalf("a new session is started with --session-id: %q", l.Argv)
	}
	if l.Argv[0] != claudePath || l.Dir != workDir {
		t.Fatalf("argv[0] %q dir %q", l.Argv[0], l.Dir)
	}
	// The data directory of the tool access is denied to the agent's file tools.
	if !slices.Contains(l.Argv, "Read(/"+toolHome+"/**)") || !slices.Contains(l.Argv, "Edit(/"+toolHome+"/**)") {
		t.Fatalf("the data directory is not denied: %q", l.Argv)
	}
	if in := string(w.file(l.Stdin)); !strings.Contains(in, `"uuid":"`+prompt1+`"`) || !strings.Contains(in, "hello") {
		t.Fatalf("the prompt on standard input: %s", in)
	}
	if filepath.Dir(l.Stdin) != filepath.Join(stateDir, "turns", session) {
		t.Fatalf("turn files in %q", filepath.Dir(l.Stdin))
	}
	env := strings.Join(l.Env, "\n")
	for _, want := range []string{"SIDERA_TOKEN=" + token, "SIDERA_HOME=" + toolHome, "CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=1000", "PATH=" + filepath.Dir(siderPath)} {
		if !strings.Contains(env, want) {
			t.Errorf("the environment lacks %q:\n%s", want, env)
		}
	}
	if strings.Contains(env, "ANTHROPIC_API_KEY") || strings.Contains(env, "leak") {
		t.Errorf("a credential of the adapter reached the process:\n%s", env)
	}

	// The turn starts when the prompt is in the record, after the session's own
	// checkpoint was stored; the process ending ends the turn.
	w.waitEvents("started " + prompt1)
	order := strings.Join(w.orderList(), "|")
	if strings.Index(order, "put session/session-1") > strings.Index(order, "started "+prompt1) || !strings.Contains(order, "put session/session-1") {
		t.Fatalf("the session checkpoint comes before the first turn.started: %s", order)
	}
	w.exit(w.lastProc(), resultOK)
	w.waitEvents("started "+prompt1, "ended "+prompt1+" completed")

	// The attempt's checkpoint says ready and holds no token.
	raw, _ := w.checkpoint("attempt/" + attempt1)
	var cp attemptCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		t.Fatal(err)
	}
	if cp.Stage != int(create.StageReady) || cp.Session != session || cp.RecordDir != recordDir || cp.Cwd != workDir || cp.PID == 0 {
		t.Fatalf("checkpoint %+v", cp)
	}
	if strings.Contains(string(raw), token) {
		t.Fatalf("the checkpoint holds the token: %s", raw)
	}

	// Asked again, the attempt is answered from its state and nothing is started.
	again, err := app.CreateSession(ctxT(t), createParams(attempt1, prompt1))
	if err != nil || again.NativeRef != res.NativeRef || !again.FirstTurnEnded || w.launchCount() != 1 {
		t.Fatalf("repeat: %+v %v, %d launches", again, err, w.launchCount())
	}
}

// The adapter dies after it started the process and before it recorded it. The
// retry finds the process by its command line and starts nothing.
func TestCreateAfterACrashBetweenStartingAndRecording(t *testing.T) {
	for _, processEnded := range []bool{false, true} {
		name := "the process still runs"
		if processEnded {
			name = "the process ended meanwhile"
		}
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.onLaunch = claudeWrites(5 * time.Millisecond)
			app1, _ := newApp(t, w)
			// The put that records the process fails: the adapter is gone, as far as the
			// attempt can tell.
			w.failPutIf = func(scope adapterproto.CheckpointScope, _ string, n int) bool {
				return scope == adapterproto.ScopeAttempt && n >= 2
			}
			if _, err := app1.CreateSession(ctxT(t), createParams(attempt1, prompt1)); err == nil {
				t.Fatal("the creation should have failed with the put")
			}
			intent, _ := w.checkpoint("attempt/" + attempt1)
			if intent == nil || w.launchCount() != 1 || w.aliveCount() != 1 {
				t.Fatalf("intent %s, %d launches, %d alive", intent, w.launchCount(), w.aliveCount())
			}
			w.failPutIf = nil
			session := sessionOf(w.launched()[0])
			if processEnded {
				w.waitRecord(session, prompt1)
				w.exit(w.lastProc(), resultOK)
			}

			// The adapter restarts; the daemon asks again with the checkpoint it holds.
			app2, _ := newApp(t, w)
			p := createParams(attempt1, prompt1)
			p.Checkpoint = intent
			res, err := app2.CreateSession(ctxT(t), p)
			if err != nil {
				t.Fatal(err)
			}
			mustValid(t, res)
			if res.Outcome != adapterproto.CreateCreated || string(res.NativeRef) != session {
				t.Fatalf("result %+v", res)
			}
			if w.launchCount() != 1 {
				t.Fatalf("a second process was started (%d)", w.launchCount())
			}
			if processEnded {
				// The turn is over, and the answer says so; its start and end are reported.
				if !res.FirstTurnEnded || res.Process != nil {
					t.Fatalf("result %+v", res)
				}
				w.waitEvents("started "+prompt1, "ended "+prompt1+" completed")
				return
			}
			if res.FirstTurnEnded || res.Process == nil || res.Process.PID != int64(w.lastProc().pid) {
				t.Fatalf("result %+v", res)
			}
			w.waitEvents("started " + prompt1)
		})
	}
}

func TestCreateFailsBeforeAnythingIsStarted(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*world, *adapterproto.SessionCreateParams)
		want   adapterproto.CreateFailure
	}{
		{"an unknown preset", func(_ *world, p *adapterproto.SessionCreateParams) { p.Preset = "nope" }, adapterproto.CreateFailPresetUnknown},
		{"a working directory that does not exist", func(_ *world, p *adapterproto.SessionCreateParams) { p.Preset = "gone" }, adapterproto.CreateFailPresetInvalid},
		{"a permission mode the runtime does not know", func(_ *world, p *adapterproto.SessionCreateParams) { p.Preset = "bad" }, adapterproto.CreateFailPresetInvalid},
		{"not logged in", func(w *world, _ *adapterproto.SessionCreateParams) { w.setLoggedIn(false) }, adapterproto.CreateFailRuntimeAuth},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			app, _ := newApp(t, w)
			p := createParams(attempt1, prompt1)
			tc.mutate(w, &p)
			res, err := app.CreateSession(ctxT(t), p)
			if err != nil {
				t.Fatal(err)
			}
			mustValid(t, res)
			if res.Outcome != adapterproto.CreateFailed || res.Reason != tc.want || w.launchCount() != 0 || len(w.putKeys()) != 0 {
				t.Fatalf("result %+v, %d launches, puts %v", res, w.launchCount(), w.putKeys())
			}
		})
	}
}

func TestCreateRefusesAnAttemptRepeatedWithOtherParameters(t *testing.T) {
	w := newWorld(t)
	w.onLaunch = claudeWrites(time.Millisecond)
	app, _ := newApp(t, w)
	if _, err := app.CreateSession(ctxT(t), createParams(attempt1, prompt1)); err != nil {
		t.Fatal(err)
	}
	other := createParams(attempt1, prompt1)
	other.FirstPrompt.Text = "something else"
	if _, err := app.CreateSession(ctxT(t), other); err == nil || !strings.Contains(err.Error(), "other parameters") {
		t.Fatalf("err = %v", err)
	}
}
