package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime/proc"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/turn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// openSession creates a session and lets its first turn end, so that the session
// is idle. It returns the session uuid.
func openSession(t *testing.T, w *world, app *App) string {
	t.Helper()
	w.onLaunch = claudeWrites(2 * time.Millisecond)
	res, err := app.CreateSession(ctxT(t), createParams(attempt1, prompt1))
	if err != nil || res.Outcome != adapterproto.CreateCreated {
		t.Fatalf("create: %+v %v", res, err)
	}
	w.waitEvents("started " + prompt1)
	w.exit(w.lastProc(), resultOK)
	w.waitEvents("started "+prompt1, "ended "+prompt1+" completed")
	return string(res.NativeRef)
}

func TestDeliverAPromptToAnIdleSession(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	session := openSession(t, w, app)
	p := adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: attempt2, Text: "more"}

	res, err := app.DeliverPrompt(ctxT(t), p)
	if err != nil || res.Stage != adapterproto.PromptAccepted {
		t.Fatalf("deliver: %+v %v", res, err)
	}
	mustValid(t, res)
	if w.launchCount() != 2 {
		t.Fatalf("%d processes started", w.launchCount())
	}
	l := w.launched()[1]
	if !slices.Contains(l.Argv, "--resume") || sessionOf(l) != session || !strings.Contains(string(w.file(l.Stdin)), "more") {
		t.Fatalf("a prompt resumes the session: %q", l.Argv)
	}
	// The dispatch is on record before the process: "no checkpoint" proves nothing was started.
	order := strings.Join(w.orderList(), "|")
	if i := strings.Index(order, "put attempt/"+attempt2); i < 0 || i > strings.LastIndex(order, "put session/session-1") {
		t.Fatalf("the attempt's checkpoint comes first: %s", order)
	}

	// The same attempt again, while it is in flight, is answered the same and starts nothing.
	again, err := app.DeliverPrompt(ctxT(t), p)
	if err != nil || again.Stage != adapterproto.PromptAccepted || w.launchCount() != 2 {
		t.Fatalf("repeat: %+v %v, %d launches", again, err, w.launchCount())
	}
	// With another text it is a conflict.
	other := p
	other.Text = "different"
	if _, err := app.DeliverPrompt(ctxT(t), other); err == nil || !strings.Contains(err.Error(), "another text") {
		t.Fatalf("err = %v", err)
	}
	// Another prompt while the turn runs is refused as busy.
	busy, err := app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: prompt2, Text: "x"})
	if err != nil || busy.Stage != adapterproto.PromptRefused || busy.Reason != adapterproto.RefusalBusy {
		t.Fatalf("busy: %+v %v", busy, err)
	}

	// The prompt reaching the record is announced, and the turn runs its course.
	w.appendRecord(filepath.Join(recordDir, session+".jsonl"), userLine(session, attempt2))
	w.waitEvents("started "+prompt1, "ended "+prompt1+" completed", "persisted "+attempt2, "started "+attempt2)
	w.exit(w.lastProc(), resultError)
	w.waitEvents("started "+prompt1, "ended "+prompt1+" completed", "persisted "+attempt2, "started "+attempt2, "ended "+attempt2+" failed")

	// Delivered and in the record: asked again, it is persisted and nothing starts.
	done, err := app.DeliverPrompt(ctxT(t), p)
	if err != nil || done.Stage != adapterproto.PromptPersisted || w.launchCount() != 2 {
		t.Fatalf("after the turn: %+v %v", done, err)
	}
}

// A process table that cannot say whether the recorded turn process runs says
// nothing about whether it does: every path that would start a process of a
// session answers with an error (or the session unreachable) and starts nothing,
// because a process started beside one that runs is a second writer.
func TestARecordedProcessThatCannotBeLookedAtNeverGetsASecondWriter(t *testing.T) {
	lookupFailure := errors.New("the process table did not answer")

	t.Run("a prompt delivered to a session", func(t *testing.T) {
		w := newWorld(t)
		app, _ := newApp(t, w)
		openSession(t, w, app)
		if res, err := app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: attempt2, Text: "more"}); err != nil || res.Stage != adapterproto.PromptAccepted {
			t.Fatalf("deliver: %+v %v", res, err)
		}
		started := w.launchCount()
		w.setLookupErr(lookupFailure)
		res, err := app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: prompt2, Text: "again"})
		if err == nil || res.Stage == adapterproto.PromptAccepted || w.launchCount() != started {
			t.Fatalf("deliver: %+v, %v, %d processes started", res, err, w.launchCount()-started)
		}
	})

	t.Run("a creation repeated after the process was recorded", func(t *testing.T) {
		w := newWorld(t)
		w.onLaunch = claudeWrites(5 * time.Millisecond)
		app1, _ := newApp(t, w)
		// The adapter is gone after the process was recorded: the third put fails.
		w.failPutIf = func(scope adapterproto.CheckpointScope, _ string, n int) bool {
			return scope == adapterproto.ScopeAttempt && n >= 3
		}
		if _, err := app1.CreateSession(ctxT(t), createParams(attempt1, prompt1)); err == nil {
			t.Fatal("the creation should have failed with the put")
		}
		w.failPutIf = nil
		recorded, _ := w.checkpoint("attempt/" + attempt1)
		if recorded == nil || w.launchCount() != 1 || w.aliveCount() != 1 {
			t.Fatalf("checkpoint %s, %d launches, %d alive", recorded, w.launchCount(), w.aliveCount())
		}

		app2, _ := newApp(t, w)
		w.setLookupErr(lookupFailure)
		p := createParams(attempt1, prompt1)
		p.Checkpoint = recorded
		res, err := app2.CreateSession(ctxT(t), p)
		if err == nil || res.Outcome == adapterproto.CreateCreated || w.launchCount() != 1 {
			t.Fatalf("create: %+v, %v, %d launches", res, err, w.launchCount())
		}
	})

	t.Run("a session taken back at start", func(t *testing.T) {
		const session = "6ee62271-42c1-4558-89cf-0e32c50e0c1b"
		w := newWorld(t)
		w.appendRecord(filepath.Join(recordDir, session+".jsonl"), userLine(session, prompt1))
		started, _ := w.Start(proc.Launch{Argv: []string{claudePath, "-p", "--resume", session}, Stdout: outFile(session, prompt1)})
		raw, _ := json.Marshal(sessionCheckpoint{V: checkpointVersion, Session: session, Cwd: workDir, RecordDir: recordDir, Turn: &turn.State{
			Attempt: prompt1, StartedReported: true, Process: turn.Process{PID: started.PID, StartUnix: started.StartUnix, StartKey: started.StartKey},
		}})
		w.setLookupErr(lookupFailure)
		_, res := newApp(t, w, adapterproto.SessionAttach{
			Endpoint: "session-1", Preset: "dev", NativeRef: session, Checkpoint: raw, ToolAccess: &adapterproto.ToolAccess{Env: map[string]string{}},
		})
		if got := res.Sessions[0]; got.State != adapterproto.AttachUnreachable || w.launchCount() != 1 {
			t.Fatalf("session state %+v, %d launches", got, w.launchCount())
		}
	})
}

// A start that cannot outlive the daemon is a warning where an operator reads it,
// at the start and in the probe; a caveat alone is only information. The turn is
// started all the same.
func TestATurnThatEndsWithTheDaemonIsLogged(t *testing.T) {
	var logs bytes.Buffer
	w := newWorld(t)
	app, _ := newApp(t, w)
	app.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	w.locked(func() { w.detachment = proc.Detachment{Caveat: "stays in the adapter's job"} })

	openSession(t, w, app)
	if _, err := app.Probe(ctxT(t), adapterproto.ProbeParams{ToolProbes: []adapterproto.ToolProbeRequest{}}); err != nil {
		t.Fatal(err)
	}
	warns := strings.Count(logs.String(), "level=WARN msg=\"a turn process ends with the adapter and the daemon\"")
	if warns != 2 || !strings.Contains(logs.String(), "stays in the adapter's job") {
		t.Fatalf("want a warning at the start and in the probe, with the caveat:\n%s", logs.String())
	}

	logs.Reset()
	w.locked(func() { w.detachment = proc.Detachment{OutlivesDaemon: true, Caveat: "no linger"} })
	if _, err := app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: attempt2, Text: "more"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "level=INFO msg=\"how a turn process is detached\"") {
		t.Fatalf("a caveat of a start that outlives the daemon is information:\n%s", logs.String())
	}
}

// A running turn is looked at every poll; its checkpoint is stored when the
// turn changes and not at every look.
func TestRunningTurnIsCheckpointedOnlyWhenItChanges(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	session := openSession(t, w, app)
	sessionPuts := func() int {
		return strings.Count(strings.Join(w.putKeys(), "|"), "session/session-1")
	}

	if res, err := app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: attempt2, Text: "more"}); err != nil || res.Stage != adapterproto.PromptAccepted {
		t.Fatalf("deliver: %+v %v", res, err)
	}
	w.appendRecord(filepath.Join(recordDir, session+".jsonl"), userLine(session, attempt2))
	w.waitEvents("started "+prompt1, "ended "+prompt1+" completed", "persisted "+attempt2, "started "+attempt2)
	// Let the monitor store the start, then let many polls pass over an unchanged turn.
	time.Sleep(20 * time.Millisecond)
	before := sessionPuts()
	time.Sleep(100 * time.Millisecond)
	if after := sessionPuts(); after != before {
		t.Fatalf("an unchanged turn was stored %d more times", after-before)
	}

	w.exit(w.lastProc(), resultOK)
	w.waitEvents("started "+prompt1, "ended "+prompt1+" completed", "persisted "+attempt2, "started "+attempt2, "ended "+attempt2+" completed")
	time.Sleep(20 * time.Millisecond)
	if sessionPuts() <= before {
		t.Fatal("the end of the turn was not stored")
	}
}

// A process that started but whose identity cannot be read runs with the prompt:
// the outcome is unknown, so it is an error for the daemon to reconcile and not a
// refusal that says nothing happened.
func TestDeliverOfAStartedProcessThatCannotBeIdentifiedIsAnError(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	openSession(t, w, app)
	w.setUnidentified(true)

	res, err := app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: attempt2, Text: "more"})
	if err == nil || res != (adapterproto.PromptDeliverResult{}) {
		t.Fatalf("result %+v, err %v: want an error and no answer", res, err)
	}
	if w.aliveCount() != 1 {
		t.Fatalf("%d processes run, want the one that was started", w.aliveCount())
	}
	// The process is the session's writer; a prompt now is busy, not a second start.
	again, err := app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: prompt2, Text: "x"})
	if err != nil || again.Reason != adapterproto.RefusalBusy || w.launchCount() != 2 {
		t.Fatalf("again: %+v %v, %d launches", again, err, w.launchCount())
	}
}

func TestDeliverRefusals(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	session := openSession(t, w, app)

	if _, err := app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "nobody", Attempt: attempt2, Text: "x"}); err == nil {
		t.Fatal("an endpoint the adapter does not hold is an error")
	}
	// Two processes of the session: the writer is not one, and nothing is started.
	for i := 0; i < 2; i++ {
		_, _ = w.Start(proc.Launch{Argv: []string{claudePath, "-p", "--resume", session}, Stdout: "x"})
	}
	res, err := app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: attempt2, Text: "x"})
	if err != nil || res.Reason != adapterproto.RefusalControlUnknown {
		t.Fatalf("two writers: %+v %v", res, err)
	}
	// The record is gone.
	w.removeFile(filepath.Join(recordDir, session+".jsonl"))
	res, err = app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: attempt2, Text: "x"})
	if err != nil || res.Reason != adapterproto.RefusalSessionLost {
		t.Fatalf("lost: %+v %v", res, err)
	}
}

func TestCloseInterruptsARunningTurnAndForgetsTheSession(t *testing.T) {
	w := newWorld(t)
	w.onInterrupt = func(w *world, p *fakeProc) { w.exit(p, "") }
	app, _ := newApp(t, w)
	w.onLaunch = claudeWrites(time.Millisecond)
	if _, err := app.CreateSession(ctxT(t), createParams(attempt1, prompt1)); err != nil {
		t.Fatal(err)
	}
	w.waitEvents("started " + prompt1)

	res, err := app.CloseSession(ctxT(t), adapterproto.CloseParams{Endpoint: "session-1"})
	if err != nil || res.Outcome != adapterproto.CloseClosed {
		t.Fatalf("close: %+v %v", res, err)
	}
	if signals := w.signalled(); len(signals) != 1 {
		t.Fatalf("signals %v", signals)
	}
	w.waitEvents("started "+prompt1, "ended "+prompt1+" interrupted")
	if _, err := app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: attempt2, Text: "x"}); err == nil {
		t.Fatal("a closed session is forgotten")
	}
	// Closing again by the native reference finds nothing running and is closed.
	res, err = app.CloseSession(ctxT(t), adapterproto.CloseParams{Endpoint: "session-1", NativeRef: "whatever"})
	if err != nil || res.Outcome != adapterproto.CloseClosed {
		t.Fatalf("by reference: %+v %v", res, err)
	}
}

func TestCloseDoesNotKillATurnThatDoesNotEnd(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	app.o.CloseWait = 30 * time.Millisecond
	w.onLaunch = claudeWrites(time.Millisecond)
	if _, err := app.CreateSession(ctxT(t), createParams(attempt1, prompt1)); err != nil {
		t.Fatal(err)
	}
	res, err := app.CloseSession(ctxT(t), adapterproto.CloseParams{Endpoint: "session-1"})
	if err != nil || res.Outcome != adapterproto.CloseUnconfirmed || res.Reason != adapterproto.CloseTurnStillRunning {
		t.Fatalf("close: %+v %v", res, err)
	}
	if signals := w.signalled(); len(signals) != 1 || w.aliveCount() != 1 {
		t.Fatalf("signals %v, alive %d: the adapter interrupts and never kills", signals, w.aliveCount())
	}
	if app.session("session-1") == nil {
		t.Fatal("a session that was not closed is still held")
	}
}

func TestCloseAfterARestartFindsTheProcessByTheNativeReference(t *testing.T) {
	w := newWorld(t)
	w.onInterrupt = func(w *world, p *fakeProc) { w.exit(p, "") }
	app, _ := newApp(t, w)
	const session = "6ee62271-42c1-4558-89cf-0e32c50e0c1b"
	w.Start(proc.Launch{Argv: []string{claudePath, "-p", "--resume", session}, Stdout: "x"})
	res, err := app.CloseSession(ctxT(t), adapterproto.CloseParams{Endpoint: "gone", NativeRef: session})
	if signals := w.signalled(); err != nil || res.Outcome != adapterproto.CloseClosed || len(signals) != 1 {
		t.Fatalf("close: %+v %v, signals %v", res, err, signals)
	}
	if _, err := app.CloseSession(ctxT(t), adapterproto.CloseParams{Endpoint: "gone"}); err == nil {
		t.Fatal("an endpoint the adapter does not hold, with no reference, is unknown")
	}
}

func TestReconcile(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	session := openSession(t, w, app)
	w.appendRecord(filepath.Join(recordDir, session+".jsonl"), userLine(session, attempt2))

	tests := []struct {
		name       string
		attempt    string
		checkpoint json.RawMessage
		verdict    adapterproto.ReconcileVerdict
		grounds    adapterproto.GroundsCode
		turn       adapterproto.TurnState
	}{
		{"the first prompt's turn completed", prompt1, nil, adapterproto.VerdictFound, adapterproto.GroundsAttemptInRecord, adapterproto.TurnStateCompleted},
		{"a prompt in the record whose output is gone failed", attempt2, nil, adapterproto.VerdictFound, adapterproto.GroundsAttemptInRecord, adapterproto.TurnStateFailed},
		{"never dispatched", prompt2, nil, adapterproto.VerdictProvablyAbsent, adapterproto.GroundsNeverDispatched, ""},
		{"dispatched and not in the record, nothing running", prompt2, json.RawMessage(`{"v":1,"stage":"dispatching"}`), adapterproto.VerdictProvablyAbsent, adapterproto.GroundsRecordCompleteWithoutAttempt, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := app.Reconcile(ctxT(t), adapterproto.ReconcileParams{Endpoint: "session-1", Attempt: adapterproto.AttemptID(tc.attempt), Checkpoint: tc.checkpoint})
			if err != nil {
				t.Fatal(err)
			}
			mustValid(t, res)
			if res.Verdict != tc.verdict || res.Grounds.Code != tc.grounds || (res.Turn != nil && res.Turn.State != tc.turn) {
				t.Fatalf("result %+v turn %+v", res, res.Turn)
			}
		})
	}
	// A process of the session that runs may still write the prompt.
	_, _ = w.Start(proc.Launch{Argv: []string{claudePath, "-p", "--resume", session}, Stdout: "x"})
	res, err := app.Reconcile(ctxT(t), adapterproto.ReconcileParams{Endpoint: "session-1", Attempt: prompt2, Checkpoint: json.RawMessage(`{"v":1}`)})
	if err != nil || res.Verdict != adapterproto.VerdictIndeterminate || res.Grounds.Code != adapterproto.GroundsTurnRunning {
		t.Fatalf("running: %+v %v", res, err)
	}
}

// The process table is read before the session file. A process that writes the
// prompt and ends between the two reads would otherwise be seen as gone while the
// file read before it lacks the prompt, and the prompt would be called absent.
func TestReconcileReadsTheProcessTableBeforeTheRecord(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	session := openSession(t, w, app)
	file := filepath.Join(recordDir, session+".jsonl")
	_, _ = w.Start(proc.Launch{Argv: []string{claudePath, "-p", "--resume", session}, Stdout: "x"})
	writer := w.lastProc()

	var once sync.Once
	w.mu.Lock()
	w.afterOpen = func(w *world, path string) {
		if path != file {
			return
		}
		once.Do(func() {
			w.appendRecord(file, userLine(session, prompt2))
			w.mu.Lock()
			writer.alive = false
			w.mu.Unlock()
		})
	}
	w.mu.Unlock()

	res, err := app.Reconcile(ctxT(t), adapterproto.ReconcileParams{Endpoint: "session-1", Attempt: prompt2, Checkpoint: json.RawMessage(`{"v":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, res)
	if res.Verdict != adapterproto.VerdictIndeterminate || res.Grounds.Code != adapterproto.GroundsTurnRunning {
		t.Fatalf("verdict %s grounds %s, want a turn that was running when the table was read", res.Verdict, res.Grounds.Code)
	}
}

// The turn process the adapter recorded is a running writer whatever its command
// line says: a claude found at another path is not matched by the table search.
func TestReconcileCountsTheRecordedTurnProcessAsRunning(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	openSession(t, w, app)
	if res, err := app.DeliverPrompt(ctxT(t), adapterproto.PromptDeliverParams{Endpoint: "session-1", Attempt: attempt2, Text: "more"}); err != nil || res.Stage != adapterproto.PromptAccepted {
		t.Fatalf("deliver: %+v %v", res, err)
	}
	p := w.lastProc()
	w.mu.Lock()
	p.args = append([]string{"/elsewhere/claude"}, p.args[1:]...)
	w.mu.Unlock()

	res, err := app.Reconcile(ctxT(t), adapterproto.ReconcileParams{Endpoint: "session-1", Attempt: prompt2, Checkpoint: json.RawMessage(`{"v":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, res)
	if res.Verdict != adapterproto.VerdictIndeterminate || res.Grounds.Code != adapterproto.GroundsTurnRunning {
		t.Fatalf("verdict %s grounds %s, want turn_running", res.Verdict, res.Grounds.Code)
	}
	w.exit(p, resultOK)
}

// An attempt is called absent only from a record whose form the lookup reads. A
// Claude Code release that wrote its user records differently would otherwise
// show every prompt absent and the daemon would send each one again.
func TestReconcileDoesNotConcludeAbsenceFromARecordFormItDoesNotRead(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	session := openSession(t, w, app)
	file := filepath.Join(recordDir, session+".jsonl")
	reconcile := func() adapterproto.ReconcileResult {
		t.Helper()
		res, err := app.Reconcile(ctxT(t), adapterproto.ReconcileParams{Endpoint: "session-1", Attempt: prompt2, Checkpoint: json.RawMessage(`{"v":1,"stage":"dispatching"}`)})
		if err != nil {
			t.Fatal(err)
		}
		mustValid(t, res)
		return res
	}
	want := func(res adapterproto.ReconcileResult, verdict adapterproto.ReconcileVerdict, grounds adapterproto.GroundsCode) {
		t.Helper()
		if res.Verdict != verdict || res.Grounds.Code != grounds {
			t.Fatalf("verdict %s grounds %s, want %s %s", res.Verdict, res.Grounds.Code, verdict, grounds)
		}
	}

	want(reconcile(), adapterproto.VerdictProvablyAbsent, adapterproto.GroundsRecordCompleteWithoutAttempt)

	// The attempt's process finished a turn, yet its uuid is nowhere in the record.
	w.setFile(outFile(session, prompt2), []byte(resultOK))
	want(reconcile(), adapterproto.VerdictIndeterminate, adapterproto.GroundsRecordUnreadable)
	w.removeFile(outFile(session, prompt2))

	// A user line without the uuid the lookup reads.
	w.appendRecord(file, `{"type":"user","sessionId":"`+session+`"}`)
	want(reconcile(), adapterproto.VerdictIndeterminate, adapterproto.GroundsRecordUnreadable)
}

// A restarted adapter takes its sessions back from their checkpoints.
func TestStartTakesSessionsBack(t *testing.T) {
	const session = "6ee62271-42c1-4558-89cf-0e32c50e0c1b"
	file := filepath.Join(recordDir, session+".jsonl")
	checkpoint := func(t turn.State) json.RawMessage {
		raw, _ := json.Marshal(sessionCheckpoint{V: checkpointVersion, Session: session, Cwd: workDir, RecordDir: recordDir, Turn: &t})
		return raw
	}
	attach := func(endpoint string, cp json.RawMessage) adapterproto.SessionAttach {
		return adapterproto.SessionAttach{
			Endpoint: adapterproto.EndpointRef(endpoint), Preset: "dev", NativeRef: session, Checkpoint: cp,
			ToolAccess: &adapterproto.ToolAccess{Env: map[string]string{}},
		}
	}

	t.Run("a turn whose start was not reported is followed, and reported", func(t *testing.T) {
		w := newWorld(t)
		started, _ := w.Start(proc.Launch{Argv: []string{claudePath, "-p", "--resume", session}, Stdout: outFile(session, prompt1)})
		w.appendRecord(file, queueLine(session), userLine(session, prompt1))
		cp := checkpoint(turn.State{Attempt: prompt1, Process: turn.Process{PID: started.PID, StartUnix: started.StartUnix, StartKey: started.StartKey}})
		_, res := newApp(t, w, attach("session-1", cp))
		mustValid(t, res)
		if got := res.Sessions[0]; got.State != adapterproto.AttachAttached || !got.TurnActive || got.Process == nil || got.Process.PID != int64(started.PID) {
			t.Fatalf("session state %+v", got)
		}
		w.waitEvents("started " + prompt1)
		w.exit(w.lastProc(), resultOK)
		w.waitEvents("started "+prompt1, "ended "+prompt1+" completed")
	})

	t.Run("a turn that ended while the adapter was away is ended once", func(t *testing.T) {
		w := newWorld(t)
		w.appendRecord(file, userLine(session, prompt1))
		started, _ := w.Start(proc.Launch{Argv: []string{claudePath, "-p", "--resume", session}, Stdout: outFile(session, prompt1)})
		w.exit(w.procByPID(started.PID), resultOK)
		cp := checkpoint(turn.State{Attempt: prompt1, StartedReported: true, Process: turn.Process{PID: started.PID, StartUnix: started.StartUnix, StartKey: started.StartKey}})
		_, res := newApp(t, w, attach("session-1", cp))
		if got := res.Sessions[0]; got.State != adapterproto.AttachAttached || got.TurnActive || got.Process != nil {
			t.Fatalf("session state %+v", got)
		}
		w.waitEvents("ended " + prompt1 + " completed")
		time.Sleep(20 * time.Millisecond)
		if got := w.eventList(); len(got) != 1 {
			t.Fatalf("events %q", got)
		}
	})

	t.Run("a process that was never recorded is followed", func(t *testing.T) {
		w := newWorld(t)
		w.appendRecord(file, userLine(session, prompt1))
		started, _ := w.Start(proc.Launch{Argv: []string{claudePath, "-p", "--resume", session}, Stdout: filepath.Join(stateDir, "out")})
		_, res := newApp(t, w, attach("session-1", checkpoint(turn.State{Attempt: prompt1, StartedReported: true, EndedReported: true})))
		if got := res.Sessions[0]; got.State != adapterproto.AttachAttached || !got.TurnActive || got.Process.PID != int64(started.PID) {
			t.Fatalf("session state %+v", got)
		}
		w.waitEvents("started ")
		w.exit(w.lastProc(), resultOK)
		// Such a process has no output file of its own, so its end is a failure.
		w.waitEvents("started ", "ended process-"+itoa(started.PID)+"-"+started.StartKey+" failed")
	})

	t.Run("a session without a record is lost, one without a checkpoint is unreachable", func(t *testing.T) {
		w := newWorld(t)
		_, res := newApp(t, w, attach("lost", checkpoint(turn.State{Attempt: prompt1})), attach("unknown", nil))
		if res.Sessions[0].State != adapterproto.AttachLost || res.Sessions[1].State != adapterproto.AttachUnreachable {
			t.Fatalf("sessions %+v", res.Sessions)
		}
	})
}

func TestProbe(t *testing.T) {
	w := newWorld(t)
	app, _ := newApp(t, w)
	res, err := app.Probe(ctxT(t), adapterproto.ProbeParams{ToolProbes: []adapterproto.ToolProbeRequest{
		{Preset: "dev", Frontend: adapterproto.FrontendCLI, Token: token, Env: map[string]string{"SIDERA_HOME": toolHome}},
		{Preset: "nope", Frontend: adapterproto.FrontendCLI, Token: token, Env: map[string]string{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, res)
	if res.Runtime == nil || res.Runtime.Name != "claude-code" || res.Runtime.Version != "2.1.316" || res.Auth != adapterproto.AuthValid {
		t.Fatalf("runtime %+v auth %v", res.Runtime, res.Auth)
	}
	wantDeps := []adapterproto.Dependency{{Name: "claude", State: adapterproto.DependencyOK}, {Name: "sidera", State: adapterproto.DependencyOK}}
	if !slices.Equal(res.Dependencies, wantDeps) {
		t.Fatalf("dependencies %+v", res.Dependencies)
	}
	var usable []string
	for _, p := range res.Presets {
		if p.Usable {
			usable = append(usable, p.Name)
		} else if len(p.Problems) == 0 {
			t.Errorf("preset %s is unusable without a problem", p.Name)
		}
	}
	if !slices.Equal(usable, []string{"dev"}) || len(res.Presets) != 3 {
		t.Fatalf("presets %+v", res.Presets)
	}
	if res.ToolProbes[0].Outcome != adapterproto.ProbeRan || res.ToolProbes[0].Method != adapterproto.ProbeUnsandboxed {
		t.Fatalf("cli probe %+v", res.ToolProbes[0])
	}
	if res.ToolProbes[1].Outcome != adapterproto.ProbeNotRun {
		t.Fatalf("a probe for an unknown preset %+v", res.ToolProbes[1])
	}

	w.setLoggedIn(false)
	res, err = app.Probe(ctxT(t), adapterproto.ProbeParams{ToolProbes: []adapterproto.ToolProbeRequest{}})
	if err != nil || res.Auth != adapterproto.AuthInvalid || res.Presets[0].Usable {
		t.Fatalf("logged out: %+v %v", res, err)
	}
}

// outFile is where the output of a turn is kept.
func outFile(session, attempt string) string {
	return filepath.Join(stateDir, "turns", session, attempt+".out.jsonl")
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
