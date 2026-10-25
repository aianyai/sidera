package fakeadapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
	"github.com/aianyai/sidera/sdk/go/peer"
)

func countKind(l *ledger.Log, kind string) int { return len(l.Kind(kind)) }

func indexOfKind(l *ledger.Log, kind string) int {
	for i, e := range l.Entries() {
		if e.Kind == kind {
			return i
		}
	}
	return -1
}

// --- the handshake and the instance lock ---

// TestInstanceLockIsTakenBeforeInitializeIsAnswered: a second adapter on one lock
// path is refused, with the protocol's own code, before it can answer; the first
// is not disturbed; and the lock is free again once the first one ended. This is
// what keeps a hung old process and its replacement from controlling sessions at
// once.
func TestInstanceLockIsTakenBeforeInitializeIsAnswered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locks", "instance.lock")
	first := newRig(t, rigOptions{})
	if err := first.initializeAt(path); err != nil {
		t.Fatalf("first adapter: %v", err)
	}
	second := newRig(t, rigOptions{})
	err := second.initializeAt(path)
	if codeOf(err) != adapterproto.CodeInstanceLocked {
		t.Fatalf("second adapter on the same lock: %v, want code %d", err, adapterproto.CodeInstanceLocked)
	}
	if n := countKind(second.ledgerLog(), ledger.KindAdapterLockRefused); n != 1 {
		t.Fatalf("%d adapter.lock.refused entries, want 1", n)
	}
	if n := countKind(second.ledgerLog(), ledger.KindAdapterLockAcquired); n != 0 {
		t.Fatalf("the refused adapter wrote adapter.lock.acquired")
	}
	if _, err := first.call(adapterproto.MethodPing, nil); err != nil {
		t.Fatalf("the first adapter was disturbed: %v", err)
	}
	if err := first.disconnect(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve returned %v", err)
	}
	third := newRig(t, rigOptions{})
	if err := third.initializeAt(path); err != nil {
		t.Fatalf("the lock was not released when the first adapter ended: %v", err)
	}
}

func TestRequestsBeforeInitializeAreRefusedExceptPing(t *testing.T) {
	r := newRig(t, rigOptions{})
	if _, err := r.call(adapterproto.MethodPing, nil); err != nil {
		t.Fatalf("ping before initialize: %v", err)
	}
	for _, m := range []string{adapterproto.MethodAdapterStart, adapterproto.MethodSessionClose, adapterproto.MethodShutdown} {
		if _, err := r.call(m, map[string]any{}); codeOf(err) != peer.CodeNotInitialized {
			t.Errorf("%s before initialize: %v, want %d", m, err, peer.CodeNotInitialized)
		}
	}
}

func TestRequestsAfterShutdownAreRefusedExceptPing(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.ready()
	r.mustCall(adapterproto.MethodShutdown, nil, nil)
	if _, err := r.closeSession("sess-1"); codeOf(err) != peer.CodeShuttingDown {
		t.Fatalf("session.close after shutdown: %v, want %d", err, peer.CodeShuttingDown)
	}
	if _, err := r.call(adapterproto.MethodPing, nil); err != nil {
		t.Fatalf("ping after shutdown: %v", err)
	}
}

func TestInitializeRefusals(t *testing.T) {
	good := func() map[string]any {
		return map[string]any{
			"protocol": map[string]int{"min": 1, "max": 1}, "daemon": map[string]string{"version": "0.1.0"},
			"instance":     map[string]string{"id": "fake", "lock_path": filepath.Join(t.TempDir(), "i.lock")},
			"capabilities": map[string]any{},
		}
	}
	rows := []struct {
		rule   string
		mutate func(map[string]any)
		code   int
	}{
		{"no version in common is -32022", func(m map[string]any) { m["protocol"] = map[string]int{"min": 2, "max": 3} }, peer.CodeUnsupportedProtocolVersion},
		{"an unknown member is -32602", func(m map[string]any) { m["extra"] = 1 }, peer.CodeInvalidParams},
		{"a missing member is -32602", func(m map[string]any) { delete(m, "daemon") }, peer.CodeInvalidParams},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			r := newRig(t, rigOptions{})
			p := good()
			row.mutate(p)
			_, err := r.call(adapterproto.MethodInitialize, p)
			if codeOf(err) != row.code {
				t.Fatalf("got %v, want code %d", err, row.code)
			}
			if row.code == peer.CodeUnsupportedProtocolVersion {
				var pe *peer.Error
				_ = errors.As(err, &pe)
				var data adapterproto.UnsupportedVersionData
				if err := adapterproto.Decode(pe.Data, &data); err != nil || data.Requested.Min != 2 || data.Supported != adapterproto.SupportedRange() {
					t.Fatalf("error data %s: %v", pe.Data, err)
				}
			}
		})
	}
	t.Run("a second initialize is -32600", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		r.initialize()
		if err := r.initializeAt(filepath.Join(t.TempDir(), "again.lock")); codeOf(err) != peer.CodeInvalidRequest {
			t.Fatalf("got %v", err)
		}
	})
}

// --- session creation ---

func TestCreateIsIdempotentPerAttemptAndConflictsOnOtherParameters(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.initialize()
	first := r.createSession(1, "sess-1")
	again := r.createSession(1, "sess-1")
	if first.NativeRef != again.NativeRef || first.NativeRef == "" {
		t.Fatalf("a retried attempt gave %q then %q", first.NativeRef, again.NativeRef)
	}
	if n := countKind(r.ledgerLog(), ledger.KindRuntimeSessionCreated); n != 1 {
		t.Fatalf("%d native sessions for one attempt, want 1", n)
	}
	other := r.createParams(1, "sess-other")
	if _, err := r.call(adapterproto.MethodSessionCreate, other); codeOf(err) != adapterproto.CodeAttemptConflict {
		t.Fatalf("the same attempt with another endpoint: %v, want %d", err, adapterproto.CodeAttemptConflict)
	}
	withCheckpoint := r.createParams(1, "sess-1")
	withCheckpoint.Checkpoint = json.RawMessage(`{"phase":"creating"}`)
	if _, err := r.call(adapterproto.MethodSessionCreate, withCheckpoint); err != nil {
		t.Fatalf("a retry that carries the stored checkpoint is the same attempt: %v", err)
	}
}

func TestCreateOutcomes(t *testing.T) {
	rows := []struct {
		rule        string
		outcome     fakeadapter.Outcome
		wantOutcome adapterproto.CreateOutcome
		wantReason  adapterproto.CreateFailure
		wantGrounds adapterproto.GroundsCode
		wantErrCode int
		sessions    int // native sessions the attempt left
	}{
		{"a refused preset leaves no session", fakeadapter.Outcome{Kind: fakeadapter.OutcomeFail, Reason: "preset_unknown"}, adapterproto.CreateFailed, adapterproto.CreateFailPresetUnknown, "", 0, 0},
		{"a runtime that refuses the credentials leaves no session", fakeadapter.Outcome{Kind: fakeadapter.OutcomeFail, Reason: "runtime_auth"}, adapterproto.CreateFailed, adapterproto.CreateFailRuntimeAuth, "", 0, 0},
		{"control_unknown fails the attempt but a native session remains", fakeadapter.Outcome{Kind: fakeadapter.OutcomeFail, Reason: "control_unknown"}, adapterproto.CreateFailed, adapterproto.CreateFailControlUnknown, "", 0, 1},
		{"an unknown outcome has a native session and its grounds", fakeadapter.Outcome{Kind: fakeadapter.OutcomeIndeterminate, Reason: "turn_running"}, adapterproto.CreateIndeterminate, "", adapterproto.GroundsTurnRunning, 0, 1},
		{"an unknown outcome without grounds says the native side is unreachable", fakeadapter.Outcome{Kind: fakeadapter.OutcomeIndeterminate}, adapterproto.CreateIndeterminate, "", adapterproto.GroundsNativeUnreachable, 0, 1},
		{"an error before the effect leaves no session", fakeadapter.Outcome{Kind: fakeadapter.OutcomeError, Code: 1042}, "", "", "", 1042, 0},
		{"an error after the effect leaves the session", fakeadapter.Outcome{Kind: fakeadapter.OutcomeError, Code: -32603, AfterEffect: true}, "", "", "", -32603, 1},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			r := newRig(t, rigOptions{})
			r.initialize()
			if err := r.knobs.QueueCreate(row.outcome); err != nil {
				t.Fatal(err)
			}
			res, err := callDecoded[adapterproto.SessionCreateResult](r, adapterproto.MethodSessionCreate, r.createParams(1, "sess-1"))
			if row.wantErrCode != 0 {
				if codeOf(err) != row.wantErrCode {
					t.Fatalf("error %v, want code %d", err, row.wantErrCode)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if res.Outcome != row.wantOutcome || res.Reason != row.wantReason {
					t.Fatalf("result %+v, want %s/%s", res, row.wantOutcome, row.wantReason)
				}
				if row.wantGrounds != "" && (res.Grounds == nil || res.Grounds.Code != row.wantGrounds) {
					t.Fatalf("grounds %+v, want %s", res.Grounds, row.wantGrounds)
				}
			}
			l := r.ledgerLog()
			if n := countKind(l, ledger.KindRuntimeSessionCreated); n != row.sessions {
				t.Fatalf("%d native sessions, want %d", n, row.sessions)
			}
			if n := countKind(l, ledger.KindKnobHit); n < 1 {
				t.Fatal("the knob left no knob.hit entry: a test could not tell that its injection point was reached")
			}
			// The queued outcome was used once; the next attempt is normal.
			if next := r.createSession(2, "sess-2"); next.Outcome != adapterproto.CreateCreated {
				t.Fatalf("the attempt after a one-shot outcome: %+v", next)
			}
		})
	}
}

func TestStickyOutcomeAppliesToEveryRequestUntilCleared(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.initialize()
	if err := r.knobs.SetCreate(fakeadapter.Outcome{Kind: fakeadapter.OutcomeFail, Reason: "native_unavailable"}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if res := r.createSession(i, "sess-x"+string(rune('0'+i))); res.Reason != adapterproto.CreateFailNativeUnavailable {
			t.Fatalf("attempt %d: %+v", i, res)
		}
	}
	r.knobs.ClearOutcomes()
	if res := r.createSession(4, "sess-x4"); res.Outcome != adapterproto.CreateCreated {
		t.Fatalf("after ClearOutcomes: %+v", res)
	}
}

func TestOutcomeKindsAreCheckedPerRequest(t *testing.T) {
	k := fakeadapter.NewKnobs()
	if err := k.SetCreate(fakeadapter.Outcome{Kind: fakeadapter.OutcomeAccepted}); err == nil {
		t.Error("a creation was given the outcome of a prompt")
	}
	if err := k.SetPrompt(fakeadapter.Outcome{Kind: fakeadapter.OutcomeFail}); err == nil {
		t.Error("a prompt was given the outcome of a creation")
	}
	if err := k.SetCreate(fakeadapter.Outcome{Kind: fakeadapter.OutcomeError}); err == nil {
		t.Error("an error outcome without a code was accepted")
	}
	if err := k.PauseAt("no.such.point"); err == nil {
		t.Error("an unknown pause point was accepted")
	}
	if err := k.SetObserved("garbled"); err == nil {
		t.Error("an unknown observed mode was accepted")
	}
}

// TestCreateWritesTheLedgerBeforeItConfirms: the entry that says the native
// session exists comes before the one that says the adapter returned it, and both
// before the daemon could have heard: "the effect happened" is always on record.
func TestCreateWritesTheLedgerBeforeItConfirms(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.initialize()
	r.createSession(1, "sess-1")
	l := r.ledgerLog()
	created, returned, received := indexOfKind(l, ledger.KindRuntimeSessionCreated), indexOfKind(l, ledger.KindNativeCreateReturned), indexOfKind(l, ledger.KindSessionCreateReceived)
	if !(received >= 0 && received < created && created < returned) {
		t.Fatalf("ledger order: received %d, native session created %d, returned %d", received, created, returned)
	}
}

// TestPauseAtCreateAfterNativeStopsBeforeTheAnswer: the native session exists and
// the adapter has not answered. A connection lost there is the case the daemon
// must survive; a retry of the attempt on a restarted adapter finds the same
// session and makes no second one.
func TestPauseAtCreateAfterNativeStopsBeforeTheAnswer(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.initialize()
	if err := r.knobs.PauseAt(fakeadapter.PauseCreateAfterNative); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		res adapterproto.SessionCreateResult
		err error
	}
	answered := make(chan outcome, 1)
	go func() {
		res, err := callDecoded[adapterproto.SessionCreateResult](r, adapterproto.MethodSessionCreate, r.createParams(1, "sess-1"))
		answered <- outcome{res, err}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if err := r.knobs.WaitPaused(ctx, fakeadapter.PauseCreateAfterNative); err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-answered:
		t.Fatalf("the adapter answered while paused: %+v %v", o.res, o.err)
	case <-time.After(50 * time.Millisecond):
	}
	if held := r.knobs.Held(); len(held) != 1 || held[0] != fakeadapter.PauseCreateAfterNative {
		t.Fatalf("Held() = %v", held)
	}
	l := r.ledgerLog()
	if countKind(l, ledger.KindRuntimeSessionCreated) != 1 || countKind(l, ledger.KindNativeCreateReturned) != 1 {
		t.Fatal("at the pause the native session and its return are not both on record")
	}
	// The connection is lost at the pause; the daemon starts the adapter again and
	// repeats the attempt.
	r.disconnect()
	if o := <-answered; o.err == nil {
		t.Fatal("a call that lost its connection returned a result")
	}
	r.connect()
	r.initialize()
	again := r.createSession(1, "sess-1")
	if again.Outcome != adapterproto.CreateCreated {
		t.Fatalf("retried attempt: %+v", again)
	}
	if n := countKind(r.ledgerLog(), ledger.KindRuntimeSessionCreated); n != 1 {
		t.Fatalf("%d native sessions after the retry, want exactly one for the attempt", n)
	}
}

func TestPauseIsReleasedAndDisarmed(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.initialize()
	_ = r.knobs.PauseAt(fakeadapter.PausePromptAfterForward)
	r.createSession(1, "sess-1")
	done := make(chan error, 1)
	go func() { _, err := r.prompt("sess-1", 2, "go"); done <- err }()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if err := r.knobs.WaitPaused(ctx, fakeadapter.PausePromptAfterForward); err != nil {
		t.Fatal(err)
	}
	r.knobs.Release(fakeadapter.PausePromptAfterForward)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(r.knobs.Held()) != 0 {
		t.Fatalf("Held() = %v after release", r.knobs.Held())
	}
	if err := r.runtime.EndTurn(string(mustRef(t, r, "sess-1")), fakeruntime.FinalCompleted); err != nil {
		t.Fatal(err)
	}
	// Disarmed: the next prompt does not stop.
	if _, err := r.prompt("sess-1", 3, "again"); err != nil {
		t.Fatal(err)
	}
}

func mustRef(t *testing.T, r *rig, _ string) string {
	t.Helper()
	for _, e := range r.ledgerLog().Kind(ledger.KindRuntimeSessionCreated) {
		return e.Attrs.NativeRef
	}
	t.Fatal("no session")
	return ""
}

func TestHeldCreateWaitsForReleaseThenGoesItsWay(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.initialize()
	_ = r.knobs.QueueCreate(fakeadapter.Outcome{Kind: fakeadapter.OutcomeHold})
	done := make(chan adapterproto.SessionCreateResult, 1)
	go func() {
		res, _ := callDecoded[adapterproto.SessionCreateResult](r, adapterproto.MethodSessionCreate, r.createParams(1, "sess-1"))
		done <- res
	}()
	r.waitFor("the hold", func() bool { return len(r.knobs.Held()) == 1 })
	if r.knobs.Held()[0] != adapterproto.MethodSessionCreate {
		t.Fatalf("Held() = %v", r.knobs.Held())
	}
	if countKind(r.ledgerLog(), ledger.KindRuntimeSessionCreated) != 0 {
		t.Fatal("the held request had its effect before it was released")
	}
	r.knobs.ReleaseHeld()
	if res := <-done; res.Outcome != adapterproto.CreateCreated {
		t.Fatalf("after release: %+v", res)
	}
}

// --- prompts ---

func TestPromptOutcomes(t *testing.T) {
	rows := []struct {
		rule      string
		outcome   *fakeadapter.Outcome
		prepare   func(r *rig, ref string)
		wantStage adapterproto.PromptStage
		wantWhy   adapterproto.PromptRefusal
		wantCode  int
		delivered int // prompts that reached the runtime beyond the first prompt
	}{
		{"a prompt is persisted by default", nil, nil, adapterproto.PromptPersisted, "", 0, 1},
		{"a prompt can be only accepted", &fakeadapter.Outcome{Kind: fakeadapter.OutcomeAccepted}, nil, adapterproto.PromptAccepted, "", 0, 1},
		{"a refusal reaches no runtime", &fakeadapter.Outcome{Kind: fakeadapter.OutcomeRefuse, Reason: "runtime_auth"}, nil, adapterproto.PromptRefused, adapterproto.RefusalRuntimeAuth, 0, 0},
		{"a running turn makes the runtime busy", nil, func(r *rig, ref string) {
			_ = r.runtime.StartTurn(ref)
		}, adapterproto.PromptRefused, adapterproto.RefusalBusy, 0, 0},
		{"a killed native process is a lost session", nil, func(r *rig, ref string) {
			_ = r.runtime.KillNative(ref)
		}, adapterproto.PromptRefused, adapterproto.RefusalSessionLost, 0, 0},
		{"an error before the effect reaches no runtime", &fakeadapter.Outcome{Kind: fakeadapter.OutcomeError, Code: 1042}, nil, "", "", 1042, 0},
		{"an error after the effect: the prompt is delivered and the answer is lost", &fakeadapter.Outcome{Kind: fakeadapter.OutcomeError, Code: -32603, AfterEffect: true}, nil, "", "", -32603, 1},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			r := newRig(t, rigOptions{})
			ref := r.ready()
			_ = r.runtime.SetPlan(ref, fakeruntime.TurnPlan{ManualStart: true})
			if row.prepare != nil {
				row.prepare(r, ref)
			}
			if row.outcome != nil {
				if err := r.knobs.QueuePrompt(*row.outcome); err != nil {
					t.Fatal(err)
				}
			}
			res, err := r.prompt("sess-1", 2, "work")
			if row.wantCode != 0 {
				if codeOf(err) != row.wantCode {
					t.Fatalf("error %v, want %d", err, row.wantCode)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if res.Stage != row.wantStage || res.Reason != row.wantWhy {
					t.Fatalf("result %+v, want %s/%s", res, row.wantStage, row.wantWhy)
				}
			}
			got := 0
			for _, e := range r.ledgerLog().Kind(ledger.KindRuntimePromptReceived) {
				if e.Attrs.Attempt == string(attemptID(2)) {
					got++
				}
			}
			if got != row.delivered {
				t.Fatalf("the prompt reached the runtime %d times, want %d", got, row.delivered)
			}
		})
	}
}

func TestPromptToAnUnknownEndpointIs1001(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.initialize()
	if _, err := r.prompt("nobody", 2, "x"); codeOf(err) != adapterproto.CodeUnknownEndpoint {
		t.Fatalf("got %v", err)
	}
}

func TestPromptIsDeliveredOncePerAttemptAndConflictsOnOtherText(t *testing.T) {
	r := newRig(t, rigOptions{})
	ref := r.ready()
	_ = r.runtime.SetPlan(ref, fakeruntime.TurnPlan{ManualStart: true})
	first, err := r.prompt("sess-1", 2, "same")
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.prompt("sess-1", 2, "same")
	if err != nil || again != first {
		t.Fatalf("the repeated attempt answered %+v, %v; first %+v", again, err, first)
	}
	if _, err := r.prompt("sess-1", 2, "different"); codeOf(err) != adapterproto.CodeAttemptConflict {
		t.Fatalf("the same attempt with other text: %v, want %d", err, adapterproto.CodeAttemptConflict)
	}
	forwarded := 0
	for _, e := range r.ledgerLog().Kind(ledger.KindPromptForwarded) {
		if e.Attrs.Attempt == string(attemptID(2)) {
			forwarded++
		}
	}
	if forwarded != 1 {
		t.Fatalf("the attempt was forwarded %d times", forwarded)
	}
}

func TestPromptLedgerEntriesHoldDigestsNotText(t *testing.T) {
	r := newRig(t, rigOptions{})
	ref := r.ready()
	_ = r.runtime.SetPlan(ref, fakeruntime.TurnPlan{ManualStart: true})
	if _, err := r.prompt("sess-1", 2, "very secret prompt text"); err != nil {
		t.Fatal(err)
	}
	for _, e := range r.ledgerLog().Entries() {
		line, _ := ledger.Encode(e)
		if bytes.Contains(line, []byte("secret")) {
			t.Fatalf("the ledger holds a prompt text: %s", line)
		}
	}
	var digests []string
	for _, e := range r.ledgerLog().Kind(ledger.KindPromptDeliverReceived) {
		digests = append(digests, e.Attrs.TextDigest)
	}
	if len(digests) != 1 || digests[0] != ledger.Digest([]byte("very secret prompt text")) {
		t.Fatalf("digests %v", digests)
	}
}

// --- reconciliation, interrupt, close ---

func TestReconcileAnswersFromTheRecord(t *testing.T) {
	rows := []struct {
		rule     string
		prepare  func(r *rig, ref string)
		attempt  int
		override *fakeadapter.Outcome
		verdict  adapterproto.ReconcileVerdict
		grounds  adapterproto.GroundsCode
		turn     adapterproto.TurnState
	}{
		{"the first prompt waits for a turn", nil, 1, nil, adapterproto.VerdictFound, adapterproto.GroundsAttemptInRecord, adapterproto.TurnStateRunning},
		{"a prompt whose turn completed", func(r *rig, ref string) {
			_, _ = r.prompt("sess-1", 2, "go")
			_ = r.runtime.EndTurn(ref, fakeruntime.FinalCompleted)
		}, 2, nil, adapterproto.VerdictFound, adapterproto.GroundsAttemptInRecord, adapterproto.TurnStateCompleted},
		{"an attempt that never arrived", nil, 9, nil, adapterproto.VerdictProvablyAbsent, adapterproto.GroundsRecordCompleteWithoutAttempt, ""},
		{"an attempt that never arrived while a turn runs may still arrive", func(r *rig, ref string) {
			_ = r.runtime.StartTurn(ref)
		}, 9, nil, adapterproto.VerdictIndeterminate, adapterproto.GroundsTurnRunning, ""},
		{"an attempt that never arrived on a dead session never will", func(r *rig, ref string) {
			_ = r.runtime.KillNative(ref)
		}, 9, nil, adapterproto.VerdictProvablyAbsent, adapterproto.GroundsRecordCompleteWithoutAttempt, ""},
		{"the knob makes the adapter unable to tell", nil, 1, &fakeadapter.Outcome{Kind: fakeadapter.OutcomeIndeterminate, Reason: "record_unreadable"}, adapterproto.VerdictIndeterminate, adapterproto.GroundsRecordUnreadable, ""},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			r := newRig(t, rigOptions{})
			ref := r.ready()
			if row.prepare != nil {
				row.prepare(r, ref)
			}
			if row.override != nil {
				_ = r.knobs.QueueReconcile(*row.override)
			}
			res, err := r.reconcile("sess-1", row.attempt)
			if err != nil {
				t.Fatal(err)
			}
			if res.Verdict != row.verdict || res.Grounds.Code != row.grounds {
				t.Fatalf("result %+v, want %s/%s", res, row.verdict, row.grounds)
			}
			if row.turn != "" && (res.Turn == nil || res.Turn.State != row.turn) {
				t.Fatalf("turn %+v, want state %s", res.Turn, row.turn)
			}
		})
	}
}

func TestInterruptOutcomes(t *testing.T) {
	rows := []struct {
		rule        string
		outcome     *fakeadapter.Outcome
		turnRunning bool
		want        adapterproto.InterruptResult
		turnLeft    bool // a turn is still running afterwards
	}{
		{"a running turn is stopped", nil, true, adapterproto.InterruptResult{Stage: adapterproto.InterruptStopped}, false},
		{"nothing to stop", nil, false, adapterproto.InterruptResult{Stage: adapterproto.InterruptNotRunning}, false},
		{"requested leaves the turn to the script", &fakeadapter.Outcome{Kind: fakeadapter.OutcomeRequested}, true, adapterproto.InterruptResult{Stage: adapterproto.InterruptRequested}, true},
		{"a refusal", &fakeadapter.Outcome{Kind: fakeadapter.OutcomeRefuse, Reason: "native_error"}, true, adapterproto.InterruptResult{Stage: adapterproto.InterruptRefused, Reason: adapterproto.InterruptNativeError}, true},
		{"the knob can say not running whatever the runtime does", &fakeadapter.Outcome{Kind: fakeadapter.OutcomeNotRunning}, true, adapterproto.InterruptResult{Stage: adapterproto.InterruptNotRunning}, true},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			r := newRig(t, rigOptions{})
			ref := r.ready()
			if row.turnRunning {
				if err := r.runtime.StartTurn(ref); err != nil {
					t.Fatal(err)
				}
			}
			if row.outcome != nil {
				_ = r.knobs.QueueInterrupt(*row.outcome)
			}
			res, err := r.interrupt("sess-1")
			if err != nil {
				t.Fatal(err)
			}
			if res != row.want {
				t.Fatalf("result %+v, want %+v", res, row.want)
			}
			sess, _ := r.runtime.Find(ref)
			if _, active := sess.TurnActive(); active != row.turnLeft {
				t.Fatalf("turn running after: %v, want %v", active, row.turnLeft)
			}
		})
	}
	t.Run("a dead session cannot be reached", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		ref := r.ready()
		_ = r.runtime.KillNative(ref)
		res, err := r.interrupt("sess-1")
		if err != nil || res.Stage != adapterproto.InterruptRefused || res.Reason != adapterproto.InterruptNativeUnreachable {
			t.Fatalf("got %+v, %v", res, err)
		}
	})
}

func TestCloseOutcomes(t *testing.T) {
	r := newRig(t, rigOptions{})
	ref := r.ready()
	_ = r.knobs.QueueClose(fakeadapter.Outcome{Kind: fakeadapter.OutcomeUnconfirmed, Reason: "turn_still_running"})
	res, err := r.closeSession("sess-1")
	if err != nil || res.Outcome != adapterproto.CloseUnconfirmed || res.Reason != adapterproto.CloseTurnStillRunning {
		t.Fatalf("unconfirmed close: %+v, %v", res, err)
	}
	if sess, _ := r.runtime.Find(ref); sess.Alive() {
		t.Fatal("the session is alive after close")
	}
	res, err = r.closeSession("sess-1")
	if err != nil || res.Outcome != adapterproto.CloseClosed {
		t.Fatalf("a second close: %+v, %v", res, err)
	}
	if n := countKind(r.ledgerLog(), ledger.KindCloseReceived); n != 2 {
		t.Fatalf("%d close.received entries", n)
	}
}

// --- adapter.start ---

func TestStartReattachesWhatItCan(t *testing.T) {
	r := newRig(t, rigOptions{})
	ref := r.ready()
	if err := r.runtime.StartTurn(ref); err != nil {
		t.Fatal(err)
	}
	sess, _ := r.runtime.Find(ref)
	dead := r.createSession(2, "sess-dead")
	_ = r.runtime.KillNative(string(dead.NativeRef))
	r.disconnect()
	r.connect()
	r.initialize()
	attach := func(ep, native string) adapterproto.SessionAttach {
		return adapterproto.SessionAttach{Endpoint: adapterproto.EndpointRef(ep), Preset: "default", NativeRef: adapterproto.NativeRef(native), ToolAccess: adapterproto.ToolAccess{Env: map[string]string{}}}
	}
	res := r.start([]adapterproto.SessionAttach{attach("sess-1", ref), attach("sess-dead", string(dead.NativeRef)), attach("sess-ghost", "n-ffffffff")}, nil)
	want := map[string]adapterproto.AttachState{"sess-1": adapterproto.AttachAttached, "sess-dead": adapterproto.AttachLost, "sess-ghost": adapterproto.AttachLost}
	for _, st := range res.Sessions {
		if st.State != want[string(st.Endpoint)] {
			t.Errorf("%s: %s, want %s", st.Endpoint, st.State, want[string(st.Endpoint)])
		}
		if st.Endpoint == "sess-1" {
			if !st.TurnActive || st.Process == nil || st.Process.PID != sess.PID() {
				t.Errorf("sess-1 report %+v", st)
			}
		}
	}
	// A prompt to a session the adapter lost is refused as lost, not as unknown.
	if res, err := r.prompt("sess-ghost", 5, "x"); err != nil || res.Reason != adapterproto.RefusalSessionLost {
		t.Fatalf("prompt to a lost session: %+v, %v", res, err)
	}
	// After the restart the attached session still reports its turn to the daemon.
	if err := r.runtime.EndTurn(ref, fakeruntime.FinalCompleted); err != nil {
		t.Fatal(err)
	}
	r.waitNotifications(adapterproto.MethodTurnEnded, 1)
}

func TestStartCanReportEverySessionUnreachable(t *testing.T) {
	r := newRig(t, rigOptions{})
	ref := r.ready()
	_ = r.knobs.QueueStart(fakeadapter.Outcome{Kind: fakeadapter.OutcomeUnreachable})
	res := r.start([]adapterproto.SessionAttach{{Endpoint: "sess-1", Preset: "default", NativeRef: adapterproto.NativeRef(ref), ToolAccess: adapterproto.ToolAccess{Env: map[string]string{}}}}, nil)
	if len(res.Sessions) != 1 || res.Sessions[0].State != adapterproto.AttachUnreachable {
		t.Fatalf("%+v", res)
	}
}

func TestPauseAtStartBeforeReplyHoldsTheDaemonOnItsStartStep(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.initialize()
	_ = r.knobs.PauseAt(fakeadapter.PauseStartBeforeReply)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.start(nil, nil)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if err := r.knobs.WaitPaused(ctx, fakeadapter.PauseStartBeforeReply); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("adapter.start was answered while paused")
	case <-time.After(50 * time.Millisecond):
	}
	// Ping is still answered: only the start request is held.
	if _, err := r.call(adapterproto.MethodPing, nil); err != nil {
		t.Fatalf("ping while paused: %v", err)
	}
	r.knobs.Release(fakeadapter.PauseStartBeforeReply)
	<-done
}

// --- notifications, times, knobs ---

// TestTimesTheAdapterReportsComeFromTheTestClock: turn notifications carry the
// time the test clock had, not the wall clock.
func TestTimesTheAdapterReportsComeFromTheTestClock(t *testing.T) {
	r := newRig(t, rigOptions{})
	ref := r.ready()
	r.clock.Advance(3 * time.Hour)
	if err := r.runtime.StartTurn(ref); err != nil {
		t.Fatal(err)
	}
	raw := r.waitNotifications(adapterproto.MethodTurnStarted, 1)[0]
	var p adapterproto.TurnStartedParams
	if err := adapterproto.Decode(raw, &p); err != nil {
		t.Fatal(err)
	}
	if want := testStart.Add(3 * time.Hour).UnixMilli(); p.AtMS != want {
		t.Fatalf("at_ms %d, want %d on the test clock", p.AtMS, want)
	}
	if p.Process == nil || p.Process.PID < 1 {
		t.Fatalf("process %+v", p.Process)
	}
}

func TestNotificationsAreCountedAsTheyAreHandedToTheProtocol(t *testing.T) {
	r := newRig(t, rigOptions{})
	ref := r.ready()
	for i := 0; i < 3; i++ {
		if err := r.runtime.StartTurn(ref); err != nil {
			t.Fatal(err)
		}
		if err := r.runtime.EndTurn(ref, fakeruntime.FinalCompleted); err != nil {
			t.Fatal(err)
		}
	}
	r.waitNotifications(adapterproto.MethodTurnEnded, 3)
	if got := r.knobs.NotificationsSent(); got != 6 {
		t.Fatalf("NotificationsSent() = %d, want 6 (three turns, two notifications each)", got)
	}
	if r.knobs.Outstanding() != 0 {
		t.Fatalf("Outstanding() = %d with nothing in flight", r.knobs.Outstanding())
	}
}

func TestObservedModes(t *testing.T) {
	rows := []struct {
		mode        fakeadapter.ObservedMode
		wantVerdict string
		check       func(t *testing.T, observed string)
	}{
		{fakeadapter.ObservedFaithful, "allowed", func(t *testing.T, o string) {
			if !strings.Contains(o, "hello from the other node") {
				t.Errorf("the faithful text is not the page: %q", o)
			}
		}},
		{fakeadapter.ObservedTruncated, "refused", func(t *testing.T, o string) {
			if !strings.HasSuffix(o, "... 5 more lines") || strings.Contains(o, "hello from the other node") {
				t.Errorf("the truncated text: %q", o)
			}
		}},
		{fakeadapter.ObservedReplaced, "refused", func(t *testing.T, o string) {
			if o != "[result saved to file]" {
				t.Errorf("the replaced text: %q", o)
			}
		}},
	}
	for _, row := range rows {
		t.Run(string(row.mode), func(t *testing.T) {
			r := newRig(t, rigOptions{})
			ref := r.ready()
			if err := r.knobs.SetObserved(row.mode); err != nil {
				t.Fatal(err)
			}
			_ = r.runtime.SetPlan(ref, fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionRecv}}})
			if _, err := r.prompt("sess-1", 2, "go"); err != nil {
				t.Fatal(err)
			}
			r.waitFor("recv.observed", func() bool { return len(r.observedCalls()) == 1 })
			call := r.observedCalls()[0]
			if string(call.Lease) != leaseA || call.Frontend != adapterproto.FrontendCLI || call.Endpoint != "sess-1" {
				t.Fatalf("call %+v", call)
			}
			row.check(t, call.Observed)
			r.waitFor("observed.sent", func() bool { return countKind(r.ledgerLog(), ledger.KindObservedSent) == 1 })
			sent := r.ledgerLog().Kind(ledger.KindObservedSent)[0]
			if sent.Attrs.Verdict != row.wantVerdict || sent.Attrs.Mode != string(row.mode) {
				t.Fatalf("observed.sent %+v", sent.Attrs)
			}
		})
	}
	t.Run("an adapter that does not acknowledge on behalf never reports", func(t *testing.T) {
		r := newRig(t, rigOptions{manifest: func(o *fakeadapter.ManifestOptions) { o.AckOnBehalf = false }})
		ref := r.ready()
		_ = r.runtime.SetPlan(ref, fakeruntime.TurnPlan{Actions: []fakeruntime.TurnAction{{Kind: fakeruntime.ActionRecv}}, EndAfter: true})
		if _, err := r.prompt("sess-1", 2, "go"); err != nil {
			t.Fatal(err)
		}
		r.waitNotifications(adapterproto.MethodTurnEnded, 1)
		if n := len(r.observedCalls()); n != 0 {
			t.Fatalf("%d recv.observed calls from an adapter that did not declare ack_on_behalf", n)
		}
	})
}

func TestEveryKnobThatChangesBehaviourLeavesAHit(t *testing.T) {
	r := newRig(t, rigOptions{manifest: func(o *fakeadapter.ManifestOptions) { o.ServerRequests = true }})
	ref := r.ready()
	ctx := r.ctx
	hits := func() map[string]bool {
		out := map[string]bool{}
		for _, e := range r.ledgerLog().Kind(ledger.KindKnobHit) {
			out[e.Attrs.Knob] = true
		}
		return out
	}
	_ = r.knobs.QueuePrompt(fakeadapter.Outcome{Kind: fakeadapter.OutcomeRefuse, Reason: "busy"})
	_, _ = r.prompt("sess-1", 2, "x")
	_ = r.knobs.QueueInterrupt(fakeadapter.Outcome{Kind: fakeadapter.OutcomeNotRunning})
	_, _ = r.interrupt("sess-1")
	_ = r.knobs.QueueReconcile(fakeadapter.Outcome{Kind: fakeadapter.OutcomeIndeterminate})
	_, _ = r.reconcile("sess-1", 1)
	_ = r.knobs.QueueClose(fakeadapter.Outcome{Kind: fakeadapter.OutcomeUnconfirmed})
	_, _ = r.closeSession("sess-1")
	_ = r.knobs.SetObserved(fakeadapter.ObservedReplaced)
	_ = r.runtime.SetPlan(ref, fakeruntime.TurnPlan{})
	_ = r.knobs.Availability(ctx, nil)
	_ = r.knobs.Persist(ctx, "sess-1", attemptID(2))
	_ = r.knobs.NativeRequest(ctx, "sess-1", "m", adapterproto.DispositionPreset)
	_, _ = r.knobs.Register(ctx, adapterproto.RegisterParams{Key: "chat:a", DisplayName: "A"})
	want := []string{
		"prompt.refuse", "interrupt.not-running", "reconcile.indeterminate", "close.unconfirmed",
		"availability", "persist", "native-request", "direct.register",
	}
	got := hits()
	for _, name := range want {
		if !got[name] {
			t.Errorf("no knob.hit named %q; hits: %v", name, got)
		}
	}
}

func TestDirectKnobsReturnWhatTheDaemonAnswered(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.initialize()
	ctx := r.ctx
	reg, err := r.knobs.Register(ctx, adapterproto.RegisterParams{Key: "chat:alice", DisplayName: "Alice"})
	if err != nil || reg.Endpoint != "ep-chat:alice" {
		t.Fatalf("Register: %+v, %v", reg, err)
	}
	if got := r.requestCount(adapterproto.MethodEndpointRegister); got != 1 {
		t.Fatalf("the daemon saw %d register requests", got)
	}
	page, err := r.knobs.Recv(ctx, adapterproto.RecvParams{Endpoint: reg.Endpoint})
	if err != nil || len(page.Page.Messages) != 1 || page.Page.Messages[0].Body != "direct hello" {
		t.Fatalf("Recv: %+v, %v", page, err)
	}
	if err := r.knobs.Unregister(ctx, adapterproto.UnregisterParams{Endpoint: reg.Endpoint}); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
}

func TestKnobsThatNeedAnAdapterFailWhenNoneServes(t *testing.T) {
	k := fakeadapter.NewKnobs()
	ctx := context.Background()
	for name, err := range map[string]error{
		"Availability": k.Availability(ctx, nil),
		"Persist":      k.Persist(ctx, "e", attemptID(1)),
		"Disconnect":   k.Disconnect(),
		"Ping":         k.Ping(ctx),
	} {
		if !errors.Is(err, fakeadapter.ErrNotServing) {
			t.Errorf("%s with no adapter serving = %v", name, err)
		}
	}
}

func TestCapabilitiesThatWereNotDeclaredAreRefused(t *testing.T) {
	t.Run("interrupt", func(t *testing.T) {
		r := newRig(t, rigOptions{manifest: func(o *fakeadapter.ManifestOptions) { o.Interrupt = false }})
		r.ready()
		if _, err := r.interrupt("sess-1"); codeOf(err) != adapterproto.CodeCapabilityNotDeclared {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a runtime", func(t *testing.T) {
		r := newRig(t, rigOptions{manifest: func(o *fakeadapter.ManifestOptions) {
			o.Runtime, o.Frontends, o.Budgets, o.RecvWaitS = false, nil, []manifest.Budget{{Frontend: manifest.FrontendDirect, Unit: manifest.UnitChars, Limit: 9000}}, nil
		}})
		r.initialize()
		if _, err := r.call(adapterproto.MethodSessionCreate, r.createParams(1, "s")); codeOf(err) != adapterproto.CodeCapabilityNotDeclared {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("direct endpoints", func(t *testing.T) {
		r := newRig(t, rigOptions{manifest: func(o *fakeadapter.ManifestOptions) {
			o.Direct = false
			o.Budgets = o.Budgets[:2]
		}})
		r.initialize()
		if _, err := r.knobs.Register(r.ctx, adapterproto.RegisterParams{Key: "k", DisplayName: "k"}); !errors.Is(err, fakeadapter.ErrNotDeclared) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("server requests", func(t *testing.T) {
		r := newRig(t, rigOptions{})
		r.initialize()
		if err := r.knobs.NativeRequest(r.ctx, "sess-1", "m", adapterproto.DispositionPreset); !errors.Is(err, fakeadapter.ErrNotDeclared) {
			t.Fatalf("got %v", err)
		}
	})
}

// --- failure modes of the connection ---

func TestHungAdapterAnswersNothingAndKeepsItsLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "i.lock")
	r := newRig(t, rigOptions{})
	if err := r.initializeAt(path); err != nil {
		t.Fatal(err)
	}
	r.knobs.Hang()
	ctx, cancel := context.WithTimeout(r.ctx, 150*time.Millisecond)
	defer cancel()
	if err := r.daemon.Call(ctx, adapterproto.MethodPing, nil, nil); err == nil {
		t.Fatal("a hung adapter answered ping")
	}
	// The process and its lock are still there: a replacement is refused.
	other := newRig(t, rigOptions{})
	if err := other.initializeAt(path); codeOf(err) != adapterproto.CodeInstanceLocked {
		t.Fatalf("the replacement of a hung adapter: %v, want %d", err, adapterproto.CodeInstanceLocked)
	}
}

func TestDisconnectEndsServeWithItsOwnError(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.initialize()
	if err := r.knobs.Disconnect(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-r.served:
		if !errors.Is(err, fakeadapter.ErrDisconnected) {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("Serve did not end after Disconnect")
	}
	r.closeConn = nil // already ended
	if n := countKind(r.ledgerLog(), ledger.KindKnobHit); n != 1 {
		t.Fatalf("%d knob.hit entries, want the one of the disconnect", n)
	}
}

func TestServeReturnsNilWhenTheDaemonClosesTheStream(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.initialize()
	_ = r.daemon.Close()
	select {
	case err := <-r.served:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve returned %v when the daemon closed the stream", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("Serve did not end")
	}
	r.closeConn = nil
}

func TestServeReturnsTheContextErrorWhenItsContextEnds(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.initialize()
	r.cancel()
	select {
	case err := <-r.served:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(waitLimit):
		t.Fatal("Serve did not end")
	}
	r.closeConn = nil
}

func TestServeRefusesAnIncompleteConfig(t *testing.T) {
	r := newRig(t, rigOptions{})
	w, _ := r.mem.Open("adapter/other", 0, r.clock.Now)
	good := fakeadapter.Config{InstanceID: "x", Now: r.clock.Now, Ledger: w, Runtime: r.runtime, Manifest: fakeadapter.DefaultManifestOptions()}
	rows := map[string]func(*fakeadapter.Config){
		"no instance id":                       func(c *fakeadapter.Config) { c.InstanceID = "" },
		"no clock":                             func(c *fakeadapter.Config) { c.Now = nil },
		"no ledger":                            func(c *fakeadapter.Config) { c.Ledger = nil },
		"a runtime declared and none to drive": func(c *fakeadapter.Config) { c.Runtime = nil },
		"a declaration of neither kind":        func(c *fakeadapter.Config) { c.Manifest.Runtime, c.Manifest.Direct = false, false },
	}
	for name, mutate := range rows {
		t.Run(name, func(t *testing.T) {
			c := good
			mutate(&c)
			pr, pw := newPipe()
			defer pr.Close()
			defer pw.Close()
			if err := fakeadapter.Serve(context.Background(), pr, pw, c); err == nil {
				t.Fatal("Serve accepted an incomplete config")
			}
		})
	}
}

// --- the manifest ---

func TestManifestOptionsRefuseDeclarationsTheDaemonWouldRefuse(t *testing.T) {
	rows := map[string]func(*fakeadapter.ManifestOptions){
		"neither runtime nor direct":   func(o *fakeadapter.ManifestOptions) { o.Runtime, o.Direct = false, false },
		"a runtime without front ends": func(o *fakeadapter.ManifestOptions) { o.Frontends = nil },
		"a wait limit above the protocol cap": func(o *fakeadapter.ManifestOptions) {
			o.RecvWaitS[adapterproto.FrontendCLI] = manifest.MaxRecvWaitSeconds + 1
		},
		"a direct limit of zero":        func(o *fakeadapter.ManifestOptions) { o.MaxDirectEndpoints = 0 },
		"an identifier that is not one": func(o *fakeadapter.ManifestOptions) { o.ID = "Not Valid" },
		"a program outside the package": func(o *fakeadapter.ManifestOptions) { o.Program = "../escape" },
	}
	for name, mutate := range rows {
		t.Run(name, func(t *testing.T) {
			o := fakeadapter.DefaultManifestOptions()
			mutate(&o)
			if err := o.Validate(); err == nil {
				t.Fatal("Validate accepted the options")
			}
			if _, err := o.Encode(); err == nil && name != "neither runtime nor direct" && name != "a direct limit of zero" && name != "a runtime without front ends" {
				t.Fatal("Encode wrote a manifest that Validate refuses")
			}
		})
	}
	if err := fakeadapter.DefaultManifestOptions().Validate(); err != nil {
		t.Fatalf("the defaults are refused: %v", err)
	}
}

func TestWritePackageWritesAManifestTheDaemonParsesAndTheProgram(t *testing.T) {
	prog := filepath.Join(t.TempDir(), "program")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "pkg")
	o := fakeadapter.DefaultManifestOptions()
	if err := o.WritePackage(dir, prog); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		t.Fatalf("the written manifest does not parse: %v", err)
	}
	if m.ID != o.ID || m.Entry.Program != o.Program {
		t.Fatalf("manifest %+v", m)
	}
	got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(o.Program)))
	if err != nil || string(got) != "#!/bin/sh\nexit 0\n" {
		t.Fatalf("the program was not copied: %q, %v", got, err)
	}
	if err := o.WritePackage(filepath.Join(dir, "x"), filepath.Join(dir, "missing")); err == nil {
		t.Fatal("WritePackage accepted a program that does not exist")
	}
}

// TestHangLeavesAHit: a hung adapter says nothing on the wire, so the ledger is
// where a test sees that its injection point was reached.
func TestHangLeavesAHit(t *testing.T) {
	r := newRig(t, rigOptions{})
	r.initialize()
	r.knobs.Hang()
	ctx, cancel := context.WithTimeout(r.ctx, 100*time.Millisecond)
	defer cancel()
	_ = r.daemon.Call(ctx, adapterproto.MethodPing, nil, nil)
	r.waitFor("the hang to be recorded", func() bool {
		for _, e := range r.ledgerLog().Kind(ledger.KindKnobHit) {
			if e.Attrs.Knob == "hang" {
				return true
			}
		}
		return false
	})
}

// TestPipeProcessIsAProcessTheAdapterHostCanSupervise: the engine's fixture runs the
// adapter in its own process through a pair of pipes; the shape is that of a child
// process: its output and input, a pid, a kill and a wait.
func TestPipeProcessIsAProcessTheAdapterHostCanSupervise(t *testing.T) {
	leakGuard(t)
	clk := clocktest.NewFake(testStart)
	mem := ledger.NewMemory()
	rt, err := fakeruntime.New(fakeruntime.Config{Now: clk.Now, Ledgers: mem, Dir: t.TempDir(), Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	w, err := mem.Open("adapter/fake", 0, clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	proc := fakeadapter.StartPipe(context.Background(), fakeadapter.Config{
		InstanceID: "fake", Now: clk.Now, Ledger: w, Runtime: rt, Manifest: fakeadapter.DefaultManifestOptions(),
	})
	out, in := proc.Stream()
	if proc.PID() != 0 {
		t.Fatalf("PID() = %d: an adapter in the test process has no pid of its own", proc.PID())
	}
	d, err := peer.New(out, in, peer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	d.Start()
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	var res adapterproto.InitializeResult
	var raw json.RawMessage
	if err := d.Call(ctx, adapterproto.MethodInitialize, adapterproto.InitializeParams{
		Protocol: adapterproto.SupportedRange(), Daemon: adapterproto.DaemonInfo{Version: "0.1.0"},
		Instance:     adapterproto.InstanceInfo{ID: "fake", LockPath: filepath.Join(t.TempDir(), "i.lock")},
		Capabilities: adapterproto.DaemonCapabilities{},
	}, &raw); err != nil {
		t.Fatal(err)
	}
	if err := adapterproto.Decode(raw, &res); err != nil {
		t.Fatal(err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("a second Kill: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Wait returned nil for an adapter that was killed")
		}
	case <-time.After(waitLimit):
		t.Fatal("Wait did not return after Kill")
	}
	select {
	case <-d.Done():
	case <-time.After(waitLimit):
		t.Fatal("the daemon's end of the stream did not end after the adapter was killed")
	}
}
