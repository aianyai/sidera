package peer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Params are the limits of a peer. A zero field means "use the default"; a
// negative field is an error.
type Params struct {
	// MaxLineBytes is the longest message accepted or sent, the terminating
	// newline not counted. A longer inbound line ends the connection.
	MaxLineBytes int
	// MaxPendingRequests is the length of the inbound request queue. A request
	// that finds it full is refused with CodeOverloaded.
	MaxPendingRequests int
	// MaxConcurrentHandlers is the number of request handlers that run at once.
	MaxConcurrentHandlers int
	// MaxPendingNotifications is the length of the inbound notification queue.
	// Notifications are handled one at a time, in arrival order. When the queue
	// is full the connection is closed.
	MaxPendingNotifications int
	// MaxOutbound is the length of the outbound line queue. When it is full
	// Call and Notify return ErrOverloaded without writing anything.
	MaxOutbound int
	// WriteTimeout is the longest a single line may take to write. A write
	// that exceeds it closes the transport.
	WriteTimeout time.Duration
}

// DefaultParams returns the starting values: 1 MiB lines, 64 queued requests,
// 16 concurrent handlers, 256 queued notifications, 256 outbound lines and a
// 10 second write timeout.
func DefaultParams() Params {
	return Params{
		MaxLineBytes:            1 << 20,
		MaxPendingRequests:      64,
		MaxConcurrentHandlers:   16,
		MaxPendingNotifications: 256,
		MaxOutbound:             256,
		WriteTimeout:            10 * time.Second,
	}
}

// resolve fills unset (zero) fields with their defaults.
func (p Params) resolve() (Params, error) {
	d := DefaultParams()
	for _, f := range []struct {
		name string
		v    *int
		def  int
	}{
		{"MaxLineBytes", &p.MaxLineBytes, d.MaxLineBytes},
		{"MaxPendingRequests", &p.MaxPendingRequests, d.MaxPendingRequests},
		{"MaxConcurrentHandlers", &p.MaxConcurrentHandlers, d.MaxConcurrentHandlers},
		{"MaxPendingNotifications", &p.MaxPendingNotifications, d.MaxPendingNotifications},
		{"MaxOutbound", &p.MaxOutbound, d.MaxOutbound},
	} {
		switch {
		case *f.v < 0:
			return Params{}, fmt.Errorf("peer: %s is negative", f.name)
		case *f.v == 0:
			*f.v = f.def
		}
	}
	switch {
	case p.WriteTimeout < 0:
		return Params{}, errors.New("peer: WriteTimeout is negative")
	case p.WriteTimeout == 0:
		p.WriteTimeout = d.WriteTimeout
	}
	return p, nil
}

// Config configures New.
type Config struct {
	Params Params
	// Logger receives protocol violations, dropped messages and the end of the
	// connection. It never receives message bodies. Nil discards everything.
	Logger *slog.Logger
}

// RequestHandler answers one inbound request. params is the raw "params"
// member, nil when absent; when present it is an object or an array. The
// context is cancelled when the other side sends $/cancel_request for this
// request and when the peer closes. A handler that is cancelled may stop early
// and return ctx.Err(), which is answered with CodeRequestCancelled, or may
// ignore the cancellation and return its result.
//
// Returning an *Error answers with that error. Any other error is answered
// with CodeInternalError and a generic message; the error itself is logged and
// never sent. A panic is recovered and treated the same way.
type RequestHandler func(ctx context.Context, params json.RawMessage) (result any, err error)

// NotificationHandler handles one inbound notification. Notifications are
// delivered one at a time in arrival order, so a slow handler delays the ones
// behind it and can overflow the queue.
type NotificationHandler func(ctx context.Context, params json.RawMessage)

// outItem is one line queued for writing.
type outItem struct {
	line []byte
	// last: close the transport once this line is written.
	last bool
}

type callResult struct {
	result json.RawMessage
	err    error
}

type pendingCall struct {
	done chan callResult // buffered; receives exactly one value
}

type inboundRequest struct {
	id      json.RawMessage
	key     string
	method  string
	params  json.RawMessage
	handler RequestHandler
	ctx     context.Context
	cancel  context.CancelFunc
}

type inboundNotification struct {
	method  string
	params  json.RawMessage
	handler NotificationHandler
}

// Peer is one end of a JSON-RPC 2.0 connection. Create it with New, register
// handlers, then call Start.
type Peer struct {
	params Params
	log    *slog.Logger
	r      io.Reader
	w      io.WriteCloser

	baseCtx    context.Context
	cancelBase context.CancelFunc

	handlersMu           sync.RWMutex
	requestHandlers      map[string]RequestHandler
	notificationHandlers map[string]NotificationHandler

	nextID atomic.Int64
	ended  atomic.Bool // set once the connection has begun to end

	// mu guards closed, cause, pending and inbound. The outbound queue is only
	// ever sent to without blocking while mu is held, so that closing cannot
	// interleave with an enqueue.
	mu      sync.Mutex
	closed  bool // guarded by mu; ended mirrors it for lock-free reads
	cause   error
	pending map[int64]*pendingCall
	inbound map[string]*inboundRequest

	out   chan outItem
	reqQ  chan *inboundRequest
	noteQ chan inboundNotification

	startOnce sync.Once
	closeOnce sync.Once
	closeErr  error
	done      chan struct{}
	wg        sync.WaitGroup // reader (when it can be interrupted) and writer
}

// New creates a peer over r and w. The peer owns both: Close, and any end of
// the connection, closes w and, when r is also an io.Closer, r. A reader that
// is not an io.Closer cannot be interrupted, so its goroutine ends only when
// the reader returns and Close does not wait for it.
//
// Nothing is read or written until Start.
func New(r io.Reader, w io.WriteCloser, cfg Config) (*Peer, error) {
	params, err := cfg.Params.resolve()
	if err != nil {
		return nil, err
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Peer{
		params:               params,
		log:                  log,
		r:                    r,
		w:                    w,
		baseCtx:              ctx,
		cancelBase:           cancel,
		requestHandlers:      make(map[string]RequestHandler),
		notificationHandlers: make(map[string]NotificationHandler),
		pending:              make(map[int64]*pendingCall),
		inbound:              make(map[string]*inboundRequest),
		out:                  make(chan outItem, params.MaxOutbound),
		reqQ:                 make(chan *inboundRequest, params.MaxPendingRequests),
		noteQ:                make(chan inboundNotification, params.MaxPendingNotifications),
		done:                 make(chan struct{}),
	}, nil
}

// Handle registers the handler for a request method, replacing any earlier
// one. It panics on an empty method, a nil handler or MethodCancelRequest,
// which the peer handles itself. Requests for methods without a handler are
// answered with CodeMethodNotFound.
func (p *Peer) Handle(method string, h RequestHandler) {
	checkRegistration(method, h == nil)
	p.handlersMu.Lock()
	defer p.handlersMu.Unlock()
	p.requestHandlers[method] = h
}

// HandleNotification registers the handler for a notification method. The
// same registration rules as Handle apply. Notifications without a handler are
// logged and dropped.
func (p *Peer) HandleNotification(method string, h NotificationHandler) {
	checkRegistration(method, h == nil)
	p.handlersMu.Lock()
	defer p.handlersMu.Unlock()
	p.notificationHandlers[method] = h
}

func checkRegistration(method string, nilHandler bool) {
	switch {
	case method == "":
		panic("peer: empty method name")
	case method == MethodCancelRequest:
		panic("peer: " + MethodCancelRequest + " is handled by the peer itself")
	case nilHandler:
		panic("peer: nil handler for " + method)
	}
}

// Start begins reading and writing. Handlers registered later are still
// found; messages that arrive before their handler exists are refused or
// dropped as unknown. Start is idempotent.
func (p *Peer) Start() {
	p.startOnce.Do(func() {
		p.wg.Add(1)
		go p.writeLoop()
		_, interruptible := p.r.(io.Closer)
		if interruptible {
			p.wg.Add(1)
		}
		go p.readLoop(interruptible)
		go p.notificationLoop()
		for range p.params.MaxConcurrentHandlers {
			go p.requestWorker()
		}
	})
}

// Done is closed when the connection has ended and the transport is closed.
// The owner of a child process kills it after Done.
func (p *Peer) Done() <-chan struct{} { return p.done }

// Err reports why the connection ended: nil while it is open, then ErrClosed
// after Close, ErrRemoteClosed, ErrLineTooLong, ErrNotificationOverflow,
// ErrWriteTimeout, or a wrapped read or write error. It is final once Done is
// closed.
func (p *Peer) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cause
}

// Close ends the connection: pending calls end with ErrClosed, handler
// contexts are cancelled, queued lines that have not been written are
// discarded and the transport is closed. It waits for the peer's reader and
// writer goroutines but not for handlers, which must return when their
// context is cancelled. Close is idempotent and safe to call from a handler.
func (p *Peer) Close() error {
	p.terminate(ErrClosed, nil)
	p.closeTransport()
	p.wg.Wait()
	return p.closeErr
}

// terminate ends the connection with the given cause; only the first call has
// any effect. With final set, that line is written before the transport is
// closed (and the transport is closed regardless if it cannot be queued
// within WriteTimeout).
func (p *Peer) terminate(cause error, final []byte) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.ended.Store(true)
	p.cause = cause
	pending := p.pending
	p.pending = nil
	p.mu.Unlock()

	switch cause {
	case ErrClosed, ErrRemoteClosed:
		p.log.Info("peer connection ended", "cause", cause)
	default:
		p.log.Warn("peer connection ended", "cause", cause)
	}
	p.cancelBase()
	for _, c := range pending {
		c.done <- callResult{err: ErrClosed}
	}
	if final != nil && p.enqueueFinal(final) {
		return
	}
	p.closeTransport()
}

func (p *Peer) enqueueFinal(line []byte) bool {
	t := time.NewTimer(p.params.WriteTimeout)
	defer t.Stop()
	select {
	case p.out <- outItem{line: line, last: true}:
		return true
	case <-t.C:
	case <-p.done:
	}
	return false
}

func (p *Peer) closeTransport() {
	p.closeOnce.Do(func() {
		p.closeErr = p.w.Close()
		if c, ok := p.r.(io.Closer); ok {
			c.Close()
		}
		close(p.done)
	})
}

// isClosed reports whether the connection has begun to end. Work still queued
// at that point is abandoned rather than run.
func (p *Peer) isClosed() bool { return p.ended.Load() }
