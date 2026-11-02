package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/adapter"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// ownedThread is a thread a person's Codex runs: the adapter's server reads its
// record and queues prompts for it, and the test plays the owner (take, finish).
type ownedThread struct {
	id       string
	turns    []*ownedTurn // oldest first
	queue    []appserver.QueuedSubmission
	ephem    bool
	subAgent bool
	archived bool
}

type ownedTurn struct {
	id          string
	status      string
	clientID    string
	startedAt   time.Time
	noStart     bool // the record shows no start
	completedAt *time.Time
}

// fakeOwner is the part of the App Server that serves threads another process
// has loaded.
type fakeOwner struct {
	t            *testing.T
	mu           sync.Mutex
	threads      map[string]*ownedThread
	experimental bool
	next         int
}

func newFakeOwner(t *testing.T, codex *fakeCodex) *fakeOwner {
	o := &fakeOwner{t: t, threads: map[string]*ownedThread{}}
	rpcErr := func(code int, msg string) *appserver.RPCError { return &appserver.RPCError{Code: code, Message: msg} }
	codex.srv.Handle("initialize", func(raw json.RawMessage) (any, *appserver.RPCError) {
		p := decode[struct {
			Capabilities struct{ ExperimentalAPI bool }
		}](raw)
		o.mu.Lock()
		o.experimental = p.Capabilities.ExperimentalAPI
		o.mu.Unlock()
		return map[string]any{"userAgent": "sidera-adapter-codex/0.166.0 (test)", "codexHome": "/h"}, nil
	})
	codex.srv.Handle("thread/read", func(raw json.RawMessage) (any, *appserver.RPCError) {
		p := decode[struct{ ThreadID string }](raw)
		o.mu.Lock()
		defer o.mu.Unlock()
		th := o.threads[p.ThreadID]
		if th == nil {
			return nil, rpcErr(-32600, "thread not loaded: "+p.ThreadID)
		}
		var parent any
		var source any = "cli"
		if th.subAgent {
			parent, source = "th-parent", map[string]any{"subAgent": map[string]any{}}
		}
		return map[string]any{"thread": map[string]any{
			"id": th.id, "ephemeral": th.ephem, "parentThreadId": parent, "source": source, "status": map[string]string{"type": "notLoaded"},
		}}, nil
	})
	codex.srv.Handle("thread/turns/list", func(raw json.RawMessage) (any, *appserver.RPCError) {
		p := decode[struct {
			ThreadID  string
			Limit     int
			ItemsView string
		}](raw)
		o.mu.Lock()
		defer o.mu.Unlock()
		th := o.threads[p.ThreadID]
		if th == nil {
			return nil, rpcErr(-32600, "thread not loaded: "+p.ThreadID)
		}
		data := []any{}
		for i := len(th.turns) - 1; i >= 0 && len(data) < p.Limit; i-- {
			t := th.turns[i]
			items := []any{}
			if t.clientID != "" {
				items = append(items, map[string]any{"type": "userMessage", "id": "i-" + t.id, "clientId": t.clientID})
			}
			var completed any
			if t.completedAt != nil {
				completed = t.completedAt.Unix()
			}
			var started any
			if !t.noStart {
				started = t.startedAt.Unix()
			}
			data = append(data, map[string]any{
				"id": t.id, "status": t.status, "itemsView": p.ItemsView, "items": items,
				"startedAt": started, "completedAt": completed,
			})
		}
		return map[string]any{"data": data, "nextCursor": nil}, nil
	})
	codex.srv.Handle("thread/queue/add", func(raw json.RawMessage) (any, *appserver.RPCError) {
		p := decode[struct {
			ThreadID            string
			ClientUserMessageID string
			Input               []appserver.UserInput
		}](raw)
		o.mu.Lock()
		defer o.mu.Unlock()
		if !o.experimental {
			return nil, rpcErr(-32600, "thread/queue/add requires experimentalApi capability")
		}
		th := o.threads[p.ThreadID]
		switch {
		case th == nil:
			return nil, rpcErr(-32603, "failed to read thread: invalid thread-store request: no rollout found for thread id "+p.ThreadID)
		case th.archived:
			return nil, rpcErr(-32600, "session "+p.ThreadID+" is archived. Run `codex unarchive` to unarchive it first.")
		}
		o.next++
		q := appserver.QueuedSubmission{ID: fmt.Sprintf("q-%d", o.next), ClientUserMessageID: p.ClientUserMessageID, Input: p.Input}
		th.queue = append(th.queue, q)
		return map[string]any{"queuedSubmission": q}, nil
	})
	codex.srv.Handle("thread/queue/list", func(raw json.RawMessage) (any, *appserver.RPCError) {
		p := decode[struct{ ThreadID string }](raw)
		o.mu.Lock()
		defer o.mu.Unlock()
		th := o.threads[p.ThreadID]
		if th == nil {
			return nil, rpcErr(-32603, "no rollout found for thread id "+p.ThreadID)
		}
		return map[string]any{"data": append([]appserver.QueuedSubmission{}, th.queue...), "nextCursor": nil}, nil
	})
	codex.srv.Handle("thread/queue/delete", func(raw json.RawMessage) (any, *appserver.RPCError) {
		p := decode[struct{ ThreadID, QueuedSubmissionID string }](raw)
		o.mu.Lock()
		defer o.mu.Unlock()
		th := o.threads[p.ThreadID]
		if th == nil {
			return nil, rpcErr(-32603, "no rollout found for thread id "+p.ThreadID)
		}
		for i, q := range th.queue {
			if q.ID == p.QueuedSubmissionID {
				th.queue = append(th.queue[:i], th.queue[i+1:]...)
				return map[string]any{"deleted": true}, nil
			}
		}
		return map[string]any{"deleted": false}, nil
	})
	return o
}

// putForeign queues an item the way the person's own tool would: not through this
// adapter.
func (o *fakeOwner) putForeign(th *ownedThread, id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	th.queue = append(th.queue, appserver.QueuedSubmission{ID: id, ClientUserMessageID: "not-ours-" + id})
}

// queueIDs lists the ids of the thread's queue, oldest first.
func (o *fakeOwner) queueIDs(th *ownedThread) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	ids := []string{}
	for _, q := range th.queue {
		ids = append(ids, q.ID)
	}
	return ids
}

func (o *fakeOwner) add(th *ownedThread) *ownedThread {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.threads[th.id] = th
	return th
}

// begin starts a turn the way a person's Codex records one that is running: the
// status is "interrupted" and there is no completion time.
func (o *fakeOwner) begin(th *ownedThread, id, clientID string, startedAt time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	th.turns = append(th.turns, &ownedTurn{id: id, status: "interrupted", clientID: clientID, startedAt: startedAt})
}

// take plays the owner taking the oldest queued prompt into a new running turn.
func (o *fakeOwner) take(th *ownedThread, turnID string) {
	o.mu.Lock()
	q := th.queue[0]
	th.queue = th.queue[1:]
	o.mu.Unlock()
	o.begin(th, turnID, q.ClientUserMessageID, time.Now())
}

func (o *fakeOwner) finish(th *ownedThread, turnID, status string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	for _, t := range th.turns {
		if t.id == turnID {
			t.status, t.completedAt = status, &now
		}
	}
}

func (o *fakeOwner) queued(th *ownedThread) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(th.queue)
}

func attachedHarness(t *testing.T) (*harness, *fakeCodex, *fakeOwner) {
	codex := newFakeCodex(t)
	owner := newFakeOwner(t, codex)
	h := newHarnessWith(t, codex, func(o *adapter.Options) {
		o.AttachPollEvery, o.AttachIdlePollEvery = 20*time.Millisecond, 20*time.Millisecond
	})
	return h, codex, owner
}

func (h *harness) attach(endpoint, thread string) (adapterproto.SessionAttachResult, error) {
	var res adapterproto.SessionAttachResult
	err := h.call(adapterproto.MethodSessionAttach, adapterproto.SessionAttachParams{Endpoint: adapterproto.EndpointRef(endpoint), NativeRef: adapterproto.NativeRef(thread)}, &res)
	return res, err
}

func (h *harness) mustAttach(endpoint, thread string) {
	h.t.Helper()
	if res, err := h.attach(endpoint, thread); err != nil || res.Outcome != adapterproto.SessionAttachAttached {
		h.t.Fatalf("attach = %+v, %v", res, err)
	}
}

func (h *harness) deliverTo(endpoint, attempt, text string) adapterproto.PromptDeliverResult {
	h.t.Helper()
	var res adapterproto.PromptDeliverResult
	h.mustCall(adapterproto.MethodPromptDeliver, adapterproto.PromptDeliverParams{
		Endpoint: adapterproto.EndpointRef(endpoint), Attempt: adapterproto.AttemptID(attempt), Text: text,
	}, &res)
	return res
}

func turnIs(id string) func(json.RawMessage) bool {
	return func(raw json.RawMessage) bool { return decode[struct{ Turn string }](raw).Turn == id }
}

// Attaching answers by what the server finds: a normal persisted thread is
// attached (and again, idempotently), an unknown one is not found, one that takes
// no queued prompt (ephemeral, a sub-agent's) is a failure that says so, and a
// server that cannot be reached is unreachable. The queue is part of the
// experimental API, so the connection asks for it.
func TestAttachSessionOutcomes(t *testing.T) {
	h, codex, owner := attachedHarness(t)
	h.start()
	owner.add(&ownedThread{id: "th-a"})
	owner.add(&ownedThread{id: "th-eph", ephem: true})
	owner.add(&ownedThread{id: "th-sub", subAgent: true})

	h.mustAttach(sessionEP, "th-a")
	h.mustAttach(sessionEP, "th-a")
	for name, tc := range map[string]struct {
		thread string
		want   adapterproto.SessionAttachResult
	}{
		"unknown":   {"th-none", adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachNotFound}},
		"ephemeral": {"th-eph", adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachFailed, Reason: adapterproto.AttachFailWakeUnavailable}},
		"sub-agent": {"th-sub", adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachFailed, Reason: adapterproto.AttachFailWakeUnavailable}},
	} {
		got, err := h.attach(otherEP, tc.thread)
		if err != nil || got.Outcome != tc.want.Outcome || got.Reason != tc.want.Reason {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
	if !owner.experimental {
		t.Error("the connection did not ask for the experimental API, which the queue needs")
	}
	if n := len(codex.callsOf("thread/resume")); n != 0 {
		t.Errorf("%d thread/resume calls: a thread another process runs is never resumed", n)
	}

	down := newHarness(t, nil)
	down.mgr.err = errors.New("no server")
	down.start()
	if got, err := down.attach(sessionEP, "th-a"); err != nil || got.Outcome != adapterproto.SessionAttachFailed || got.Reason != adapterproto.AttachFailNativeUnreachable {
		t.Errorf("no server: %+v, %v", got, err)
	}
}

// An attachment that comes before adapter.start is answered with an error, and
// leaves no session behind that adapter.start would lose with its reading running.
func TestAttachSessionBeforeStartIsAnError(t *testing.T) {
	h, codex, owner := attachedHarness(t)
	owner.add(&ownedThread{id: "th-a"})
	if res, err := h.attach(sessionEP, "th-a"); err == nil {
		t.Fatalf("attach before start = %+v", res)
	}
	h.start()
	time.Sleep(100 * time.Millisecond)
	if n := len(codex.callsOf("thread/turns/list")); n != 0 {
		t.Fatalf("%d readings of a thread that was not attached", n)
	}
}

// The turns the person runs are reported without an attempt, as they happen: the
// turns the thread had when it was attached are history, a running turn shows as
// "interrupted" with no completion time and ends when it has one, and a turn that
// shows no end for longer than the limit is taken for abandoned.
func TestAttachedHumanTurns(t *testing.T) {
	h, _, owner := attachedHarness(t)
	h.start()
	done := time.Now().Add(-time.Hour)
	th := owner.add(&ownedThread{id: "th-a", turns: []*ownedTurn{
		{id: "old", status: "completed", clientID: "c0", startedAt: done, completedAt: &done},
		{id: "crashed", status: "interrupted", startedAt: time.Now().Add(-5 * time.Hour)},
	}})
	h.mustAttach(sessionEP, "th-a")
	if n := h.countNotes(adapterproto.MethodTurnStarted); n != 0 {
		t.Fatalf("%d turns reported for history", n)
	}

	owner.begin(th, "t1", "", time.Now())
	started := decode[adapterproto.TurnStartedParams](h.waitNote(adapterproto.MethodTurnStarted, turnIs("t1")))
	if started.Endpoint != sessionEP || started.Attempt != "" {
		t.Fatalf("turn.started = %+v: a turn the person runs names no attempt", started)
	}
	time.Sleep(100 * time.Millisecond)
	if n := h.countNotes(adapterproto.MethodTurnEnded); n != 0 {
		t.Fatalf("a turn with no completion time was reported ended: %v", h.noteNames())
	}
	owner.finish(th, "t1", "interrupted")
	h.waitNote(adapterproto.MethodTurnEnded, func(r json.RawMessage) bool {
		return turnIs("t1")(r) && ended(r, adapterproto.TurnInterrupted)
	})

	// A turn that began long ago and shows no end is reported, and ended as failed.
	owner.begin(th, "t2", "", time.Now().Add(-3*time.Hour))
	h.waitNote(adapterproto.MethodTurnStarted, turnIs("t2"))
	h.waitNote(adapterproto.MethodTurnEnded, func(r json.RawMessage) bool {
		return turnIs("t2")(r) && ended(r, adapterproto.TurnFailed)
	})

	// A running turn, newer than the limit, is the person's at attachment.
	h2, _, owner2 := attachedHarness(t)
	h2.start()
	running := owner2.add(&ownedThread{id: "th-b"})
	owner2.begin(running, "t9", "", time.Now())
	h2.mustAttach(sessionEP, "th-b")
	h2.waitNote(adapterproto.MethodTurnStarted, turnIs("t9"))
}

// The thread's history is rebuilt by the server at every reading, so the pace is
// quick only while something is waited for: a prompt of the adapter's own, queued
// or taken and not yet seen, or a turn that is open. Queueing a prompt ends the
// slow wait at once.
//
// The slow wait is far longer than the test, so a reading that comes while nothing
// is awaited can only be the quick pace's, and the quick pace is shown by readings
// that keep coming unprompted, which only it can bring. How many come in a window
// depends on the machine's timers and scheduler, so no rate is asserted.
func TestAttachedReadingPaceFollowsWhatIsAwaited(t *testing.T) {
	codex := newFakeCodex(t)
	owner := newFakeOwner(t, codex)
	h := newHarnessWith(t, codex, func(o *adapter.Options) {
		o.AttachPollEvery, o.AttachIdlePollEvery = 20*time.Millisecond, time.Hour
	})
	h.start()
	th := owner.add(&ownedThread{id: "th-a"})
	h.mustAttach(sessionEP, "th-a")
	readings := func() int { return len(codex.callsOf("thread/turns/list")) }
	// quick fails unless n more readings come, one after another, without being asked.
	quick := func(n int, what string) {
		t.Helper()
		target := readings() + n
		deadline := time.Now().Add(10 * time.Second)
		for readings() < target {
			if time.Now().After(deadline) {
				t.Fatalf("fewer than %d readings in 10s %s", n, what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	quiet := func(what string) {
		t.Helper()
		before := readings()
		time.Sleep(500 * time.Millisecond)
		if n := readings() - before; n != 0 {
			t.Fatalf("%d readings in 500ms %s", n, what)
		}
	}
	quiet("of an idle thread")
	h.deliverTo(sessionEP, attempt1, "hello")
	quick(5, "with a prompt queued")
	owner.take(th, "t1")
	h.waitNote(adapterproto.MethodTurnStarted, turnIs("t1"))
	quick(5, "with a turn open")
	owner.finish(th, "t1", "completed")
	h.waitNote(adapterproto.MethodTurnEnded, turnIs("t1"))
	time.Sleep(200 * time.Millisecond) // the pace is chosen after a reading
	quiet("after the turn ended")
}

// A turn that shows no start is counted from when the adapter first saw it, so that
// one the record never ends is abandoned after the limit like any other.
func TestAttachedTurnWithoutStartIsStaleFromFirstSight(t *testing.T) {
	codex := newFakeCodex(t)
	owner := newFakeOwner(t, codex)
	h := newHarnessWith(t, codex, func(o *adapter.Options) {
		o.AttachPollEvery, o.AttachIdlePollEvery, o.TurnStaleAfter = 20*time.Millisecond, 20*time.Millisecond, 600*time.Millisecond
	})
	h.start()
	th := owner.add(&ownedThread{id: "th-a"})
	h.mustAttach(sessionEP, "th-a")
	owner.mu.Lock()
	th.turns = append(th.turns, &ownedTurn{id: "t1", status: "interrupted", noStart: true})
	owner.mu.Unlock()
	h.waitNote(adapterproto.MethodTurnStarted, turnIs("t1"))
	time.Sleep(200 * time.Millisecond)
	if n := h.countNotes(adapterproto.MethodTurnEnded); n != 0 {
		t.Fatalf("a turn without a start was abandoned at once: %v", h.noteNames())
	}
	h.waitNote(adapterproto.MethodTurnEnded, func(r json.RawMessage) bool {
		return turnIs("t1")(r) && ended(r, adapterproto.TurnFailed)
	})
}

// A turn the owner runs shows "interrupted" with no completion time until it ends,
// and holds its prompt only once the user message is written. Reconciling between
// the owner taking the prompt and the end of the turn must not read that turn as
// over: before the message is written it may be about to hold the prompt, so the
// verdict is not "absent", and after, the prompt is found in a turn that runs.
func TestAttachedReconcileWhileTheOwnerRunsTheTurn(t *testing.T) {
	codex := newFakeCodex(t)
	owner := newFakeOwner(t, codex)
	// The reading never catches up with the turn: the verdict rests on the record.
	h := newHarnessWith(t, codex, func(o *adapter.Options) { o.AttachPollEvery, o.AttachIdlePollEvery = time.Hour, time.Hour })
	h.start()
	th := owner.add(&ownedThread{id: "th-a"})
	h.mustAttach(sessionEP, "th-a")
	h.deliverTo(sessionEP, attempt1, "hello")

	reconcile := func() adapterproto.ReconcileResult {
		var rec adapterproto.ReconcileResult
		h.mustCall(adapterproto.MethodPromptReconcile, adapterproto.ReconcileParams{Endpoint: sessionEP, Attempt: attempt1}, &rec)
		return rec
	}
	owner.take(th, "t1")
	owner.mu.Lock()
	th.turns[0].clientID = "" // the turn is written before its user message
	owner.mu.Unlock()
	if rec := reconcile(); rec.Verdict != adapterproto.VerdictIndeterminate || rec.Grounds.Code != adapterproto.GroundsTurnRunning {
		t.Fatalf("the owner runs a turn with no prompt in it yet: %+v", rec)
	}
	owner.mu.Lock()
	th.turns[0].clientID = attempt1
	owner.mu.Unlock()
	if rec := reconcile(); rec.Verdict != adapterproto.VerdictFound || rec.Turn == nil || rec.Turn.State != adapterproto.TurnStateRunning {
		t.Fatalf("the prompt is in a turn that runs: %+v", rec)
	}
	owner.finish(th, "t1", "completed")
	if rec := reconcile(); rec.Verdict != adapterproto.VerdictFound || rec.Turn.State != adapterproto.TurnStateCompleted {
		t.Fatalf("the turn ended: %+v", rec)
	}
}

// A prompt is queued with its attempt as the client id. It is reported persisted and
// its turn started, with the attempt, when the owner takes it; nothing is reported
// while it waits, and what the daemon may conclude from the record while it waits is
// nothing.
func TestAttachedPromptDelivery(t *testing.T) {
	h, codex, owner := attachedHarness(t)
	h.start()
	th := owner.add(&ownedThread{id: "th-a"})
	h.mustAttach(sessionEP, "th-a")

	if got := h.deliverTo(sessionEP, attempt1, "hello"); got.Stage != adapterproto.PromptAccepted {
		t.Fatalf("deliver = %+v", got)
	}
	add := decode[struct {
		ThreadID, ClientUserMessageID string
		Input                         []appserver.UserInput
	}](codex.callsOf("thread/queue/add")[0].Params)
	if add.ThreadID != "th-a" || add.ClientUserMessageID != attempt1 || len(add.Input) != 1 || add.Input[0].Text != "hello" {
		t.Fatalf("queue/add = %+v", add)
	}
	// The same attempt is answered again, not queued again.
	if got := h.deliverTo(sessionEP, attempt1, "hello"); got.Stage != adapterproto.PromptAccepted || len(codex.callsOf("thread/queue/add")) != 1 {
		t.Fatalf("repeat = %+v, %d queue/add calls", got, len(codex.callsOf("thread/queue/add")))
	}

	var rec adapterproto.ReconcileResult
	h.mustCall(adapterproto.MethodPromptReconcile, adapterproto.ReconcileParams{Endpoint: sessionEP, Attempt: attempt1}, &rec)
	if rec.Verdict != adapterproto.VerdictIndeterminate {
		t.Fatalf("a prompt still queued: %+v", rec)
	}
	time.Sleep(100 * time.Millisecond)
	if n := h.countNotes(adapterproto.MethodTurnStarted) + h.countNotes(adapterproto.MethodPromptPersisted); n != 0 {
		t.Fatalf("progress reported for a prompt nobody took: %v", h.noteNames())
	}

	owner.take(th, "t1")
	h.waitNote(adapterproto.MethodPromptPersisted, func(r json.RawMessage) bool {
		return decode[adapterproto.PromptPersistedParams](r).Attempt == attempt1
	})
	started := decode[adapterproto.TurnStartedParams](h.waitNote(adapterproto.MethodTurnStarted, turnIs("t1")))
	if started.Attempt != attempt1 {
		t.Fatalf("turn.started = %+v", started)
	}
	owner.finish(th, "t1", "completed")
	h.waitNote(adapterproto.MethodTurnEnded, func(r json.RawMessage) bool {
		return turnIs("t1")(r) && ended(r, adapterproto.TurnCompleted)
	})
	h.mustCall(adapterproto.MethodPromptReconcile, adapterproto.ReconcileParams{Endpoint: sessionEP, Attempt: attempt1}, &rec)
	if rec.Verdict != adapterproto.VerdictFound {
		t.Fatalf("a prompt in the record: %+v", rec)
	}
	if n := h.countNotes(adapterproto.MethodTurnStarted); n != 1 {
		t.Fatalf("%d turn.started: %v", n, h.noteNames())
	}
}

// At most one wake-up of Sidera waits in the person's queue: a prompt nobody took is
// taken back when a newer one is queued, one the owner took is left to its turn, and
// an item Sidera did not add is never touched.
func TestAttachedPromptsQueuedAreWithdrawnWhenSuperseded(t *testing.T) {
	h, codex, owner := attachedHarness(t)
	h.start()
	th := owner.add(&ownedThread{id: "th-a"})
	h.mustAttach(sessionEP, "th-a")
	owner.putForeign(th, "person-1")

	h.deliverTo(sessionEP, attempt1, "first")
	h.deliverTo(sessionEP, attempt2, "second")
	if ids := owner.queueIDs(th); len(ids) != 2 || ids[0] != "person-1" || ids[1] != "q-2" {
		t.Fatalf("queue = %v, want the person's item and the second prompt only", ids)
	}
	// The first prompt is withdrawn, so the record cannot show it: absent, and a
	// repeat of its attempt is a new prompt.
	var rec adapterproto.ReconcileResult
	h.mustCall(adapterproto.MethodPromptReconcile, adapterproto.ReconcileParams{Endpoint: sessionEP, Attempt: attempt1}, &rec)
	if rec.Verdict == adapterproto.VerdictFound {
		t.Fatalf("a withdrawn prompt is found: %+v", rec)
	}

	// The person's own item goes first and then the owner takes the second prompt: a
	// third is queued, and nothing is withdrawn, since the second is no longer in the
	// queue.
	owner.mu.Lock()
	th.queue = th.queue[1:]
	owner.mu.Unlock()
	owner.take(th, "t1")
	h.deliverTo(sessionEP, attempt3, "third")
	if n := len(codex.callsOf("thread/queue/delete")); n != 1 {
		t.Fatalf("%d queue/delete calls, want the one that took back the first prompt", n)
	}
	if ids := owner.queueIDs(th); len(ids) != 1 || ids[0] != "q-3" {
		t.Fatalf("queue = %v", ids)
	}
}

// The prompts this adapter queued and nobody took are taken back when the endpoint
// detaches; the person's own items stay.
func TestAttachedPromptsQueuedAreWithdrawnWhenTheEndpointDetaches(t *testing.T) {
	h, _, owner := attachedHarness(t)
	h.start()
	th := owner.add(&ownedThread{id: "th-a"})
	h.mustAttach(sessionEP, "th-a")
	owner.putForeign(th, "person-1")
	h.deliverTo(sessionEP, attempt1, "wake")
	if err := h.daemon.Notify(context.Background(), adapterproto.MethodSessionDetached, adapterproto.SessionDetachedParams{Endpoint: sessionEP}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for ids := owner.queueIDs(th); len(ids) != 1 || ids[0] != "person-1"; ids = owner.queueIDs(th) {
		if time.Now().After(deadline) {
			t.Fatalf("queue = %v after the detach", ids)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A thread the server no longer knows ends the reading. A turn that was open ends
// with it, as failed, and the endpoint says why it takes no prompt.
func TestAttachedThreadGoneEndsTheOpenTurnAndTheEndpointIsUnavailable(t *testing.T) {
	h, _, owner := attachedHarness(t)
	h.start()
	th := owner.add(&ownedThread{id: "th-a"})
	h.mustAttach(sessionEP, "th-a")
	owner.begin(th, "t1", "", time.Now())
	h.waitNote(adapterproto.MethodTurnStarted, turnIs("t1"))
	owner.mu.Lock()
	delete(owner.threads, "th-a")
	owner.mu.Unlock()
	h.waitNote(adapterproto.MethodTurnEnded, func(r json.RawMessage) bool {
		return turnIs("t1")(r) && ended(r, adapterproto.TurnFailed)
	})
	waitAvailabilityNow(t, h, limitedToSession)
}

// A thread that cannot take a queued prompt refuses it as native_unavailable and
// the endpoint stays: the prompt is certainly not queued, so it may be tried again.
func TestAttachedPromptRefusedWhenTheThreadTakesNone(t *testing.T) {
	h, _, owner := attachedHarness(t)
	h.start()
	th := owner.add(&ownedThread{id: "th-a"})
	h.mustAttach(sessionEP, "th-a")

	owner.mu.Lock()
	th.archived = true
	owner.mu.Unlock()
	got := h.deliverTo(sessionEP, attempt1, "hello")
	if got.Stage != adapterproto.PromptRefused || got.Reason != adapterproto.RefusalNativeUnavailable {
		t.Fatalf("archived: %+v", got)
	}
	// The condition is the endpoint's alone, so the adapter's other sessions go on.
	waitAvailabilityNow(t, h, limitedToSession)
	owner.mu.Lock()
	th.archived = false
	owner.mu.Unlock()
	if got := h.deliverTo(sessionEP, attempt1, "hello"); got.Stage != adapterproto.PromptAccepted || owner.queued(th) != 1 {
		t.Fatalf("again, after the thread was restored: %+v", got)
	}
	waitAvailabilityNow(t, h, func(r json.RawMessage) bool { return len(decode[adapterproto.AvailabilityParams](r).Conditions) == 0 })
}

// waitAvailabilityNow waits until the newest availability snapshot satisfies ok; an
// earlier one that did is no answer, since a snapshot replaces the one before.
func waitAvailabilityNow(t *testing.T, h *harness, ok func(json.RawMessage) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok(h.lastAvailability()) {
		if time.Now().After(deadline) {
			t.Fatalf("the newest availability is %s", h.lastAvailability())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// limitedToSession reports whether a snapshot is exactly the condition of a thread that
// takes no prompt, limited to the attached session's endpoint.
func limitedToSession(raw json.RawMessage) bool {
	c := decode[adapterproto.AvailabilityParams](raw).Conditions
	return len(c) == 1 && c[0].Condition == adapterproto.ConditionNativeUnavailable &&
		len(c[0].Endpoints) == 1 && c[0].Endpoints[0] == sessionEP && c[0].SinceMS != 0 && c[0].Detail != ""
}

// The condition of a thread that takes no prompt ends with the endpoint, and the
// thread the person runs is left as it is.
func TestTheConditionOfAThreadEndsWhenItsEndpointDetaches(t *testing.T) {
	h, _, owner := attachedHarness(t)
	h.start()
	th := owner.add(&ownedThread{id: "th-a"})
	h.mustAttach(sessionEP, "th-a")
	owner.mu.Lock()
	th.archived = true
	owner.mu.Unlock()
	if got := h.deliverTo(sessionEP, attempt1, "hello"); got.Stage != adapterproto.PromptRefused {
		t.Fatalf("archived: %+v", got)
	}
	waitAvailabilityNow(t, h, limitedToSession)
	if err := h.daemon.Notify(context.Background(), adapterproto.MethodSessionDetached, adapterproto.SessionDetachedParams{Endpoint: sessionEP}); err != nil {
		t.Fatal(err)
	}
	waitAvailabilityNow(t, h, func(r json.RawMessage) bool { return len(decode[adapterproto.AvailabilityParams](r).Conditions) == 0 })
}

// The adapter stops reading a session when the daemon says its endpoint is closed.
func TestSessionDetachedStopsTheReading(t *testing.T) {
	h, codex, owner := attachedHarness(t)
	h.start()
	owner.add(&ownedThread{id: "th-a"})
	h.mustAttach(sessionEP, "th-a")
	time.Sleep(100 * time.Millisecond)
	if n := len(codex.callsOf("thread/turns/list")); n < 2 {
		t.Fatalf("%d readings: the session is not read", n)
	}
	if err := h.daemon.Notify(context.Background(), adapterproto.MethodSessionDetached, adapterproto.SessionDetachedParams{Endpoint: sessionEP}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	n := len(codex.callsOf("thread/turns/list"))
	time.Sleep(150 * time.Millisecond)
	if after := len(codex.callsOf("thread/turns/list")); after != n {
		t.Fatalf("the reading went on after the detach: %d then %d", n, after)
	}
}

// An attached session is the person's: closing and interrupting it are the wrong
// kind of endpoint, and the thread is not touched.
func TestAttachedSessionTakesNeitherCloseNorInterrupt(t *testing.T) {
	h, codex, owner := attachedHarness(t)
	h.start()
	owner.add(&ownedThread{id: "th-a"})
	h.mustAttach(sessionEP, "th-a")

	var closeRes adapterproto.CloseResult
	if code := codeOf(t, h.call(adapterproto.MethodSessionClose, adapterproto.CloseParams{Endpoint: sessionEP, NativeRef: "th-a"}, &closeRes)); code != adapterproto.CodeWrongEndpointKind {
		t.Fatalf("close: code %d", code)
	}
	var intRes adapterproto.InterruptResult
	if code := codeOf(t, h.call(adapterproto.MethodSessionInterrupt, adapterproto.InterruptParams{Endpoint: sessionEP}, &intRes)); code != adapterproto.CodeWrongEndpointKind {
		t.Fatalf("interrupt: code %d", code)
	}
	for _, m := range []string{"thread/archive", "thread/unsubscribe", "turn/interrupt"} {
		if n := len(codex.callsOf(m)); n != 0 {
			t.Errorf("%d %s calls", n, m)
		}
	}
}

// adapter.start takes attached entries back: a thread that is running a turn says
// so in the answer (the daemon is told by it, not by a notification), an unknown one
// is lost, and the reading goes on.
func TestStartResumesAttachedSessions(t *testing.T) {
	codex := newFakeCodex(t)
	owner := newFakeOwner(t, codex)
	h := newHarnessWith(t, codex, func(o *adapter.Options) {
		o.AttachPollEvery, o.AttachIdlePollEvery = 20*time.Millisecond, 20*time.Millisecond
	})
	th := owner.add(&ownedThread{id: "th-a"})
	owner.begin(th, "t1", "", time.Now())
	res := h.start(
		adapterproto.SessionAttach{Endpoint: sessionEP, Attached: true, NativeRef: "th-a"},
		adapterproto.SessionAttach{Endpoint: otherEP, Attached: true, NativeRef: "th-none"},
	)
	if len(res.Sessions) != 2 || res.Sessions[0].State != adapterproto.AttachAttached || !res.Sessions[0].TurnActive || res.Sessions[1].State != adapterproto.AttachLost {
		t.Fatalf("start = %+v", res)
	}
	if n := h.countNotes(adapterproto.MethodTurnStarted); n != 0 {
		t.Fatalf("the running turn was reported although the answer carries it: %v", h.noteNames())
	}
	owner.finish(th, "t1", "completed")
	h.waitNote(adapterproto.MethodTurnEnded, turnIs("t1"))
}
