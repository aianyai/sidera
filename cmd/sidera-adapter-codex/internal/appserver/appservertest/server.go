// Package appservertest is a fake App Server for tests: a WebSocket server that
// answers the methods a test registers, sends the notifications and requests a
// test asks for and can drop its connections, so that the client and the adapter
// are tested against the wire without a Codex installation.
package appservertest

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
)

// HandlerFunc answers one request. The result is encoded as the answer, an error
// is sent as an error answer.
type HandlerFunc func(params json.RawMessage) (any, *appserver.RPCError)

// Call is a request the server received.
type Call struct {
	Method string
	Params json.RawMessage
}

// Server is a fake App Server.
type Server struct {
	// Endpoint is where it listens.
	Endpoint appserver.Endpoint

	tb       testing.TB
	srv      *http.Server
	ln       net.Listener
	dir      string
	token    string
	mu       sync.Mutex
	handlers map[string]HandlerFunc
	conns    []*websocket.Conn
	calls    []Call
	pending  map[int64]chan json.RawMessage
	nextID   int64
}

// NewUnix starts a server on a Unix socket in a short temporary directory (a
// socket path is limited to about a hundred bytes).
func NewUnix(tb testing.TB) *Server {
	tb.Helper()
	dir, err := os.MkdirTemp("", "as")
	if err != nil {
		tb.Fatal(err)
	}
	path := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		tb.Fatal(err)
	}
	s := newServer(tb, ln, dir)
	s.Endpoint = appserver.Endpoint{Network: "unix", Address: path}
	return s
}

// NewTCP starts a server on a loopback port that asks for the bearer token.
func NewTCP(tb testing.TB, token string) *Server {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	s := newServer(tb, ln, "")
	s.token = token
	s.Endpoint = appserver.Endpoint{Network: "tcp", Address: ln.Addr().String(), Token: token}
	return s
}

func newServer(tb testing.TB, ln net.Listener, dir string) *Server {
	s := &Server{tb: tb, ln: ln, dir: dir, handlers: map[string]HandlerFunc{}, pending: map[int64]chan json.RawMessage{}}
	s.srv = &http.Server{Handler: http.HandlerFunc(s.serve)}
	go func() { _ = s.srv.Serve(ln) }()
	tb.Cleanup(s.Close)
	return s
}

// Close stops the server and removes its socket directory.
func (s *Server) Close() {
	s.DropConnections()
	_ = s.srv.Close()
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}

// Handle registers the answer to a method.
func (s *Server) Handle(method string, f HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = f
}

// Calls returns the requests received so far, in order.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// Notify sends a notification to every connection.
func (s *Server) Notify(method string, params any) {
	s.send(map[string]any{"method": method, "params": params})
}

// Request sends a request of the server to the client on the newest connection
// and returns the result it answers.
func (s *Server) Request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan json.RawMessage, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	s.send(map[string]any{"id": id, "method": method, "params": params})
	select {
	case raw := <-ch:
		return raw, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// DropConnections closes every connection without a close handshake, as a server
// that died would.
func (s *Server) DropConnections() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.CloseNow()
	}
}

func (s *Server) send(msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		s.tb.Error(err)
		return
	}
	s.mu.Lock()
	conns := append([]*websocket.Conn(nil), s.conns...)
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, c := range conns {
		_ = c.Write(ctx, websocket.MessageText, data)
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if s.token != "" && r.Header.Get("Authorization") != "Bearer "+s.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(appserver.MaxMessageBytes)
	s.mu.Lock()
	s.conns = append(s.conns, c)
	s.mu.Unlock()
	for {
		_, data, err := c.Read(context.Background())
		if err != nil {
			return
		}
		var f struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(data, &f); err != nil {
			return
		}
		switch {
		case f.Method == "":
			s.answered(f.ID, f.Result)
		case len(f.ID) == 0:
			s.record(f.Method, f.Params)
		default:
			s.record(f.Method, f.Params)
			s.mu.Lock()
			h := s.handlers[f.Method]
			s.mu.Unlock()
			reply := map[string]any{"id": f.ID}
			if h == nil {
				reply["error"] = appserver.RPCError{Code: appserver.CodeMethodNotFound, Message: "no handler for " + f.Method}
			} else if result, rpcErr := h(f.Params); rpcErr != nil {
				reply["error"] = rpcErr
			} else {
				reply["result"] = result
			}
			out, _ := json.Marshal(reply)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := c.Write(ctx, websocket.MessageText, out)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (s *Server) record(method string, params json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, Call{Method: method, Params: params})
}

func (s *Server) answered(rawID, result json.RawMessage) {
	var id int64
	if json.Unmarshal(rawID, &id) != nil {
		return
	}
	s.mu.Lock()
	ch := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()
	if ch != nil {
		ch <- result
	}
}
