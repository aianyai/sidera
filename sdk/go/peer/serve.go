package peer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strconv"
)

// handleLine classifies one inbound line and acts on it. It runs on the read
// loop and never runs application code.
func (p *Peer) handleLine(line []byte) {
	msg, cerr := p.dialect.classify(line)
	if cerr != nil {
		p.log.Warn("invalid message", "code", cerr.err.Code, "reason", cerr.err.Message)
		p.send(errorLine(cerr.id, cerr.err))
		return
	}
	switch msg.kind {
	case kindRequest:
		p.acceptRequest(msg)
	case kindNotification:
		p.acceptNotification(msg)
	case kindResponse:
		p.routeResponse(msg)
	}
}

// acceptRequest decides what happens to a request before any of its content
// is looked at: unknown method, an id already in flight, a full queue. Only
// then is it queued for a handler. A -32001 therefore always means the handler
// did not run.
func (p *Peer) acceptRequest(msg message) {
	p.handlersMu.RLock()
	h := p.requestHandlers[msg.method]
	p.handlersMu.RUnlock()
	if h == nil {
		p.log.Warn("request for unknown method", "method", clip(msg.method, 64))
		p.send(errorLine(msg.id, NewError(CodeMethodNotFound, "method not found")))
		return
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	if _, inFlight := p.inbound[msg.key]; inFlight {
		p.mu.Unlock()
		p.log.Warn("request id already in flight", "method", clip(msg.method, 64))
		p.send(errorLine(msg.id, NewError(CodeInvalidRequest, "request id already in flight")))
		return
	}
	ctx, cancel := context.WithCancel(p.baseCtx)
	req := &inboundRequest{id: msg.id, key: msg.key, method: msg.method, params: msg.params, handler: h, ctx: ctx, cancel: cancel}
	select {
	case p.reqQ <- req:
		p.inbound[msg.key] = req
		p.mu.Unlock()
	default:
		p.mu.Unlock()
		cancel()
		p.log.Warn("request queue full", "method", clip(msg.method, 64))
		p.send(errorLine(msg.id, NewError(p.dialect.OverloadedCode, "overloaded")))
	}
}

func (p *Peer) acceptNotification(msg message) {
	if msg.method == p.dialect.CancelMethod {
		p.cancelRequest(msg.params)
		return
	}
	p.handlersMu.RLock()
	h := p.notificationHandlers[msg.method]
	p.handlersMu.RUnlock()
	if h == nil {
		p.log.Warn("unknown notification dropped", "method", clip(msg.method, 64))
		return
	}
	select {
	case p.noteQ <- inboundNotification{method: msg.method, params: msg.params, handler: h}:
	default:
		p.log.Error("notification queue full", "method", clip(msg.method, 64))
		p.terminate(ErrNotificationOverflow, nil)
	}
}

// cancelRequest cancels the context of an in-flight inbound request and marks
// it cancelled. Unless the dialect is SilentOnCancel the request is still
// answered, exactly once, by its worker.
func (p *Peer) cancelRequest(params json.RawMessage) {
	id, ok := p.dialect.CancelTarget(params)
	// CancelTarget is the dialect's own function and a custom one may not
	// validate the id it returns, so the id is checked here before it is used as
	// a key.
	if !ok || id.Validate() != nil {
		p.log.Warn("cancel request names no usable request id")
		return
	}
	p.mu.Lock()
	req := p.inbound[id.key()]
	if req != nil {
		req.cancelled = true
	}
	p.mu.Unlock()
	if req == nil {
		p.log.Debug("cancel for a request that is not in flight")
		return
	}
	req.cancel()
}

// routeResponse hands a response to the call waiting for it. A response that
// matches no pending call (the call timed out, or the id was never ours) is
// dropped.
func (p *Peer) routeResponse(msg message) {
	if msg.key == "" {
		p.log.Warn("response without an id dropped", "code", errorCode(msg.err))
		return
	}
	var c *pendingCall
	if msg.key[0] == 'n' {
		if id, err := strconv.ParseInt(msg.key[1:], 10, 64); err == nil {
			p.mu.Lock()
			c = p.pending[id]
			delete(p.pending, id)
			p.mu.Unlock()
		}
	}
	if c == nil {
		p.log.Warn("late or unknown response dropped", "id", clip(string(msg.id), 64))
		return
	}
	switch {
	case msg.malformed:
		p.log.Warn("malformed response", "id", clip(string(msg.id), 64))
		c.done <- callResult{err: ErrInvalidResponse}
	case msg.err != nil:
		c.done <- callResult{err: msg.err}
	default:
		c.done <- callResult{result: msg.result}
	}
}

func errorCode(e *Error) int {
	if e == nil {
		return 0
	}
	return e.Code
}

// requestWorker is one of MaxConcurrentHandlers goroutines that run request
// handlers.
func (p *Peer) requestWorker() {
	for {
		select {
		case req := <-p.reqQ:
			if p.isClosed() {
				return
			}
			p.serve(req)
		case <-p.done:
			return
		}
	}
}

// serve answers one request exactly once, or not at all when the dialect is
// SilentOnCancel and a cancellation arrived before the answer was queued.
func (p *Peer) serve(req *inboundRequest) {
	defer req.cancel()
	var line []byte
	switch {
	case req.ctx.Err() != nil:
		// Cancelled while queued: the handler never starts, the answer is still owed.
		line = errorLine(req.id, NewError(CodeRequestCancelled, "request cancelled"))
	case !structured(req.params):
		line = errorLine(req.id, NewError(CodeInvalidParams, "params must be an object or an array"))
	default:
		line = p.invoke(req)
	}
	// The id stops being in flight before the answer is queued: the other
	// side may reuse it as soon as it reads the answer.
	// A cancellation that finds the request in flight is thereby ordered before
	// the answer; one that comes later finds nothing and changes nothing.
	p.mu.Lock()
	if p.inbound[req.key] == req {
		delete(p.inbound, req.key)
	}
	silent := p.dialect.SilentOnCancel && req.cancelled
	p.mu.Unlock()
	if silent {
		p.log.Debug("cancelled request not answered", "method", clip(req.method, 64))
		return
	}
	p.send(line)
}

// invoke runs the handler and builds the reply line. Whatever the handler
// returned, the line is one that the schema accepts and the other side's limit
// allows: a result that is not an object, one that does not marshal and a reply
// that is too long are all answered with -32603.
func (p *Peer) invoke(req *inboundRequest) []byte {
	var line []byte
	result, err := p.callHandler(req)
	if err != nil {
		line = errorLine(req.id, p.errorFor(req, err))
	} else if line, err = resultLine(req.id, result); err != nil {
		p.log.Error("result is not sendable", "method", clip(req.method, 64), "err", err)
		line = errorLine(req.id, NewError(CodeInternalError, "internal error"))
	}
	if len(line)-1 > p.params.MaxLineBytes {
		p.log.Error("reply exceeds the line limit", "method", clip(req.method, 64))
		line = errorLine(req.id, NewError(CodeInternalError, "reply too large"))
	}
	return line
}

// errorFor chooses the error a failed handler is answered with. A nil *Error
// inside a non-nil error is returned as nil, which errorLine answers as an
// internal error.
func (p *Peer) errorFor(req *inboundRequest, err error) *Error {
	var re *Error
	switch {
	case errors.As(err, &re):
		return re
	case req.ctx.Err() != nil && errors.Is(err, req.ctx.Err()):
		return NewError(CodeRequestCancelled, "request cancelled")
	}
	p.log.Error("handler failed", "method", clip(req.method, 64), "err", err)
	return NewError(CodeInternalError, "internal error")
}

func (p *Peer) callHandler(req *inboundRequest) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("handler panicked", "method", clip(req.method, 64), "panic", r, "stack", string(debug.Stack()))
			result, err = nil, fmt.Errorf("handler panicked: %v", r)
		}
	}()
	return req.handler(req.ctx, req.params)
}

// notificationLoop handles notifications one at a time in arrival order.
func (p *Peer) notificationLoop() {
	for {
		select {
		case n := <-p.noteQ:
			if p.isClosed() {
				return
			}
			p.notify(n)
		case <-p.done:
			return
		}
	}
}

func (p *Peer) notify(n inboundNotification) {
	if !structured(n.params) {
		p.log.Warn("notification with unusable params dropped", "method", clip(n.method, 64))
		return
	}
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("notification handler panicked", "method", clip(n.method, 64), "panic", r, "stack", string(debug.Stack()))
		}
	}()
	n.handler(p.baseCtx, n.params)
}
