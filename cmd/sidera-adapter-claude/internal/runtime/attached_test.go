package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/attached"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

const (
	claudePID  = 4242
	claudeSess = "6ee62271-42c1-4558-89cf-0e32c50e0c1b"
	clearedSes = "9a1b2c3d-0000-4000-8000-000000000001"
	inboxSock  = "/run/test/claude-4242.sock"
	inboxToken = "inbox-token-that-must-never-be-logged"
	ep         = adapterproto.EndpointRef("attached-1")

	// claudeStart is the second the fake Claude process started.
	claudeStart = 1793597671
)

func wakeOf(socket, tok string, pid int) adapterproto.WakeInfo {
	w := adapterproto.WakeInfo{}
	if socket != "" {
		w[attached.WakeSocket] = socket
	}
	if tok != "" {
		w[attached.WakeToken] = tok
	}
	if pid != 0 {
		w[attached.WakePID] = fmt.Sprint(pid)
	}
	return w
}

func fullWake() adapterproto.WakeInfo { return wakeOf(inboxSock, inboxToken, claudePID) }

func (w *world) claudeProc(pid int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.procs[pid] = &fakeProc{pid: pid, startUnix: claudeStart, alive: true}
}

func (w *world) killClaude(pid int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.procs[pid].alive = false
}

// registryDoc is a registry file in the form the runtime writes it.
func registryDoc(pid int, session, status string, statusAt int64, extra map[string]any) []byte {
	doc := map[string]any{
		"pid": pid, "sessionId": session, "cwd": workDir, "kind": "interactive", "version": "2.1.316",
		"peerProtocol": 1, "messagingSocketPath": inboxSock,
		"procStart": time.Unix(claudeStart, 0).UTC().Format(time.ANSIC),
		"status":    status, "statusUpdatedAt": statusAt, "updatedAt": statusAt,
	}
	for k, v := range extra {
		if v == nil {
			delete(doc, k)
		} else {
			doc[k] = v
		}
	}
	b, _ := json.Marshal(doc)
	return b
}

// setRegistry writes the registry file of the fake Claude process.
func (w *world) setRegistry(session, status string, statusAt int64, extra ...map[string]any) {
	doc := map[string]any{}
	for _, e := range extra {
		for k, v := range e {
			doc[k] = v
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.files[attached.RegistryFile(claudeHome, claudePID)] = registryDoc(claudePID, session, status, statusAt, doc)
}

func (w *world) removeRegistry() {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.files, attached.RegistryFile(claudeHome, claudePID))
}

func transcript(session string) string { return filepath.Join(recordDir, session+".jsonl") }

// peerRecord is the user record of a message written to the inbox.
func peerRecord(session, uuid string) string {
	return `{"type":"user","uuid":"` + uuid + `","origin":{"kind":"peer"},"message":{"role":"user","content":"hi"},"sessionId":"` + session + `"}`
}

func humanRecord(session, uuid string) string {
	return `{"type":"user","uuid":"` + uuid + `","origin":{"kind":"human"},"message":{"role":"user","content":"hi"},"sessionId":"` + session + `"}`
}

func turnEndRecord(session string) string {
	return `{"type":"system","subtype":"turn_duration","durationMs":1500,"sessionId":"` + session + `"}`
}

func (w *world) noteList() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.notes)
}

// waitNotes waits until the daemon was told exactly these notices about attached sessions.
func (w *world) waitNotes(want ...string) {
	w.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := w.noteList()
		if slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("notices\n got %q\nwant %q", got, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// settle gives the monitor several looks at the world.
func settle() { time.Sleep(60 * time.Millisecond) }

func (w *world) lastAvailability() []adapterproto.Availability {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.avail) == 0 {
		return nil
	}
	return w.avail[len(w.avail)-1].Conditions
}

// attachedWorld is a world with a running Claude process, an idle session in the registry
// and an adapter that took the session.
func attachedWorld(t *testing.T) (*world, *App) {
	t.Helper()
	w := newWorld(t)
	w.claudeProc(claudePID)
	w.setRegistry(claudeSess, "idle", 1000)
	app, _ := newApp(t, w)
	res, err := app.AttachSession(context.Background(), adapterproto.SessionAttachParams{Endpoint: ep, NativeRef: claudeSess, Wake: fullWake()})
	if err != nil || res.Outcome != adapterproto.SessionAttachAttached {
		t.Fatalf("attach: %+v, %v", res, err)
	}
	return w, app
}

func deliver(app *App, attempt, text string) (adapterproto.PromptDeliverResult, error) {
	return app.DeliverPrompt(context.Background(), adapterproto.PromptDeliverParams{Endpoint: ep, Attempt: adapterproto.AttemptID(attempt), Text: text})
}

func TestAttachOutcomes(t *testing.T) {
	notFound := adapterproto.SessionAttachNotFound
	failed := adapterproto.SessionAttachFailed
	tests := []struct {
		name   string
		setup  func(w *world)
		wake   adapterproto.WakeInfo
		want   adapterproto.SessionAttachOutcome
		reason adapterproto.SessionAttachFailure
		detail string
	}{
		{"a session with a wake", func(w *world) {}, fullWake(), adapterproto.SessionAttachAttached, "", ""},
		{"no registry entry", func(w *world) { w.removeRegistry() }, fullWake(), notFound, "", "no registry entry"},
		{"the process has ended", func(w *world) { w.killClaude(claudePID) }, fullWake(), notFound, "", "ended"},
		{"the number belongs to another process now", func(w *world) {
			w.setRegistry(claudeSess, "idle", 1000, map[string]any{"procStart": "Mon Nov  2 01:00:00 2026"})
		}, fullWake(), notFound, "", "not the one"},
		{"a session that is not interactive", func(w *world) {
			w.setRegistry(claudeSess, "idle", 1000, map[string]any{"kind": "sdk"})
		}, fullWake(), notFound, "", "not an interactive"},
		{"a registry entry in a form that is not read", func(w *world) {
			w.setRegistry(claudeSess, "idle", 1000, map[string]any{"status": nil})
		}, fullWake(), failed, adapterproto.AttachFailNativeError, "form"},
		{"the process table cannot be read", func(w *world) { w.lookupErr = errors.New("sysctl failed") }, fullWake(), failed, adapterproto.AttachFailNativeUnreachable, "sysctl"},
		{"a wake without a socket", func(w *world) {}, wakeOf("", inboxToken, claudePID), failed, adapterproto.AttachFailWakeUnavailable, "carries no inbox socket"},
		{"a wake without a socket for a runtime that lists no messaging", func(w *world) {
			w.setRegistry(claudeSess, "idle", 1000, map[string]any{"peerProtocol": nil, "messagingSocketPath": nil})
		}, wakeOf("", "", claudePID), failed, adapterproto.AttachFailWakeUnavailable, "too old"},
		{"a wake that names no process", func(w *world) {}, wakeOf(inboxSock, inboxToken, 0), failed, adapterproto.AttachFailWakeUnavailable, attached.WakePID},
		{"a wake whose process is not a number", func(w *world) {}, adapterproto.WakeInfo{attached.WakePID: "x"}, failed, adapterproto.AttachFailWakeUnavailable, attached.WakePID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.claudeProc(claudePID)
			w.setRegistry(claudeSess, "idle", 1000)
			tc.setup(w)
			app, _ := newApp(t, w)
			res, err := app.AttachSession(context.Background(), adapterproto.SessionAttachParams{Endpoint: ep, NativeRef: claudeSess, Wake: tc.wake})
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != tc.want || res.Reason != tc.reason || !strings.Contains(res.Detail, tc.detail) {
				t.Fatalf("result %+v, want outcome %s reason %q detail containing %q", res, tc.want, tc.reason, tc.detail)
			}
			if err := res.Validate(); err != nil {
				t.Fatalf("an invalid result: %v", err)
			}
		})
	}
}

func TestAttachingAgainReplacesTheWake(t *testing.T) {
	w, app := attachedWorld(t)
	other := "/run/test/other.sock"
	res, _ := app.AttachSession(context.Background(), adapterproto.SessionAttachParams{Endpoint: ep, NativeRef: claudeSess, Wake: wakeOf(other, "", claudePID)})
	if res.Outcome != adapterproto.SessionAttachAttached {
		t.Fatalf("repeat: %+v", res)
	}
	if got := len(app.attached); got != 1 {
		t.Fatalf("%d sessions held after a repeat", got)
	}
	if _, err := deliver(app, attempt1, "x"); err != nil {
		t.Fatal(err)
	}
	if got := w.inboxWrites(); len(got) != 1 || got[0].socket != other || strings.Contains(got[0].data, "auth") {
		t.Fatalf("the message went %+v: it goes to the new socket, without the dropped token", got)
	}
	// A repeat with no wake means none held.
	res, _ = app.AttachSession(context.Background(), adapterproto.SessionAttachParams{Endpoint: ep, NativeRef: claudeSess})
	if res.Outcome != adapterproto.SessionAttachFailed || res.Reason != adapterproto.AttachFailWakeUnavailable {
		t.Fatalf("a repeat without a wake: %+v", res)
	}
	if r, _ := deliver(app, attempt2, "y"); r.Stage != adapterproto.PromptRefused || r.Reason != adapterproto.RefusalNativeUnavailable {
		t.Fatalf("delivery without a wake: %+v", r)
	}
}

func TestADeliveredPromptIsWrittenAndFollowedToTheEndOfItsTurn(t *testing.T) {
	w, app := attachedWorld(t)
	res, err := deliver(app, attempt1, "wake up\nplease")
	if err != nil || res.Stage != adapterproto.PromptAccepted {
		t.Fatalf("deliver: %+v, %v", res, err)
	}
	want := `{"type":"auth","token":"` + inboxToken + `"}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"wake up\nplease"},"uuid":"` + attempt1 + `"}` + "\n"
	if got := w.inboxWrites(); len(got) != 1 || got[0].socket != inboxSock || got[0].data != want {
		t.Fatalf("written %+v\nwant   %q", got, want)
	}
	w.mu.Lock()
	_, dispatched := w.checkpts["attempt/"+attempt1]
	w.mu.Unlock()
	if !dispatched {
		t.Fatal("the attempt's checkpoint must be stored before the message is written")
	}
	settle()
	if n := w.noteList(); len(n) != 0 {
		t.Fatalf("nothing is reported before the record shows the prompt: %q", n)
	}

	// The session wakes: its record, then its status.
	w.appendRecord(transcript(claudeSess), peerRecord(claudeSess, attempt1))
	w.waitNotes("persisted "+attempt1, "started "+attempt1+" attempt="+attempt1)
	w.setRegistry(claudeSess, "busy", 2000)
	w.appendRecord(transcript(claudeSess), turnEndRecord(claudeSess))
	w.setRegistry(claudeSess, "idle", 3000)
	w.waitNotes("persisted "+attempt1, "started "+attempt1+" attempt="+attempt1, "ended "+attempt1+" completed")

	// The same attempt again is answered from what is known, and writes nothing.
	if r, err := deliver(app, attempt1, "wake up\nplease"); err != nil || r.Stage != adapterproto.PromptPersisted {
		t.Fatalf("repeat: %+v, %v", r, err)
	}
	if _, err := deliver(app, attempt1, "another text"); !isCode(err, adapterproto.CodeAttemptConflict) {
		t.Fatalf("the attempt with another text: %v", err)
	}
	if len(w.inboxWrites()) != 1 {
		t.Fatalf("a repeat wrote again: %+v", w.inboxWrites())
	}
}

func isCode(err error, code int) bool {
	var pe *peer.Error
	return errors.As(err, &pe) && pe.Code == code
}

func TestATurnThePersonStartsIsReportedWithoutAnAttempt(t *testing.T) {
	w, app := attachedWorld(t)
	_ = app
	w.setRegistry(claudeSess, "busy", 2000)
	w.waitNotes("started person-2000 attempt=")
	w.setRegistry(claudeSess, "waiting", 2500) // a permission prompt is the same turn
	settle()
	w.appendRecord(transcript(claudeSess), humanRecord(claudeSess, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), turnEndRecord(claudeSess))
	w.setRegistry(claudeSess, "idle", 3000)
	w.waitNotes("started person-2000 attempt=", "ended person-2000 completed")
	// A background shell after the turn is not a turn.
	w.setRegistry(claudeSess, "shell", 4000)
	settle()
	if n := w.noteList(); len(n) != 2 {
		t.Fatalf("notices %q", n)
	}
}

func TestASessionAttachedInTheMiddleOfATurnReportsIt(t *testing.T) {
	w := newWorld(t)
	w.claudeProc(claudePID)
	w.setRegistry(claudeSess, "busy", 2000)
	w.appendRecord(transcript(claudeSess), humanRecord(claudeSess, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), turnEndRecord(claudeSess), humanRecord(claudeSess, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"))
	app, _ := newApp(t, w)
	if res, _ := app.AttachSession(context.Background(), adapterproto.SessionAttachParams{Endpoint: ep, NativeRef: claudeSess, Wake: fullWake()}); res.Outcome != adapterproto.SessionAttachAttached {
		t.Fatalf("attach: %+v", res)
	}
	w.waitNotes("started person-2000 attempt=")
	// The history before the attachment is not replayed: only its own end counts.
	w.appendRecord(transcript(claudeSess), turnEndRecord(claudeSess))
	w.setRegistry(claudeSess, "idle", 3000)
	w.waitNotes("started person-2000 attempt=", "ended person-2000 completed")
}

func TestClearIsFollowed(t *testing.T) {
	w, app := attachedWorld(t)
	w.setRegistry(clearedSes, "idle", 2000) // /clear: a new session in the same process
	settle()
	if _, err := deliver(app, attempt1, "after the clear"); err != nil {
		t.Fatal(err)
	}
	// The record is in the new session's file, which did not exist before.
	w.appendRecord(transcript(clearedSes), peerRecord(clearedSes, attempt1))
	w.waitNotes("persisted "+attempt1, "started "+attempt1+" attempt="+attempt1)
}

// A turn that was running when the session was cleared ends with the session.
func TestAClearEndsTheRunningTurn(t *testing.T) {
	w, _ := attachedWorld(t)
	w.setRegistry(claudeSess, "busy", 2000)
	w.waitNotes("started person-2000 attempt=")
	w.setRegistry(clearedSes, "idle", 3000)
	w.waitNotes("started person-2000 attempt=", "ended person-2000 interrupted")
}

// What the old file shows when the session is cleared is told before the turn ends
// with the session: the end of the turn that was written just before the clear is
// not lost to an interruption.
func TestTheEndOfATurnWrittenJustBeforeAClearIsNotLost(t *testing.T) {
	w, _ := attachedWorld(t)
	w.appendRecord(transcript(claudeSess), `{"type":"queue-operation","operation":"enqueue","sessionId":"`+claudeSess+`"}`)
	w.setRegistry(claudeSess, "busy", 2000)
	w.waitNotes("started person-2000 attempt=")
	var fired atomic.Bool
	w.mu.Lock()
	w.afterOpen = func(w *world, path string) {
		if path != transcript(claudeSess) || !fired.CompareAndSwap(false, true) {
			return
		}
		// A look has just read the old file. The turn ends and the session is cleared
		// after that, before the next look.
		w.mu.Lock()
		w.files[path] = append(w.files[path], turnEndRecord(claudeSess)+"\n"...)
		w.files[attached.RegistryFile(claudeHome, claudePID)] = registryDoc(claudePID, clearedSes, "idle", 3000, nil)
		w.mu.Unlock()
	}
	w.mu.Unlock()
	w.waitNotes("started person-2000 attempt=", "ended person-2000 completed")
}

func TestAPromptWrittenBeforeAClearIsFoundInTheNewFile(t *testing.T) {
	w, app := attachedWorld(t)
	if _, err := deliver(app, attempt1, "x"); err != nil {
		t.Fatal(err)
	}
	// The session is cleared before the record is seen, and writes the record to the new file.
	w.setRegistry(clearedSes, "idle", 2000)
	settle()
	w.appendRecord(transcript(clearedSes), peerRecord(clearedSes, attempt1))
	w.waitNotes("persisted "+attempt1, "started "+attempt1+" attempt="+attempt1)
}

// The turn end and the registry's idle status are written while a look is in
// progress, after the look read the transcript. A status read after the transcript is
// the one the look acts on: the busy status that was read first belongs to a turn
// the transcript, read at the next look, will end, and must not open a turn that is
// over already.
func TestAStatusReadBeforeTheTranscriptOpensNoTurn(t *testing.T) {
	w, _ := attachedWorld(t)
	w.appendRecord(transcript(claudeSess), `{"type":"queue-operation","operation":"enqueue","sessionId":"`+claudeSess+`"}`)
	settle()
	var fired atomic.Bool
	w.mu.Lock()
	w.afterOpen = func(w *world, path string) {
		w.mu.Lock()
		busy := bytes.Contains(w.files[attached.RegistryFile(claudeHome, claudePID)], []byte(`"busy"`))
		w.mu.Unlock()
		if path != transcript(claudeSess) || !busy || !fired.CompareAndSwap(false, true) {
			return
		}
		// The turn ends in the middle of the look.
		w.appendRecord(path, turnEndRecord(claudeSess))
		w.setRegistry(claudeSess, "idle", 3000)
	}
	w.mu.Unlock()
	w.setRegistry(claudeSess, "busy", 2000)
	for deadline := time.Now().Add(5 * time.Second); !fired.Load() && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	settle()
	if n := w.noteList(); len(n) != 0 {
		t.Fatalf("a turn that was over was reported: %q", n)
	}
}

// A session that stays busy from one turn to the next shows no new status: the user
// record that begins the second turn tells it.
func TestBackToBackTurnsWhileTheSessionStaysBusy(t *testing.T) {
	w, _ := attachedWorld(t)
	const second = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	w.setRegistry(claudeSess, "busy", 2000)
	w.waitNotes("started person-2000 attempt=")
	w.appendRecord(transcript(claudeSess), humanRecord(claudeSess, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), turnEndRecord(claudeSess))
	w.waitNotes("started person-2000 attempt=", "ended person-2000 completed")
	w.appendRecord(transcript(claudeSess), humanRecord(claudeSess, second))
	w.waitNotes("started person-2000 attempt=", "ended person-2000 completed", "started person-"+second+" attempt=")
	w.appendRecord(transcript(claudeSess), turnEndRecord(claudeSess))
	w.setRegistry(claudeSess, "idle", 3000)
	w.waitNotes("started person-2000 attempt=", "ended person-2000 completed", "started person-"+second+" attempt=", "ended person-"+second+" completed")
}

func TestAGoneSessionRefusesPromptsAndIsReportedUnavailable(t *testing.T) {
	w, app := attachedWorld(t)
	w.setRegistry(claudeSess, "busy", 2000)
	w.waitNotes("started person-2000 attempt=")

	w.killClaude(claudePID)
	w.removeRegistry()
	// The running turn ended with the session, and the endpoint is reported unavailable.
	w.waitNotes("started person-2000 attempt=", "ended person-2000 interrupted")
	deadline := time.Now().Add(5 * time.Second)
	for len(w.lastAvailability()) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	got := w.lastAvailability()
	if len(got) != 1 || got[0].Condition != adapterproto.ConditionNativeUnavailable || !slices.Equal(got[0].Endpoints, []adapterproto.EndpointRef{ep}) {
		t.Fatalf("availability %+v", got)
	}
	r, err := deliver(app, attempt1, "x")
	if err != nil || r.Stage != adapterproto.PromptRefused || r.Reason != adapterproto.RefusalNativeUnavailable {
		t.Fatalf("delivery to a gone session: %+v, %v", r, err)
	}
	if len(w.inboxWrites()) != 0 {
		t.Fatal("nothing is written to a session that is gone")
	}
	// The endpoint stays open: the session coming back is followed again.
	w.mu.Lock()
	w.procs[claudePID].alive = true
	w.mu.Unlock()
	w.setRegistry(claudeSess, "idle", 5000)
	for deadline := time.Now().Add(5 * time.Second); len(w.lastAvailability()) != 0 && time.Now().Before(deadline); {
		time.Sleep(2 * time.Millisecond)
	}
	if got := w.lastAvailability(); len(got) != 0 {
		t.Fatalf("availability after the session came back: %+v", got)
	}
}

// A session that was unreachable is reported reachable as soon as the adapter has
// read its registry successfully again, by an attachment or a delivery, and does not
// wait for the slow looks it gets while down.
func TestAReachableSessionIsNotReportedUnavailableAnyLonger(t *testing.T) {
	for name, act := range map[string]func(*testing.T, *world, *App){
		"an attachment": func(t *testing.T, w *world, app *App) {
			if res, err := app.AttachSession(context.Background(), adapterproto.SessionAttachParams{Endpoint: ep, NativeRef: claudeSess, Wake: fullWake()}); err != nil || res.Outcome != adapterproto.SessionAttachAttached {
				t.Fatalf("attach: %+v, %v", res, err)
			}
		},
		"a delivery": func(t *testing.T, w *world, app *App) {
			if r, err := deliver(app, attempt1, "x"); err != nil || r.Stage != adapterproto.PromptAccepted {
				t.Fatalf("deliver: %+v, %v", r, err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			w, app := attachedWorld(t)
			w.killClaude(claudePID)
			for deadline := time.Now().Add(5 * time.Second); len(w.lastAvailability()) == 0 && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			if len(w.lastAvailability()) != 1 {
				t.Fatal("not reported unavailable")
			}
			w.mu.Lock()
			w.procs[claudePID].alive = true
			w.mu.Unlock()
			act(t, w, app)
			if got := w.lastAvailability(); len(got) != 0 {
				t.Fatalf("availability after the session was reached: %+v", got)
			}
		})
	}
}

func TestAnInboxThatCannotBeConnectedRefusesThePrompt(t *testing.T) {
	w, app := attachedWorld(t)
	w.inboxErr = fmt.Errorf("%w: connection refused", ErrInboxUnreachable)
	r, err := deliver(app, attempt1, "x")
	if err != nil || r.Stage != adapterproto.PromptRefused || r.Reason != adapterproto.RefusalNativeUnavailable {
		t.Fatalf("%+v, %v", r, err)
	}
	if got := w.lastAvailability(); len(got) != 1 || got[0].Condition != adapterproto.ConditionNativeUnavailable {
		t.Fatalf("availability %+v", got)
	}
	// The refused attempt left nothing expected: the same attempt may be tried again.
	w.inboxErr = nil
	if r, err := deliver(app, attempt1, "x"); err != nil || r.Stage != adapterproto.PromptAccepted {
		t.Fatalf("retry: %+v, %v", r, err)
	}
	if got := w.lastAvailability(); len(got) != 0 {
		t.Fatalf("availability after a delivery: %+v", got)
	}
}

func TestAWriteThatFailedAfterTheConnectionIsAnUnknownOutcome(t *testing.T) {
	w, app := attachedWorld(t)
	w.inboxErr = errors.New("broken pipe")
	if r, err := deliver(app, attempt1, "x"); err == nil {
		t.Fatalf("the outcome of a write that may have arrived is not a refusal: %+v", r)
	}
	// If it arrived, the record settles the attempt all the same.
	w.inboxErr = nil
	w.appendRecord(transcript(claudeSess), peerRecord(claudeSess, attempt1))
	w.waitNotes("persisted "+attempt1, "started "+attempt1+" attempt="+attempt1)
}

func TestAnAttachedEndpointIsNotClosedOrInterrupted(t *testing.T) {
	w, app := attachedWorld(t)
	w.setRegistry(claudeSess, "busy", 2000)
	w.waitNotes("started person-2000 attempt=")
	_, err := app.CloseSession(context.Background(), adapterproto.CloseParams{Endpoint: ep, NativeRef: claudeSess})
	if !isCode(err, adapterproto.CodeWrongEndpointKind) {
		t.Fatalf("close: %v", err)
	}
	_, err = app.Interrupt(context.Background(), adapterproto.InterruptParams{Endpoint: ep})
	if !isCode(err, adapterproto.CodeWrongEndpointKind) {
		t.Fatalf("interrupt: %v", err)
	}
	w.mu.Lock()
	signals := len(w.signals)
	w.mu.Unlock()
	if signals != 0 || !w.procs[claudePID].alive || len(w.inboxWrites()) != 0 {
		t.Fatal("the person's session was touched")
	}
	// An endpoint nobody holds is still unknown to interrupt.
	if _, err := app.Interrupt(context.Background(), adapterproto.InterruptParams{Endpoint: "nobody"}); isCode(err, adapterproto.CodeWrongEndpointKind) {
		t.Fatalf("interrupt of an unknown endpoint: %v", err)
	}
}

func TestDetachingStopsTheWatch(t *testing.T) {
	w, app := attachedWorld(t)
	app.SessionDetached(context.Background(), adapterproto.SessionDetachedParams{Endpoint: ep})
	w.setRegistry(claudeSess, "busy", 2000)
	settle()
	if n := w.noteList(); len(n) != 0 {
		t.Fatalf("a detached session is not watched: %q", n)
	}
	if _, err := deliver(app, attempt1, "x"); !isCode(err, adapterproto.CodeUnknownEndpoint) {
		t.Fatalf("delivery after the detach: %v", err)
	}
	if len(w.inboxWrites()) != 0 {
		t.Fatal("the wake was kept")
	}
	app.SessionDetached(context.Background(), adapterproto.SessionDetachedParams{Endpoint: ep}) // a repeat is harmless
}

// A delivery that was under way when the endpoint was detached or dropped cannot
// bring the condition of the endpoint back.
func TestADroppedEndpointGetsNoConditionBack(t *testing.T) {
	for name, drop := range map[string]func(*App){
		"a detach": func(app *App) {
			app.SessionDetached(context.Background(), adapterproto.SessionDetachedParams{Endpoint: ep})
		},
		"a start that does not list it": func(app *App) { startApp(t, app) },
	} {
		t.Run(name, func(t *testing.T) {
			w, app := attachedWorld(t)
			s := app.attachedSession(ep) // what a delivery that began before the drop holds
			drop(app)
			w.inboxErr = fmt.Errorf("%w: refused", ErrInboxUnreachable)
			if r, err := app.deliverAttached(context.Background(), s, adapterproto.PromptDeliverParams{Endpoint: ep, Attempt: attempt1, Text: "x"}); err != nil || r.Stage != adapterproto.PromptRefused {
				t.Fatalf("%+v, %v", r, err)
			}
			if got := w.lastAvailability(); len(got) != 0 {
				t.Fatalf("availability %+v", got)
			}
		})
	}
}

// What is remembered of the delivered prompts is bounded: a prompt whose record never
// came is not waited for for ever.
func TestPromptsNeverSeenAreForgottenWithTheMemoryOfThem(t *testing.T) {
	w, app := attachedWorld(t)
	attemptN := func(i int) string { return fmt.Sprintf("%08x-0000-4000-8000-000000000000", i+1) }
	for i := 0; i <= sentMemory; i++ {
		if r, err := deliver(app, attemptN(i), "x"); err != nil || r.Stage != adapterproto.PromptAccepted {
			t.Fatalf("deliver %d: %+v, %v", i, r, err)
		}
	}
	s := app.attachedSession(ep)
	s.mu.Lock()
	expected := len(s.follower.Expected())
	s.mu.Unlock()
	if expected != sentMemory {
		t.Fatalf("%d prompts are waited for, want %d", expected, sentMemory)
	}
	// The record of the forgotten prompt is no longer taken for the proof of a
	// delivery (it shows as a message of another sender, which begins a turn without
	// an attempt); the newest one's is.
	w.appendRecord(transcript(claudeSess), peerRecord(claudeSess, attemptN(0)))
	first := "started person-" + attemptN(0) + " attempt="
	w.waitNotes(first)
	w.appendRecord(transcript(claudeSess), peerRecord(claudeSess, attemptN(sentMemory)))
	w.waitNotes(first, "persisted "+attemptN(sentMemory))
}

func TestDetachingClearsTheUnavailability(t *testing.T) {
	w, app := attachedWorld(t)
	w.inboxErr = fmt.Errorf("%w: refused", ErrInboxUnreachable)
	_, _ = deliver(app, attempt1, "x")
	if len(w.lastAvailability()) != 1 {
		t.Fatal("not reported")
	}
	app.SessionDetached(context.Background(), adapterproto.SessionDetachedParams{Endpoint: ep})
	if got := w.lastAvailability(); len(got) != 0 {
		t.Fatalf("availability %+v", got)
	}
}

func attachedEntry(wake adapterproto.WakeInfo, checkpoint any) adapterproto.SessionAttach {
	e := adapterproto.SessionAttach{Endpoint: ep, Attached: true, NativeRef: claudeSess, Wake: wake}
	if checkpoint != nil {
		e.Checkpoint, _ = json.Marshal(checkpoint)
	}
	return e
}

func TestAnAttachedSessionIsTakenBackAtStart(t *testing.T) {
	cp := attachedCheckpoint{V: checkpointVersion, Attached: true, PID: claudePID}
	tests := []struct {
		name      string
		setup     func(w *world)
		entry     adapterproto.SessionAttach
		wantState adapterproto.AttachState
		wantTurn  bool
		held      bool
	}{
		{"with its wake", func(w *world) {}, attachedEntry(fullWake(), nil), adapterproto.AttachAttached, false, true},
		{"without a wake, by its checkpoint", func(w *world) {}, attachedEntry(nil, cp), adapterproto.AttachAttached, false, true},
		{"a turn is running", func(w *world) { w.setRegistry(claudeSess, "busy", 2000) }, attachedEntry(nil, cp), adapterproto.AttachAttached, true, true},
		{"the session has ended", func(w *world) { w.killClaude(claudePID) }, attachedEntry(nil, cp), adapterproto.AttachLost, false, false},
		{"its registry entry is gone", func(w *world) { w.removeRegistry() }, attachedEntry(fullWake(), nil), adapterproto.AttachLost, false, false},
		{"nothing names its process", func(w *world) {}, attachedEntry(nil, nil), adapterproto.AttachUnreachable, false, false},
		{"the process table cannot be read", func(w *world) { w.lookupErr = errors.New("sysctl failed") }, attachedEntry(fullWake(), nil), adapterproto.AttachUnreachable, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.claudeProc(claudePID)
			w.setRegistry(claudeSess, "idle", 1000)
			tc.setup(w)
			app, res := newApp(t, w, tc.entry)
			if len(res.Sessions) != 1 {
				t.Fatalf("answer %+v", res)
			}
			got := res.Sessions[0]
			if got.State != tc.wantState || got.TurnActive != tc.wantTurn {
				t.Fatalf("state %+v, want %s turn_active=%v", got, tc.wantState, tc.wantTurn)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("an invalid state: %v", err)
			}
			if held := app.attachedSession(ep) != nil; held != tc.held {
				t.Fatalf("held = %v", held)
			}
		})
	}
}

func TestAnAttachedSessionWithoutAWakeWaitsForTheNextAttachment(t *testing.T) {
	w := newWorld(t)
	w.claudeProc(claudePID)
	w.setRegistry(claudeSess, "busy", 2000)
	app, res := newApp(t, w, attachedEntry(nil, attachedCheckpoint{V: checkpointVersion, Attached: true, PID: claudePID}))
	if !res.Sessions[0].TurnActive {
		t.Fatalf("answer %+v", res)
	}
	// The turn the answer told of is not told again, and its end is.
	w.appendRecord(transcript(claudeSess), turnEndRecord(claudeSess))
	w.setRegistry(claudeSess, "idle", 3000)
	w.waitNotes("ended person-2000 completed")

	if r, _ := deliver(app, attempt1, "x"); r.Stage != adapterproto.PromptRefused || r.Reason != adapterproto.RefusalNativeUnavailable {
		t.Fatalf("delivery without a wake: %+v", r)
	}
	if got := w.lastAvailability(); len(got) != 1 {
		t.Fatalf("availability %+v", got)
	}
	r, _ := app.AttachSession(context.Background(), adapterproto.SessionAttachParams{Endpoint: ep, NativeRef: claudeSess, Wake: fullWake()})
	if r.Outcome != adapterproto.SessionAttachAttached {
		t.Fatalf("attach: %+v", r)
	}
	if got := w.lastAvailability(); len(got) != 0 {
		t.Fatalf("availability after the wake came: %+v", got)
	}
	if res, err := deliver(app, attempt2, "y"); err != nil || res.Stage != adapterproto.PromptAccepted {
		t.Fatalf("delivery with the wake: %+v, %v", res, err)
	}
}

func TestAttachedReconciliation(t *testing.T) {
	w, app := attachedWorld(t)
	reconcile := func(attempt string, checkpoint bool) adapterproto.ReconcileResult {
		t.Helper()
		p := adapterproto.ReconcileParams{Endpoint: ep, Attempt: adapterproto.AttemptID(attempt)}
		if checkpoint {
			p.Checkpoint = json.RawMessage(`{"v":1}`)
		}
		res, err := app.Reconcile(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if r := reconcile(attempt2, false); r.Verdict != adapterproto.VerdictProvablyAbsent || r.Grounds.Code != adapterproto.GroundsNeverDispatched {
		t.Fatalf("never dispatched: %+v", r)
	}
	w.appendRecord(transcript(claudeSess), humanRecord(claudeSess, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), turnEndRecord(claudeSess))
	if r := reconcile(attempt2, true); r.Verdict != adapterproto.VerdictProvablyAbsent || r.Grounds.Code != adapterproto.GroundsRecordCompleteWithoutAttempt {
		t.Fatalf("dispatched, not in the record, session idle: %+v", r)
	}
	w.setRegistry(claudeSess, "busy", 2000)
	if r := reconcile(attempt2, true); r.Verdict != adapterproto.VerdictIndeterminate || r.Grounds.Code != adapterproto.GroundsTurnRunning {
		t.Fatalf("dispatched, session busy: %+v", r)
	}
	w.appendRecord(transcript(claudeSess), peerRecord(claudeSess, attempt1))
	if r := reconcile(attempt1, true); r.Verdict != adapterproto.VerdictFound || r.Turn.State != adapterproto.TurnStateRunning {
		t.Fatalf("in the record, turn running: %+v", r)
	}
	w.setRegistry(claudeSess, "idle", 3000)
	if r := reconcile(attempt1, true); r.Verdict != adapterproto.VerdictFound || r.Turn.State != adapterproto.TurnStateInterrupted {
		t.Fatalf("in the record, nothing ended it: %+v", r)
	}
	w.appendRecord(transcript(claudeSess), turnEndRecord(claudeSess))
	if r := reconcile(attempt1, true); r.Verdict != adapterproto.VerdictFound || r.Turn.State != adapterproto.TurnStateCompleted {
		t.Fatalf("in the record, ended: %+v", r)
	}
}

// Nothing the wake holds reaches a log, at any level, whatever happens to a delivery.
func TestTheWakeNeverReachesTheLog(t *testing.T) {
	var logs bytes.Buffer
	w := newWorld(t)
	w.claudeProc(claudePID)
	w.setRegistry(claudeSess, "idle", 1000)
	app, _ := newApp(t, w)
	app.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx := context.Background()
	_, _ = app.AttachSession(ctx, adapterproto.SessionAttachParams{Endpoint: ep, NativeRef: claudeSess, Wake: fullWake()})
	_, _ = deliver(app, attempt1, "a prompt text that is not logged either")
	w.inboxErr = fmt.Errorf("%w: connection refused", ErrInboxUnreachable)
	_, _ = deliver(app, attempt2, "x")
	w.inboxErr = errors.New("broken pipe")
	_, _ = deliver(app, "55555555-5555-4555-8555-555555555555", "x")
	w.inboxErr = nil
	w.setRegistry(claudeSess, "idle", 1000, map[string]any{"procStart": "Mon Nov  2 01:00:00 2026"}) // a pid reuse is logged
	_, _ = deliver(app, "66666666-6666-4666-8666-666666666666", "x")
	w.setRegistry(claudeSess, "idle", 1000, map[string]any{"status": nil})
	_, _ = app.AttachSession(ctx, adapterproto.SessionAttachParams{Endpoint: ep, NativeRef: claudeSess, Wake: fullWake()})
	_, _ = app.AttachSession(ctx, adapterproto.SessionAttachParams{Endpoint: ep, NativeRef: claudeSess, Wake: adapterproto.WakeInfo{attached.WakePID: inboxToken}})
	app.SessionDetached(ctx, adapterproto.SessionDetachedParams{Endpoint: ep})
	settle()
	if logs.Len() == 0 {
		t.Fatal("nothing was logged: the test proves nothing")
	}
	for _, secret := range []string{inboxToken, inboxSock, "not logged either"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the log holds %q:\n%s", secret, logs.String())
		}
	}
	// Neither the token nor the socket is written to the daemon as a checkpoint.
	w.mu.Lock()
	defer w.mu.Unlock()
	for key, cp := range w.checkpts {
		if strings.Contains(string(cp), inboxToken) || strings.Contains(string(cp), inboxSock) {
			t.Fatalf("checkpoint %s holds the wake: %s", key, cp)
		}
	}
}

// The probe's conditions and those of the attached sessions are one snapshot.
func TestAvailabilityKeepsTheConditionsOfTheProbe(t *testing.T) {
	w, app := attachedWorld(t)
	// A probe that finds sidera missing.
	app.mu.Lock()
	app.sidera = ""
	app.mu.Unlock()
	app.setProbe(context.Background(), []adapterproto.Availability{{Condition: adapterproto.ConditionDependencyMissing, Detail: "sidera was not found"}})
	w.inboxErr = fmt.Errorf("%w: refused", ErrInboxUnreachable)
	_, _ = deliver(app, attempt1, "x")
	got := w.lastAvailability()
	if len(got) != 2 || got[0].Condition != adapterproto.ConditionDependencyMissing || got[1].Condition != adapterproto.ConditionNativeUnavailable {
		t.Fatalf("availability %+v", got)
	}
	app.setProbe(context.Background(), []adapterproto.Availability{})
	if got := w.lastAvailability(); len(got) != 1 || got[0].Condition != adapterproto.ConditionNativeUnavailable {
		t.Fatalf("availability %+v", got)
	}
}

// A session the daemon no longer lists has been closed, and its notice may have been missed.
func TestStartDropsAttachedSessionsItNoLongerLists(t *testing.T) {
	w, app := attachedWorld(t)
	w.inboxErr = fmt.Errorf("%w: refused", ErrInboxUnreachable)
	_, _ = deliver(app, attempt1, "x")
	startApp(t, app)
	if app.attachedSession(ep) != nil {
		t.Fatal("the session is still held")
	}
	if got := w.lastAvailability(); len(got) != 0 {
		t.Fatalf("availability %+v", got)
	}
	w.setRegistry(claudeSess, "busy", 2000)
	settle()
	if n := w.noteList(); len(n) != 0 {
		t.Fatalf("a dropped session is not watched: %q", n)
	}
}
