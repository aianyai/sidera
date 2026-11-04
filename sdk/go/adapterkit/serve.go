package adapterkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// Config is what an adapter says about itself and the application it serves with.
type Config struct {
	// Adapter is the identity answered in initialize; it must be what the manifest
	// of the package says.
	Adapter adapterproto.AdapterInfo
	// Capabilities are what the adapter declares in initialize. A request that
	// needs a capability that is not declared is answered capability_not_declared.
	Capabilities adapterproto.Capabilities
	// App answers the requests the kit does not.
	App Application
	// Peer sets the limits of the connection; the zero value is the defaults.
	Peer peer.Params
	// Logger receives protocol violations and the end of the connection, never a
	// message body. Nil discards everything.
	Logger *slog.Logger
}

func (c Config) validate() error {
	if c.App == nil {
		return errors.New("adapterkit: Config.App is nil")
	}
	// What initialize will answer must be a valid answer, which is found out here
	// and not by the daemon.
	if err := (adapterproto.InitializeResult{ProtocolVersion: adapterproto.ProtocolMin, Adapter: c.Adapter, Capabilities: c.Capabilities}).Validate(); err != nil {
		return fmt.Errorf("adapterkit: the identity or the capabilities of the adapter: %w", err)
	}
	return nil
}

// ServeStdio serves the protocol over the standard input and output of the
// process, which is how the daemon runs an adapter. See Serve.
func ServeStdio(ctx context.Context, cfg Config) error {
	return Serve(ctx, os.Stdin, os.Stdout, cfg)
}

// Serve runs the adapter side of the protocol over r and w until the daemon closes
// the stream or ctx ends. It returns nil when the daemon closed the stream, the
// error of ctx when ctx ended, and otherwise the reason the connection failed. The
// instance lock, if it was taken, is released when Serve returns.
func Serve(ctx context.Context, r io.Reader, w io.WriteCloser, cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	p, err := peer.New(r, w, peer.Config{Params: cfg.Peer, Logger: cfg.Logger})
	if err != nil {
		return err
	}
	s := &server{cfg: cfg, p: p, conn: &Conn{p: p}, log: cfg.Logger}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.register()
	defer s.releaseLock()
	p.Start()
	var ended error
	select {
	case <-p.Done():
		ended = p.Err()
	case <-ctx.Done():
	}
	_ = p.Close()
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(ended, peer.ErrRemoteClosed):
		return nil
	}
	return ended
}

// server is one connection: the state the rules of the protocol are about, which
// is whether the daemon has initialized the adapter and whether it has asked it to
// end, and the instance lock.
type server struct {
	cfg  Config
	p    *peer.Peer
	conn *Conn
	log  *slog.Logger

	mu           sync.Mutex
	initializing bool
	initialized  bool
	shuttingDown bool
	closed       bool // the connection has ended: nothing may take the lock any more
	release      func()
}

func (s *server) register() {
	s.p.Handle(adapterproto.MethodInitialize, s.initialize)
	s.p.Handle(adapterproto.MethodPing, func(context.Context, json.RawMessage) (any, error) { return adapterproto.Empty{}, nil })
	s.p.Handle(adapterproto.MethodShutdown, s.shutdown)
	// A method the adapter has no handler for is not found, but the order of the
	// states comes first: before initialize and after shutdown it is the state that
	// is answered.
	s.p.HandleDefault(func(context.Context, string, json.RawMessage) (any, error) {
		if err := s.admit(); err != nil {
			return nil, err
		}
		return nil, peer.NewError(peer.CodeMethodNotFound, "method not found")
	})

	app := s.cfg.App
	serve(s, adapterproto.MethodAdapterStart, nil, s.start)
	serve(s, adapterproto.MethodAdapterProbe, nil, s.probe)
	serve(s, adapterproto.MethodSessionCreate, declaresRuntime, app.CreateSession)
	serve(s, adapterproto.MethodPromptDeliver, declaresRuntime, app.DeliverPrompt)
	serve(s, adapterproto.MethodPromptReconcile, declaresRuntime, app.Reconcile)
	serve(s, adapterproto.MethodSessionInterrupt, declaresRuntime, app.Interrupt)
	serve(s, adapterproto.MethodSessionClose, declaresRuntime, app.CloseSession)
	serve(s, adapterproto.MethodSessionAttach, declaresAttach, app.AttachSession)
	// A notification has no answer to carry a refusal: one that is not admitted or
	// not valid is dropped.
	notify(s, adapterproto.MethodEndpointPending, nil, app.EndpointPending)
	notify(s, adapterproto.MethodSessionDetached, declaresAttach, app.SessionDetached)
}

// capabilityGate says whether the capabilities an adapter declared include the
// one a method needs.
type capabilityGate func(adapterproto.Capabilities) bool

// declaresRuntime is the gate of the methods of a runtime adapter.
func declaresRuntime(c adapterproto.Capabilities) bool { return c.Runtime != nil }

// declaresAttach is the gate of the methods of attached sessions.
func declaresAttach(c adapterproto.Capabilities) bool { return c.Runtime != nil && c.Runtime.Attach }

// start passes adapter.start to the application. Nothing of attached sessions
// reaches an application that did not declare attach, so an entry of an attached
// session is refused here as session.attach is, before the application sees it.
func (s *server) start(ctx context.Context, p adapterproto.StartParams) (adapterproto.StartResult, error) {
	attached := func(e adapterproto.SessionAttach) bool { return bool(e.Attached) }
	if !declaresAttach(s.cfg.Capabilities) && slices.ContainsFunc(p.Sessions, attached) {
		return adapterproto.StartResult{}, notDeclared(adapterproto.MethodAdapterStart + " with an attached session")
	}
	ctx, cancel := answerWithinLimit(ctx)
	defer cancel()
	return s.cfg.App.Start(ctx, p)
}

// probe passes adapter.probe to the application, which has to answer inside the
// limit of a handshake call.
func (s *server) probe(ctx context.Context, p adapterproto.ProbeParams) (adapterproto.ProbeResult, error) {
	ctx, cancel := answerWithinLimit(ctx)
	defer cancel()
	return s.cfg.App.Probe(ctx, p)
}

// answerMargin is what the deadline of Start and Probe leaves of
// adapterproto.HandshakeLimit for the answer to reach the daemon and for the daemon
// to read it.
const answerMargin = 2 * time.Second

// answerWithinLimit gives ctx the deadline by which Start and Probe must have
// answered, counted from the receipt of the request, which is when the daemon
// started its clock, give or take the transport.
func answerWithinLimit(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, adapterproto.HandshakeLimit-answerMargin)
}

// notify registers the handler of a notification of the daemon, behind the same
// checks as a request.
func notify[P any, PP interface {
	*P
	adapterproto.Message
}](s *server, method string, gate capabilityGate, f func(context.Context, P)) {
	s.p.HandleNotification(method, func(ctx context.Context, raw json.RawMessage) {
		var params P
		if s.admit() != nil || (gate != nil && !gate(s.cfg.Capabilities)) || adapterproto.Decode(raw, PP(&params)) != nil {
			return
		}
		f(ctx, params)
	})
}

// admit is the admission check every method but initialize and ping passes: before
// initialize it is not_initialized (-32003), after shutdown began it is
// shutting_down (-32002).
func (s *server) admit() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.shuttingDown:
		return peer.NewError(peer.CodeShuttingDown, "shutting down")
	case !s.initialized:
		return peer.NewError(peer.CodeNotInitialized, "initialize first")
	}
	return nil
}

// serve registers the handler of a request of the daemon: the admission check, the
// capability the method needs if any, strict decoding of the parameters, the
// application, and validation of the result before it is sent.
func serve[P any, PP interface {
	*P
	adapterproto.Message
}, R adapterproto.Message](s *server, method string, needs capabilityGate, f func(context.Context, P) (R, error)) {
	s.p.Handle(method, func(ctx context.Context, raw json.RawMessage) (any, error) {
		if err := s.admit(); err != nil {
			return nil, err
		}
		if needs != nil && !needs(s.cfg.Capabilities) {
			return nil, notDeclared(method)
		}
		var params P
		if err := decodeParams(raw, PP(&params)); err != nil {
			return nil, err
		}
		res, err := f(ctx, params)
		if err != nil {
			return nil, err
		}
		if err := res.Validate(); err != nil {
			return nil, fmt.Errorf("adapterkit: the answer of the application to %s is invalid: %w", method, err)
		}
		return res, nil
	})
}

// decodeParams strictly decodes the params of a request; a request without params
// is read as an empty object.
func decodeParams(raw json.RawMessage, into adapterproto.Message) error {
	if raw == nil {
		raw = json.RawMessage(`{}`)
	}
	if err := adapterproto.Decode(raw, into); err != nil {
		return peer.NewError(peer.CodeInvalidParams, err.Error())
	}
	return nil
}

func (s *server) initialize(ctx context.Context, raw json.RawMessage) (any, error) {
	// After shutdown began only ping is served, whatever the request carries.
	s.mu.Lock()
	shutting := s.shuttingDown
	s.mu.Unlock()
	if shutting {
		return nil, peer.NewError(peer.CodeShuttingDown, "shutting down")
	}
	var p adapterproto.InitializeParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	switch {
	case s.shuttingDown:
		s.mu.Unlock()
		return nil, peer.NewError(peer.CodeShuttingDown, "shutting down")
	case s.initialized || s.initializing:
		s.mu.Unlock()
		return nil, peer.NewError(peer.CodeInvalidRequest, "already initialized")
	}
	s.initializing = true
	s.mu.Unlock()
	version, err := s.begin(ctx, p)
	if err != nil {
		s.mu.Lock()
		s.initializing = false
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Lock()
	s.initializing, s.initialized = false, true
	s.mu.Unlock()
	res := adapterproto.InitializeResult{ProtocolVersion: version, Adapter: s.cfg.Adapter, Capabilities: s.cfg.Capabilities}
	return res, nil
}

// begin does what must be done before the answer to initialize: it negotiates the
// version, takes the instance lock, and has the application initialize. The lock
// comes before the answer: an adapter that has answered controls sessions, and
// two of them must never at once. A failure leaves no lock held.
func (s *server) begin(ctx context.Context, p adapterproto.InitializeParams) (int, error) {
	version, err := adapterproto.Negotiate(p.Protocol, adapterproto.SupportedRange())
	if err != nil {
		var nc *adapterproto.NoCommonVersionError
		if errors.As(err, &nc) {
			e := adapterproto.NewUnsupportedVersionError(nc)
			return 0, (*peer.Error)(&e)
		}
		return 0, peer.NewError(peer.CodeInvalidParams, err.Error())
	}
	// Taking the lock and keeping it for the connection is one step under the
	// mutex: the handler of a connection that has just ended may still be running,
	// and a lock taken after the release would be held until the process ends.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, peer.NewError(peer.CodeShuttingDown, "the connection has ended")
	}
	release, err := lockInstance(p.Instance.LockPath)
	if err == nil {
		s.release = release
	}
	s.mu.Unlock()
	switch {
	case errors.Is(err, errLocked):
		return 0, peer.NewError(adapterproto.CodeInstanceLocked, "another process holds the instance lock")
	case err != nil:
		return 0, fmt.Errorf("adapterkit: taking the instance lock: %w", err)
	}
	if err := s.cfg.App.Initialize(ctx, s.conn, p.Instance); err != nil {
		s.dropLock()
		return 0, err
	}
	return version, nil
}

func (s *server) shutdown(ctx context.Context, _ json.RawMessage) (any, error) {
	if err := s.admit(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.shuttingDown = true
	s.mu.Unlock()
	s.cfg.App.Shutdown(ctx)
	return adapterproto.Empty{}, nil
}

// releaseLock ends the connection's claim on the instance: the lock is released,
// and no handler that is still running may take it afterwards.
func (s *server) releaseLock() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.dropLock()
}

// dropLock releases the instance lock if the connection holds it.
func (s *server) dropLock() {
	s.mu.Lock()
	rel := s.release
	s.release = nil
	s.mu.Unlock()
	if rel != nil {
		rel()
	}
}
