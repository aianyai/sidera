package engine

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Leases while the agent works, and acknowledging for it.
//
// A page that recv hands out is leased for delivery.LeaseDuration, and the lease is
// what keeps the message from being handed out again while the agent still holds it.
// An agent that works through a long turn without acknowledging would see its message
// again as a possible duplicate every few minutes, so the node extends the leases of
// an endpoint for as long as the adapter reports a turn running for it, never past
// delivery.LeaseRenewalCap from the issue (delivery.Record.Renew decides all of that;
// this file only asks when). A lease that was not extended in time, or whose cap was
// reached, runs out and is released by the tick of its record like any other; there is
// no state in which an expired lease is held, because the delivery rules requeue it.
//
// The extension is made before the lease runs out, renewLead before, because the rules
// refuse to extend one that already has. The schedule keeps one entry for each
// endpoint, at the earliest instant an extension of one of its leases is due, and the
// entry is planned again whenever what it depends on changes: a record of the endpoint,
// a turn that started or ended, a connection that began or ended.
//
// An adapter whose runtime shows the model the page it was handed says so with
// recv.observed, and the node acknowledges the page for the agent when what the model
// saw contains the page in full. That is the one place the node acknowledges for
// someone else, so it acknowledges only what it is certain of: the page must be one
// this node handed out and still remembers (a restart forgets them, and the agent then
// acknowledges by itself or is handed the message again), and a result the runtime cut
// or replaced never counts.

// renewLead is how long before a lease runs out the node extends it. A lease is
// extended to a full LeaseDuration from the instant of the extension, so extending at
// half the duration keeps each lease live with room for a loop that is busy for a while.
const renewLead = delivery.LeaseDuration / 2

func renewKey(id ident.EndpointID) schedKey { return schedKey{schedRenew, id.String()} }

// markLeasesFor says the leases of the endpoint are to be looked at at the next step of
// the loop.
func (e *Engine) markLeasesFor(id ident.EndpointID) {
	if promptable(e.view.endpoints[id]) {
		e.leaseDirty[id] = true
	}
}

// noteLeasesInput is the event of a target record of the endpoint that was taken in,
// changed or dropped: a lease was issued, extended or ended.
func (e *Engine) noteLeasesInput(recipient ident.EndpointID) { e.markLeasesFor(recipient) }

// noteLeasesOfInstance is the event of an adapter whose connection or turns changed:
// the turns of every session of the instance may have started or ended.
func (e *Engine) noteLeasesOfInstance(instance string) {
	for id, st := range e.view.endpoints {
		if st.rec.Instance == instance {
			e.markLeasesFor(id)
		}
	}
}

// noteLeasesOfEndpoint is noteLeasesOfInstance for a turn that began or ended, which
// the adapter names by the endpoint's reference. An endpoint the engine does not know
// has no lease.
func (e *Engine) noteLeasesOfEndpoint(ref adapterproto.EndpointRef) {
	if id, err := ident.ParseEndpointID(string(ref)); err == nil {
		e.markLeasesFor(id)
	}
}

// turnActive says whether the adapter of the endpoint reports a turn running for it. A
// turn the adapter has not reported, or whose adapter is not connected, is not one.
func (e *Engine) turnActive(st *endpointState) bool {
	a := e.adapters[st.rec.Instance]
	return a != nil && a.online && a.busy[adapterproto.EndpointRef(st.rec.ID.String())]
}

// serveLeases extends the leases that are due for the endpoints that are marked, in the
// order of their ids so that two runs write in the same order. It is a step of the loop
// like serveBlocked.
func (e *Engine) serveLeases() {
	if e.Phase() != PhaseRunning || e.fatal != nil {
		return
	}
	if len(e.leaseDirty) == 0 {
		return
	}
	e.inLoop.Store(true)
	defer e.inLoop.Store(false)
	now := e.d.Clock.Now()
	e.observe(now)
	if e.fatal != nil {
		return
	}
	ids := make([]ident.EndpointID, 0, len(e.leaseDirty))
	for id := range e.leaseDirty {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	for _, id := range ids {
		delete(e.leaseDirty, id)
		e.renewLeases(now, id)
		if e.fatal != nil {
			return
		}
	}
}

// renewLeases extends the leases of the endpoint that are due and sets the entry of the
// schedule to the next instant one is. While the wall clock is in doubt nothing is
// extended, as nothing is judged expired.
func (e *Engine) renewLeases(now time.Time, id ident.EndpointID) {
	key := renewKey(id)
	st := e.view.endpoints[id]
	if !promptable(st) || !e.turnActive(st) {
		e.sched.remove(key)
		return
	}
	if e.paused {
		e.sched.set(key, now.Add(e.p.RetryBackoff))
		return
	}
	var due []store.InRecord
	for _, in := range e.view.queuedIn(id) {
		at, ok := renewalAt(in.Record, now)
		if !ok || at.After(now) {
			continue
		}
		rec, _ := in.Record.Renew(now, true)
		in.Record = rec
		due = append(due, in)
	}
	for len(due) > 0 {
		n := min(len(due), e.p.ChunkReplaces)
		if err := e.d.Store.Lease(due[:n]); err != nil {
			e.classify(err)
			if e.fatal == nil {
				e.log.Warn("leases could not be extended; they are tried again", "endpoint", id.String(), "error", err.Error())
				e.sched.set(key, now.Add(e.p.RetryBackoff))
			}
			return
		}
		for _, in := range due[:n] {
			e.view.setIn(in)
		}
		due = due[n:]
	}
	var next time.Time
	for _, in := range e.view.queuedIn(id) {
		if at, ok := renewalAt(in.Record, now); ok && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	if next.IsZero() {
		e.sched.remove(key)
		return
	}
	e.sched.set(key, next)
}

// renewalAt is the instant the lease of the record is to be extended at, and false when
// it cannot be: it holds no live lease or the lease already lives as long as the cap
// allows. The delivery rules answer that; asking them with a turn active is asking
// only about the lease.
func renewalAt(r delivery.Record, now time.Time) (time.Time, bool) {
	if _, res := r.Renew(now, true); res != delivery.Renewed {
		return time.Time{}, false
	}
	return r.Lease.ExpiresAt.Add(-renewLead), true
}

// ---------------------------------------------------------------------------
// recv.observed

type cmdObserved struct {
	call[adapterproto.RecvObservedResult]
	instance string
	params   adapterproto.RecvObservedParams
}

func (c *cmdObserved) gate() gate { return gateAdapter }
func (c *cmdObserved) run(e *Engine, now time.Time) {
	res, err := e.observed(now, c.instance, c.params)
	c.done(res, e.classify(err))
}

// RecvObserved judges the result of a recv as the adapter read it in the runtime's
// record and, when the model was shown the whole page, acknowledges every part of it for
// the agent and says what became of each. A result that is not the whole page, because
// the runtime cut it or replaced it with a notice or it is something else, is refused
// with the reason, and nothing is acknowledged: the page is left to the agent's own
// acknowledgement or to the lease, which hands the message out again. A page the node
// does not remember, which is every page of an earlier run of the node, is refused as
// unknown for the same reason: what cannot be compared is not acknowledged.
func (e *Engine) RecvObserved(ctx context.Context, instance string, p adapterproto.RecvObservedParams) (adapterproto.RecvObservedResult, error) {
	c := &cmdObserved{call: newCall[adapterproto.RecvObservedResult](), instance: instance, params: p}
	return roundTrip(ctx, e, submitInternal, c, c.call)
}

func (e *Engine) observed(now time.Time, instance string, p adapterproto.RecvObservedParams) (adapterproto.RecvObservedResult, error) {
	var none adapterproto.RecvObservedResult
	adm, ok := e.admission(instance)
	if !ok {
		return none, refuse(ReasonInstanceUnknown)
	}
	st, err := e.view.resolveSession(instance, p.Endpoint)
	if err != nil {
		return none, err
	}
	refused := func(r adapterproto.AckReason) (adapterproto.RecvObservedResult, error) {
		return adapterproto.RecvObservedResult{Verdict: adapterproto.AckRefused, Reason: r, Items: []adapterproto.PartAck{}}, nil
	}
	lease, err := delivery.ParseLeaseID(string(p.Lease))
	if err != nil {
		return refused(adapterproto.AckUnknownLease)
	}
	page := e.pages[lease]
	if page == nil || page.endpoint != st.rec.ID {
		return refused(adapterproto.AckUnknownLease)
	}
	if verdict, reason := handoff.CheckBeforeAck(adm.Markers(), p.Observed, page.output); verdict != handoff.AckAllowed {
		return refused(refusalReason(reason))
	}
	items, err := e.ack(now, st, AckRequest{Lease: p.Lease})
	var ref *Refusal
	if errors.As(err, &ref) && ref.Reason == ReasonLeaseUnknown {
		return refused(adapterproto.AckUnknownLease) // every record the page was handed out on is gone
	}
	if err != nil {
		return none, err
	}
	return adapterproto.RecvObservedResult{Verdict: adapterproto.AckAllowed, Reason: adapterproto.AckSeenInFull, Items: items}, nil
}

// refusalReason is what the protocol calls a reason of handoff.CheckBeforeAck for a
// refusal.
func refusalReason(r handoff.AckReason) adapterproto.AckReason {
	switch r {
	case handoff.AckReplaced:
		return adapterproto.AckReplaced
	case handoff.AckTruncated:
		return adapterproto.AckTruncated
	}
	return adapterproto.AckContentMismatch
}
