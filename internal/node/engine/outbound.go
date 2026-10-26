package engine

import (
	"context"
	"errors"
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
}

func newOutbox() *outbox { return &outbox{state: map[string]*outState{}} }

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
	b := e.outbox
	b.mu.Lock()
	active := b.active
	b.mu.Unlock()
	return active && e.tr != nil && e.tr.Waiting() > 0
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
			res.reports = rc.Reports
		case workPull:
			var ans transport.PullAnswer
			ans, res.err = e.tr.Pull(ctx, w.node, transport.PullRequest{Message: w.message, Recipients: w.recipients})
			res.reports = ans.Reports
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
	// A control signal is sent by the creation of a session, which is not this
	// worker's work; a record that was lost holds nothing to send.
	if !ok || o.Lost != nil || o.Key.Control || e.tr == nil || e.d.Keys == nil {
		delete(e.outbox.state, name)
		return
	}
	st := e.outbox.state[name]
	if st == nil {
		st = &outState{sent: map[ident.NodeID]bool{}}
		e.outbox.state[name] = st
	}
	if st.inflight > 0 {
		return
	}
	need := map[ident.NodeID]workKind{}
	pulled := map[ident.NodeID][]ident.EndpointID{}
	for _, t := range o.Outbound.Deliveries() {
		kind := outboundNeed(t.Delivery, o.Outbound.AcceptBefore, now, st.sent[t.Address.Node])
		if kind != 0 && (need[t.Address.Node] == 0 || kind == workSend) {
			need[t.Address.Node] = kind
		}
		if kind == workPull {
			pulled[t.Address.Node] = append(pulled[t.Address.Node], t.Address.Endpoint)
		}
	}
	if len(need) == 0 {
		delete(e.outbox.state, name)
		return
	}
	nodes := make([]ident.NodeID, 0, len(need))
	for n := range need {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].String() < nodes[j].String() })
	for _, n := range nodes {
		w := outWork{kind: need[n], name: name, node: n, message: o.Outbound.Message, recipients: pulled[n]}
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

// outboundNeed says what a delivery of a source record needs next: a send while
// its node has not answered and the delivery deadline has not passed, a pull
// while it is not settled and the report deadline has not passed, nothing
// otherwise. The report deadline already holds the tolerance of the clock. After
// the delivery deadline a target that was never reached is asked, not sent to:
// whatever it would do with the message, it would refuse it as late.
func outboundNeed(d delivery.Record, acceptBefore, now time.Time, sent bool) workKind {
	switch {
	case d.State.IsTerminal() || now.After(d.ReportBy):
		return 0
	case d.State == delivery.StateQueued && !sent && !now.After(acceptBefore):
		return workSend
	}
	return workPull
}

type cmdOutResult struct {
	call[struct{}]
	work outWork
	// reports are the signed reports a receipt or an answer to a pull carried.
	reports []record.Record
	err     error
}

func (c *cmdOutResult) gate() gate { return gateLoopback }
func (c *cmdOutResult) run(e *Engine, now time.Time) {
	e.outResult(now, c.work, c.reports, c.err)
	c.done(struct{}{}, nil)
}

// outResult takes in what a call of the worker brought. A call that got no answer
// is made again after ResendBackoff (the envelope is made anew, with evidence
// signed at that moment, but the request digest is the same, so the target
// answers a send it already processed from its record). A call
// that did is applied: the reports are verified and applied to the source record,
// which is written with the body released when no delivery needs it any more.
func (e *Engine) outResult(now time.Time, w outWork, reports []record.Record, callErr error) {
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
		e.log.Debug("no answer from the target; it is tried again", "message", string(w.message), "error", callErr.Error())
		if st.inflight == 0 {
			e.sched.set(schedKey{schedOutbound, w.name}, now.Add(e.p.ResendBackoff))
		}
		return
	}
	if w.kind == workSend {
		st.sent[w.node] = true
	}
	if err := e.applyReports(now, w.name, w.node, reports); err != nil {
		e.classify(err) // a fatal error stops the loop after this command
		if e.fatal == nil {
			e.log.Warn("reports could not be written; they are asked for again", "message", string(w.message), "error", err.Error())
			e.sched.set(schedKey{schedOutbound, w.name}, now.Add(e.p.RetryBackoff))
		}
		return
	}
	if st.inflight == 0 {
		e.afterOutbound(w.name, now)
	}
}

// afterOutbound schedules the next look at a record whose work is all back: a
// send again after ResendBackoff while a node has not answered, else a pull one
// interval from now while a delivery is not settled, nothing when none is.
func (e *Engine) afterOutbound(name string, now time.Time) {
	o, ok := e.view.out[name]
	st := e.outbox.state[name]
	if !ok || o.Lost != nil || st == nil {
		delete(e.outbox.state, name)
		return
	}
	var next time.Duration
	for _, t := range o.Outbound.Deliveries() {
		switch outboundNeed(t.Delivery, o.Outbound.AcceptBefore, now, st.sent[t.Address.Node]) {
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

// applyReports verifies the reports of a node about a message this node sent and
// applies each to the source record, and writes the record when one changed it. A
// report that does not verify, or that no state of the delivery can follow, is the
// peer's fault and is logged and left out: what the others say is still taken.
func (e *Engine) applyReports(now time.Time, name string, node ident.NodeID, recs []record.Record) error {
	old := e.view.out[name]
	o := old
	changed := false
	for _, rec := range recs {
		sr, err := e.verifyReport(now, rec, node)
		if err != nil {
			e.log.Warn("a report of the target did not verify", "node", node.String(), "error", err.Error())
			continue
		}
		next, effect, outcomes, err := o.Outbound.ApplyReport(now, sr)
		if err != nil {
			e.log.Warn("a report of the target contradicts the delivery", "node", node.String(), "error", err.Error())
			continue
		}
		if effect != envelope.ReportApplied {
			continue
		}
		o.Outbound = next
		o = e.withFeedback(now, o, ident.Address{Node: node, Endpoint: sr.Report().Recipient}, outcomes)
		changed = true
	}
	if !changed {
		return nil
	}
	o = settledSource(o)
	release := e.view.bodyRefs.unreferencedAfter([]*store.BodyRef{old.Body}, []*store.BodyRef{o.Body})
	if err := e.d.Store.SourceUpdate(o, release); err != nil {
		return err
	}
	e.view.setOut(o)
	return nil
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
		if body, err = e.d.Store.ReadBody(*o.Body); err != nil {
			return nil, err
		}
	}
	env := envelope.Envelope{
		ID: o.Outbound.Message, To: slices.Clone(o.Key.Recipients), MultiRecipient: len(o.Key.Recipients) > 1,
		DiscloseRecipients: o.Envelope.DiscloseRecipients, Kind: envelope.KindMessage, ReplyTo: o.Envelope.ReplyTo, Thread: o.Envelope.Thread,
		Seq: o.Envelope.Seq, SentAt: o.Envelope.SentAt, AcceptBefore: o.Outbound.AcceptBefore, ReportBy: o.Envelope.ReportBy,
		ContentType: o.Envelope.ContentType, Body: body,
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
