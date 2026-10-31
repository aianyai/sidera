package engine

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// The execution of the two signals that act on a session, close and interrupt.
//
// A signal that the endpoint does not take is answered when it is taken in, without
// an adapter (takeInControl). One it takes is carried out by the adapter: the
// control record is accepted, the adapter is called by a worker the engine owns,
// and the result is a command of the loop (cmdExecuteResult) that settles the
// signal in one transaction (control-result): the reply is made and stored with the
// delivery's end and, for a close, the session endpoint is closed, its token gone,
// in the same transaction. The endpoint stays open until then, so what is queued for
// it is not closed under the signal: a close that reaches the endpoint first makes
// the signal's own record unknown, as it does for any accepted signal.
//
// Nothing is queued behind the messages of the session. The signal is not a record
// of the session's queue that recv hands out (queuedIn leaves control records out):
// it is carried out as soon as it is accepted, so an interrupt reaches a session
// whose queue is full and whose turn is running.
//
// What the adapter cannot be asked now (it is not connected, a call got no answer)
// is tried again until Params.ControlReplyDeadline, and then told: a close as
// unconfirmed, with the endpoint closed, because the channel is closed whatever the
// adapter does, and an interrupt as failed, which stopped nothing and may be asked
// again. An interrupt is told failed at once when the adapter is not connected: a
// signal to stop a turn that waits for an adapter to come back is a signal that
// stops it late. A result that could not be written is kept and written again, until
// it is, and the adapter is not asked a second time for it.

// execution is what the loop remembers of the carrying out of one signal.
type execution struct {
	// inflight says a call is with the adapter: its result schedules the next try,
	// and no second call is made beside it.
	inflight bool
	// offline says the last try found the adapter not connected, which is the reason
	// the deadline gives when it settles the signal.
	offline bool
	// requested says the adapter asked the runtime to stop the turn and the turn has
	// not ended: the signal is settled by the end of the turn, or as requested when
	// Params.InterruptConfirmWait is over.
	requested bool
	// result is what the signal ends with, made and not yet written: the write failed,
	// and the adapter has done what it was asked, so only the write is tried again.
	result *envelope.ControlResult
}

// executable says whether the record is a close or an interrupt that is not settled.
func executable(c store.CtlRecord) bool {
	return c.Record.State == delivery.StateAccepted && (c.Kind == envelope.KindSessionClose || c.Kind == envelope.KindSessionInterrupt)
}

// signalSupported says whether an endpoint takes the signal. What the kind of the
// endpoint decides it decides alone; an interrupt of a session also needs the
// adapter to have declared it, which is known once the instance has been admitted. An
// instance that has not been admitted since the engine started is asked, and found
// offline, rather than answered as not taking a signal it may take.
func (e *Engine) signalSupported(st *endpointState, kind envelope.Kind) bool {
	action, _ := kind.Action()
	if adm, ok := e.admission(st.rec.Instance); ok {
		return adm.AcceptsSignal(st.rec.Kind, action)
	}
	return st.rec.Kind == adapterreg.KindSession
}

// unsupportedResult is the reply of an endpoint that does not take the signal.
func unsupportedResult(kind envelope.Kind) envelope.ControlResult {
	if kind == envelope.KindSessionClose {
		return envelope.ControlResult{Signal: adapterproto.SignalSessionClose, Close: &adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyUnsupported}}
	}
	return envelope.ControlResult{Signal: adapterproto.SignalSessionInterrupt, Interrupt: &adapterproto.ControlInterruptReply{Outcome: adapterproto.InterruptReplyUnsupported}}
}

// takeInExecute takes in a close or an interrupt the endpoint takes: the control
// record is accepted, and the adapter is called at once.
func (e *Engine) takeInExecute(now time.Time, env envelope.Envelope, st *endpointState, c store.CtlRecord) error {
	if !e.signalSupported(st, env.Kind) {
		return e.answerControl(now, c, unsupportedResult(env.Kind))
	}
	if err := e.d.Store.ControlIntake(nil, nil, c, nil); err != nil {
		return err
	}
	e.view.setCtl(c)
	e.beginExecute(now, c)
	return nil
}

// beginExecute starts the carrying out of an accepted signal: its deadline is an
// entry of the schedule, and the adapter is called at once.
func (e *Engine) beginExecute(now time.Time, c store.CtlRecord) {
	e.sched.set(schedKey{schedExecuteDeadline, c.Key.String()}, c.AcceptedAt.Add(e.p.ControlReplyDeadline))
	e.startExecute(now, c.Key)
}

// executing is the control record of the key when its signal is a close or an
// interrupt that is not settled.
func (e *Engine) executing(key store.TargetKey) (store.CtlRecord, bool) {
	c, ok := e.view.ctl[key]
	return c, ok && executable(c)
}

// executionOf is what the loop remembers of the signal, made when first asked for.
func (e *Engine) executionOf(key store.TargetKey) *execution {
	x := e.executions[key]
	if x == nil {
		x = &execution{}
		e.executions[key] = x
	}
	return x
}

// startExecute calls the adapter for a signal that is not settled and has no call
// out. An adapter that is not connected is waited for, for a close (the next
// connection of the instance starts the signals it owes: redriveExecutions), and
// answers an interrupt at once.
func (e *Engine) startExecute(now time.Time, key store.TargetKey) {
	c, ok := e.executing(key)
	if !ok || !now.Before(c.AcceptedAt.Add(e.p.ControlReplyDeadline)) {
		return // its time is up: the deadline settles it, and the adapter is not asked again
	}
	x := e.executionOf(key)
	st := e.view.endpoints[c.Key.Recipient]
	if x.inflight || x.requested || x.result != nil || st == nil {
		return
	}
	instance := st.rec.Instance
	if a := e.adapters[instance]; e.d.Adapters == nil || a == nil || !a.online {
		x.offline = true
		if c.Kind == envelope.KindSessionInterrupt {
			e.settleFailedExecution(now, c, x)
		}
		return
	}
	ref := adapterproto.EndpointRef(c.Key.Recipient.String())
	var native adapterproto.NativeRef
	if st.rec.Session != nil {
		native = adapterproto.NativeRef(st.rec.Session.NativeRef)
	}
	x.inflight, x.offline = true, false
	if !e.spawn(func(ctx context.Context) {
		e.adapterCalls.Add(1)
		r := &cmdExecuteResult{call: newCall[struct{}](), key: key}
		if c.Kind == envelope.KindSessionClose {
			r.closed, r.err = e.d.Adapters.Close(ctx, instance, adapterproto.CloseParams{Endpoint: ref, NativeRef: native})
		} else {
			r.interrupted, r.err = e.d.Adapters.Interrupt(ctx, instance, adapterproto.InterruptParams{Endpoint: ref})
		}
		e.adapterCalls.Add(-1)
		_, _ = roundTrip(ctx, e, submitInternal, r, r.call)
	}) {
		x.inflight = false
	}
}

type cmdExecuteResult struct {
	call[struct{}]
	key         store.TargetKey
	closed      adapterproto.CloseResult
	interrupted adapterproto.InterruptResult
	err         error
}

func (c *cmdExecuteResult) gate() gate { return gateAlways }
func (c *cmdExecuteResult) run(e *Engine, now time.Time) {
	e.executeResult(now, c)
	c.done(struct{}{}, nil)
}

// executeResult takes in what the call of a signal brought. A result the reply can
// be made of settles the signal; a call that failed is tried again after
// Params.RetryBackoff, until the deadline, and one that did not reach an adapter
// waits for the next connection (for an interrupt it is the answer).
func (e *Engine) executeResult(now time.Time, r *cmdExecuteResult) {
	x := e.executions[r.key]
	if x != nil {
		x.inflight = false
	}
	c, ok := e.executing(r.key)
	if !ok {
		e.forgetExecution(r.key) // settled while the call was out: the owner closed the endpoint
		return
	}
	if x == nil {
		return
	}
	switch {
	case errors.Is(r.err, context.Canceled):
		return // the engine is stopping; the start finds the signal again
	case errors.Is(r.err, adapterhost.ErrNotConnected), errors.Is(r.err, peer.ErrClosed):
		// A connection that closes with the call out is one that is gone: the call may
		// have reached the adapter, and the signal is told as when it is not connected.
		x.offline = true
		if c.Kind == envelope.KindSessionInterrupt {
			e.settleFailedExecution(now, c, x)
			return
		}
		e.sched.set(schedKey{schedExecuteRetry, r.key.String()}, now.Add(e.p.RetryBackoff))
		return
	case r.err != nil:
		e.log.Warn("a call to carry out a control signal got no answer; it is made again", "record", r.key.String(), "error", r.err.Error())
		e.sched.set(schedKey{schedExecuteRetry, r.key.String()}, now.Add(e.p.RetryBackoff))
		return
	}
	if c.Kind == envelope.KindSessionClose {
		reply, err := adapterreg.CloseReply(r.closed)
		if err != nil {
			e.log.Error("the adapter's answer to a close has no reply; it is asked again", "record", r.key.String(), "error", err.Error())
			e.sched.set(schedKey{schedExecuteRetry, r.key.String()}, now.Add(e.p.RetryBackoff))
			return
		}
		e.settleExecution(now, c, envelope.ControlResult{Signal: adapterproto.SignalSessionClose, Close: &reply}, schedExecuteRetry)
		return
	}
	if r.interrupted.Stage == adapterproto.InterruptRequested && e.turnRunning(c) {
		// The runtime was asked to stop the turn: the end of the turn tells the sender it
		// stopped, and its not ending in time, that it was requested.
		x.requested = true
		e.sched.set(schedKey{schedInterruptConfirm, r.key.String()}, now.Add(e.p.InterruptConfirmWait))
		return
	}
	reply, err := adapterreg.InterruptReply(r.interrupted, true)
	if err != nil {
		e.log.Error("the adapter's answer to an interrupt has no reply; it is asked again", "record", r.key.String(), "error", err.Error())
		e.sched.set(schedKey{schedExecuteRetry, r.key.String()}, now.Add(e.p.RetryBackoff))
		return
	}
	e.settleExecution(now, c, envelope.ControlResult{Signal: adapterproto.SignalSessionInterrupt, Interrupt: &reply}, schedExecuteRetry)
}

// turnRunning says whether the adapter has reported a turn of the session of an
// interrupt that has not ended.
func (e *Engine) turnRunning(c store.CtlRecord) bool {
	st := e.view.endpoints[c.Key.Recipient]
	a := e.adapters[e.instanceOfEndpoint(st)]
	return a != nil && a.busy[adapterproto.EndpointRef(c.Key.Recipient.String())]
}

func (e *Engine) instanceOfEndpoint(st *endpointState) string {
	if st == nil {
		return ""
	}
	return st.rec.Instance
}

// interruptTurnEnded settles the interrupts that wait for the end of the turn of a
// session: the turn has ended, so the sender is told it stopped.
func (e *Engine) interruptTurnEnded(now time.Time, ref adapterproto.EndpointRef) {
	for _, key := range e.executionKeys() {
		c, ok := e.executing(key)
		if x := e.executions[key]; !ok || x == nil || !x.requested || adapterproto.EndpointRef(c.Key.Recipient.String()) != ref {
			continue
		}
		e.settleExecution(now, c, envelope.ControlResult{
			Signal: adapterproto.SignalSessionInterrupt, Interrupt: &adapterproto.ControlInterruptReply{Outcome: adapterproto.InterruptReplyStopped},
		}, schedExecuteRetry)
	}
}

// requestedResult is the reply of an interrupt whose turn did not end within the wait:
// the interrupt was requested and not confirmed.
func requestedResult() envelope.ControlResult {
	return envelope.ControlResult{
		Signal: adapterproto.SignalSessionInterrupt, Interrupt: &adapterproto.ControlInterruptReply{Outcome: adapterproto.InterruptReplyRequested},
	}
}

// interruptConfirmDue tells the sender of an interrupt whose turn did not end within
// the wait that the interrupt was requested and not confirmed.
func (e *Engine) interruptConfirmDue(now time.Time, key store.TargetKey) {
	c, ok := e.executing(key)
	if !ok {
		return
	}
	e.settleExecution(now, c, requestedResult(), schedExecuteRetry)
}

// executeRetry is the next try of a signal: a result whose write failed is written
// again and the adapter is not asked, otherwise the adapter is called.
func (e *Engine) executeRetry(now time.Time, key store.TargetKey) {
	if c, ok := e.executing(key); ok {
		if x := e.executions[key]; x != nil && x.result != nil {
			e.settleExecution(now, c, *x.result, schedExecuteRetry)
			return
		}
	}
	e.startExecute(now, key)
}

// executeDeadline settles a signal whose time is up, as its outcome could not be
// confirmed, or with the result it already has when only its write failed. While the
// wall clock is in doubt nothing is judged late, and it is looked at again; so is a
// write that fails, because after the deadline nothing else would try it.
func (e *Engine) executeDeadline(now time.Time, key store.TargetKey) {
	c, ok := e.executing(key)
	if !ok {
		return
	}
	if e.paused {
		e.sched.set(schedKey{schedExecuteDeadline, key.String()}, now.Add(e.p.RetryBackoff))
		return
	}
	x := e.executionOf(key)
	switch {
	case x.result != nil:
		e.settleExecution(now, c, *x.result, schedExecuteDeadline)
	case x.requested:
		e.settleExecution(now, c, requestedResult(), schedExecuteDeadline)
	default:
		e.settleExecution(now, c, e.failedResult(c, x), schedExecuteDeadline)
	}
}

// settleFailedExecution settles a signal the adapter could not be asked, as failedResult says.
func (e *Engine) settleFailedExecution(now time.Time, c store.CtlRecord, x *execution) {
	e.settleExecution(now, c, e.failedResult(c, x), schedExecuteRetry)
}

// failedResult is the reply of a signal the adapter could not be asked: a close as
// unconfirmed, an interrupt as failed, for the reason the adapter was away or did not
// answer.
func (e *Engine) failedResult(c store.CtlRecord, x *execution) envelope.ControlResult {
	st := e.view.endpoints[c.Key.Recipient]
	offline := x.offline
	if a := e.adapters[e.instanceOfEndpoint(st)]; a == nil || !a.online {
		offline = true
	}
	if c.Kind == envelope.KindSessionClose {
		reason := adapterproto.CloseReasonNativeUnreachable
		if offline {
			reason = adapterproto.CloseReasonAdapterOffline
		}
		return envelope.ControlResult{
			Signal: adapterproto.SignalSessionClose, Close: &adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyUnconfirmed, Reason: reason},
		}
	}
	reason := adapterproto.InterruptReasonNativeUnreachable
	if offline {
		reason = adapterproto.InterruptReasonAdapterOffline
	}
	return envelope.ControlResult{
		Signal: adapterproto.SignalSessionInterrupt, Interrupt: &adapterproto.ControlInterruptReply{Outcome: adapterproto.InterruptReplyFailed, Reason: reason},
	}
}

// settleExecution ends a signal in one transaction: the reply is made and stored and
// the delivery of the signal ends, and for a close, whatever its outcome (the
// channel closes whatever the adapter did), the session endpoint closes with its
// token dropped. An endpoint the owner closed meanwhile stays as it is.
//
// A write that fails keeps the result on the execution and is tried again after
// Params.RetryBackoff by the entry of the schedule that again names: the adapter is
// not asked again, since it has done what it was asked and an interrupt asked a second
// time may reach the next turn, or find none and hide that the first one stopped it.
func (e *Engine) settleExecution(now time.Time, c store.CtlRecord, result envelope.ControlResult, again schedKind) {
	err := e.writeExecution(now, c, result)
	if err == nil {
		return
	}
	e.classify(err) // a fatal error stops the loop after this command
	if e.fatal == nil {
		e.log.Warn("the result of a control signal could not be recorded; it is tried again", "record", c.Key.String(), "error", err.Error())
		x := e.executionOf(c.Key)
		x.result, x.requested = &result, false
		e.sched.remove(schedKey{schedInterruptConfirm, c.Key.String()})
		e.sched.set(schedKey{again, c.Key.String()}, now.Add(e.p.RetryBackoff))
	}
}

func (e *Engine) writeExecution(now time.Time, c store.CtlRecord, result envelope.ControlResult) error {
	n, err := e.withReply(now, c, result)
	if errors.Is(err, errReplyNotCommittable) {
		// The signal was closed while it was carried out (its endpoint was closed, and a
		// signal accepted for a closed endpoint ends unknown, as the adapter may have
		// acted): no reply can be stored, and its sender is told by the record's state.
		e.log.Info("a control signal ended before its result was in", "record", c.Key.String(), "state", c.Record.State.String())
		e.forgetExecution(c.Key)
		return nil
	}
	if err != nil {
		return err
	}
	var write *store.ControlEffect
	st := e.view.endpoints[c.Key.Recipient]
	if c.Kind == envelope.KindSessionClose && st != nil && st.rec.Status == store.EndpointOpen {
		closed := st.rec
		closed.Status, closed.Token = store.EndpointClosed, nil
		if result.Close != nil && result.Close.Outcome == adapterproto.CloseReplyClosed && closed.ReleasePending() {
			closed = releasedRecord(closed) // the answer of the signal is the release
		}
		write, n.ClosedEndpoint = &store.ControlEffect{EndpointRecord: closed}, closed.ID
	}
	if err := e.d.Store.ControlResult(n, write); err != nil {
		return err
	}
	e.view.setCtl(n)
	if write != nil {
		e.putEndpoint(write.EndpointRecord, st.decl)
		if e.view.Draining(write.ID) {
			e.later(&cmdDrain{endpoint: write.ID})
		}
	}
	e.forgetExecution(c.Key)
	return nil
}

// forgetExecution stops the work for a signal: its deadline, its retry and its wait
// for the end of a turn.
func (e *Engine) forgetExecution(key store.TargetKey) {
	e.sched.remove(schedKey{schedExecuteDeadline, key.String()})
	e.sched.remove(schedKey{schedExecuteRetry, key.String()})
	e.sched.remove(schedKey{schedInterruptConfirm, key.String()})
	delete(e.executions, key)
	e.promptsDirty = true // a session held busy by the signal may now be prompted
}

// executionKeys are the keys of the signals that are not settled and have work, in
// the order of their names.
func (e *Engine) executionKeys() []store.TargetKey {
	var keys []store.TargetKey
	for k := range e.view.ctl {
		if _, ok := e.executing(k); ok {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys
}

// redriveExecutions calls the adapter again for every close the instance owes: it has
// just connected. An interrupt was answered when the adapter was found away.
func (e *Engine) redriveExecutions(instance string, now time.Time) {
	for _, key := range e.executionKeys() {
		if c := e.view.ctl[key]; c.Kind == envelope.KindSessionClose && e.instanceOfEndpoint(e.view.endpoints[c.Key.Recipient]) == instance {
			e.startExecute(now, key)
		}
	}
}

// recoverExecutions goes on with every close and interrupt that was not settled when
// the node went down (step 11): one past its deadline is settled as the deadline
// settles it, one whose time is not up has its deadline set again and, when its
// adapter is connected, its call made again. A signal thus reaches the adapter at
// least once: a close asked again does what it did before, as a session that is
// released stays so, but an interrupt asked again after the adapter restarted may
// reach a later turn or find none and answer not_running.
func (e *Engine) recoverExecutions(now time.Time) error {
	for _, key := range e.executionKeys() {
		c := e.view.ctl[key]
		due := c.AcceptedAt.Add(e.p.ControlReplyDeadline)
		if !now.Before(due) && !e.paused {
			e.executeDeadline(now, key)
			if e.fatal != nil {
				return e.fatal
			}
			continue
		}
		e.sched.set(schedKey{schedExecuteDeadline, key.String()}, due)
		e.startExecute(now, key)
	}
	return nil
}
