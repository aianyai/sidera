package engine

import (
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/node/store"
)

// The passing of time changes a record that is not final: a message nobody reads is
// rejected when the endpoint's queue time runs out, and a delivery that is still not
// final when report_by comes is closed. The delivery rules say when (Record.NextTick)
// and what (Record.Tick); this file runs them.
//
// Each target record and each source record has an entry in the schedule at its next
// time-driven change, which the view keeps (view.onTick), so that no write has to
// remember to move it. When entries fall due their records are ticked together, a
// chunk of at most Params.ChunkTickRecords to a transaction; at the start every record
// that is due is ticked in the step that does so, before the feedback goes out.
// A control record is ticked like a target record: an accepted one that has no reply
// at the settle deadline becomes delivery_unknown (the signal was handed to the
// adapter and its reply was never committed), and one that is not final after
// report_by closes. A creation is settled by the engine long before either (see
// create.go), so what these rules close is the signal whose reply a crash lost.
//
// A source delivery that is still queued or accepted when report_by passes is not
// closed at once: the outbound worker pulls its target one more time while the target
// still keeps its records (Engine.finalPullDue), and the close follows that pull, whatever
// came of it. At the start the tick comes before the outbound step, so it leaves such
// a delivery alone and the outbound step makes the pull; while the node runs the tick
// asks for it.

// watchTicks makes the schedule follow the view from now on, and gives every record
// the view holds the entry it is owed.
func (e *Engine) watchTicks() {
	e.view.onTick = func(record string, next time.Time) {
		key := schedKey{schedTick, record}
		if next.IsZero() {
			e.sched.remove(key)
			return
		}
		e.sched.set(key, next)
	}
	for k, in := range e.view.in {
		e.view.ticks(inTick(k), in.Record.NextTick())
	}
	for k, c := range e.view.ctl {
		e.view.ticks(ctlTick(k), c.Record.NextTick())
	}
	for name, o := range e.view.out {
		e.view.ticks(outTick(name), outNextTick(o))
	}
}

// cmdTick ticks the next chunk of the records whose entries fell due.
type cmdTick struct{}

func (cmdTick) gate() gate { return gateAlways }
func (cmdTick) fail(error) {}
func (cmdTick) run(e *Engine, now time.Time) {
	n := min(len(e.dueTicks), e.p.ChunkTickRecords)
	chunk := e.dueTicks[:n:n]
	e.dueTicks = e.dueTicks[n:]
	_ = e.tickRecords(now, chunk) // a fatal error is e.fatal, which the loop reads
	if len(e.dueTicks) > 0 {
		e.later(cmdTick{})
		return
	}
	e.tickQueued = false
}

// tickDue is the due entry of a record: it is put with the others that fell due and
// the chunks are queued behind the commands the inbox holds.
func (e *Engine) tickDue(record string) {
	e.dueTicks = append(e.dueTicks, record)
	if !e.tickQueued {
		e.tickQueued = true
		e.later(cmdTick{})
	}
}

// recoverTick ticks every record whose entry is due, in chunks, in the step that does
// so at the start: they are the entries the view gave the records the scan entry
// loaded. The entries of the records that changed were moved by the view, and an entry
// of one that was due and did not change is gone with this step: nothing about the
// record would be different the next time.
func (e *Engine) recoverTick(now time.Time) error {
	keys := e.sched.takeDueOf(schedTick, now)
	due := make([]string, len(keys))
	for i, k := range keys {
		due[i] = k.id
	}
	for len(due) > 0 {
		n := min(len(due), e.p.ChunkTickRecords)
		if err := e.tickRecords(now, due[:n]); err != nil {
			return err
		}
		due = due[n:]
	}
	return nil
}

// tickRecords ticks the records, one transaction for all of them. A write that failed
// for a reason a retry may cure leaves them as they were and puts their entries back
// after RetryBackoff; only a fatal storage error is returned.
func (e *Engine) tickRecords(now time.Time, records []string) error {
	err := e.tickBatch(now, records)
	if err == nil {
		return nil
	}
	e.classify(err)
	if e.fatal != nil {
		return err
	}
	e.log.Warn("records could not be ticked; they are tried again", "error", err.Error())
	for _, r := range records {
		e.sched.set(schedKey{schedTick, r}, now.Add(e.p.RetryBackoff))
	}
	return nil
}

// tickBatch moves the records the time has changed and writes them in one
// transaction, with the feedback the change calls for, and takes what was written
// into the view. A record the rules do not change is left out. While the wall clock
// is in doubt it moves none.
func (e *Engine) tickBatch(now time.Time, records []string) error {
	if e.paused {
		// The time is judged by a clock that cannot be trusted until the owner clears
		// the pause: what the node holds is left as it is, and looked at again.
		for _, r := range records {
			e.sched.set(schedKey{schedTick, r}, now.Add(e.p.RetryBackoff))
		}
		return nil
	}
	var b store.TickBatch
	var outs []store.OutRecord
	var waiting []finalPullWait
	for _, r := range records {
		switch {
		case strings.HasPrefix(r, "in/"):
			key, err := store.ParseTargetKey(strings.TrimPrefix(r, "in/"))
			if err != nil {
				e.log.Error("the schedule names a target record that is not one", "record", r)
				continue
			}
			in, ok := e.view.in[key]
			if !ok {
				continue
			}
			rec, outcomes, err := in.Record.Tick(now)
			if err != nil {
				e.log.Error("a target record cannot be ticked", "record", r, "error", err.Error())
				continue
			}
			if !applied(outcomes) {
				continue
			}
			in.Record = rec
			b.In = append(b.In, settledIn(in))
		case strings.HasPrefix(r, "ctl/"):
			key, err := store.ParseTargetKey(strings.TrimPrefix(r, "ctl/"))
			if err != nil {
				e.log.Error("the schedule names a control record that is not one", "record", r)
				continue
			}
			c, ok := e.view.ctl[key]
			if !ok {
				continue
			}
			rec, outcomes, err := c.Record.Tick(now)
			if err != nil {
				e.log.Error("a control record cannot be ticked", "record", r, "error", err.Error())
				continue
			}
			if !applied(outcomes) {
				continue
			}
			c.Record = rec
			if !rec.NeedsBody() {
				c.Body = nil
			}
			b.Ctl = append(b.Ctl, c)
		case strings.HasPrefix(r, "out/"):
			old, ok := e.view.out[strings.TrimPrefix(r, "out/")]
			if !ok {
				continue
			}
			o, changed, waitUntil := e.tickedSource(now, old)
			if !waitUntil.IsZero() {
				waiting = append(waiting, finalPullWait{r, waitUntil})
			}
			if !changed {
				continue
			}
			outs = append(outs, o)
			b.Out = append(b.Out, o)
		}
	}
	if len(b.In)+len(b.Out)+len(b.Ctl) > 0 {
		if err := e.d.Store.Tick(b); err != nil {
			return err
		}
		for _, in := range b.In {
			e.view.setIn(in)
		}
		for _, c := range b.Ctl {
			e.view.setCtl(c)
		}
		for _, o := range outs {
			e.putOut(now, o)
			for _, t := range o.Outbound.Deliveries() {
				if t.Delivery.State.IsTerminal() {
					delete(e.outbox.finalPulled, finalPullKey{o.Name(), t.Address.Endpoint})
				}
			}
		}
	}
	e.holdForFinalPulls(now, waiting)
	return nil
}

// finalPullWait is a source record, by its entry of the schedule, with a delivery
// whose close waits for the pull before it, and the instant that waiting ends at the
// latest.
type finalPullWait struct {
	record string
	until  time.Time
}

// holdForFinalPulls has the outbound worker look at the source records whose close
// waits for the pull before it, and sets each one's entry of the schedule to the
// instant the wait ends at the latest: a pull that never comes (the window was over
// before the worker took the record up, or a send answered with a conflict left the
// record with no work) must not keep the delivery open until the next start. It runs
// after the records were written, because writing one moves its entry. At the start
// the outbound step looks at every record, after the feedback step, so nothing is
// asked for before it.
func (e *Engine) holdForFinalPulls(now time.Time, waiting []finalPullWait) {
	for _, w := range waiting {
		e.sched.set(schedKey{schedTick, w.record}, w.until)
		if e.Phase() != PhaseRecovering {
			e.sched.set(schedKey{schedOutbound, strings.TrimPrefix(w.record, "out/")}, now)
		}
	}
}

// tickedSource is the source record after report_by passed for each of its
// deliveries that the time closes, with the feedback that calls for (the delivery
// rules say which: a close from a state the sender was told nothing of tells it), the
// body reference dropped once no delivery needs the body, whether anything changed,
// and, when a delivery was left open for the pull that comes before its close, the
// first instant at which the pull is no longer made and the delivery closes without it
// (the zero time when none was).
func (e *Engine) tickedSource(now time.Time, o store.OutRecord) (store.OutRecord, bool, time.Time) {
	ev := delivery.Event{Type: delivery.EventReportByPassed}
	changed := false
	if o.Lost != nil {
		lost := *o.Lost
		lost.Deliveries = append([]delivery.Record(nil), lost.Deliveries...)
		for i, d := range lost.Deliveries {
			next, out, err := d.Apply(now, ev)
			if err != nil {
				e.log.Error("a delivery of a lost source record cannot be ticked", "record", o.Name(), "error", err.Error())
				continue
			}
			if out.Result == delivery.ResultApplied {
				lost.Deliveries[i], changed = next, true
			}
		}
		o.Lost = &lost
		return o, changed, time.Time{}
	}
	var waitUntil time.Time
	for _, t := range o.Outbound.Deliveries() {
		if e.finalPullPending(o, t.Address.Endpoint, t.Delivery, now) {
			if until := finalPullUntil(t.Delivery.ReportBy).Add(time.Nanosecond); until.After(waitUntil) {
				waitUntil = until
			}
			continue
		}
		next, eff, out, err := o.Outbound.ApplyObservation(now, t.Address.Node, t.Address.Endpoint, ev)
		if err != nil {
			e.log.Error("a delivery of a source record cannot be ticked", "record", o.Name(), "error", err.Error())
			continue
		}
		if eff == envelope.ReportApplied && out.Result == delivery.ResultApplied {
			o.Outbound, changed = next, true
			o = e.withFeedback(now, o, t.Address, []delivery.Outcome{out})
		}
	}
	return settledSource(o), changed, waitUntil
}

// applied reports whether any of the outcomes changed the record.
func applied(outcomes []delivery.Outcome) bool {
	for _, o := range outcomes {
		if o.Result == delivery.ResultApplied {
			return true
		}
	}
	return false
}
