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
	dropped := map[string]int{}
	var refs []store.BodyRef
	drop := func(r *store.BodyRef) {
		if r != nil {
			if dropped[r.Name()] == 0 {
				refs = append(refs, *r)
			}
			dropped[r.Name()]++
		}
	}
	for _, in := range ins {
		rec, o, err := in.Record.Apply(now, event)
		if err != nil || o.Result != delivery.ResultApplied {
			unchanged = append(unchanged, in.Key)
			continue
		}
		in.Record = rec
		drop(in.Body)
		in.Body = nil
		b.In = append(b.In, in)
	}
	for _, c := range ctls {
		rec, o, err := c.Record.Apply(now, event)
		if err != nil || o.Result != delivery.ResultApplied {
			unchanged = append(unchanged, c.Key)
			continue
		}
		c.Record = rec
		drop(c.Body)
		c.Body = nil
		b.Ctl = append(b.Ctl, c)
	}
	for _, r := range refs {
		if counts[r.Name()]-dropped[r.Name()] <= 0 {
			b.Release = append(b.Release, r)
		}
	}
	return b, unchanged
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
// is queued behind whatever the inbox already holds, so that a long queue never
// holds the loop for long. A crash between two chunks leaves a closed endpoint
// with records still queued, which the scan entry closes the same way.
type cmdDrain struct{ endpoint ident.EndpointID }

func (c *cmdDrain) gate() gate { return gateAlways }
func (c *cmdDrain) fail(error) {}
func (c *cmdDrain) run(e *Engine, now time.Time) {
	err := e.drainChunk(now, c.endpoint)
	if err = e.classify(err); err != nil && e.fatal == nil {
		e.log.Warn("closing the records of a closed endpoint failed; it is tried again", "endpoint", c.endpoint.String(), "error", err.Error())
		e.sched.set(schedKey{schedDrain, c.endpoint.String()}, now.Add(e.p.RetryBackoff))
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
		if err := e.d.Store.EndpointDrain(b); err != nil {
			return err
		}
		for _, in := range b.In {
			e.view.setIn(afterWrite(in))
		}
		for _, c := range b.Ctl {
			e.view.setCtl(c)
		}
	}
	if e.view.Draining(id) {
		e.cont = append(e.cont, &cmdDrain{endpoint: id})
	}
	return nil
}
