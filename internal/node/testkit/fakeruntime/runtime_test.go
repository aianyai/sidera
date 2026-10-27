package fakeruntime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

var start = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

const (
	limit  = 10 * time.Second
	leaseA = "0123456789abcdef0123456789abcdef"
)

func attempt(n int) string {
	return "00000000-0000-4000-8000-" + strings.Repeat("0", 11) + string(rune('0'+n))
}

// scriptedRunner is the ActionRunner of the tests: it answers every action from a
// function, and can hold an action until the test lets it go.
type scriptedRunner struct {
	mu      sync.Mutex
	calls   []string
	started chan string
	gate    chan struct{} // when not nil an action waits for a value (or the context)
	answer  func(fakeruntime.TurnAction) (fakeruntime.ActionResult, error)
}

func (r *scriptedRunner) RunAction(ctx context.Context, _ fakeruntime.SessionInfo, a fakeruntime.TurnAction) (fakeruntime.ActionResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, a.Kind)
	r.mu.Unlock()
	if r.started != nil {
		select {
		case r.started <- a.Kind:
		case <-ctx.Done():
			return fakeruntime.ActionResult{}, ctx.Err()
		}
	}
	if r.gate != nil {
		select {
		case <-r.gate:
		case <-ctx.Done():
			return fakeruntime.ActionResult{}, ctx.Err()
		}
	}
	if r.answer != nil {
		return r.answer(a)
	}
	return fakeruntime.ActionResult{}, nil
}

func (r *scriptedRunner) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// watcher records the events a session reports to its adapter.
type watcher struct {
	mu      sync.Mutex
	started []fakeruntime.TurnStartedEvent
	ended   []fakeruntime.TurnEndedEvent
	results []fakeruntime.ToolResultEvent
	changed chan struct{}
}

func newWatcher() *watcher { return &watcher{changed: make(chan struct{}, 64)} }

func (w *watcher) note() {
	select {
	case w.changed <- struct{}{}:
	default:
	}
}

func (w *watcher) TurnStarted(e fakeruntime.TurnStartedEvent) {
	w.mu.Lock()
	w.started = append(w.started, e)
	w.mu.Unlock()
	w.note()
}

func (w *watcher) TurnEnded(e fakeruntime.TurnEndedEvent) {
	w.mu.Lock()
	w.ended = append(w.ended, e)
	w.mu.Unlock()
	w.note()
}

func (w *watcher) ToolResult(e fakeruntime.ToolResultEvent) {
	w.mu.Lock()
	w.results = append(w.results, e)
	w.mu.Unlock()
	w.note()
}

func (w *watcher) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for {
		w.mu.Lock()
		ok := cond()
		w.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		select {
		case <-w.changed:
		case <-time.After(5 * time.Millisecond):
		}
	}
}

type env struct {
	t      *testing.T
	clock  *clocktest.Fake
	mem    *ledger.Memory
	dir    string
	rt     *fakeruntime.Runtime
	runner *scriptedRunner
	// holdFirst leaves the plan of the first turn as the Config gave it.
	holdFirst bool
}

func newEnv(t *testing.T, mutate func(*fakeruntime.Config)) *env {
	t.Helper()
	leakGuard(t)
	e := &env{t: t, clock: clocktest.NewFake(start), mem: ledger.NewMemory(), dir: t.TempDir(), runner: &scriptedRunner{}}
	// Most tests drive the turns of a session by hand, so by default the first
	// prompt waits for the script; create hands the session the plan of an ordinary
	// turn once it has a watcher. A test of the first turn itself sets FirstTurn.
	cfg := fakeruntime.Config{Now: e.clock.Now, Ledgers: e.mem, Dir: e.dir, Seed: 9, Runner: e.runner, FirstTurn: fakeruntime.TurnPlan{ManualStart: true}}
	if mutate != nil {
		mutate(&cfg)
	}
	rt, err := fakeruntime.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.rt = rt
	t.Cleanup(func() { _ = rt.Close() })
	return e
}

func (e *env) create(n int, endpoint string) (fakeruntime.Session, *watcher) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	s, err := e.rt.Create(ctx, fakeruntime.CreateRequest{Attempt: attempt(n), Endpoint: endpoint, Preset: "default", FirstAttempt: attempt(n), FirstPrompt: "hello", Env: map[string]string{"SIDERA_TOKEN": "t"}})
	if err != nil {
		e.t.Fatalf("Create: %v", err)
	}
	w := newWatcher()
	s.Watch(w)
	if e.holdFirst {
		return s, w
	}
	if err := e.rt.SetPlan(s.Ref(), fakeruntime.TurnPlan{}); err != nil {
		e.t.Fatal(err)
	}
	return s, w
}

func (e *env) log() *ledger.Log {
	e.t.Helper()
	l, err := e.mem.Log()
	if err != nil {
		e.t.Fatal(err)
	}
	return l
}

func (e *env) deliver(s fakeruntime.Session, n int, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	return s.Deliver(ctx, attempt(n), text)
}

func ledgerDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// nativeRef is the reference of the session the fake runtime makes for an
// attempt: written here independently, from the documented derivation, not taken
// from the code under test.
func nativeRef(seed uint64, attemptID string) string {
	b := []byte{byte(seed >> 56), byte(seed >> 48), byte(seed >> 40), byte(seed >> 32), byte(seed >> 24), byte(seed >> 16), byte(seed >> 8), byte(seed)}
	sum := sha256.Sum256(append(b, attemptID...))
	return "n-" + hex.EncodeToString(sum[:4])
}

func TestCreateIsDeterministicAndIdempotentPerAttempt(t *testing.T) {
	e := newEnv(t, nil)
	s1, _ := e.create(1, "sess-1")
	if want := nativeRef(9, attempt(1)); s1.Ref() != want {
		t.Fatalf("Ref() = %s, want %s derived from the seed and the attempt", s1.Ref(), want)
	}
	again, _ := e.create(1, "sess-1")
	if again.Ref() != s1.Ref() {
		t.Fatalf("the same attempt gave a second session %s", again.Ref())
	}
	s2, _ := e.create(2, "sess-2")
	if s2.Ref() == s1.Ref() {
		t.Fatal("two attempts share a native reference")
	}
	created := e.log().Kind(ledger.KindRuntimeSessionCreated)
	if len(created) != 2 {
		t.Fatalf("%d runtime.session.created entries, want one per attempt (2)", len(created))
	}
	for _, c := range created {
		if !strings.HasPrefix(c.Writer, "runtime/n-") || c.Attrs.NativeRef == "" {
			t.Errorf("entry %+v", c)
		}
	}
	if s1.PID() < 1 || s1.StartKey() == "" || s1.PID() == s2.PID() {
		t.Errorf("process identity: pid %d key %q (other pid %d)", s1.PID(), s1.StartKey(), s2.PID())
	}
}

func TestPromptStartsATurnAtOnceUnlessTheScriptHoldsItBack(t *testing.T) {
	e := newEnv(t, nil)
	s, w := e.create(1, "sess-1")
	if err := e.deliver(s, 2, "go"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "turn.started", func() bool { return len(w.started) == 1 })
	if w.started[0].Attempt != attempt(2) || w.started[0].AtMS != start.UnixMilli() {
		t.Fatalf("turn start %+v", w.started[0])
	}
	if turn, active := s.TurnActive(); !active || turn != w.started[0].Turn {
		t.Fatalf("TurnActive() = %q, %v", turn, active)
	}

	// The next prompt waits for the script: a plan that holds the start back.
	if err := e.rt.EndTurn(s.Ref(), fakeruntime.FinalCompleted); err != nil {
		t.Fatal(err)
	}
	if err := e.rt.SetPlan(s.Ref(), fakeruntime.TurnPlan{ManualStart: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.deliver(s, 3, "wait for it"); err != nil {
		t.Fatal(err)
	}
	if _, active := s.TurnActive(); active {
		t.Fatal("a turn started although the plan holds the start back")
	}
	if err := e.rt.StartTurn(s.Ref()); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "second turn.started", func() bool { return len(w.started) == 2 })
	if w.started[1].Attempt != attempt(3) {
		t.Fatalf("the scripted turn belongs to attempt %q, want the prompt that was waiting: %s", w.started[1].Attempt, attempt(3))
	}
}

func TestTurnsEndOnlyWhenTheScriptSaysSoAndTimeNeverEndsOne(t *testing.T) {
	e := newEnv(t, nil)
	s, w := e.create(1, "sess-1")
	if err := e.deliver(s, 2, "go"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "turn.started", func() bool { return len(w.started) == 1 })
	e.clock.Advance(48 * time.Hour)
	time.Sleep(50 * time.Millisecond)
	if _, active := s.TurnActive(); !active {
		t.Fatal("a turn ended because time passed")
	}
	if err := e.rt.EndTurn(s.Ref(), fakeruntime.FinalCompleted); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "turn.ended", func() bool { return len(w.ended) == 1 })
	if w.ended[0].Final != fakeruntime.FinalCompleted || w.ended[0].AtMS != start.Add(48*time.Hour).UnixMilli() {
		t.Fatalf("turn end %+v: it carries the test clock at the moment of the script's call", w.ended[0])
	}
	if err := e.rt.EndTurn(s.Ref(), fakeruntime.FinalCompleted); !errors.Is(err, fakeruntime.ErrNoTurn) {
		t.Fatalf("ending a turn that is not running: %v", err)
	}
}

func TestPromptDuringATurnIsRefusedAsBusy(t *testing.T) {
	e := newEnv(t, nil)
	s, w := e.create(1, "sess-1")
	if err := e.deliver(s, 2, "go"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "turn.started", func() bool { return len(w.started) == 1 })
	if err := e.deliver(s, 3, "while busy"); !errors.Is(err, fakeruntime.ErrBusy) {
		t.Fatalf("Deliver during a turn = %v, want ErrBusy", err)
	}
	if n := len(e.log().Kind(ledger.KindRuntimePromptReceived)); n != 2 {
		// The first prompt of the session (create) and the one that started the turn.
		t.Fatalf("%d runtime.prompt.received entries; a refused prompt must leave none (want 2)", n)
	}
}

func TestPromptIsDeliveredAtMostOncePerAttempt(t *testing.T) {
	e := newEnv(t, nil)
	s, _ := e.create(1, "sess-1")
	if err := e.rt.SetPlan(s.Ref(), fakeruntime.TurnPlan{ManualStart: true}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := e.deliver(s, 2, "same attempt"); err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}
	got := 0
	for _, p := range e.log().Kind(ledger.KindRuntimePromptReceived) {
		if p.Attrs.Attempt == attempt(2) {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("attempt 2 is in the ledger %d times, want 1", got)
	}
}

func TestEveryPromptIsRecordedWithItsDigestAndNeverItsText(t *testing.T) {
	e := newEnv(t, nil)
	s, _ := e.create(1, "sess-1")
	if err := e.rt.SetPlan(s.Ref(), fakeruntime.TurnPlan{ManualStart: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.deliver(s, 2, "a secret prompt text"); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range e.log().Kind(ledger.KindRuntimePromptReceived) {
		if p.Attrs.Attempt == attempt(2) {
			found = true
			if p.Attrs.TextDigest != ledgerDigest("a secret prompt text") {
				t.Errorf("digest %s", p.Attrs.TextDigest)
			}
		}
		line, _ := ledger.Encode(p)
		if bytes.Contains(line, []byte("secret")) {
			t.Errorf("the ledger holds the prompt text: %s", line)
		}
	}
	if !found {
		t.Fatal("no entry for the prompt")
	}
}

func TestActionsRunInOrderAndIdleMeansNoneIsUnfinished(t *testing.T) {
	e := newEnv(t, nil)
	e.runner.started = make(chan string)
	e.runner.gate = make(chan struct{})
	s, w := e.create(1, "sess-1")
	if err := e.rt.SetPlan(s.Ref(), fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{
		{Kind: fakeruntime.ActionRecv}, {Kind: fakeruntime.ActionAck}, {Kind: fakeruntime.ActionReply},
	}}); err != nil {
		t.Fatal(err)
	}
	if !e.rt.Idle() {
		t.Fatal("not idle before anything was asked")
	}
	if err := e.deliver(s, 2, "go"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "turn.started", func() bool { return len(w.started) == 1 })
	for i, want := range []string{"recv", "ack", "reply"} {
		select {
		case got := <-e.runner.started:
			if got != want {
				t.Fatalf("action %d is %s, want %s", i, got, want)
			}
		case <-time.After(limit):
			t.Fatalf("action %d never started", i)
		}
		if e.rt.Idle() {
			t.Fatalf("the runtime is idle while action %d is running", i)
		}
		e.runner.gate <- struct{}{}
	}
	deadline := time.Now().Add(limit)
	for !e.rt.Idle() {
		if time.Now().After(deadline) {
			t.Fatal("never idle after the last action")
		}
		time.Sleep(time.Millisecond)
	}
	if _, active := s.TurnActive(); !active {
		t.Fatal("the turn ended by itself after its last action; only EndAfter or the script ends it")
	}
}

// A fixture's quiet compares the count between two looks, so a turn whose actions began
// and finished in between must change it even though the runtime is idle on both.
func TestActivityCountsTheBeginAndEndOfATurnsActions(t *testing.T) {
	e := newEnv(t, nil)
	e.runner.started = make(chan string)
	e.runner.gate = make(chan struct{})
	s, w := e.create(1, "sess-1")
	if err := e.rt.SetPlan(s.Ref(), fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionAck}}}); err != nil {
		t.Fatal(err)
	}
	idle, before := e.rt.Activity()
	if !idle {
		t.Fatal("not idle before anything was asked")
	}
	if err := e.deliver(s, 2, "go"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "turn.started", func() bool { return len(w.started) == 1 })
	<-e.runner.started
	if idle, moves := e.rt.Activity(); idle || moves <= before {
		t.Fatalf("Activity() = %v, %d while an action runs, want not idle and a count above %d", idle, moves, before)
	}
	_, running := e.rt.Activity()
	e.runner.gate <- struct{}{}
	deadline := time.Now().Add(limit)
	for {
		idle, moves := e.rt.Activity()
		if idle {
			if moves <= running {
				t.Fatalf("the count is %d after the actions finished, want it above %d", moves, running)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("never idle after the last action")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestEndAfterEndsTheTurnWhenTheLastActionIsDone(t *testing.T) {
	e := newEnv(t, nil)
	s, w := e.create(1, "sess-1")
	if err := e.rt.SetPlan(s.Ref(), fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionAck}}, EndAfter: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.deliver(s, 2, "go"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "turn.ended", func() bool { return len(w.ended) == 1 })
	if w.ended[0].Final != fakeruntime.FinalCompleted {
		t.Fatalf("final %q", w.ended[0].Final)
	}
	if got := e.runner.kinds(); len(got) != 1 || got[0] != "ack" {
		t.Fatalf("runner calls %v", got)
	}
}

func pageWith(lease string, msgs ...adapterproto.PageMessage) []byte {
	b, _ := json.Marshal(adapterproto.RecvPage{Lease: adapterproto.LeaseRef(lease), Messages: msgs})
	return b
}

// TestRecvActionRecordsWhatTheModelSawAndReportsTheToolResult: for each message
// of a page the runtime writes what the model saw; the page text itself goes to
// the session record and to the adapter as the observed tool result.
func TestRecvActionRecordsWhatTheModelSawAndReportsTheToolResult(t *testing.T) {
	page := pageWith(leaseA,
		adapterproto.PageMessage{ID: "m1", From: "alice/agent", Body: "first body"},
		adapterproto.PageMessage{ID: "m2", From: "alice/agent", PossiblyDuplicate: true, Body: "second body", Part: &adapterproto.PagePart{Index: 1, Total: 2, Label: "part 1/2", Next: &adapterproto.PageNext{Msg: "m2", Part: 2}}},
	)
	e := newEnv(t, nil)
	e.runner.answer = func(a fakeruntime.TurnAction) (fakeruntime.ActionResult, error) {
		return fakeruntime.ActionResult{Output: page}, nil
	}
	s, w := e.create(1, "sess-1")
	if err := e.rt.SetPlan(s.Ref(), fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionRecv, Args: map[string]string{"frontend": "mcp"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := e.deliver(s, 2, "go"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "the tool result", func() bool { return len(w.results) == 1 })
	if r := w.results[0]; r.Lease != leaseA || r.Frontend != "mcp" || r.Text != string(page) {
		t.Fatalf("tool result %+v", r)
	}
	var saw []ledger.Entry
	for _, en := range e.log().Kind(ledger.KindModelSaw) {
		saw = append(saw, en)
	}
	if len(saw) != 2 {
		t.Fatalf("%d model.saw entries, want one per message", len(saw))
	}
	first, second := saw[0], saw[1]
	if first.Writer != "runtime/"+s.Ref() || first.Attrs.Endpoint != "sess-1" || first.Attrs.MessageID != "m1" ||
		first.Attrs.From != "alice/agent" || first.Attrs.BodyDigest != ledgerDigest("first body") {
		t.Errorf("first entry %+v", first.Attrs)
	}
	if first.Attrs.PossiblyDuplicate != nil && *first.Attrs.PossiblyDuplicate {
		t.Error("the first message is marked as a possible duplicate")
	}
	if second.Attrs.PossiblyDuplicate == nil || !*second.Attrs.PossiblyDuplicate || second.Attrs.Part != 1 || second.Attrs.Parts != 2 {
		t.Errorf("second entry %+v: it must carry the duplicate mark and its part 1 of 2", second.Attrs)
	}
	for _, en := range saw {
		line, _ := ledger.Encode(en)
		if bytes.Contains(line, []byte("body\"")) || bytes.Contains(line, []byte("first body")) {
			t.Errorf("a body is in the ledger: %s", line)
		}
	}
}

func TestSystemFeedbackIsTaggedByTheClassifier(t *testing.T) {
	page := pageWith(leaseA,
		adapterproto.PageMessage{ID: "m1", From: "node/system", Body: "delivery d1 was rejected"},
		adapterproto.PageMessage{ID: "m2", From: "alice/agent", Body: "plain"},
	)
	e := newEnv(t, func(c *fakeruntime.Config) {
		c.Feedback = func(from, body string) (string, bool) {
			if from == "node/system" {
				return "rejected", true
			}
			return "", false
		}
	})
	e.runner.answer = func(fakeruntime.TurnAction) (fakeruntime.ActionResult, error) {
		return fakeruntime.ActionResult{Output: page}, nil
	}
	s, w := e.create(1, "sess-1")
	_ = e.rt.SetPlan(s.Ref(), fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionRecv}}})
	if err := e.deliver(s, 2, "go"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "the tool result", func() bool { return len(w.results) == 1 })
	saw := e.log().Kind(ledger.KindModelSaw)
	if len(saw) != 2 || saw[0].Attrs.Feedback != "rejected" || saw[1].Attrs.Feedback != "" {
		t.Fatalf("feedback tags: %+v", saw)
	}
}

func TestInterruptEndsARunningTurnAndAnIdleOneIsNotRunning(t *testing.T) {
	e := newEnv(t, nil)
	e.runner.started = make(chan string, 1)
	e.runner.gate = make(chan struct{}) // the action never finishes by itself
	s, w := e.create(1, "sess-1")
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	if res, err := s.Interrupt(ctx); err != nil || res != fakeruntime.InterruptNotRunning {
		t.Fatalf("Interrupt with no turn = %v, %v", res, err)
	}
	_ = e.rt.SetPlan(s.Ref(), fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionRecv}}})
	if err := e.deliver(s, 2, "go"); err != nil {
		t.Fatal(err)
	}
	<-e.runner.started
	if res, err := s.Interrupt(ctx); err != nil || res != fakeruntime.InterruptStopped {
		t.Fatalf("Interrupt of a running turn = %v, %v", res, err)
	}
	w.waitFor(t, "turn.ended", func() bool { return len(w.ended) == 1 })
	if w.ended[0].Final != fakeruntime.FinalInterrupted {
		t.Fatalf("final %q, want interrupted", w.ended[0].Final)
	}
	if !waitIdle(e.rt) {
		t.Fatal("an interrupted turn left an action running")
	}
	if got := e.log().Kind(ledger.KindRuntimeInterrupted); len(got) != 1 {
		t.Fatalf("%d runtime.interrupted entries", len(got))
	}
}

func waitIdle(rt *fakeruntime.Runtime) bool {
	deadline := time.Now().Add(limit)
	for !rt.Idle() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	return true
}

func TestCloseReleasesTheSessionAndEndsARunningTurn(t *testing.T) {
	e := newEnv(t, nil)
	s, w := e.create(1, "sess-1")
	if err := e.deliver(s, 2, "go"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "turn.started", func() bool { return len(w.started) == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Alive() {
		t.Fatal("a closed session is alive")
	}
	if got := e.log().Kind(ledger.KindRuntimeClosed); len(got) != 1 {
		t.Fatalf("%d runtime.closed entries", len(got))
	}
	if err := e.deliver(s, 3, "late"); !errors.Is(err, fakeruntime.ErrNotAlive) {
		t.Fatalf("Deliver to a closed session = %v", err)
	}
	if s2, ok := e.rt.Find(s.Ref()); !ok || s2.Alive() {
		t.Fatalf("Find after close: %v, alive %v", ok, ok && s2.Alive())
	}
}

func TestKilledNativeStopsReportingAndLeavesTheRecordReadable(t *testing.T) {
	e := newEnv(t, nil)
	s, w := e.create(1, "sess-1")
	if err := e.deliver(s, 2, "go"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "turn.started", func() bool { return len(w.started) == 1 })
	if err := e.rt.KillNative(s.Ref()); err != nil {
		t.Fatal(err)
	}
	if s.Alive() {
		t.Fatal("a killed session is alive")
	}
	if _, active := s.TurnActive(); active {
		t.Fatal("a killed session still has an active turn")
	}
	if got := e.log().Kind(ledger.KindRuntimeKilled); len(got) != 1 {
		t.Fatalf("%d runtime.killed entries", len(got))
	}
	// The process cannot end its turn, but the adapter that is online sees it die,
	// and every turn has exactly one end: it is reported once, as failed.
	w.mu.Lock()
	ended := append([]fakeruntime.TurnEndedEvent(nil), w.ended...)
	w.mu.Unlock()
	if len(ended) != 1 || ended[0].Final != fakeruntime.FinalFailed || ended[0].Turn != w.started[0].Turn {
		t.Fatalf("turn ends after the kill: %+v, want the running turn %s ended once as failed", ended, w.started[0].Turn)
	}
	if err := e.deliver(s, 3, "late"); !errors.Is(err, fakeruntime.ErrNotAlive) {
		t.Fatalf("Deliver to a killed session = %v", err)
	}
	rec, err := s.Reconcile(attempt(2))
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Found || !rec.WriterStopped || rec.TurnState != fakeruntime.FinalFailed {
		t.Fatalf("Reconcile of a delivered attempt on a dead session = %+v, want its turn failed", rec)
	}
	raw, err := os.ReadFile(filepath.Join(e.dir, s.Ref()+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := fakeruntime.ParseRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Kind == fakeruntime.EventTurnEnded {
			t.Fatalf("the record of a killed process holds a turn end: %+v; a dead writer writes nothing", ev)
		}
	}
	rec, err = s.Reconcile(attempt(7))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Found || !rec.WriterStopped {
		t.Fatalf("Reconcile of an attempt that never arrived on a dead session = %+v", rec)
	}
}

func TestReconcileReadsTheRecord(t *testing.T) {
	e := newEnv(t, nil)
	s, w := e.create(1, "sess-1")
	rows := []struct {
		name  string
		setup func()
		id    int
		want  fakeruntime.Reconciliation
	}{
		{"the first prompt of the session is in the record", func() {}, 1, fakeruntime.Reconciliation{Found: true}},
		{"an attempt that never arrived, with the session idle", func() {}, 9, fakeruntime.Reconciliation{}},
		{"a prompt whose turn is running", func() {
			_ = e.deliver(s, 2, "go")
			w.waitFor(t, "turn.started", func() bool { return len(w.started) == 1 })
		}, 2, fakeruntime.Reconciliation{Found: true, TurnState: "running"}},
		{"an attempt that never arrived, with a turn running", func() {}, 9, fakeruntime.Reconciliation{TurnRunning: true}},
		{"a prompt whose turn ended", func() {
			_ = e.rt.EndTurn(s.Ref(), fakeruntime.FinalCompleted)
		}, 2, fakeruntime.Reconciliation{Found: true, TurnState: "completed"}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			r.setup()
			got, err := s.Reconcile(attempt(r.id))
			if err != nil {
				t.Fatal(err)
			}
			got.Turn = "" // the reference of a turn is the runtime's own
			if got != r.want {
				t.Fatalf("Reconcile = %+v, want %+v", got, r.want)
			}
		})
	}
}

func TestRecordTimesComeFromTheTestClock(t *testing.T) {
	e := newEnv(t, nil)
	e.clock.Advance(7 * time.Minute)
	s, w := e.create(1, "sess-1")
	if err := e.deliver(s, 2, "go"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "turn.started", func() bool { return len(w.started) == 1 })
	raw, err := os.ReadFile(filepath.Join(e.dir, s.Ref()+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := fakeruntime.ParseRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 3 {
		t.Fatalf("%d events in the record", len(events))
	}
	for _, ev := range events {
		if ev.AtMS != start.Add(7*time.Minute).UnixMilli() {
			t.Errorf("event %+v is not stamped with the test clock", ev)
		}
	}
	for _, en := range e.log().Entries() {
		if en.AtMS != start.Add(7*time.Minute).UnixMilli() {
			t.Errorf("ledger entry %+v is not stamped with the test clock", en)
		}
	}
}

func TestNewRefusesAnIncompleteConfig(t *testing.T) {
	good := fakeruntime.Config{Now: func() time.Time { return start }, Ledgers: ledger.NewMemory(), Dir: t.TempDir()}
	rows := map[string]func(*fakeruntime.Config){
		"a runtime without the test clock":     func(c *fakeruntime.Config) { c.Now = nil },
		"a runtime without a ledger":           func(c *fakeruntime.Config) { c.Ledgers = nil },
		"a runtime without a record directory": func(c *fakeruntime.Config) { c.Dir = "" },
	}
	for name, f := range rows {
		t.Run(name, func(t *testing.T) {
			c := good
			f(&c)
			if rt, err := fakeruntime.New(c); err == nil {
				_ = rt.Close()
				t.Fatal("New accepted it")
			}
		})
	}
}

func TestUnknownSessionIsReported(t *testing.T) {
	e := newEnv(t, nil)
	for name, err := range map[string]error{
		"SetPlan":    e.rt.SetPlan("nope", fakeruntime.TurnPlan{}),
		"StartTurn":  e.rt.StartTurn("nope"),
		"EndTurn":    e.rt.EndTurn("nope", fakeruntime.FinalCompleted),
		"KillNative": e.rt.KillNative("nope"),
	} {
		if !errors.Is(err, fakeruntime.ErrUnknownSession) {
			t.Errorf("%s of an unknown session = %v", name, err)
		}
	}
	if _, ok := e.rt.Find("nope"); ok {
		t.Error("Find invented a session")
	}
}

func TestACreateWhoseContextEndedMakesNoSession(t *testing.T) {
	e := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.rt.Create(ctx, fakeruntime.CreateRequest{Attempt: attempt(1), Endpoint: "sess-1", Preset: "default", FirstAttempt: attempt(1), FirstPrompt: "x"}); err == nil {
		t.Fatal("Create with a finished context succeeded")
	}
	if n := len(e.log().Kind(ledger.KindRuntimeSessionCreated)); n != 0 {
		t.Fatalf("%d sessions were recorded", n)
	}
}

func leakGuard(t *testing.T) {
	t.Helper()
	check := func() []string {
		deadline := time.Now().Add(5 * time.Second)
		for {
			var buf bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
			var found []string
			for _, block := range strings.Split(buf.String(), "\n\n") {
				if strings.Contains(block, "testkit/fakeruntime.") && !strings.Contains(block, "_test.go") {
					found = append(found, block)
				}
			}
			if len(found) == 0 || time.Now().After(deadline) {
				return found
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if left := check(); len(left) != 0 {
		t.Fatalf("goroutines of the package alive before the test:\n%s", strings.Join(left, "\n\n"))
	}
	t.Cleanup(func() {
		if left := check(); len(left) != 0 {
			t.Errorf("%d goroutines leaked:\n%s", len(left), strings.Join(left, "\n\n"))
		}
	})
}

// TestAnExecActionThatNamesAFrontendIsAReceive: in the binary layer the model reads
// by running the real client, so the action is a command line; naming the front end
// says that its output is what a receive printed, and it is recorded as one.
func TestAnExecActionThatNamesAFrontendIsAReceive(t *testing.T) {
	page := pageWith(leaseA, adapterproto.PageMessage{ID: "m1", From: "alice/agent", Body: "read by a command"})
	e := newEnv(t, nil)
	e.runner.answer = func(fakeruntime.TurnAction) (fakeruntime.ActionResult, error) {
		return fakeruntime.ActionResult{Output: page}, nil
	}
	s, w := e.create(1, "sess-1")
	_ = e.rt.SetPlan(s.Ref(), fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{
		{Kind: fakeruntime.ActionExec, Args: map[string]string{"argv": `["sidera","recv"]`, "frontend": "mcp"}},
		{Kind: fakeruntime.ActionExec, Args: map[string]string{"argv": `["sidera","status"]`}},
	}})
	if err := e.deliver(s, 2, "go"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "the tool result", func() bool { return len(w.results) == 1 })
	if r := w.results[0]; r.Lease != leaseA || r.Frontend != "mcp" {
		t.Fatalf("tool result %+v", r)
	}
	if !waitIdle(e.rt) {
		t.Fatal("not idle")
	}
	// The second command printed a page too, but named no front end: it is not a
	// receive, so there is one model.saw entry and one tool result.
	if n := len(e.log().Kind(ledger.KindModelSaw)); n != 1 {
		t.Fatalf("%d model.saw entries, want 1", n)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.results) != 1 {
		t.Fatalf("%d tool results, want 1", len(w.results))
	}
}

// --- the first turn ---

// createUnwatched creates the session of attempt n and does not set a watcher.
func (e *env) createUnwatched(n int) fakeruntime.Session {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	s, err := e.rt.Create(ctx, fakeruntime.CreateRequest{Attempt: attempt(n), Endpoint: "sess-" + string(rune('0'+n)), Preset: "default", FirstAttempt: attempt(n), FirstPrompt: "hello", Env: map[string]string{"SIDERA_TOKEN": "t"}})
	if err != nil {
		e.t.Fatalf("Create: %v", err)
	}
	return s
}

// TestTheFirstPromptStartsTheFirstTurnWhenTheAdapterTakesCharge: a real runtime
// starts working on its first prompt at once. The fake does the same, at the
// moment the adapter that made the session takes charge of it, so that the turn's
// events have somewhere to go; the plan of that turn is the one the runtime was
// given for first turns, and the zero plan starts it automatically.
func TestTheFirstPromptStartsTheFirstTurnWhenTheAdapterTakesCharge(t *testing.T) {
	e := newEnv(t, func(c *fakeruntime.Config) { c.FirstTurn = fakeruntime.TurnPlan{} })
	s := e.createUnwatched(1)
	if _, active := s.TurnActive(); active {
		t.Fatal("a turn started before anyone watched the session: its events would reach nobody")
	}
	w := newWatcher()
	s.Watch(w)
	w.waitFor(t, "the first turn.started", func() bool { return len(w.started) == 1 })
	if got := w.started[0]; got.Attempt != attempt(1) {
		t.Fatalf("the first turn belongs to attempt %q, want the first prompt %s", got.Attempt, attempt(1))
	}
	// Watching again (an adapter that came back) does not start another turn.
	s.Watch(newWatcher())
	if turn, active := s.TurnActive(); !active || turn != w.started[0].Turn {
		t.Fatalf("TurnActive() = %q, %v after a second Watch", turn, active)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.started) != 1 {
		t.Fatalf("%d turn starts", len(w.started))
	}
}

func TestTheFirstTurnTakesItsPlanFromTheConfigAndAManualPlanHoldsItBack(t *testing.T) {
	page := pageWith(leaseA, adapterproto.PageMessage{ID: "m1", From: "alice/agent", Body: "b"})
	e := newEnv(t, func(c *fakeruntime.Config) {
		c.FirstTurn = fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionRecv}}, EndAfter: true}
	})
	e.runner.answer = func(fakeruntime.TurnAction) (fakeruntime.ActionResult, error) {
		return fakeruntime.ActionResult{Output: page}, nil
	}
	s := e.createUnwatched(1)
	w := newWatcher()
	s.Watch(w)
	w.waitFor(t, "the first turn to end", func() bool { return len(w.ended) == 1 })
	if got := e.runner.kinds(); len(got) != 1 || got[0] != "recv" {
		t.Fatalf("actions of the first turn: %v", got)
	}

	// A plan for the sessions created from now on; the session that exists is not
	// affected, and a manual plan leaves the start to the script.
	if err := e.rt.SetFirstTurn(fakeruntime.TurnPlan{ManualStart: true}); err != nil {
		t.Fatal(err)
	}
	s2 := e.createUnwatched(2)
	w2 := newWatcher()
	s2.Watch(w2)
	time.Sleep(50 * time.Millisecond)
	if _, active := s2.TurnActive(); active {
		t.Fatal("a manual first plan did not hold the first turn back")
	}
	if err := e.rt.StartTurn(s2.Ref()); err != nil {
		t.Fatal(err)
	}
	w2.waitFor(t, "the scripted first turn", func() bool { return len(w2.started) == 1 })
	if err := e.rt.SetFirstTurn(fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: "dance"}}}); err == nil {
		t.Fatal("a plan with an unknown action was accepted")
	}
}

// --- what the model saw ---

// TestARecvPartActionIsAReceive: a part of a page the model asked for is a page
// like any other: what it saw is recorded and the adapter may observe it.
func TestARecvPartActionIsAReceive(t *testing.T) {
	page := pageWith(leaseA, adapterproto.PageMessage{ID: "m1", From: "alice/agent", Body: "second part", Part: &adapterproto.PagePart{Index: 2, Total: 2, Label: "part 2/2"}})
	e := newEnv(t, nil)
	e.runner.answer = func(fakeruntime.TurnAction) (fakeruntime.ActionResult, error) {
		return fakeruntime.ActionResult{Output: page}, nil
	}
	s, w := e.create(1, "sess-1")
	_ = e.rt.SetPlan(s.Ref(), fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionRecvPart, Args: map[string]string{"frontend": "mcp"}}}})
	if err := e.deliver(s, 2, "go"); err != nil {
		t.Fatal(err)
	}
	w.waitFor(t, "the tool result", func() bool { return len(w.results) == 1 })
	if r := w.results[0]; r.Lease != leaseA || r.Frontend != "mcp" {
		t.Fatalf("tool result %+v", r)
	}
	saw := e.log().Kind(ledger.KindModelSaw)
	if len(saw) != 1 || saw[0].Attrs.MessageID != "m1" || saw[0].Attrs.Part != 2 || saw[0].Attrs.Parts != 2 {
		t.Fatalf("model.saw entries %+v, want the one message, part 2 of 2", saw)
	}
}

// TestAReceiveThatPrintsNoPageIsAnErrorInTheRecord: the model was shown some text
// in answer to a receive; if it cannot be read as a page nothing is recorded as
// seen and nothing is reported to the adapter, and the record says why, instead of
// the receive leaving no trace at all.
func TestAReceiveThatPrintsNoPageIsAnErrorInTheRecord(t *testing.T) {
	rows := []struct {
		name   string
		action fakeruntime.TurnAction
	}{
		{"a recv", fakeruntime.TurnAction{Kind: fakeruntime.ActionRecv}},
		{"a recv_part", fakeruntime.TurnAction{Kind: fakeruntime.ActionRecvPart}},
		{"an exec that names a front end", fakeruntime.TurnAction{Kind: fakeruntime.ActionExec, Args: map[string]string{"argv": `["sidera","recv"]`, "frontend": "cli"}}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			e := newEnv(t, nil)
			e.runner.answer = func(fakeruntime.TurnAction) (fakeruntime.ActionResult, error) {
				return fakeruntime.ActionResult{Output: []byte("error: the daemon is not running\n")}, nil
			}
			s, w := e.create(1, "sess-1")
			_ = e.rt.SetPlan(s.Ref(), fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{r.action}})
			if err := e.deliver(s, 2, "go"); err != nil {
				t.Fatal(err)
			}
			if !waitIdle(e.rt) {
				t.Fatal("not idle")
			}
			raw, err := os.ReadFile(filepath.Join(e.dir, s.Ref()+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			events, err := fakeruntime.ParseRecord(raw)
			if err != nil {
				t.Fatal(err)
			}
			var results []fakeruntime.Event
			for _, ev := range events {
				if ev.Kind == fakeruntime.EventToolResult {
					results = append(results, ev)
				}
			}
			if len(results) != 1 || results[0].Error == "" || !strings.Contains(results[0].Text, "daemon is not running") {
				t.Fatalf("tool results in the record: %+v, want one with an error and the text the model saw", results)
			}
			if n := len(e.log().Kind(ledger.KindModelSaw)); n != 0 {
				t.Fatalf("%d model.saw entries for output that is not a page", n)
			}
			w.mu.Lock()
			defer w.mu.Unlock()
			if len(w.results) != 0 {
				t.Fatalf("%d tool results reported to the adapter for output that is not a page", len(w.results))
			}
		})
	}
}

// --- a killed process ---

// TestAKilledRuntimeWithNobodyWatchingReconcilesItsTurnAsFailed: no adapter is
// there to end the turn, and the turn is not left running.
func TestAKilledRuntimeWithNobodyWatchingReconcilesItsTurnAsFailed(t *testing.T) {
	e := newEnv(t, func(c *fakeruntime.Config) { c.FirstTurn = fakeruntime.TurnPlan{} })
	s := e.createUnwatched(1)
	if err := e.rt.StartTurn(s.Ref()); err != nil {
		t.Fatal(err)
	}
	if err := e.rt.KillNative(s.Ref()); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Reconcile(attempt(1))
	if err != nil || rec.TurnState != fakeruntime.FinalFailed || !rec.WriterStopped {
		t.Fatalf("Reconcile = %+v, %v; want the turn failed", rec, err)
	}
}

func TestCreatedNamesTheEntryTheRuntimeWroteForTheSession(t *testing.T) {
	e := newEnv(t, nil)
	s, _ := e.create(1, "sess-1")
	ref := s.Created()
	if ref.Writer != "runtime/"+s.Ref() {
		t.Fatalf("Created().Writer = %q", ref.Writer)
	}
	var named *ledger.Entry
	for _, en := range e.log().Writer(ref.Writer) {
		if en.Seq == ref.Seq {
			named = &en
		}
	}
	if named == nil || named.Kind != ledger.KindRuntimeSessionCreated || named.Attrs.NativeRef != s.Ref() {
		t.Fatalf("entry %d of %s is %+v, want runtime.session.created for the session", ref.Seq, ref.Writer, named)
	}
}
