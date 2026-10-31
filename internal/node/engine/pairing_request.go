package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/pairing"
)

// This file is the owner's request for a pairing: the operation that starts the
// online segment with another node. It is answered when the segment ends, with the
// request the node saved or the target's refusal, so the owner has the code to
// compare in the answer.
//
// The order is the one of an adapter installation, with the work that waits for
// something outside done by a worker: the loop looks the request up in the stored
// results and takes the request in, the worker dials, the loop starts the segment
// on the stream, and the worker waits for the segment to end and answers. What ends
// the segment is a call of the state machine, and the call that ends it stores the
// result (pairingPerform asks endingRequests), so a result is never stored without
// the request it reports, and never twice.

// requestingOperation is an operation the loop does not run but starts: its answer
// comes from a worker once something outside the node has happened.
type requestingOperation interface {
	Operation
	pairingTarget() (node ident.NodeID, hints []string)
	// result words the answer from the request the node saved, or from the target's
	// refusal when there is no request.
	result(saved *pairing.InitiatorRecord, refusal pairing.RefusalReason) ([]byte, error)
}

// PairingRequested is what a request for a pairing came to, for the result to say.
type PairingRequested struct {
	Node ident.NodeID
	// Saved is the request this node saved when the target accepted it; nil when the
	// target refused.
	Saved *pairing.InitiatorRecord
	// Refusal says why the target refused, when it did.
	Refusal pairing.RefusalReason
}

// RequestPairingOp is the operation that asks a node for a pairing: it dials the
// node, starts the online segment and answers when the segment has ended. hints are
// addresses to dial for this pairing only, never kept; with none the node is dialed
// on the address book. encode makes the result; it runs in the loop and must not
// wait.
func RequestPairingOp(node ident.NodeID, hints []string, encode func(PairingRequested) ([]byte, error)) Operation {
	return &requestPairingOp{trustOp{admin.OpConfirmTrust}, node, hints, encode}
}

type requestPairingOp struct {
	trustOp
	node   ident.NodeID
	hints  []string
	encode func(PairingRequested) ([]byte, error)
}

// run is never called: Execute starts this operation instead of running it.
func (o *requestPairingOp) run(*Engine, *opRun) ([]byte, error) {
	return nil, errors.New("engine: a request for a pairing is started by its own command and not run")
}

func (o *requestPairingOp) pairingTarget() (ident.NodeID, []string) { return o.node, o.hints }

func (o *requestPairingOp) result(saved *pairing.InitiatorRecord, refusal pairing.RefusalReason) ([]byte, error) {
	return o.encode(PairingRequested{Node: o.node, Saved: saved, Refusal: refusal})
}

// pairingRequestInFlight is a request for a pairing from the moment the loop takes
// it in until it is answered.
type pairingRequestInFlight struct {
	cmd ManagementCommand
	op  requestingOperation
	sid pairing.SessionID
	// live: the session is registered and the state machine has started the segment.
	live bool
	// closedEarly: the stream ended before the session was registered.
	closedEarly bool
	// ended carries the answer to the worker, once. Only endRequest sends on it.
	ended chan requestEnd
}

type requestEnd struct {
	res StoredResult
	err error
}

type cmdRequest struct {
	call[StoredResult]
	cmd ManagementCommand
	op  requestingOperation
}

func (c *cmdRequest) gate() gate { return gateWrite }
func (c *cmdRequest) run(e *Engine, now time.Time) {
	if res, answered, err := e.replayOf(c.cmd); answered {
		c.done(res, err)
		return
	}
	if err := e.takeRequest(c); err != nil {
		c.fail(e.classify(err))
	}
}

// takeRequest takes a request that is to run in: either it is refused, or a worker
// is started that answers the command, and the loop does not.
func (e *Engine) takeRequest(c *cmdRequest) error {
	node, hints := c.op.pairingTarget()
	switch {
	case e.d.Keys == nil:
		return errNoKeys
	case e.net == nil:
		// Without a network there is nothing to reach the node over.
		return refuse(ReasonDirectoryUnavailable)
	}
	if _, running := e.pairing.requests[c.cmd.Key]; running {
		// The request that is running is this one, sent again too early.
		return refuse(ReasonLoopBusy)
	}
	e.pairing.last++
	req := &pairingRequestInFlight{cmd: c.cmd, op: c.op, sid: e.pairing.last, ended: make(chan requestEnd, 1)}
	if !e.spawn(func(ctx context.Context) { e.runRequest(ctx, c, req, node, hints) }) {
		return refuse(ReasonShuttingDown)
	}
	e.pairing.requests[c.cmd.Key] = req
	return nil
}

// runRequest is the worker of a request: it dials, hands the stream to the loop and
// waits, bounded by the engine's context, for the loop to say the segment ended.
func (e *Engine) runRequest(ctx context.Context, c *cmdRequest, req *pairingRequestInFlight, node ident.NodeID, hints []string) {
	started := &cmdRequestStarted{call: newCall[struct{}](), req: req}
	started.peer, started.stream, started.err = e.net.StartPairing(ctx, node, hints, req.sid)
	if _, err := roundTrip(ctx, e, submitInternal, started, started.call); err != nil {
		// The loop did not take the stream, which means the engine is ending.
		if started.stream != nil {
			started.stream.Close()
		}
		c.fail(err)
		return
	}
	select {
	case end := <-req.ended:
		c.done(end.res, end.err)
	case <-ctx.Done():
		c.fail(refuse(ReasonShuttingDown))
	}
}

// endRequest answers a request, once: the loop is the only caller, and the request
// is in the table until it is answered, so whichever call of the state machine, or
// failure to dial, comes first answers it and the others find it gone.
func (e *Engine) endRequest(req *pairingRequestInFlight, res StoredResult, err error) {
	if e.pairing.requests[req.cmd.Key] != req {
		return
	}
	delete(e.pairing.requests, req.cmd.Key)
	req.ended <- requestEnd{res, err}
}

type cmdRequestStarted struct {
	call[struct{}]
	req    *pairingRequestInFlight
	peer   PairingPeer
	stream PairingStream
	err    error
}

func (c *cmdRequestStarted) gate() gate { return gateLoopback }
func (c *cmdRequestStarted) run(e *Engine, now time.Time) {
	e.requestStarted(now, c)
	c.done(struct{}{}, nil)
}

// requestStarted starts the online segment on the stream the worker opened. A
// stream that could not be opened, one that ended before the engine held it, and a
// command the state machine does not execute (it is a refusal, and stores nothing)
// all end the request here, and the stream is closed.
func (e *Engine) requestStarted(now time.Time, c *cmdRequestStarted) {
	req := c.req
	if e.pairing.requests[req.cmd.Key] != req {
		if c.stream != nil {
			c.stream.Close()
		}
		return
	}
	if c.err != nil {
		e.endRequest(req, StoredResult{}, e.dialRefusal(c.err))
		return
	}
	if req.closedEarly {
		c.stream.Close()
		e.endRequest(req, StoredResult{}, refuse(ReasonDirectoryUnavailable))
		return
	}
	fail := func(err error) {
		c.stream.Close()
		delete(e.pairing.sessions, req.sid)
		req.live = false
		e.endRequest(req, StoredResult{}, e.classify(err))
	}
	node, hints := req.op.pairingTarget()
	e.pairing.sessions[req.sid] = &pairingSession{side: sideInitiator, stream: c.stream, peer: c.peer}
	req.live = true
	next, out, acts, err := e.pairing.state.Initiate(e.pairingWorld(), c.peer.Conn, req.sid, pairing.Request{Actor: admin.LocalOwner(), ID: req.cmd.Key.ID},
		pairing.Initiation{Target: node, Addresses: hints}, e.randomReader(), now)
	if err == nil {
		err = e.pairingRefusal(out, "addresses")
	}
	if err == nil {
		err = e.pairingPerform(next, acts, nil)
	}
	if err != nil {
		fail(err)
		return
	}
	e.pairingFollow(now)
}

// dialRefusal is the answer to a request whose stream could not be opened: a node
// that cannot be reached is the refusal that says so, and the engine's own end is
// that of its context.
func (e *Engine) dialRefusal(err error) error {
	switch {
	case errors.Is(err, transport.ErrUnreachable):
		return refuse(ReasonDirectoryUnavailable)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return refuse(ReasonShuttingDown)
	}
	return fmt.Errorf("engine: opening the pairing stream: %w", err)
}

// requestOf is the request whose session is sid, if one is in flight.
func (e *Engine) requestOf(sid pairing.SessionID) *pairingRequestInFlight {
	for _, r := range e.pairing.requests {
		if r.sid == sid {
			return r
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// What ends a request

// requestEnding is a request that a call of the state machine ends, with what the
// call says of it.
type requestEnding struct {
	req *pairingRequestInFlight
	// saved is the request the call saves, which is the end that has a request; nil
	// for an end without one.
	saved *pairing.InitiatorRecord
	// refusal is why the target refused, for an end without a request.
	refusal pairing.RefusalReason
}

// liveRequestFor is the request whose segment is open to the node. Initiate allows
// one segment to a node at a time, so there is at most one.
func (e *Engine) liveRequestFor(node ident.NodeID) *pairingRequestInFlight {
	for _, r := range e.pairing.requests {
		if n, _ := r.op.pairingTarget(); r.live && n == node {
			return r
		}
	}
	return nil
}

// endingRequests finds the requests a list of actions ends. A segment ends when the
// state machine saves the request the target accepted (the first record of an
// identifier this node does not hold yet; the same node's other records change
// later and end nothing), or when it says the segment ended without one.
func (e *Engine) endingRequests(acts []pairing.Action) []requestEnding {
	var ends []requestEnding
	for _, a := range acts {
		switch a := a.(type) {
		case pairing.PersistInitiator:
			if _, held := e.pairing.state.Initiator(a.Record.ID()); held {
				continue
			}
			if req := e.liveRequestFor(a.Record.Transcript.Terms().Target.Node()); req != nil {
				rec := a.Record
				ends = append(ends, requestEnding{req: req, saved: &rec})
			}
		case pairing.Notify:
			if k := a.Notice.Kind; k != pairing.NoticeNotStarted && k != pairing.NoticeAttemptUsed {
				continue
			}
			if req := e.liveRequestFor(a.Notice.Remote); req != nil {
				ends = append(ends, requestEnding{req: req, refusal: a.Notice.Refusal})
			}
		}
	}
	return ends
}

// savingRequest is the end that the persist of the record is, if it is one.
func savingRequest(ends []requestEnding, rec pairing.InitiatorRecord) *requestEnding {
	for i := range ends {
		if s := ends[i].saved; s != nil && s.ID() == rec.ID() {
			return &ends[i]
		}
	}
	return nil
}

// persistRequested saves the request the target accepted and the result of the
// owner's request in one transaction, and answers the request.
func (e *Engine) persistRequested(end *requestEnding) error {
	result, x, err := e.requestResult(end)
	if err != nil {
		return err
	}
	rec, err := store.PreparePairingInitiator(*end.saved)
	if err != nil {
		return err
	}
	pr, err := store.PrepareAdminResult(x.record(result))
	if err != nil {
		return err
	}
	tx := e.d.Writer.Begin(&declTrustRequest)
	defer tx.Close()
	if err := store.PutPairingInitiator(tx, store.RolePairingInitiator, rec); err != nil {
		return err
	}
	if err := store.PutAdminResult(tx, store.RoleResult, pr); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	e.view.putStored(end.req.cmd.Key, end.req.cmd.Fingerprint, result)
	e.endRequest(end.req, StoredResult{Result: result}, nil)
	return nil
}

// endUnsaved answers a request whose segment ended without a saved request: the
// target's refusal is a result, stored alone; anything else (the connection was
// lost, an attempt was used) is no result of the owner's request, stores nothing,
// and leaves its identifier free for the owner to ask again.
func (e *Engine) endUnsaved(end requestEnding) {
	if end.refusal == 0 {
		e.endRequest(end.req, StoredResult{}, refuse(ReasonDirectoryUnavailable))
		return
	}
	result, x, err := e.requestResult(&end)
	if err == nil {
		err = e.storeResultOnly(x, result)
	}
	if err != nil {
		e.endRequest(end.req, StoredResult{}, e.classify(err))
		return
	}
	e.view.putStored(end.req.cmd.Key, end.req.cmd.Fingerprint, result)
	e.endRequest(end.req, StoredResult{Result: result}, nil)
}

// requestResult is the result of a request that ended, and the run it is stored
// under.
func (e *Engine) requestResult(end *requestEnding) ([]byte, *opRun, error) {
	c := end.req.cmd
	x := &opRun{now: e.d.Clock.Now(), key: c.Key, fingerprint: c.Fingerprint, name: c.Name}
	result, err := end.req.op.result(end.saved, end.refusal)
	if err != nil {
		return nil, nil, fmt.Errorf("engine: the result of %s: %w", c.Name, err)
	}
	return result, x, nil
}
