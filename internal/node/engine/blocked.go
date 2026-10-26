package engine

import (
	"slices"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Blocked delivery. Delivery to a session endpoint is blocked when prompts have been
// failing to reach the session for longer than the threshold because of a cause that
// still holds (package handoff, AssessBlocked). This file keeps the moments at which
// that is judged, and carries out the verdicts: the fact goes on the target records of
// the messages that wait, and the endpoint's attempts record, the head, says it is
// reported. The sender learns it the way it learns every other fact about a delivery:
// by pulling the report of the target, which carries the flag, and the delivery rules
// produce the item of feedback once per delivery; nothing here tells a sender anything.
//
// A block is written like a prompt: the target records first and the head last, in one
// call (store.Blocked). A crash between leaves some records marked and the head not
// saying so, which costs nothing as long as marks are found by themselves: a verdict
// that says nothing is reported lifts the mark of every record that still has one.
// Judging is repeated whenever its input may have changed, and a verdict that changes
// nothing writes nothing.

// markBlockedFor says the endpoint is to be judged at the next step of the loop.
func (e *Engine) markBlockedFor(id ident.EndpointID) {
	if st := e.view.endpoints[id]; promptable(st) {
		e.blockedDirty[id] = true
	}
}

// noteBlockedInput is the event of a target record of the endpoint that was taken in,
// changed or dropped: what waits for the endpoint is what the judgment reads.
func (e *Engine) noteBlockedInput(recipient ident.EndpointID) { e.markBlockedFor(recipient) }

// noteBlockedInstance is the event of an adapter whose connection, availability or
// service state changed: the cause of every session of the instance may have started
// or ended.
func (e *Engine) noteBlockedInstance(instance string) {
	for id, st := range e.view.endpoints {
		if st.rec.Instance == instance {
			e.markBlockedFor(id)
		}
	}
}

// noteBlockedAll is the event that may change the judgment of every session: the node
// begins to serve, or the owner has cleared a wall clock that was set back.
func (e *Engine) noteBlockedAll() {
	for id := range e.view.endpoints {
		e.markBlockedFor(id)
	}
}

// blockedKey is the entry of the schedule for the judgment of an endpoint.
func blockedKey(id ident.EndpointID) schedKey { return schedKey{schedBlocked, id.String()} }

// serveBlocked judges the endpoints that are marked, in the order of their ids so that
// two runs write in the same order. It is a step of the loop like servePrompts and
// comes before it: a mark that a verdict writes is an event the prompt rules read.
func (e *Engine) serveBlocked() {
	if e.Phase() != PhaseRunning || e.fatal != nil {
		return
	}
	if e.servingSince.IsZero() {
		// The node has just begun to serve: what waited while it started, or since
		// before it went down, has had no event, so every session is judged once.
		e.servingSince = e.d.Clock.Now()
		e.noteBlockedAll()
	}
	if len(e.blockedDirty) == 0 {
		return
	}
	e.inLoop.Store(true)
	defer e.inLoop.Store(false)
	now := e.d.Clock.Now()
	e.observe(now)
	if e.fatal != nil || e.paused || e.retired {
		// While the wall clock is in doubt the windows of the rules cannot be judged,
		// and a retired node reports nothing in its own name; the owner's clearing of
		// the pause judges every session again.
		return
	}
	ids := make([]ident.EndpointID, 0, len(e.blockedDirty))
	for id := range e.blockedDirty {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	for _, id := range ids {
		e.judgeBlocked(now, id)
		delete(e.blockedDirty, id)
		if e.fatal != nil {
			return
		}
	}
}

// blockedSignals are the causes that keep the adapter from working for the endpoint. For
// an endpoint whose block is not reported, an adapter that has not been online since the
// node began to serve is offline since then: before that the node did not look, and a block
// reported on the node's own start for an adapter that is about to connect would tell
// senders what is not so. For an endpoint whose block is reported the start of the cause
// is left unknown, which measures from the oldest message that waits: a restart must not
// shorten the failing run of a block the senders were told of, because a correction
// would then be false and the block, told once per delivery, would never be told again.
// The block then goes on until a connection shows the cause gone or a prompt is delivered.
// A block is reported when the head says so or when a message that waits carries the mark:
// a crash between the writes of the marks and of the head leaves senders that may already
// have been told.
func (e *Engine) blockedSignals(st *endpointState, head store.AttemptsRecord, pending []store.InRecord) handoff.Signals {
	sig := e.adapterSignals(st.rec.Instance, adapterproto.EndpointRef(st.rec.ID.String()))
	if sig.AdapterOffline && sig.Since.IsZero() && !blockedReported(head, pending) {
		sig.Since = e.servingSince
	}
	return sig
}

// blockedReported says whether senders may have been told that delivery to the endpoint is
// blocked.
func blockedReported(head store.AttemptsRecord, pending []store.InRecord) bool {
	return head.Blocked.Cause != 0 || slices.ContainsFunc(pending, func(in store.InRecord) bool { return in.Record.Blocked })
}

// judgeBlocked asks whether delivery to the endpoint is blocked and writes what the
// verdict changes. A write that fails changes nothing and is tried again after
// RetryBackoff; a block that is not yet long enough is judged again at the instant
// the rule names.
func (e *Engine) judgeBlocked(now time.Time, id ident.EndpointID) {
	key := blockedKey(id)
	st := e.view.endpoints[id]
	if !promptable(st) {
		e.sched.remove(key)
		return
	}
	head := e.view.attempts[id]
	head.Endpoint = id
	pending := e.pendingOf(now, st)
	in := handoff.BlockedInput{Attempts: head.Attempts, Signals: e.blockedSignals(st, head, pending), Reported: head.Blocked}
	if len(pending) > 0 {
		in.OldestPendingAt = pending[0].AcceptedAt
	}
	v := handoff.AssessBlocked(in, handoff.DefaultBlockedParams(), now)

	event := delivery.Event{Type: delivery.EventUnblocked}
	targets := e.view.queuedIn(id)
	switch v.Status {
	case handoff.BlockedNew, handoff.BlockedOngoing:
		cause, err := adapterreg.BlockedCause(v.Cause)
		if err != nil {
			e.log.Error("delivery is blocked for a cause the delivery rules have no name for; it is not reported", "endpoint", id.String(), "error", err.Error())
			return
		}
		event = delivery.Event{Type: delivery.EventBlocked, Cause: cause}
		targets = pending
		if v.Status == handoff.BlockedNew {
			head.Blocked = handoff.BlockedReport{Cause: v.Cause, Since: v.Since}
		}
	case handoff.BlockedRecovered:
		head.Blocked = handoff.BlockedReport{}
	}
	var ins []store.InRecord
	for _, t := range targets {
		if event.Type == delivery.EventUnblocked && !t.Record.Blocked {
			continue // marks are found by themselves, whatever the head says
		}
		rec, out, err := t.Record.Apply(now, event)
		if err != nil {
			e.log.Error("a block could not be applied to the message it is for", "message", string(t.Key.Message), "error", err.Error())
			continue
		}
		if out.Result == delivery.ResultApplied {
			t.Record = rec
			ins = append(ins, t)
		}
	}
	if old := e.view.attempts[id].Blocked; len(ins) == 0 && old.Cause == head.Blocked.Cause && old.Since.Equal(head.Blocked.Since) {
		e.armBlocked(key, v)
		return
	}
	if err := e.d.Store.Blocked(head, ins); err != nil {
		e.classify(err)
		if e.fatal == nil {
			e.log.Warn("the judgment of blocked delivery could not be recorded; it is tried again", "endpoint", id.String(), "error", err.Error())
			e.sched.set(key, now.Add(e.p.RetryBackoff))
		}
		return
	}
	for _, in := range ins {
		e.view.setIn(in)
	}
	e.view.attempts[id] = head
	e.armBlocked(key, v)
}

// armBlocked points the entry of the endpoint at the instant the rule says to judge it
// again, and takes it off when the rule names none.
func (e *Engine) armBlocked(key schedKey, v handoff.BlockedVerdict) {
	if v.RetryAt.IsZero() {
		e.sched.remove(key)
		return
	}
	e.sched.set(key, v.RetryAt)
}

// probeFallback is the end of the wait for the probe of an adapter that connected:
// the connection has been in service all along (the host puts it in service before it
// probes), and a probe that failed is only logged by the host, so nothing else would
// judge what waited for the adapter. It is judged once, as a probe that answered is.
func (e *Engine) probeFallback(now time.Time, instance string) {
	if st := e.adapters[instance]; st != nil && st.online {
		e.noteInstanceProbed(instance, now)
	}
}
