package engine

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/reply"
)

// The outbound side: what the node does after it accepted a message, until every
// delivery of it is settled. A message is sent to the node of each recipient, and
// the reports the target signs come back in the receipt or in a pull. The sending
// is done by one worker, outside the loop, in the order the work was given, so
// that a step of a scenario has one order of commands; the loop schedules (the
// retries and the pulls are entries of its schedule on the injected clock) and
// applies what the worker brings back.

// workKind says what one piece of outbound work is.
type workKind uint8

const (
	// workSend sends the envelope to the node of its recipients.
	workSend workKind = iota + 1
	// workPull asks the node for the reports of the deliveries of a message.
	workPull
	// workFinalPull is the one pull made after report_by, before a delivery that is
	// still queued or accepted is closed (see Engine.finalPullDue). It asks like workPull;
	// the loop treats its end, whatever the answer, as the moment the delivery may
	// close.
	workFinalPull
)

// outWork is one call to the transport.
type outWork struct {
	kind workKind
	// name is the source record the work is for; node is the node it goes to.
	name string
	node ident.NodeID
	// sender and data are the sending endpoint and the encoded envelope of a send.
	sender ident.EndpointID
	data   []byte
	// message is the message a pull is about, and recipients are the endpoints on
	// the node whose deliveries of it the pull asks about.
	message    delivery.MessageID
	recipients []ident.EndpointID
}

// outbox is the queue of the outbound worker and what the loop remembers about
// the messages it is working on. The queue is shared with the worker and guarded;
// the rest belongs to the loop.
type outbox struct {
	mu     sync.Mutex
	queue  []outWork
	active bool // a worker goroutine is running

	// state is what the loop knows of each source record that is not settled.
	state map[string]*outState
	// finalPulled are the deliveries, by source record and recipient, whose pull
	// before the close has been made, or has failed, in this run: the close then no
	// longer waits for it. Like state it is not persisted: after a restart the
	// engine works out from the records which deliveries still need the pull, and a
	// pull made twice is harmless. An entry goes when its delivery closes.
	finalPulled map[finalPullKey]struct{}
}

// finalPullKey names one delivery of a source record.
type finalPullKey struct {
	name      string
	recipient ident.EndpointID
}

// outState is the loop's memory of one source record's outbound work. It is not
// persisted: after a restart every unsettled record is sent again, which the
// target answers from the record it kept.
type outState struct {
	// sent are the nodes a send of the envelope got an answer from: they have it,
	// and what is missing is their reports. A node that has not answered is still
	// sent to, whatever the others did.
	sent map[ident.NodeID]bool
	// inflight is how many pieces of work for the record are with the worker.
	inflight int
	// failingSince is, for each node a call has failed to, the time of the first
	// failure of the run that is going on: it is forgotten when a call to the node
	// gets an answer. It says for how long contact has been failing.
	failingSince map[ident.NodeID]time.Time
}

func newOutbox() *outbox {
	return &outbox{state: map[string]*outState{}, finalPulled: map[finalPullKey]struct{}{}}
}

func (b *outbox) next() (outWork, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) == 0 {
		b.active = false
		return outWork{}, false
	}
	w := b.queue[0]
	b.queue = b.queue[1:]
	return w, true
}

// give hands the worker a piece of work and starts it when none runs. It runs in
// the loop, which is where workers are started.
func (e *Engine) give(w outWork) {
	b := e.outbox
	b.mu.Lock()
	b.queue = append(b.queue, w)
	start := !b.active
	b.active = true
	b.mu.Unlock()
	if start && !e.spawn(e.pumpOutbox) {
		b.mu.Lock()
		b.active = false
		b.mu.Unlock()
	}
}

// outboundParked says that the worker has nothing to do but wait out a delay the
// transport was asked to make: that is not work, and the engine is quiet.
func (e *Engine) outboundParked() bool {
	return e.outboundActive() && e.tr != nil && e.tr.Waiting() > 0
}

// outboundActive says that the worker goroutine is running, whether it works or waits.
func (e *Engine) outboundActive() bool {
	b := e.outbox
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active
}

// pumpOutbox is the worker: it makes the calls one after another and hands each
// result to the loop, waiting until it was applied before it takes the next, so
// that what the loop decides from one answer is decided before the next call.
func (e *Engine) pumpOutbox(ctx context.Context) {
	for {
		w, ok := e.outbox.next()
		if !ok {
			return
		}
		res := &cmdOutResult{call: newCall[struct{}](), work: w}
		switch w.kind {
		case workSend:
			var rc transport.Receipt
			rc, res.err = e.tr.Send(ctx, w.node, w.sender, w.data)
			res.reports, res.controlReply = rc.Reports, rc.ControlReply
		case workPull, workFinalPull:
			var ans transport.PullAnswer
			ans, res.err = e.tr.Pull(ctx, w.node, transport.PullRequest{Message: w.message, Recipients: w.recipients})
			res.reports, res.noRecord, res.controlReply = ans.Reports, ans.NoRecord, ans.ControlReply
		}
		if _, err := roundTrip(ctx, e, submitInternal, res, res.call); err != nil && ctx.Err() != nil {
			return
		}
	}
}

// dispatchOutbound looks at a source record and gives the worker what it needs:
// a send to each node that has not answered, or else a pull of the reports of
// each delivery that is not settled. A record with nothing left to do is
// forgotten. Nothing is scheduled while work for the record is with the worker:
// its result schedules the next look.
func (e *Engine) dispatchOutbound(name string, now time.Time) {
	o, ok := e.view.out[name]
	if !ok {
		e.forgetOutbound(name)
		return
	}
	if !e.outboundWorks(o) {
		delete(e.outbox.state, name)
		return
	}
	st := e.outbox.state[name]
	if st == nil {
		st = &outState{sent: map[ident.NodeID]bool{}, failingSince: map[ident.NodeID]time.Time{}}
		e.outbox.state[name] = st
	}
	if st.inflight > 0 {
		return
	}
	plan := e.outboundPlan(o.Outbound, now, st.sent, e.finalPulledOf(name))
	if len(plan) == 0 {
		delete(e.outbox.state, name)
		return
	}
	nodes := make([]ident.NodeID, 0, len(plan))
	for n := range plan {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].String() < nodes[j].String() })
	for _, n := range nodes {
		w := outWork{kind: plan[n].kind, name: name, node: n, message: o.Outbound.Message, recipients: plan[n].recipients}
		if w.kind == workSend {
			data, err := e.encodeFor(now, o, n)
			if err != nil {
				if reason, ok := ReasonOf(err); ok && reason == ReasonClockRollback {
					e.log.Debug("the wall clock cannot be trusted, so the evidence of a reply is not signed; the message is tried again", "message", string(o.Outbound.Message))
				} else {
					e.log.Error("a message cannot be encoded to be sent; it is tried again", "message", string(o.Outbound.Message), "error", err.Error())
				}
				e.sched.set(schedKey{schedOutbound, name}, now.Add(e.p.RetryBackoff))
				continue
			}
			w.data, w.sender = data, o.Key.Sender
		}
		st.inflight++
		e.give(w)
	}
}

// outboundWorks says whether the outbound worker carries a source record: it does
// not for the replacement of a lost record, which holds nothing to send, nor while
// the engine has no transport or keys to send with. A control signal is carried like
// a message. A delivery of a record it does not carry is never pulled, so nothing
// waits for a pull before it closes.
func (e *Engine) outboundWorks(o store.OutRecord) bool {
	return o.Lost == nil && e.tr != nil && e.d.Keys != nil
}

// finalPullUntil is the last instant at which a target is still pulled for a delivery
// with this report_by: the retention the target's store gives the records of a
// delivery, whose longest term is the one of the facts reply permission reads
// (reply.RetainFactsUntil). Asking later would find nothing, and the target's "no
// record" would say nothing about the delivery.
func finalPullUntil(reportBy time.Time) time.Time { return reply.RetainFactsUntil(reportBy) }

// finalPullDue says whether a delivery is waiting for its pull before the close at
// now: it is queued or accepted, so its sender was told no outcome and the target may
// hold one; report_by has passed, so the ordinary pulls are over; the target still
// keeps its records; and the wall clock is not in doubt, because the pull is the step
// before a judgement by a deadline and none is made until the owner has cleared the
// pause (tickBatch holds the close too, and asks again once it is cleared). A delivery
// in any other state is closed without it, because the sender holds the notice that
// says what is known, or it is final.
func (e *Engine) finalPullDue(d delivery.Record, now time.Time) bool {
	return !e.paused && (d.State == delivery.StateQueued || d.State == delivery.StateAccepted) &&
		now.After(d.ReportBy) && !now.After(finalPullUntil(d.ReportBy))
}

// finalPullPending says whether the close of a delivery of the source record must wait
// for the pull the engine makes before it: the worker carries the record, the pull is
// due and it has not been made in this run.
func (e *Engine) finalPullPending(o store.OutRecord, recipient ident.EndpointID, d delivery.Record, now time.Time) bool {
	if _, done := e.outbox.finalPulled[finalPullKey{o.Name(), recipient}]; done {
		return false
	}
	return e.outboundWorks(o) && e.finalPullDue(d, now)
}

// nodeNeed is the next piece of work a source record has for one node: a send, or
// a pull that asks about the recipients on it whose deliveries are not settled.
type nodeNeed struct {
	kind       workKind
	recipients []ident.EndpointID
}

// outboundPlan says what a source record needs next, by node: a send to a node
// when one of its deliveries needs one (the send carries every recipient on the
// node), otherwise a pull naming each recipient on the node that still needs one.
// A node with nothing to do is not in the plan. A derived delivery, which only the
// record of a control signal has, is planned like the others.
func (e *Engine) outboundPlan(o envelope.Outbound, now time.Time, sent map[ident.NodeID]bool, finalPulled map[ident.EndpointID]bool) map[ident.NodeID]nodeNeed {
	plan := map[ident.NodeID]nodeNeed{}
	for _, t := range o.Deliveries() {
		kind := e.outboundNeed(t.Delivery, o.AcceptBefore, now, sent[t.Address.Node], finalPulled[t.Address.Endpoint])
		if kind == 0 {
			continue
		}
		n := plan[t.Address.Node]
		if n.kind == 0 || kind == workSend || (kind == workPull && n.kind == workFinalPull) {
			n.kind = kind
		}
		n.recipients = append(n.recipients, t.Address.Endpoint)
		plan[t.Address.Node] = n
	}
	// Recipients are the pull's: a send carries every recipient of the envelope.
	for node, n := range plan {
		if n.kind == workSend {
			n.recipients = nil
			plan[node] = n
		}
	}
	return plan
}

// outboundNeed says what a delivery of a source record needs next: a send while
// its node has not answered and the delivery deadline has not passed, a pull
// while it is not settled and the report deadline has not passed, the one pull
// before the close once it has passed (finalPullDue) unless it was made already
// (finalPulled), nothing otherwise. The report deadline already holds the tolerance
// of the clock. After the delivery deadline a target that was never reached is
// asked, not sent to: whatever it would do with the message, it would refuse it as
// late.
func (e *Engine) outboundNeed(d delivery.Record, acceptBefore, now time.Time, sent, finalPulled bool) workKind {
	switch {
	case d.State.IsTerminal():
		return 0
	case now.After(d.ReportBy):
		if finalPulled || !e.finalPullDue(d, now) {
			return 0
		}
		return workFinalPull
	case d.State == delivery.StateQueued && !sent && !now.After(acceptBefore):
		return workSend
	}
	return workPull
}

// finalPulledOf are the recipients of the source record whose pull before the close
// is made.
func (e *Engine) finalPulledOf(name string) map[ident.EndpointID]bool {
	done := map[ident.EndpointID]bool{}
	for k := range e.outbox.finalPulled {
		if k.name == name {
			done[k.recipient] = true
		}
	}
	return done
}

// forgetOutbound takes the loop's memory of a source record's outbound work out of
// the outbox: its state and the deliveries whose pull before the close was made. It is
// for a record that is gone, or swept; an entry of finalPulled goes by itself when its
// delivery closes in a tick, but one whose delivery became final by a report stays
// until the record is forgotten.
func (e *Engine) forgetOutbound(name string) {
	delete(e.outbox.state, name)
	maps.DeleteFunc(e.outbox.finalPulled, func(k finalPullKey, _ struct{}) bool { return k.name == name })
}

type cmdOutResult struct {
	call[struct{}]
	work outWork
	// reports are the signed reports a receipt or an answer to a pull carried.
	reports []record.Record
	// noRecord are the recipients a target answered to a pull that it holds no
	// record of; a receipt has none.
	noRecord []ident.EndpointID
	// controlReply is the signed control reply a receipt or an answer to a pull
	// carried, nil when it carried none.
	controlReply *record.Record
	err          error
}

func (c *cmdOutResult) gate() gate { return gateLoopback }
func (c *cmdOutResult) run(e *Engine, now time.Time) {
	e.outResult(now, c.work, c.reports, c.noRecord, c.controlReply, c.err)
	c.done(struct{}{}, nil)
}

// outResult takes in what a call of the worker brought. A call that got no answer
// is made again after ResendBackoff (the envelope is made anew, with evidence
// signed at that moment, but the request digest is the same, so the target
// answers a send it already processed from its record). A call
// that did is applied: the reports are verified and applied to the source record,
// and so are the recipients the target says it holds no record of, which is
// written with the body released when no delivery needs it any more. A successful
// answer to a pull that has no report for a delivery with no receipt is a query that
// found none, which is what the expiry of a delivery is judged by; for any other
// delivery it says nothing yet and the next pull asks again. A call that got no
// answer is also what the unreachable hint is judged by.
func (e *Engine) outResult(now time.Time, w outWork, reports []record.Record, noRecord []ident.EndpointID, controlReply *record.Record, callErr error) {
	st := e.outbox.state[w.name]
	if st == nil { // the record was settled or taken away while the call was out
		return
	}
	st.inflight--
	switch {
	case callErr != nil && errors.Is(callErr, context.Canceled):
		return // the engine is stopping; a start picks the record up again
	case callErr != nil:
		if reason, ok := ReasonOf(callErr); ok && reason == ReasonMessageConflict {
			e.log.Error("the target holds another request under the identifier of a message; it is not sent again", "message", string(w.message))
			delete(e.outbox.state, w.name)
			return
		}
		if w.kind == workFinalPull {
			// The last look is not tried again: the delivery closes, and its sender is
			// told that the outcome could not be learned.
			e.log.Debug("no answer from the target to the pull before the close", "message", string(w.message), "error", callErr.Error())
			e.finalPullMade(now, w, st)
			return
		}
		e.log.Debug("no answer from the target; it is tried again", "message", string(w.message), "error", callErr.Error())
		if _, failing := st.failingSince[w.node]; !failing {
			st.failingSince[w.node] = now
		}
		if err := e.noteContactFailing(now, w.name, w.node, st.failingSince[w.node]); err != nil {
			e.classify(err) // a fatal error stops the loop after this command
			if e.fatal == nil {
				e.log.Warn("the failure to reach a node could not be recorded; it is recorded with the next call", "message", string(w.message), "error", err.Error())
			}
		}
		if st.inflight == 0 {
			e.sched.set(schedKey{schedOutbound, w.name}, now.Add(e.p.ResendBackoff))
		}
		return
	}
	delete(st.failingSince, w.node)
	if w.kind == workSend {
		st.sent[w.node] = true
	}
	var pulled []ident.EndpointID
	if w.kind == workPull || w.kind == workFinalPull {
		pulled = w.recipients
	}
	if err := e.applyAnswer(now, w.name, w.node, reports, noRecord, controlReply, pulled); err != nil {
		e.classify(err) // a fatal error stops the loop after this command
		if e.fatal == nil {
			e.log.Warn("reports could not be written; they are asked for again", "message", string(w.message), "error", err.Error())
			e.sched.set(schedKey{schedOutbound, w.name}, now.Add(e.p.RetryBackoff))
		}
		return
	}
	if w.kind == workFinalPull {
		e.finalPullMade(now, w, st)
		return
	}
	if st.inflight == 0 {
		e.afterOutbound(w.name, now)
	}
}

// finalPullMade takes the pull before the close as made, whatever came of it, and
// closes the deliveries of the record whose close was waiting for it. The ones whose
// pull is still with the worker keep waiting.
func (e *Engine) finalPullMade(now time.Time, w outWork, st *outState) {
	for _, r := range w.recipients {
		e.outbox.finalPulled[finalPullKey{w.name, r}] = struct{}{}
	}
	_ = e.tickRecords(now, []string{outTick(w.name)}) // a fatal error is e.fatal, which the loop reads
	if st.inflight == 0 {
		e.afterOutbound(w.name, now)
	}
}

// afterOutbound schedules the next look at a record whose work is all back: a
// send again after ResendBackoff while a node has not answered, else a pull one
// interval from now while a delivery is not settled, nothing when none is. A
// delivery that is waiting for the pull before its close is looked at at once: that
// pull is the last, and the close waits for it.
func (e *Engine) afterOutbound(name string, now time.Time) {
	o, ok := e.view.out[name]
	st := e.outbox.state[name]
	if !ok {
		e.forgetOutbound(name)
		return
	}
	if o.Lost != nil || st == nil {
		delete(e.outbox.state, name)
		return
	}
	finalPulled := e.finalPulledOf(name)
	var next time.Duration
	for _, t := range o.Outbound.Deliveries() {
		switch e.outboundNeed(t.Delivery, o.Outbound.AcceptBefore, now, st.sent[t.Address.Node], finalPulled[t.Address.Endpoint]) {
		case workFinalPull:
			e.dispatchOutbound(name, now)
			return
		case workSend:
			next = e.p.ResendBackoff
		case workPull:
			if next == 0 {
				next = e.p.PullInterval
			}
		}
		if next == e.p.ResendBackoff {
			break
		}
	}
	if next == 0 {
		delete(e.outbox.state, name)
		return
	}
	e.sched.set(schedKey{schedOutbound, name}, now.Add(next))
}

// sourceChange is the change one answer from a node, or one failure to get an
// answer, makes to a source record: each fact is applied to the delivery it is
// about by the rules of the envelope and delivery packages, and the record is written
// once, with the feedback the facts call for, when any of them changed it.
type sourceChange struct {
	e       *Engine
	now     time.Time
	node    ident.NodeID
	old, o  store.OutRecord
	changed bool
	// replied is set when the change took in a control reply, which the record is
	// written with by the transaction of that.
	replied bool
}

// changeSource starts a change of the source record of the name, from what the view
// holds of it; ok is false when the record is not there or holds nothing tracked (the
// replacement of a lost one).
func (e *Engine) changeSource(now time.Time, name string, node ident.NodeID) (*sourceChange, bool) {
	o, ok := e.view.out[name]
	if !ok || o.Lost != nil {
		return nil, false
	}
	return &sourceChange{e: e, now: now, node: node, old: o, o: o}, true
}

// take puts the delivery the rules changed in the record, with the feedback the
// outcomes call for.
func (c *sourceChange) take(next envelope.Outbound, recipient ident.EndpointID, outcomes []delivery.Outcome) {
	c.o.Outbound = next
	c.o = c.e.withFeedback(c.now, c.o, ident.Address{Node: c.node, Endpoint: recipient}, outcomes)
	c.changed = true
}

// applyControlReply takes in the control reply of the signal, which the one rule
// of the envelope package applies: the delivery of the signal ends, and the
// initial message of a creation gets its delivery to the session endpoint the reply
// names. A reply that does not verify, one that is not the signal's or that
// contradicts what the record accepted before is the peer's fault, logged and left
// out; the same reply again changes nothing.
func (c *sourceChange) applyControlReply(rec record.Record) {
	e := c.e
	sr, err := e.verifyControlReply(c.now, rec, c.node)
	if err != nil {
		e.log.Warn("a control reply of the target did not verify", "node", c.node.String(), "error", err.Error())
		return
	}
	if c.o.Outbound.ControlReplyDigest == sr.Digest() {
		return
	}
	h, err := envelope.HandleControlReply(c.now, c.o.Outbound, sr, nil)
	if err != nil {
		e.log.Warn("a control reply of the target is refused", "node", c.node.String(), "error", err.Error())
		return
	}
	encoded, err := rec.Encode()
	if err != nil {
		e.log.Error("a control reply that verified cannot be encoded", "error", err.Error())
		return
	}
	c.o.Outbound, c.o.ControlReply = h.Outbound, encoded
	target := c.o.Outbound.Recipients[0].Address
	c.o = e.withFeedback(c.now, c.o, target, []delivery.Outcome{h.Control})
	c.changed, c.replied = true, true
}

// observe applies a fact the sender sees itself to the delivery to recipient, and
// takes it when the delivery rules did not ignore it.
//
// None is applied while the wall clock is in doubt: each of them is a judgement by a
// deadline, and a clock set back and then moved on is not one to judge by until the
// owner has cleared it.
func (c *sourceChange) observe(recipient ident.EndpointID, ev delivery.Event) {
	if c.e.paused {
		return
	}
	next, effect, outcome, err := c.o.Outbound.ApplyObservation(c.now, c.node, recipient, ev)
	if err != nil {
		c.e.log.Warn("an observation of a delivery is refused", "node", c.node.String(), "error", err.Error())
		return
	}
	if effect == envelope.ReportApplied && outcome.Result == delivery.ResultApplied {
		c.take(next, recipient, []delivery.Outcome{outcome})
	}
}

// commit writes the record and takes it into the view; it does nothing when
// nothing changed. The store removes the body of a record that is written without
// one.
func (c *sourceChange) commit() error {
	if !c.changed {
		return nil
	}
	o := settledSource(c.o)
	write := c.e.d.Store.SourceUpdate
	if c.replied {
		write = c.e.d.Store.ControlReply
	}
	if err := write(o); err != nil {
		return err
	}
	c.e.putOut(c.now, o)
	return nil
}

// applyAnswer applies what a node answered about a message this node sent: the
// control reply, when the message is a control signal and the answer has it, and
// each report is verified and applied to the source record, and each recipient the node
// lists as having no record is applied by the rule of the delivery. It writes the
// record when one of them changed it. Whatever is not applied, a report that does
// not verify, one that no state of the delivery can follow, an answer about a node
// that is not the delivery's, is the peer's fault and is logged and left out: what
// the others say is still taken. A target says at most one thing about a recipient,
// its report or its absence (transport.PullAnswer); a recipient it both reports and
// lists is such a fault too, and the listing is the half that is left out, because
// the report is signed and the listing is not.
//
// pulled are the recipients the answer is to a pull for (nil for the answer to a
// send). A recipient of them that the answer has no report for, listed as having no
// record or in neither list, is a query that found no receipt: it is what the
// expiry of a delivery that has none is judged by (delivery.EventExpiryQueried),
// and the delivery rules ignore it for every delivery that has one.
func (e *Engine) applyAnswer(now time.Time, name string, node ident.NodeID, recs []record.Record, noRecord []ident.EndpointID, controlReply *record.Record, pulled []ident.EndpointID) error {
	c, ok := e.changeSource(now, name, node)
	if !ok {
		return nil
	}
	if controlReply != nil {
		// The reply comes first: it ends the delivery of the signal and binds what the
		// signal made, which a report that comes with it may be about.
		c.applyControlReply(*controlReply)
	}
	reported := map[ident.EndpointID]bool{}
	for _, rec := range recs {
		sr, err := e.verifyReport(now, rec, node)
		if err != nil {
			e.log.Warn("a report of the target did not verify", "node", node.String(), "error", err.Error())
			continue
		}
		reported[sr.Report().Recipient] = true
		next, effect, outcomes, err := c.o.Outbound.ApplyReport(now, sr)
		if err != nil {
			e.log.Warn("a report of the target contradicts the delivery", "node", node.String(), "error", err.Error())
			continue
		}
		// A report the delivery ignores can still teach the record when the message
		// arrived, from which the reply window is counted, so every applied report
		// is written.
		if effect == envelope.ReportApplied {
			c.take(next, sr.Report().Recipient, outcomes)
		}
	}
	for _, recipient := range noRecord {
		if reported[recipient] {
			e.log.Warn("the target answered both a report and no record for a recipient; the no record is left out", "node", node.String(), "message", string(c.o.Outbound.Message))
			continue
		}
		next, effect, outcome, err := c.o.Outbound.ApplyNoRecord(now, node, recipient)
		if err != nil {
			e.log.Warn("the target says it holds no record of a delivery that is not on it", "node", node.String(), "error", err.Error())
			continue
		}
		// An answer the delivery ignores (no receipt yet, already settled, past
		// report_by) leaves the record as it is and is not written.
		if effect == envelope.ReportApplied && outcome.Result == delivery.ResultApplied {
			c.take(next, recipient, []delivery.Outcome{outcome})
		}
	}
	for _, recipient := range pulled {
		if !reported[recipient] {
			c.observe(recipient, delivery.Event{Type: delivery.EventExpiryQueried, QueriedAt: now})
		}
	}
	return c.commit()
}

// noteContactFailing tells the deliveries of a record on a node that contact with
// it has been failing since the given time. The delivery rules judge whether that
// is news (delivery.EventContactFailing): for a delivery that has no receipt, once it
// has failed long enough while the sender still retries. A call that failed for any
// reason but the engine's own stopping counts: the target did not answer in a way
// that carries the message on.
func (e *Engine) noteContactFailing(now time.Time, name string, node ident.NodeID, since time.Time) error {
	c, ok := e.changeSource(now, name, node)
	if !ok {
		return nil
	}
	for _, t := range c.o.Outbound.Deliveries() {
		if t.Address.Node == node {
			c.observe(t.Address.Endpoint, delivery.Event{Type: delivery.EventContactFailing, Cause: delivery.CauseNetwork, Since: since})
		}
	}
	return c.commit()
}

// encodeFor is the envelope of a source record as it is sent to one node. It is
// made anew from the record and the body each time a send is made, so that the
// bytes of a send again differ from the first only in the evidence of a reply,
// which is signed at the moment it is sent. The evidence is no part of the request
// digest the target compares a repeat with, so a send again is the same request.
// It refuses while the node may not sign, because the evidence is a signature.
func (e *Engine) encodeFor(now time.Time, o store.OutRecord, node ident.NodeID) ([]byte, error) {
	var body []byte
	if o.Body != nil {
		var err error
		if body, err = e.readBody(now, o, *o.Body); err != nil {
			return nil, err
		}
	}
	env := envelope.Envelope{
		ID: o.Outbound.Message, To: slices.Clone(o.Key.Recipients), MultiRecipient: len(o.Key.Recipients) > 1,
		DiscloseRecipients: o.Envelope.DiscloseRecipients, Kind: o.Outbound.Kind, ReplyTo: o.Envelope.ReplyTo, Thread: o.Envelope.Thread,
		Seq: o.Envelope.Seq, SentAt: o.Envelope.SentAt, AcceptBefore: o.Outbound.AcceptBefore, ReportBy: o.Envelope.ReportBy,
		ContentType: o.Envelope.ContentType, Body: body, Preset: o.Envelope.Preset,
	}
	if env.ReplyTo != "" {
		if rec, ok, err := e.evidenceFor(now, o.Key.Sender, env.ReplyTo); err != nil {
			return nil, err
		} else if ok {
			env.Evidence = []record.Record{rec}
		}
	}
	return env.TrimmedFor(node).Encode(envelope.DefaultLimits())
}

// evidenceFor is this node's signed report of its delivery of a message to the
// endpoint, which a reply carries to prove that the message arrived. It is not
// there when the node holds no record of the message, or more than one.
func (e *Engine) evidenceFor(now time.Time, recipient ident.EndpointID, message delivery.MessageID) (record.Record, bool, error) {
	keys := e.view.received[msgKey{recipient, message}]
	if len(keys) != 1 {
		return record.Record{}, false, nil
	}
	rec, err := e.signReport(now, e.view.in[keys[0]])
	if err != nil {
		return record.Record{}, false, err
	}
	return rec, true, nil
}

// recoverOutbound goes on with the outgoing messages after a start: every source
// record that is not settled is looked at again, in the order of the records.
func (e *Engine) recoverOutbound(now time.Time) error {
	names := make([]string, 0, len(e.view.out))
	for name := range e.view.out {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		e.dispatchOutbound(name, now)
	}
	return nil
}
