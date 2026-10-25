package fakectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/aianyai/sidera/sdk/go/peer"
)

// Handler answers a request that a connected process made of the server. It is
// told which connection asked.
type Handler func(ctx context.Context, from *Conn, params json.RawMessage) (any, error)

// Server is the fixture's end of the control channel: it accepts connections of
// fake processes on the loopback interface and gives the fixture a Conn for each.
type Server struct {
	ln      net.Listener
	stop    func() bool // stops the context hook
	wg      sync.WaitGroup
	closing chan struct{}

	mu       sync.Mutex
	handlers map[string]Handler
	conns    []*Conn
	changed  chan struct{} // closed and replaced whenever a connection says hello
}

// Listen starts a server on 127.0.0.1 and a free port. It stops when ctx ends
// or when Close is called.
func Listen(ctx context.Context) (*Server, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{
		ln: ln, closing: make(chan struct{}),
		handlers: map[string]Handler{}, changed: make(chan struct{}),
	}
	s.stop = context.AfterFunc(ctx, func() { _ = s.Close() })
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

// Addr is the address a fake process dials, "127.0.0.1:port".
func (s *Server) Addr() string { return s.ln.Addr().String() }

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		c, err := s.newConn(nc)
		if err != nil {
			_ = nc.Close()
			continue
		}
		// The peer is started before the server can close it, so that Close never
		// races with Start.
		c.p.Start()
		s.mu.Lock()
		select {
		case <-s.closing:
			s.mu.Unlock()
			_ = c.p.Close()
			return
		default:
		}
		s.conns = append(s.conns, c)
		s.mu.Unlock()
		// The hello may have been answered before the connection was listed,
		// and nobody waiting for it would have seen it.
		s.announce()
	}
}

// Handle registers the server's answer to a request method that connected
// processes may make. Registering the same method twice replaces the handler.
// The method is refused with peer.CodeNotInitialized on a connection that has not
// said hello.
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	s.handlers[method] = h
	conns := append([]*Conn(nil), s.conns...)
	s.mu.Unlock()
	for _, c := range conns {
		c.install(method, h)
	}
}

// Wait blocks until a process with the given id has said hello and its
// connection is alive, and returns the most recent such connection.
func (s *Server) Wait(ctx context.Context, id string) (*Conn, error) {
	for {
		s.mu.Lock()
		var found *Conn
		for _, c := range s.conns {
			if h, ok := c.helloIfDone(); ok && h.ID == id && !c.isDone() {
				found = c
			}
		}
		changed := s.changed
		s.mu.Unlock()
		if found != nil {
			return found, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.closing:
			return nil, errors.New("fakectl: the server is closed")
		}
	}
}

// Conns returns the live connections that have said hello.
func (s *Server) Conns() []*Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Conn
	for _, c := range s.conns {
		if _, ok := c.helloIfDone(); ok && !c.isDone() {
			out = append(out, c)
		}
	}
	return out
}

// Broadcast sends a notification to every live connection of a role. It returns
// the errors of the connections it could not reach, joined.
func (s *Server) Broadcast(ctx context.Context, role, method string, params any) error {
	var errs []error
	for _, c := range s.Conns() {
		if c.Hello().Role == role {
			if err := c.Notify(ctx, method, params); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", c.Hello().ID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// Close stops accepting, ends every connection and waits for the server's
// goroutines. It is safe to call more than once.
func (s *Server) Close() error {
	s.mu.Lock()
	select {
	case <-s.closing:
		s.mu.Unlock()
		return nil
	default:
		close(s.closing)
	}
	conns := append([]*Conn(nil), s.conns...)
	s.mu.Unlock()
	s.stop()
	err := s.ln.Close()
	for _, c := range conns {
		_ = c.p.Close()
	}
	s.wg.Wait()
	return err
}

func (s *Server) announce() {
	s.mu.Lock()
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}

// Conn is the server's end of one connection.
type Conn struct {
	srv *Server
	p   *peer.Peer

	mu      sync.Mutex
	hello   HelloParams
	said    bool
	reports []ReportParams
	arrived chan struct{} // closed and replaced when a report arrives
}

func (s *Server) newConn(nc net.Conn) (*Conn, error) {
	p, err := peer.New(nc, nc, peer.Config{})
	if err != nil {
		return nil, err
	}
	c := &Conn{srv: s, p: p, arrived: make(chan struct{})}
	p.Handle(MethodHello, c.handleHello)
	p.HandleNotification(MethodReport, c.handleReport)
	s.mu.Lock()
	handlers := make(map[string]Handler, len(s.handlers))
	for m, h := range s.handlers {
		handlers[m] = h
	}
	s.mu.Unlock()
	for m, h := range handlers {
		c.install(m, h)
	}
	return c, nil
}

func (c *Conn) install(method string, h Handler) {
	c.p.Handle(method, func(ctx context.Context, params json.RawMessage) (any, error) {
		if _, ok := c.helloIfDone(); !ok {
			return nil, peer.NewError(peer.CodeNotInitialized, "say hello first")
		}
		return h(ctx, c, params)
	})
}

func (c *Conn) handleHello(_ context.Context, params json.RawMessage) (any, error) {
	v, err := DecodeParams(MethodHello, params)
	if err != nil {
		return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
	}
	c.mu.Lock()
	if c.said {
		c.mu.Unlock()
		return nil, peer.NewError(peer.CodeInvalidRequest, "hello was already said")
	}
	c.hello, c.said = v.(HelloParams), true
	c.mu.Unlock()
	c.srv.announce()
	return struct{}{}, nil
}

func (c *Conn) handleReport(_ context.Context, params json.RawMessage) {
	if _, ok := c.helloIfDone(); !ok {
		return
	}
	v, err := DecodeParams(MethodReport, params)
	if err != nil {
		return
	}
	c.mu.Lock()
	c.reports = append(c.reports, v.(ReportParams))
	close(c.arrived)
	c.arrived = make(chan struct{})
	c.mu.Unlock()
}

func (c *Conn) helloIfDone() (HelloParams, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hello, c.said
}

func (c *Conn) isDone() bool {
	select {
	case <-c.p.Done():
		return true
	default:
		return false
	}
}

// Hello returns who the connected process said it is.
func (c *Conn) Hello() HelloParams {
	h, _ := c.helloIfDone()
	return h
}

// Done is closed when the connection has ended.
func (c *Conn) Done() <-chan struct{} { return c.p.Done() }

// SetClock sets the process's test clock and returns once it has done so.
func (c *Conn) SetClock(ctx context.Context, ms int64) error {
	p := ClockSetParams{ToMS: ms}
	if err := p.validate(); err != nil {
		return err
	}
	return c.p.Call(ctx, MethodClockSet, p, nil)
}

// SetStep announces the script step to the process and returns once it knows it.
func (c *Conn) SetStep(ctx context.Context, step string) error {
	p := StepSetParams{Step: step}
	if err := p.validate(); err != nil {
		return err
	}
	return c.p.Call(ctx, MethodStepSet, p, nil)
}

// Knob turns the named knob of the process. args is marshalled to a JSON object
// (nil for none); the object the knob answers with is decoded into result, which
// may be nil.
func (c *Conn) Knob(ctx context.Context, name string, args, result any) error {
	p := KnobParams{Name: name}
	if args != nil {
		raw, err := json.Marshal(args)
		if err != nil {
			return err
		}
		p.Args = raw
	}
	if err := p.validate(); err != nil {
		return err
	}
	return c.p.Call(ctx, MethodKnob, p, result)
}

// Notify sends a notification of the caller's own method to the process.
func (c *Conn) Notify(ctx context.Context, method string, params any) error {
	return c.p.Notify(ctx, method, params)
}

// Call sends a request of the caller's own method to the process.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	return c.p.Call(ctx, method, params, result)
}

// NextReport returns the oldest report of the process that has not been read,
// waiting for one if there is none.
func (c *Conn) NextReport(ctx context.Context) (ReportParams, error) {
	for {
		c.mu.Lock()
		if len(c.reports) > 0 {
			r := c.reports[0]
			c.reports = c.reports[1:]
			c.mu.Unlock()
			return r, nil
		}
		arrived := c.arrived
		c.mu.Unlock()
		select {
		case <-arrived:
		case <-ctx.Done():
			return ReportParams{}, ctx.Err()
		case <-c.p.Done():
			return ReportParams{}, errors.New("fakectl: the connection ended")
		}
	}
}
