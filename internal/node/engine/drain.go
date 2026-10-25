package engine

import (
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
)

// closeBatch closes the given records because their endpoint is closed: every one
// that a forced close changes becomes rejected (or delivery_unknown when it had
// been handed out, which the delivery rules decide) and drops its reference to
// its body. The bodies that no record refers to afterwards are released. counts
// is how many records refer to each body now; it is not changed.
//
// A record the forced close does not change is not in the batch; its key is in
// unchanged. The delivery rules close every accepted record of a closed
// endpoint, so a record there is a defect that must not keep the drain going for
// ever.
func closeBatch(counts bodyCounts, now time.Time, ins []store.InRecord, ctls []store.CtlRecord) (b store.DrainBatch, unchanged []store.TargetKey) {
	event := delivery.Event{Type: delivery.EventRejected, Reject: delivery.RejectEndpointClosed}
	var dropped []*store.BodyRef
	for _, in := range ins {
		rec, o, err := in.Record.Apply(now, event)
		if err != nil || o.Result != delivery.ResultApplied {
			unchanged = append(unchanged, in.Key)
			continue
		}
		in.Record = rec
		in, ref := settledIn(in)
		dropped = append(dropped, ref)
		b.In = append(b.In, in)
	}
	for _, c := range ctls {
		rec, o, err := c.Record.Apply(now, event)
		if err != nil || o.Result != delivery.ResultApplied {
			unchanged = append(unchanged, c.Key)
			continue
		}
		c.Record = rec
		if !rec.NeedsBody() {
			dropped = append(dropped, c.Body)
			c.Body = nil
		}
		b.Ctl = append(b.Ctl, c)
	}
	b.Release = counts.unreferencedAfter(dropped, nil)
	return b, unchanged
}

// settledIn is the target record with its reference to the body dropped once the
// delivery rules say the record no longer needs the bytes (it is not accepted any
// more), and the reference that was dropped, nil when none was. It is the one rule
// for when a target record lets go of its body: whoever moves a record to a final
// state writes the record this returns and releases the body when nothing else
// refers to it.
func settledIn(in store.InRecord) (store.InRecord, *store.BodyRef) {
	if in.Record.NeedsBody() || in.Body == nil {
		return in, nil
	}
	ref := in.Body
	in.Body = nil
	return in, ref
}

// queuedFor lists the records queued for an endpoint, in a fixed order, at most
// limit of them (all when limit is not positive): the target records first.
func (v *view) queuedFor(id ident.EndpointID, limit int) (ins []store.InRecord, ctls []store.CtlRecord) {
	keys := make([]store.TargetKey, 0, len(v.queued[id]))
	for k := range v.queued[id] {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	for _, k := range keys {
		if limit > 0 && len(ins)+len(ctls) >= limit {
			break
		}
		if in, ok := v.in[k]; ok {
			ins = append(ins, in)
		} else if c, ok := v.ctl[k]; ok {
			ctls = append(ctls, c)
		}
	}
	return ins, ctls
}

// cmdDrain closes the next chunk of the records queued for a closed endpoint. The
// closing of an endpoint is its head record, written at once, and these
// continuations: each takes at most Params.ChunkReplaces records, and the next
// is queued behind whatever the inbox holds at that moment and ahead of what
// arrives later, so that a long queue never holds the loop for long and a busy
// inbox never starves it. A crash between two chunks leaves a closed endpoint
// with records still queued, which the scan entry closes the same way.
type cmdDrain struct{ endpoint ident.EndpointID }

func (c *cmdDrain) gate() gate { return gateAlways }
func (c *cmdDrain) fail(error) {}
func (c *cmdDrain) run(e *Engine, now time.Time) {
	err := e.drainChunk(now, c.endpoint)
	if err = e.classify(err); err != nil && e.fatal == nil {
		e.log.Warn("closing the records of a closed endpoint failed; it is tried again", "endpoint", c.endpoint.String(), "error", err.Error())
		e.sched.set(schedKey{schedDrain, c.endpoint.String()}, now.Add(e.p.RetryBackoff))
		return
	}
	if !e.view.Draining(c.endpoint) {
		e.releaseParked(c.endpoint)
	}
}

func (e *Engine) drainChunk(now time.Time, id ident.EndpointID) error {
	if !e.view.Draining(id) {
		return nil
	}
	ins, ctls := e.view.queuedFor(id, e.p.ChunkReplaces)
	b, unchanged := closeBatch(e.view.bodyRefs, now, ins, ctls)
	for _, k := range unchanged {
		e.log.Error("a record queued for a closed endpoint is not changed by closing it", "record", k.String())
		e.view.queue(k.Recipient, k, false)
	}
	if len(b.In)+len(b.Ctl) > 0 {
		written, err := e.d.Store.EndpointDrain(b)
		if err != nil {
			return err
		}
		for _, in := range written {
			e.view.setIn(in)
		}
		for _, c := range b.Ctl {
			e.view.setCtl(c)
		}
	}
	if e.view.Draining(id) {
		e.later(&cmdDrain{endpoint: id})
	}
	return nil
}
