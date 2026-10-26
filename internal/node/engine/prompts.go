package engine

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Prompting. A session endpoint is woken by a prompt, a text that says messages wait
// and carries none of them. Whether to send one is the decision of package handoff
// (DecideNodePrompts, which asks DecidePrompt of each endpoint and adds the cap on the
// turns that prompts started); this file keeps what that decision reads, asks it at
// every moment its answer may have changed, and carries out the verdicts.
//
// What the decision reads about an endpoint that the node does not keep on disk is
// memory: the event that is waiting to be judged (promptState.trigger), when the
// endpoint last called recv, and whether a turn that a prompt started may be running.
// A restart forgets them and starts with every endpoint judged once, which costs at
// most one extra prompt for one whose agent had read the last. What it must not forget
// is on disk: the attempts of the endpoint, and the one in flight.
//
// A prompt is carried out in this order. The attempt is recorded as in flight
// (prompt-begin) before the adapter is called, so that a node that dies in between
// finds it. The call runs in a worker, and its result is a command of the loop that
// records how the attempt ended (prompt-end): the target records the prompt covers are
// marked notified first and the endpoint's attempts last. An outcome that is not known
// is asked of the adapter (prompt.reconcile) and never sent again: a second prompt for
// the same attempt could start a second turn.

// promptState is what the loop remembers of the prompting of one session endpoint.
type promptState struct {
	// trigger is the event of the endpoint that waits to be judged: an arrival or the
	// end of a turn. It stays until a prompt is sent to the endpoint or nothing waits
	// for it, so that an endpoint held back by the cap is judged again, with the event
	// that brought it, whenever a place may have fallen free. Zero: no event, and the
	// endpoint is not judged.
	trigger handoff.PromptTrigger
	// lastRecv is when the endpoint last called recv.
	lastRecv time.Time
	// flight is the prompt whose outcome is not on disk.
	flight *promptFlight
	// again says the endpoint is judged once more at its retry, whether or not an event
	// of its own came: its prompt found the adapter not in service.
	again bool
	// turn says a turn that a prompt started may be running: from the dispatch of the
	// prompt until its turn ends or the prompt is known not to have started one. It
	// counts against the cap on the node, and the endpoint is busy while it holds.
	turn bool
}

// promptFlight is a prompt in flight: recorded in the attempts of the endpoint before
// the adapter was called, and not yet recorded as delivered, failed or unknown.
type promptFlight struct {
	attempt adapterproto.AttemptID
	kind    handoff.PromptKind
	// covers are the target records the prompt is for.
	covers []store.TargetKey
	// at is when the prompt was dispatched; zero for one found on disk at the start,
	// which takes the time of its settlement.
	at time.Time
	// calling says a call (the prompt or its reconciliation) is with the adapter.
	calling bool
	// outcome is how the attempt ended once that is known, and zero until then; an
	// outcome that could not be written is kept here for the retry.
	outcome handoff.AttemptOutcome
}

func (e *Engine) promptOf(id ident.EndpointID) *promptState {
	p := e.prompts[id]
	if p == nil {
		p = &promptState{}
		e.prompts[id] = p
	}
	return p
}

func promptKey(kind schedKind, id ident.EndpointID) schedKey { return schedKey{kind, id.String()} }

// promptable says whether the endpoint is a session that is open and mapped to a
// native session: the only kind of endpoint a prompt goes into.
func promptable(st *endpointState) bool {
	return st != nil && st.rec.Kind == adapterreg.KindSession && st.rec.Status == store.EndpointOpen &&
		st.rec.Session != nil && st.rec.Session.NativeRef != ""
}

// ---------------------------------------------------------------------------
// The events that make the decision be asked again

// noteArrival is the event of a message that waits for the endpoint: it was taken
// in, or its lease ran out and it is queued again.
func (e *Engine) noteArrival(id ident.EndpointID) {
	if st := e.view.endpoints[id]; promptable(st) {
		if p := e.promptOf(id); p.trigger == 0 {
			p.trigger = handoff.TriggerArrival
		}
		e.promptsDirty = true
	}
}

// noteTurnEnded is the event of a turn of the endpoint's session that ended, whoever
// started it: the place it held, if a prompt started it, is free, and an agent that
// left its prompt unread is due the one repeat.
func (e *Engine) noteTurnEnded(id ident.EndpointID) {
	if st := e.view.endpoints[id]; promptable(st) {
		p := e.promptOf(id)
		p.turn = false
		p.trigger = handoff.TriggerTurnEnd
	}
	e.promptsDirty = true
}

// noteInstanceStarted is the event of an adapter that came online: the turns it
// reports are those that are running, so a turn that was counted and is not among
// them is over, and every endpoint of the instance is judged, since what waited for an
// adapter that was away has had no other event to be judged by.
func (e *Engine) noteInstanceStarted(instance string, now time.Time) {
	st := e.adapters[instance]
	for id, es := range e.view.endpoints {
		if es.rec.Instance != instance || !promptable(es) {
			continue
		}
		p := e.promptOf(id)
		if p.flight == nil && p.turn && !st.busy[adapterproto.EndpointRef(id.String())] {
			p.turn = false
		}
		if p.trigger == 0 {
			p.trigger = handoff.TriggerArrival
		}
	}
	e.promptsDirty = true
	e.redrivePrompts(instance, now)
}

// ---------------------------------------------------------------------------
// What the decision reads

// pendingOf are the messages that wait for the endpoint's agent, oldest first: the ones
// a recv would hand something out of, less those whose check at the hand-out is a
// suspension, which is no message waiting and no reason to wake anyone.
func (e *Engine) pendingOf(now time.Time, st *endpointState) []store.InRecord {
	var out []store.InRecord
	for _, in := range e.view.queuedIn(st.rec.ID) {
		if !in.Record.AwaitsHandout() {
			continue
		}
		auth, err := e.handoutAuthorization(now, st, in)
		if err != nil || auth == delivery.AuthorizationSuspend {
			continue
		}
		out = append(out, in)
	}
	return out
}

// promptRound is what one asking of the decision is made of.
type promptRound struct {
	waiting []handoff.WaitingPrompt
	covers  map[ident.EndpointID][]store.TargetKey
	running int
}

// promptParams are the values of the prompt rules: the decided ones, and the cap on
// the node's turns, which the owner may change.
func (e *Engine) promptParams() handoff.PromptParams {
	p := handoff.DefaultPromptParams()
	p.MaxConcurrentTurns = e.d.Config.Node().Settings.NodePromptTurns
	return p
}

// promptRound collects what the decision needs. An endpoint whose adapter is offline
// is neither waiting (a prompt for it is bound to fail, and the failure would be
// taken for a reason to call delivery blocked) nor running (the state of its turn is
// unknown, and a turn nobody can observe must not stop the node); it is judged again
// when the adapter comes back. An endpoint with nothing that waits has no event left
// to be judged by.
func (e *Engine) promptRound(now time.Time) promptRound {
	r := promptRound{covers: map[ident.EndpointID][]store.TargetKey{}}
	for id, p := range e.prompts {
		st := e.view.endpoints[id]
		if st == nil {
			delete(e.prompts, id)
			continue
		}
		a := e.adapters[st.rec.Instance]
		if !promptable(st) || a == nil || !a.online {
			continue
		}
		flying := p.turn || p.flight != nil
		if flying {
			r.running++
		}
		if p.trigger == 0 {
			continue
		}
		pending := e.pendingOf(now, st)
		if len(pending) == 0 {
			p.trigger = 0
			e.sched.remove(promptKey(schedPromptRetry, id))
			continue
		}
		in := handoff.PromptInput{
			Trigger: p.trigger, Pending: len(pending), LastArrivalAt: pending[len(pending)-1].AcceptedAt,
			LastRecvAt: p.lastRecv, Busy: flying || a.busy[adapterproto.EndpointRef(id.String())],
		}
		if head := e.view.attempts[id]; head.Attempts != nil {
			in.Attempts = head.Attempts
		}
		keys := make([]store.TargetKey, len(pending))
		for i, m := range pending {
			keys[i] = m.Key
		}
		r.covers[id] = keys
		r.waiting = append(r.waiting, handoff.WaitingPrompt{Endpoint: id, WaitingSince: pending[0].AcceptedAt, Input: in})
	}
	return r
}

// ---------------------------------------------------------------------------
// Asking the decision

// servePrompts asks the decision for every waiting endpoint, when something has made
// its answer able to change, and carries out what it releases. It is a step of the
// loop like serveReady: it reads the clock once, and it starts workers.
func (e *Engine) servePrompts() {
	if e.Phase() != PhaseRunning || e.fatal != nil || e.d.Adapters == nil {
		return
	}
	if !e.promptsStarted {
		// The node has just begun to serve: what waited while it started, or since
		// before it went down, has had no event, so every session is judged once.
		e.promptsStarted, e.promptsDirty = true, true
		for id, st := range e.view.endpoints {
			if promptable(st) {
				if p := e.promptOf(id); p.trigger == 0 {
					p.trigger = handoff.TriggerArrival
				}
			}
		}
	}
	if !e.promptsDirty {
		return
	}
	e.inLoop.Store(true)
	defer e.inLoop.Store(false)
	now := e.d.Clock.Now()
	e.observe(now)
	if e.fatal != nil || e.paused || e.retired {
		// While the wall clock is in doubt the windows of the prompt rules cannot be
		// judged, and a retired node does nothing in its own name; the owner's clearing of
		// the pause asks again.
		return
	}
	e.promptsDirty = false
	round := e.promptRound(now)
	decisions, err := handoff.DecideNodePrompts(round.waiting, round.running, e.promptParams(), now)
	if err != nil {
		e.log.Error("the prompt rules could not be asked", "error", err.Error())
		return
	}
	for _, d := range decisions {
		key := promptKey(schedPromptRetry, d.Endpoint)
		switch {
		case d.Decision.Action == handoff.PromptSend:
			e.sched.remove(key)
			e.dispatchPrompt(now, d.Endpoint, d.Decision, round.covers[d.Endpoint])
		case !d.Decision.RetryAt.IsZero():
			e.sched.set(key, d.Decision.RetryAt)
		default:
			e.sched.remove(key)
		}
		if e.fatal != nil {
			return
		}
	}
}

// dispatchPrompt records the attempt as in flight and calls the adapter. A write that
// fails leaves the endpoint as it was and judges it again after RetryBackoff.
func (e *Engine) dispatchPrompt(now time.Time, id ident.EndpointID, d handoff.PromptDecision, covers []store.TargetKey) {
	st, p := e.view.endpoints[id], e.promptOf(id)
	retry := func(why string, err error) {
		e.log.Warn(why+"; the endpoint is judged again", "endpoint", id.String(), "error", err.Error())
		e.sched.set(promptKey(schedPromptRetry, id), now.Add(e.p.RetryBackoff))
	}
	attempt, err := e.newAttemptID()
	if err != nil {
		retry("an attempt of a prompt could not be drawn", err)
		return
	}
	head := e.view.attempts[id]
	head.Endpoint = id
	head.InFlight = &store.InFlightPrompt{Attempt: attempt, Kind: d.Kind, Covers: covers}
	if err := e.d.Store.PromptBegin(head); err != nil {
		e.classify(err)
		if e.fatal == nil {
			retry("the attempt of a prompt could not be recorded", err)
		}
		return
	}
	e.view.attempts[id] = head
	p.flight = &promptFlight{attempt: attempt, kind: d.Kind, covers: covers, at: now}
	p.turn, p.trigger = true, 0
	params := adapterproto.PromptDeliverParams{
		Endpoint: adapterproto.EndpointRef(id.String()), Attempt: attempt, Text: handoff.PromptText(d.Count),
	}
	e.callDeliver(st.rec.Instance, id, params)
}

// callDeliver sends the prompt to the adapter from a worker.
func (e *Engine) callDeliver(instance string, id ident.EndpointID, params adapterproto.PromptDeliverParams) {
	e.promptOf(id).flight.calling = true
	if !e.spawn(func(ctx context.Context) {
		e.adapterCalls.Add(1)
		res, err := e.d.Adapters.DeliverPrompt(ctx, instance, params)
		e.adapterCalls.Add(-1)
		c := &cmdPromptResult{call: newCall[struct{}](), endpoint: id, attempt: params.Attempt, res: res, err: err}
		_, _ = roundTrip(ctx, e, submitInternal, c, c.call)
	}) {
		e.promptOf(id).flight.calling = false
	}
}

type cmdPromptResult struct {
	call[struct{}]
	endpoint ident.EndpointID
	attempt  adapterproto.AttemptID
	res      adapterproto.PromptDeliverResult
	err      error
}

func (c *cmdPromptResult) gate() gate { return gateAlways }
func (c *cmdPromptResult) run(e *Engine, now time.Time) {
	e.promptResult(now, c)
	c.done(struct{}{}, nil)
}

// promptResult takes in what the call of a prompt brought. A prompt the adapter took
// is delivered and one it refused certainly is not. An outcome that is not known is
// asked of the adapter, and never sent again.
func (e *Engine) promptResult(now time.Time, r *cmdPromptResult) {
	p := e.prompts[r.endpoint]
	if p == nil || p.flight == nil || p.flight.attempt != r.attempt {
		return
	}
	p.flight.calling = false
	outcome, notInService := handoff.AttemptUnknown, false
	switch {
	case r.err == nil:
		var err error
		if outcome, err = adapterreg.PromptOutcome(r.res); err != nil {
			e.log.Error("the answer to a prompt is not one the protocol defines; its outcome is asked of the adapter", "endpoint", r.endpoint.String(), "error", err.Error())
			outcome = handoff.AttemptUnknown
		}
	case e.ctx.Err() != nil:
		return // the node is stopping: the next start finds the attempt in flight and asks
	case errors.Is(r.err, adapterhost.ErrNotConnected):
		// Nothing was sent. The adapter has told the node that it started, and the host
		// puts the connection in service a moment later: the endpoint is judged again
		// when the adapter has been probed, which is in service, and at the retry.
		outcome, notInService = handoff.AttemptFailed, true
	default:
		outcome = adapterreg.PromptOutcomeOnError(r.err)
	}
	if outcome == handoff.AttemptUnknown {
		e.startReconcile(now, r.endpoint)
		return
	}
	p.flight.outcome = outcome
	e.settlePrompt(now, r.endpoint)
	if notInService && p.flight == nil {
		p.again = true
		e.sched.set(promptKey(schedPromptRetry, r.endpoint), now.Add(e.p.CreateRetryBackoff))
	}
}

// ---------------------------------------------------------------------------
// Reconciliation

// startReconcile asks the adapter whether the attempt in flight reached the session.
// An adapter that is not connected is asked when it is.
func (e *Engine) startReconcile(now time.Time, id ident.EndpointID) {
	p, st := e.prompts[id], e.view.endpoints[id]
	if p == nil || p.flight == nil || p.flight.calling || st == nil {
		return
	}
	if p.flight.outcome != 0 {
		e.settlePrompt(now, id)
		return
	}
	if a := e.adapters[st.rec.Instance]; e.d.Adapters == nil || a == nil || !a.online {
		return
	}
	params := adapterproto.ReconcileParams{Endpoint: adapterproto.EndpointRef(id.String()), Attempt: p.flight.attempt}
	if cp, ok := e.view.checkpoints[store.CheckpointKey{Scope: store.CheckpointScopeAttempt, ID: string(p.flight.attempt)}]; ok {
		params.Checkpoint = json.RawMessage(slices.Clone(cp))
	}
	instance, attempt := st.rec.Instance, p.flight.attempt
	p.flight.calling = true
	if !e.spawn(func(ctx context.Context) {
		e.adapterCalls.Add(1)
		res, err := e.d.Adapters.Reconcile(ctx, instance, params)
		e.adapterCalls.Add(-1)
		c := &cmdReconcileResult{call: newCall[struct{}](), endpoint: id, attempt: attempt, res: res, err: err}
		_, _ = roundTrip(ctx, e, submitInternal, c, c.call)
	}) {
		p.flight.calling = false
	}
}

type cmdReconcileResult struct {
	call[struct{}]
	endpoint ident.EndpointID
	attempt  adapterproto.AttemptID
	res      adapterproto.ReconcileResult
	err      error
}

func (c *cmdReconcileResult) gate() gate { return gateAlways }
func (c *cmdReconcileResult) run(e *Engine, now time.Time) {
	e.reconcileResult(now, c)
	c.done(struct{}{}, nil)
}

// reconcileResult takes in the verdict of the adapter on an attempt. Found is a
// delivery, and the turn the prompt started counts against the cap while it runs;
// provably absent is a failure; anything else, a call that got no answer included,
// leaves the outcome unknown, which counts toward the hourly cap and nothing else. The
// attempt is not sent again whatever the verdict: a prompt that may have started a turn
// is never sent a second time.
func (e *Engine) reconcileResult(now time.Time, r *cmdReconcileResult) {
	p := e.prompts[r.endpoint]
	if p == nil || p.flight == nil || p.flight.attempt != r.attempt {
		return
	}
	p.flight.calling = false
	switch {
	case r.err != nil && e.ctx.Err() != nil:
		return
	case errors.Is(r.err, adapterhost.ErrNotConnected):
		e.sched.set(promptKey(schedPromptReconcile, r.endpoint), now.Add(e.p.RetryBackoff))
		return
	case r.err != nil:
		e.log.Warn("the adapter gave no verdict on a prompt; its outcome stays unknown", "endpoint", r.endpoint.String(), "error", r.err.Error())
		p.flight.outcome, p.turn = handoff.AttemptUnknown, false
	default:
		outcome, err := adapterreg.ReconcileOutcome(r.res)
		if err != nil {
			e.log.Error("the verdict on a prompt is not one the protocol defines; its outcome stays unknown", "endpoint", r.endpoint.String(), "error", err.Error())
			outcome = handoff.AttemptUnknown
		}
		p.flight.outcome = outcome
		p.turn = outcome == handoff.AttemptDelivered && r.res.Turn != nil && r.res.Turn.State == adapterproto.TurnStateRunning
	}
	e.settlePrompt(now, r.endpoint)
}

// redrivePrompts asks again for every attempt in flight that has no call out and
// belongs to the instance: it has just connected, and what it was asked before its
// connection ended it may or may not have done.
func (e *Engine) redrivePrompts(instance string, now time.Time) {
	var ids []ident.EndpointID
	for id, p := range e.prompts {
		if st := e.view.endpoints[id]; p.flight != nil && st != nil && st.rec.Instance == instance {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	for _, id := range ids {
		e.startReconcile(now, id)
	}
}

// reconcilePrompts is the part of step 11 of the start that is for prompts: every
// attempt that was in flight when the node went down is asked of its adapter, which
// may be asked when it connects. What an attempt found in flight says is only that
// the adapter was called, so nothing is inferred from it.
func (e *Engine) reconcilePrompts(now time.Time) {
	var ids []ident.EndpointID
	for id, head := range e.view.attempts {
		if head.InFlight == nil {
			continue
		}
		if !promptable(e.view.endpoints[id]) {
			e.log.Info("an attempt was in flight for an endpoint that is no longer open; it is left", "endpoint", id.String())
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	for _, id := range ids {
		f := e.view.attempts[id].InFlight
		e.promptOf(id).flight = &promptFlight{attempt: f.Attempt, kind: f.Kind, covers: slices.Clone(f.Covers)}
		e.startReconcile(now, id)
	}
}

// ---------------------------------------------------------------------------
// Settling an attempt

// attemptWindow is how long an attempt matters to the hourly cap.
const attemptWindow = time.Hour

// trimAttempts drops the attempts nothing reads any more: those that are older than
// the window of the hourly cap and come before the newest delivered first prompt,
// which is where the chain of repeats the rules count begins.
func trimAttempts(attempts []handoff.Attempt, now time.Time) []handoff.Attempt {
	keep := 0
	for i, a := range attempts {
		if a.Outcome == handoff.AttemptDelivered && a.Kind == handoff.PromptKindFirst {
			keep = i
		}
	}
	for keep > 0 && attempts[keep-1].At.After(now.Add(-attemptWindow)) {
		keep--
	}
	return attempts[keep:]
}

// settlePrompt records how the attempt in flight ended, whose outcome is known. The
// target records it covers are marked notified when it was delivered, and the
// endpoint's attempts say it is done last. The checkpoint of the attempt is dropped
// after that, never before: it is what a reconciliation of an attempt that did not
// settle is given. A write that fails is tried again after RetryBackoff, with the
// outcome kept.
func (e *Engine) settlePrompt(now time.Time, id ident.EndpointID) {
	p := e.prompts[id]
	f := p.flight
	at := f.at
	if at.IsZero() {
		at = now
	}
	head := e.view.attempts[id]
	head.Attempts = trimAttempts(append(slices.Clone(head.Attempts), handoff.Attempt{At: at, Outcome: f.outcome, Kind: f.kind}), now)
	head.InFlight = nil
	var ins []store.InRecord
	if f.outcome == handoff.AttemptDelivered {
		for _, k := range f.covers {
			in, ok := e.view.in[k]
			if !ok {
				continue
			}
			rec, out, err := in.Record.Apply(now, delivery.Event{Type: delivery.EventNotified})
			if err != nil {
				e.log.Error("a prompt could not be marked on the message it is for", "message", string(k.Message), "error", err.Error())
				continue
			}
			if out.Result == delivery.ResultApplied {
				in.Record = rec
				ins = append(ins, in)
			}
		}
	}
	if err := e.d.Store.PromptEnd(head, ins); err != nil {
		e.classify(err)
		if e.fatal == nil {
			e.log.Warn("the outcome of a prompt could not be recorded; it is tried again", "endpoint", id.String(), "error", err.Error())
			e.sched.set(promptKey(schedPromptSettle, id), now.Add(e.p.RetryBackoff))
		}
		return
	}
	for _, in := range ins {
		e.view.setIn(in)
	}
	e.view.attempts[id] = head
	p.flight = nil
	if f.outcome != handoff.AttemptDelivered {
		p.turn = false
	}
	e.sched.remove(promptKey(schedPromptSettle, id))
	e.sched.remove(promptKey(schedPromptReconcile, id))
	e.dropAttemptCheckpoint(f.attempt)
	e.promptsDirty = true
}

// dropAttemptCheckpoint removes the checkpoint of an attempt that has settled. A
// failure leaves a file nobody asks for again, so it is logged and not tried again.
func (e *Engine) dropAttemptCheckpoint(attempt adapterproto.AttemptID) {
	key := store.CheckpointKey{Scope: store.CheckpointScopeAttempt, ID: string(attempt)}
	if err := e.d.Store.CheckpointDrop([]store.CheckpointKey{key}); err != nil {
		e.classify(err)
		if e.fatal == nil {
			e.log.Warn("the checkpoint of a settled attempt could not be removed", "attempt", string(attempt), "error", err.Error())
		}
		return
	}
	delete(e.view.checkpoints, key)
}

// retryPrompt is the due retry of an endpoint that is held back or whose prompt found
// the adapter not in service: it is judged again, with the event it has or, for the
// latter, as if a message had just arrived.
func (e *Engine) retryPrompt(id ident.EndpointID) {
	if p := e.prompts[id]; p != nil && p.again {
		p.again = false
		if p.trigger == 0 && promptable(e.view.endpoints[id]) {
			p.trigger = handoff.TriggerArrival
		}
	}
	e.promptsDirty = true
}

// fireSettle is the retry of an outcome that could not be written.
func (e *Engine) fireSettle(now time.Time, id ident.EndpointID) {
	if p := e.prompts[id]; p != nil && p.flight != nil && p.flight.outcome != 0 {
		e.settlePrompt(now, id)
	}
}
