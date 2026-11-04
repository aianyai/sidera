package console

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// requestTimeout bounds one local request, the longest wait of endpoint.recv
// (adapterproto.MaxWaitSeconds) included.
const requestTimeout = (adapterproto.MaxWaitSeconds + 30) * time.Second

// The pause between attempts to register the owner endpoint grows from the first
// to the second.
const (
	registerRetryMin = 200 * time.Millisecond
	registerRetryMax = 5 * time.Second
)

// serve runs the adapter role over the streams of the daemon until it closes them.
func serve(ctx context.Context, r io.Reader, w io.WriteCloser, log *slog.Logger) error {
	app := newAdapter(log)
	defer app.close()
	return adapterkit.Serve(ctx, r, w, adapterkit.Config{
		Adapter:      adapterproto.AdapterInfo{ID: AdapterID, Version: release.Version()},
		Capabilities: adapterproto.Capabilities{Direct: &adapterproto.DirectCapabilities{MaxEndpoints: maxEndpoints}},
		App:          app,
		Logger:       log,
	})
}

// adapter is the adapter role: it owns the owner endpoint and turns each local
// request into the endpoint.* call it names. It keeps no message and no state a
// restart would lose, except the senders it has seen (see route).
type adapter struct {
	adapterkit.Base
	log *slog.Logger
	// ctx ends when the adapter stops; every call to the daemon and every local
	// request is bound to it.
	ctx    context.Context
	cancel context.CancelFunc

	conn     *adapterkit.Conn
	stateDir string

	mu       sync.Mutex
	node     string
	endpoint adapterproto.EndpointRef
	listener net.Listener
	// senders maps a message the endpoint received to the address that sent it, so
	// that a reply names the one recipient the daemon accepts. Only this process
	// knows it: after a restart the person gives the address with --to.
	senders map[adapterproto.MessageID]adapterproto.Address
}

func newAdapter(log *slog.Logger) *adapter {
	ctx, cancel := context.WithCancel(context.Background())
	return &adapter{log: log, ctx: ctx, cancel: cancel, senders: map[adapterproto.MessageID]adapterproto.Address{}}
}

// Initialize keeps what the adapter needs to talk to the daemon and the directory
// it keeps its socket in.
func (a *adapter) Initialize(_ context.Context, conn *adapterkit.Conn, instance adapterproto.InstanceInfo) error {
	a.conn, a.stateDir = conn, instance.StateDir
	return nil
}

// Start opens the control channel and begins registering the owner endpoint, and
// answers without waiting for the registration: the daemon serves endpoint.*
// requests only once it is running, and it is not running while it waits for this
// answer, so waiting here would stall every restart of the daemon until the
// handshake times out. Until the registration completes, local requests are
// refused as starting.
func (a *adapter) Start(ctx context.Context, p adapterproto.StartParams) (adapterproto.StartResult, error) {
	a.mu.Lock()
	a.node = p.Node
	listening := a.listener != nil
	a.mu.Unlock()
	if !listening {
		l, err := listenLocal(a.stateDir)
		if err != nil {
			return adapterproto.StartResult{}, err
		}
		a.mu.Lock()
		a.listener = l
		a.mu.Unlock()
		go a.accept(l)
		go a.registerOwner()
	}
	return a.Base.Start(ctx, p)
}

// registerOwner registers the owner endpoint, retrying with a growing pause until
// it succeeds or the adapter stops. The endpoint is the same one after every
// restart, and the daemon keeps what it received while the adapter was down. The
// daemon holds the request until it is running, so a long wait is not a failure.
func (a *adapter) registerOwner() {
	pause := registerRetryMin
	for {
		reg, err := a.conn.Register(a.ctx, adapterproto.RegisterParams{
			Key:         endpointKey,
			DisplayName: "Owner console",
			Description: "The node owner's console: messages for the owner wait here until sidera-adapter-console recv shows them.",
		})
		if err == nil {
			a.mu.Lock()
			a.endpoint = reg.Endpoint
			a.mu.Unlock()
			return
		}
		if a.ctx.Err() != nil {
			return
		}
		a.log.Warn("registering the owner endpoint", "error", err, "retry_in", pause)
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(pause):
		}
		pause = min(2*pause, registerRetryMax)
	}
}

// Shutdown closes the control channel; the process ends when the daemon closes the
// stream.
func (a *adapter) Shutdown(context.Context, adapterproto.ShutdownParams) error {
	a.close()
	return nil
}

func (a *adapter) close() {
	a.cancel()
	a.mu.Lock()
	l := a.listener
	a.listener = nil
	a.mu.Unlock()
	if l != nil {
		_ = l.Close()
	}
}

func (a *adapter) accept(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			if a.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			a.log.Error("accepting a local connection", "error", err)
			select {
			case <-a.ctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		go a.handle(c)
	}
}

// handle serves one connection: one request line in, one response line out.
func (a *adapter) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(requestTimeout))
	var resp response
	req, err := readRequest(c)
	if err != nil {
		resp = fail(err)
	} else {
		ctx, cancel := context.WithTimeout(a.ctx, requestTimeout)
		resp = a.dispatch(ctx, req)
		cancel()
	}
	line, err := json.Marshal(resp)
	if err != nil {
		line, _ = json.Marshal(fail(err))
	}
	if _, err := c.Write(append(line, '\n')); err != nil {
		a.log.Warn("answering a local request", "error", err)
	}
}

func readRequest(r io.Reader) (request, error) {
	line, err := bufio.NewReader(io.LimitReader(r, maxLine+1)).ReadBytes('\n')
	if err != nil {
		return request{}, fmt.Errorf("reading the request line: %w", err)
	}
	if len(line) > maxLine {
		return request{}, errors.New("the request line is too long")
	}
	var req request
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return request{}, fmt.Errorf("decoding the request: %w", err)
	}
	return req, nil
}

func fail(err error) response {
	var pe *peer.Error
	if errors.As(err, &pe) {
		return response{Error: &failure{Code: pe.Code, Message: pe.Message}}
	}
	return response{Error: &failure{Message: err.Error()}}
}

// dispatch makes the call a request names, as the owner endpoint.
func (a *adapter) dispatch(ctx context.Context, req request) response {
	a.mu.Lock()
	node, endpoint := a.node, a.endpoint
	a.mu.Unlock()
	if endpoint == "" {
		return fail(errors.New("the adapter is starting and the owner endpoint is not registered yet: try again in a moment"))
	}
	resp := response{Node: node}
	switch req.Op {
	case opWhoami:
		resp.Endpoint = endpoint
	case opSend:
		if req.Send == nil {
			return fail(errors.New("send needs its parameters"))
		}
		p := *req.Send
		p.Endpoint = endpoint
		if p.ReplyTo != "" && len(p.To) == 0 {
			from, ok := a.sender(p.ReplyTo)
			if !ok {
				return fail(fmt.Errorf("this process has not seen who sent message %s (it may be from before a restart): give the sender with --to", p.ReplyTo))
			}
			p.To = []adapterproto.Address{from}
		}
		res, err := a.conn.Send(ctx, p)
		if err != nil {
			return fail(err)
		}
		resp.Send = &res
	case opStatus:
		if req.Status == nil {
			return fail(errors.New("status needs its parameters"))
		}
		p := *req.Status
		p.Endpoint = endpoint
		res, err := a.conn.MessageStatus(ctx, p)
		if err != nil {
			return fail(err)
		}
		resp.Status = &res
	case opRecv:
		if req.Recv == nil {
			return fail(errors.New("recv needs its parameters"))
		}
		p := *req.Recv
		p.Endpoint = endpoint
		res, err := a.conn.Recv(ctx, p)
		if err != nil {
			return fail(err)
		}
		a.remember(res.Page.Messages)
		resp.Recv = &res
	case opAck:
		if req.Ack == nil {
			return fail(errors.New("ack needs its parameters"))
		}
		p := *req.Ack
		p.Endpoint = endpoint
		res, err := a.conn.Ack(ctx, p)
		if err != nil {
			return fail(err)
		}
		resp.Ack = &res
	default:
		return fail(fmt.Errorf("unknown operation %q", req.Op))
	}
	return resp
}

func (a *adapter) remember(msgs []adapterproto.PageMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range msgs {
		a.senders[m.ID] = m.From
	}
}

func (a *adapter) sender(id adapterproto.MessageID) (adapterproto.Address, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	from, ok := a.senders[id]
	return from, ok
}
