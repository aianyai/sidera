package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// MaxMessageBytes is the read limit of a connection. The server sends messages of
// up to 16 MiB; the library's own default of 32 KiB would end the connection on
// the first large thread read, so the limit is set to twice the server's.
const MaxMessageBytes = 32 << 20

// writeTimeout bounds one write, so that a server that stopped reading cannot
// hold the read loop, which owes it the answers of its own requests.
const writeTimeout = 10 * time.Second

// ErrClosed is returned by calls that were pending, or are made, when the
// connection has ended. Err gives the reason.
var ErrClosed = errors.New("appserver: connection closed")

// RPCError is an error answer of the server. The server's frames are JSON-RPC
// without the "jsonrpc" member, so the type is the client's own.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("app server error %d: %s", e.Code, e.Message) }

// JSON-RPC error codes the client answers the server with.
const (
	CodeMethodNotFound = -32601
	CodeInternalError  = -32603
)

// Notification is a message of the server that wants no answer.
type Notification struct {
	Method string
	Params json.RawMessage
}

// Handlers is what the client calls for what the server sends.
type Handlers struct {
	// ServerRequest answers a request of the server. It runs on the read loop,
	// which must not wait: it returns at once, from a pure decision. A nil handler
	// answers every request method-not-found.
	ServerRequest func(method string, params json.RawMessage) (result any, rpcErr *RPCError)
	// Notification receives the notifications one at a time, in the order they
	// arrived, on a goroutine of its own, so that it may call the server.
	Notification func(Notification)
}

// Endpoint says where an App Server listens.
type Endpoint struct {
	// Network is "unix" or "tcp".
	Network string
	// Address is the socket path, or host:port.
	Address string
	// Token is the capability token a tcp listener asks for; a unix socket needs
	// none, its permissions are the check.
	Token string
}

// Client is one connection to an App Server. It is safe for concurrent use.
type Client struct {
	ws       *websocket.Conn
	handlers Handlers

	nextID atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan response
	err     error // why the connection ended; set once, under mu

	events *eventQueue
	done   chan struct{} // closed when the connection ended and every notification was handled
}

type response struct {
	result json.RawMessage
	err    *RPCError
}

// frame is any message of the server: a request has a method and an id, a
// notification a method only, a response an id only. Unknown members are
// ignored: the protocol is external and grows.
type frame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

// Dial connects to the server and starts reading. The connection ends when the
// server closes it or Close is called; Done says when.
func Dial(ctx context.Context, ep Endpoint, h Handlers) (*Client, error) {
	ws, err := dialWebSocket(ctx, ep)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(MaxMessageBytes)
	c := &Client{
		ws:       ws,
		handlers: h,
		pending:  make(map[int64]chan response),
		events:   newEventQueue(),
		done:     make(chan struct{}),
	}
	go c.readLoop()
	go c.dispatch()
	return c, nil
}

// Probe reports whether something answers the WebSocket handshake at ep, which
// is how a server that is up differs from a leftover socket.
func Probe(ctx context.Context, ep Endpoint) error {
	ws, err := dialWebSocket(ctx, ep)
	if err != nil {
		return err
	}
	return ws.CloseNow()
}

func dialWebSocket(ctx context.Context, ep Endpoint) (*websocket.Conn, error) {
	opts := &websocket.DialOptions{}
	url := "ws://" + ep.Address
	switch ep.Network {
	case "unix":
		url = "ws://localhost/"
		opts.HTTPClient = &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", ep.Address)
			},
		}}
	case "tcp":
		if ep.Token != "" {
			opts.HTTPHeader = http.Header{"Authorization": {"Bearer " + ep.Token}}
		}
	default:
		return nil, fmt.Errorf("appserver: unknown network %q", ep.Network)
	}
	ws, _, err := websocket.Dial(ctx, url, opts)
	return ws, err
}

// Done is closed once the connection has ended and every notification that
// arrived before it was handed to the handler.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err says why the connection ended, or nil while it is open.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the connection.
func (c *Client) Close() error {
	c.fail(errors.New("appserver: closed by the client"))
	return nil
}

// Call sends a request and waits for its answer, decoding the result into
// result when it is not nil. A server's error answer is an *RPCError.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	id := c.nextID.Add(1)
	ch := make(chan response, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return ErrClosed
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if err := c.write(ctx, msg); err != nil {
		return err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return ErrClosed // the connection ended before the answer
		}
		if r.err != nil {
			return r.err
		}
		if result != nil && len(r.result) > 0 {
			if err := json.Unmarshal(r.result, result); err != nil {
				return fmt.Errorf("appserver: %s result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Notify sends a notification.
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	msg := map[string]any{"method": method}
	if params != nil {
		msg["params"] = params
	}
	return c.write(ctx, msg)
}

func (c *Client) write(ctx context.Context, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, data); err != nil {
		c.fail(err)
		return ErrClosed
	}
	return nil
}

// fail ends the connection for the first reason given.
func (c *Client) fail(err error) {
	c.mu.Lock()
	first := c.err == nil
	if first {
		c.err = err
	}
	c.mu.Unlock()
	if first {
		_ = c.ws.CloseNow()
	}
}

func (c *Client) readLoop() {
	defer func() {
		c.mu.Lock()
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
		c.events.close()
	}()
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			c.fail(err)
			return
		}
		var f frame
		if err := json.Unmarshal(data, &f); err != nil {
			c.fail(fmt.Errorf("appserver: unreadable frame: %w", err))
			return
		}
		switch {
		case f.Method != "" && len(f.ID) > 0:
			c.answerRequest(f)
		case f.Method != "":
			c.events.push(Notification{Method: f.Method, Params: f.Params})
		case len(f.ID) > 0:
			c.deliver(f)
		}
	}
}

func (c *Client) deliver(f frame) {
	var id int64
	if err := json.Unmarshal(f.ID, &id); err != nil {
		return // an answer to a request that is not ours
	}
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- response{result: f.Result, err: f.Error}
	}
}

func (c *Client) answerRequest(f frame) {
	reply := map[string]any{"id": f.ID}
	var result any
	var rpcErr *RPCError
	if c.handlers.ServerRequest == nil {
		rpcErr = &RPCError{Code: CodeMethodNotFound, Message: "method not found"}
	} else {
		result, rpcErr = c.handlers.ServerRequest(f.Method, f.Params)
	}
	if rpcErr != nil {
		reply["error"] = rpcErr
	} else {
		if result == nil {
			result = struct{}{}
		}
		reply["result"] = result
	}
	data, err := json.Marshal(reply)
	if err != nil {
		data, _ = json.Marshal(map[string]any{"id": f.ID, "error": &RPCError{Code: CodeInternalError, Message: "reply not encodable"}})
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, data); err != nil {
		c.fail(err)
	}
}

// dispatch hands the notifications to the handler in order and closes done after
// the last one.
func (c *Client) dispatch() {
	defer close(c.done)
	for {
		n, ok := c.events.pop()
		if !ok {
			return
		}
		if c.handlers.Notification != nil {
			c.handlers.Notification(n)
		}
	}
}

// eventQueue is an unbounded first-in first-out queue: the read loop must never
// wait for the handler (it owes the server answers), and a notification that is
// dropped could leave a turn unreported.
type eventQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []Notification
	closed bool
}

func newEventQueue() *eventQueue {
	q := &eventQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *eventQueue) push(n Notification) {
	q.mu.Lock()
	q.items = append(q.items, n)
	q.mu.Unlock()
	q.cond.Signal()
}

func (q *eventQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// pop waits for the next notification; it returns false once the queue is closed
// and empty.
func (q *eventQueue) pop() (Notification, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.items) == 0 {
		return Notification{}, false
	}
	n := q.items[0]
	q.items[0] = Notification{}
	q.items = q.items[1:]
	return n, true
}
