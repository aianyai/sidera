package adapterhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// conn is one connection to one adapter process: the process, the protocol peer
// over its streams, what the adapter declared, and what is counted of it.
type conn struct {
	s    *supervisor
	inst string
	log  *slog.Logger
	proc Process
	logw *logWriter
	p    *peer.Peer // nil until the peer is made

	// exited is closed when the process has ended. The goroutine that waits for it
	// is not waited for by the host: a process that cannot be ended must not hold
	// the host.
	exited chan struct{}

	admission atomic.Pointer[adapterreg.Admission] // nil until admitted
	closing   atomic.Bool                          // shutdown began: requests get -32002

	// ctx is the connection's own context, for what the forwarder asks of the
	// engine; it ends when the connection is torn down without being drained.
	ctx    context.Context
	cancel context.CancelFunc

	notes     chan pendingNote // the notifications waiting for the engine, in arrival order
	fwdDone   chan struct{}    // closed when the forwarder has returned
	fwdOnce   sync.Once        // the forwarder was started, or fwdDone closed without one
	closeOnce sync.Once

	mu       sync.Mutex // guards the counts below
	handled  int
	buffered int
	requests int
	calls    map[string]int
}

type pendingNote struct {
	method string
	params adapterproto.Message
}

func (s *supervisor) newConn(proc Process, logw *logWriter) *conn {
	ctx, cancel := context.WithCancel(s.h.ctx)
	c := &conn{
		s:       s,
		inst:    s.id,
		log:     s.log,
		proc:    proc,
		logw:    logw,
		exited:  make(chan struct{}),
		ctx:     ctx,
		cancel:  cancel,
		notes:   make(chan pendingNote, s.h.params.NotificationBuffer),
		fwdDone: make(chan struct{}),
		calls:   map[string]int{},
	}
	go func() {
		_ = proc.Wait()
		close(c.exited)
	}()
	s.mu.Lock()
	s.cur = c
	if s.stopping {
		c.closing.Store(true)
	}
	s.mu.Unlock()
	return c
}

// counts is the part of ConnCounts the connection knows.
func (c *conn) counts() ConnCounts {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := ConnCounts{Handled: c.handled, Buffered: c.buffered, Requests: c.requests}
	for m, n := range c.calls {
		for range n {
			out.Calls = append(out.Calls, m)
		}
	}
	slices.Sort(out.Calls)
	return out
}

// track counts a request of the host as sent and not answered until the returned
// function is called. A ping is not counted: it only asks whether the adapter is
// there. Whoever sends a request on a goroutine of its own and waits for it
// elsewhere counts it before it starts waiting, so that a caller that finds the
// host idle finds the request in the counts.
func (c *conn) track(method string) (done func()) {
	if method == adapterproto.MethodPing {
		return func() {}
	}
	c.mu.Lock()
	c.calls[method]++
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		if c.calls[method]--; c.calls[method] == 0 {
			delete(c.calls, method)
		}
		c.mu.Unlock()
	}
}

// call sends one request to the adapter, counted while it is out, and decodes its
// result strictly.
func (c *conn) call(ctx context.Context, method string, params, result adapterproto.Message) error {
	defer c.track(method)()
	return c.send(ctx, method, params, result)
}

// send writes one request and decodes its result strictly. The parameters are
// checked first: the host never writes a message the protocol would refuse. The
// request is not counted: see track.
func (c *conn) send(ctx context.Context, method string, params, result adapterproto.Message) error {
	if err := params.Validate(); err != nil {
		return fmt.Errorf("adapterhost: parameters of %s: %w", method, err)
	}
	var raw json.RawMessage
	if err := c.p.Call(ctx, method, params, &raw); err != nil {
		return err
	}
	if err := adapterproto.Decode(raw, result); err != nil {
		return fmt.Errorf("adapterhost: result of %s: %w", method, err)
	}
	return nil
}

// admit lets the adapter's requests through and starts the forwarder: from here
// on what the adapter says reaches the engine.
func (c *conn) admit(a adapterreg.Admission) {
	c.admission.Store(&a)
	c.fwdOnce.Do(func() {
		c.s.spawn(c.forward)
	})
}

// abortNow ends the process and the stream without a word to the adapter.
func (c *conn) abortNow() {
	_ = c.proc.Kill()
	if c.p != nil {
		_ = c.p.Close()
	}
}

// fail ends a connection the engine cannot use.
func (c *conn) fail(err error) {
	c.log.Warn("the connection cannot be used", "err", err)
	_ = c.p.Close()
}

// close ends the connection's own goroutines and returns when the forwarder has
// returned: the connection's context ends first, so that the forwarder hands the
// engine nothing more, then the stream is closed, and what is buffered and not yet
// handed to the engine is dropped. A connection that was drained first has nothing
// left to drop.
func (c *conn) close() {
	c.closeOnce.Do(func() {
		c.cancel()
		if c.p != nil {
			_ = c.p.Close()
		}
		c.fwdOnce.Do(func() { close(c.fwdDone) })
	})
	<-c.fwdDone
	// Whatever is left was never handed to the engine.
	for {
		select {
		case <-c.notes:
			c.mu.Lock()
			c.buffered--
			c.mu.Unlock()
		default:
			return
		}
	}
}

// forward takes the notifications from the buffer in order and hands them to the
// engine, one at a time. When the connection ended it hands over what is already
// buffered and returns; when the connection is closed (its context ended) it
// returns without handing over anything more.
func (c *conn) forward() {
	defer close(c.fwdDone)
	eng := c.s.h.env.Engine
	for {
		if c.ctx.Err() != nil {
			return
		}
		select {
		case n := <-c.notes:
			if !c.deliver(eng, n) {
				return
			}
		case <-c.p.Done():
			for c.ctx.Err() == nil {
				select {
				case n := <-c.notes:
					if !c.deliver(eng, n) {
						return
					}
				default:
					return
				}
			}
			return
		case <-c.ctx.Done():
			return
		}
	}
}

// deliver hands one notification to the engine and counts it as handled when the
// call returns. A notification the admission does not allow is dropped, and
// counts as handled: the host is done with it. It reports false when the engine
// could not take the notification, which ends the connection.
func (c *conn) deliver(eng Engine, n pendingNote) bool {
	adm := c.admission.Load()
	if code := adm.Check(kindOf(n.method, adm), n.method); code != 0 {
		c.log.Warn("a notification of the adapter was dropped: its declaration does not allow it", "method", n.method, "code", code)
		c.finished()
		return true
	}
	err := eng.AdapterNotified(c.ctx, c.inst, Notification{Method: n.method, Params: n.params})
	c.finished()
	if err != nil {
		if c.ctx.Err() == nil {
			c.fail(fmt.Errorf("the engine could not take %s: %w", n.method, err))
		}
		return false
	}
	return true
}

// finished moves one buffered notification to handled.
func (c *conn) finished() {
	c.mu.Lock()
	c.handled++
	c.buffered--
	c.mu.Unlock()
}

// register installs the handlers of everything the adapter may send, before the
// peer starts: an adapter's first line must find its handler. A method of the
// table that only the daemon may call gets a handler too, so that before
// admission it is answered -32003 like every request; after admission the
// declaration's check turns it into -32601.
func (c *conn) register() {
	for _, m := range adapterproto.Methods() {
		switch {
		case m.Name == adapterproto.MethodCancelRequest:
			// The peer handles it.
		case m.Name == adapterproto.MethodPing:
			c.p.Handle(m.Name, func(context.Context, json.RawMessage) (any, error) {
				return adapterproto.Empty{}, nil
			})
		case m.Notification && m.Direction == adapterproto.AdapterToDaemon:
			c.p.HandleNotification(m.Name, c.notificationHandler(m))
		case !m.Notification:
			c.p.Handle(m.Name, c.requestHandler(m))
		}
	}
}

// notificationHandler checks and buffers one notification. The peer calls it one
// notification at a time; while the buffer is full it blocks, which is the bound:
// the peer's own queue then fills and the peer ends the connection.
func (c *conn) notificationHandler(m adapterproto.MethodInfo) peer.NotificationHandler {
	return func(ctx context.Context, raw json.RawMessage) {
		if raw == nil {
			raw = json.RawMessage(`{}`)
		}
		msg, err := m.DecodeParams(raw)
		if err != nil {
			c.log.Warn("a notification of the adapter was dropped: it is not valid", "method", m.Name, "err", err)
			c.mu.Lock()
			c.handled++
			c.mu.Unlock()
			return
		}
		c.mu.Lock()
		c.buffered++
		c.mu.Unlock()
		select {
		case c.notes <- pendingNote{method: m.Name, params: asValue(msg)}:
		case <-ctx.Done():
			c.mu.Lock()
			c.buffered--
			c.mu.Unlock()
		}
	}
}

// asValue returns what a pointer to a message points to: the engine receives the
// parameters of a notification as the value of their type.
func asValue(m adapterproto.Message) adapterproto.Message {
	return reflect.ValueOf(m).Elem().Interface().(adapterproto.Message)
}

// requestHandler answers one request of the adapter, exactly once through the
// peer. The host does not refuse requests for being too many: the peer does, with
// -32001, before any handler runs, which keeps that code meaning "the handler did
// not run".
func (c *conn) requestHandler(m adapterproto.MethodInfo) peer.RequestHandler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		c.mu.Lock()
		c.requests++
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			c.requests--
			c.mu.Unlock()
		}()
		if c.closing.Load() {
			return nil, refusal(peer.CodeShuttingDown)
		}
		adm := c.admission.Load()
		if adm == nil {
			return nil, refusal(peer.CodeNotInitialized)
		}
		if code := adm.Check(kindOf(m.Name, adm), m.Name); code != 0 {
			return nil, refusal(code)
		}
		return c.dispatch(ctx, m.Name, raw)
	}
}

// kindOf is the kind of endpoint a message of the adapter is judged against: the
// host never resolves an endpoint, so it asks about the kind the method belongs
// to. A checkpoint and an availability snapshot, which every adapter may send,
// belong to the runtime when the adapter declared one, and to a direct endpoint
// otherwise.
func kindOf(method string, adm *adapterreg.Admission) adapterreg.EndpointKind {
	switch method {
	case adapterproto.MethodEndpointRegister, adapterproto.MethodEndpointUnregister,
		adapterproto.MethodEndpointSend, adapterproto.MethodEndpointRecv,
		adapterproto.MethodEndpointAck, adapterproto.MethodEndpointMessageStatus:
		return adapterreg.KindDirect
	case adapterproto.MethodRecvObserved:
		return adapterreg.KindSession
	case adapterproto.MethodCheckpointPut, adapterproto.MethodAdapterAvailability:
		if adm.Declares(adapterreg.KindRuntime) {
			return adapterreg.KindRuntime
		}
		return adapterreg.KindDirect
	}
	return adapterreg.KindRuntime
}

// refusal is the error a request is answered with for a code the host decided.
func refusal(code int) *peer.Error {
	name := peer.CodeName(code)
	if name == "" {
		for _, c := range adapterproto.ApplicationCodes() {
			if c.Code == code {
				name = c.Name
			}
		}
	}
	if name == "" {
		name = "error"
	}
	return peer.NewError(code, name)
}

// dispatch decodes the parameters of a request that passed the checks, asks the
// engine and returns its result.
func (c *conn) dispatch(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	h := c.s.h
	eng := h.env.Engine
	switch method {
	case adapterproto.MethodEndpointRegister:
		return serveRequest(c, method, raw, func(p *adapterproto.RegisterParams) (adapterproto.Message, error) {
			return eng.RegisterDirect(ctx, c.inst, *p)
		})
	case adapterproto.MethodEndpointUnregister:
		return serveRequest(c, method, raw, func(p *adapterproto.UnregisterParams) (adapterproto.Message, error) {
			return adapterproto.Empty{}, eng.UnregisterDirect(ctx, c.inst, *p)
		})
	case adapterproto.MethodCheckpointPut:
		return serveRequest(c, method, raw, func(p *adapterproto.CheckpointPutParams) (adapterproto.Message, error) {
			return adapterproto.Empty{}, eng.CheckpointPut(ctx, c.inst, *p)
		})
	case adapterproto.MethodEndpointSend, adapterproto.MethodEndpointRecv,
		adapterproto.MethodEndpointAck, adapterproto.MethodEndpointMessageStatus:
		ec := h.endpoints
		if ec == nil {
			return nil, refusal(peer.CodeMethodNotFound)
		}
		return c.dispatchEndpoint(ctx, ec, method, raw)
	case adapterproto.MethodRecvObserved:
		oc := h.observed
		if oc == nil {
			return nil, refusal(peer.CodeMethodNotFound)
		}
		return serveRequest(c, method, raw, func(p *adapterproto.RecvObservedParams) (adapterproto.Message, error) {
			return oc.RecvObserved(ctx, c.inst, *p)
		})
	}
	// Every method of the table that reaches here is one the declaration's check
	// let through, so it is one this switch knows.
	return nil, refusal(peer.CodeMethodNotFound)
}

func (c *conn) dispatchEndpoint(ctx context.Context, ec EndpointCalls, method string, raw json.RawMessage) (any, error) {
	switch method {
	case adapterproto.MethodEndpointSend:
		return serveRequest(c, method, raw, func(p *adapterproto.SendParams) (adapterproto.Message, error) {
			return ec.EndpointSend(ctx, c.inst, *p)
		})
	case adapterproto.MethodEndpointRecv:
		return serveRequest(c, method, raw, func(p *adapterproto.RecvParams) (adapterproto.Message, error) {
			return ec.EndpointRecv(ctx, c.inst, *p)
		})
	case adapterproto.MethodEndpointAck:
		return serveRequest(c, method, raw, func(p *adapterproto.AckParams) (adapterproto.Message, error) {
			return ec.EndpointAck(ctx, c.inst, *p)
		})
	}
	return serveRequest(c, method, raw, func(p *adapterproto.MessageStatusParams) (adapterproto.Message, error) {
		return ec.EndpointMessageStatus(ctx, c.inst, *p)
	})
}

// serveRequest decodes the parameters strictly, calls the engine and checks its
// result. Parameters that do not decode are -32602, except a checkpoint whose
// state is too large, which is its own code. An error of the engine that is a
// *peer.Error is the answer as it is; any other is an internal error, whose text
// the peer logs and never sends. A result that is not a valid result of the
// method is an internal error too: the adapter must not read a message the
// protocol forbids.
func serveRequest[P any, PP interface {
	*P
	adapterproto.Message
}](c *conn, method string, raw json.RawMessage, do func(*P) (adapterproto.Message, error)) (any, error) {
	if raw == nil {
		raw = json.RawMessage(`{}`)
	}
	p := new(P)
	if err := adapterproto.Decode(raw, PP(p)); err != nil {
		var ve *adapterproto.ValidationError
		if method == adapterproto.MethodCheckpointPut && errors.As(err, &ve) && ve.Field == "state" && ve.Beyond {
			return nil, peer.NewError(adapterproto.CodeCheckpointTooLarge, ve.Msg)
		}
		return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
	}
	res, err := do(p)
	if err != nil {
		// The peer answers an error that is or wraps a *peer.Error as it is, and any
		// other as an internal error without its text.
		return nil, err
	}
	if err := res.Validate(); err != nil {
		c.log.Error("the engine's result is not valid", "method", method, "err", err)
		return nil, fmt.Errorf("the result of %s is not valid: %w", method, err)
	}
	return res, nil
}
