// Package agentapi serves an endpoint's Agent interface over the node's local
// socket. The token identifies the endpoint in the engine command that acts for it.
package agentapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/localhttp"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

const pathPrefix = "/v1/agent/"

// Config gives the Agent interface its engine and diagnostic log.
type Config struct {
	Service engine.AgentService
	Log     *slog.Logger
}

// Server starts in probe-only mode so an adapter can verify its tools while the
// engine recovers. EnableMethods opens endpoint methods after recovery.
type Server struct {
	svc     engine.AgentService
	http    *localhttp.Server
	enabled atomic.Bool
}

// EnableMethods opens the full interface after the engine has finished recovery.
// The transition is one-way and safe to repeat.
func (s *Server) EnableMethods() { s.enabled.Store(true) }

// New makes a server without opening a listener.
func New(c Config) (*Server, error) {
	if c.Service == nil {
		return nil, errors.New("agentapi: no service")
	}
	s := &Server{svc: c.Service}
	var err error
	s.http, err = localhttp.New(localhttp.Config{Kind: ipc.KindAgent, PathPrefix: pathPrefix, Lookup: s.lookup, Log: c.Log})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Listener is an Agent listener that Listen checked and opened through ipc.
type Listener struct{ l *localhttp.Listener }

// Close closes a listener that no server took.
func (l *Listener) Close() error { return l.l.Close() }

// Listen opens only an Agent address. The caller holds the node's instance lock.
func Listen(addr ipc.Address, lock ipc.InstanceLock, opts ipc.Options) (*Listener, error) {
	l, err := localhttp.Listen(ipc.KindAgent, addr, lock, opts)
	if err != nil {
		return nil, err
	}
	return &Listener{l}, nil
}

// Serve owns the listener until Shutdown or Close.
func (s *Server) Serve(l *Listener) error {
	if l == nil {
		return s.http.Serve(nil)
	}
	return s.http.Serve(l.l)
}

// Shutdown stops accepting requests and waits within ctx, closing what remains.
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

// Close ends serving immediately.
func (s *Server) Close() error { return s.http.Close() }

func knownMethod(name string) bool {
	switch name {
	case "me", "declare", "peers", "directory", "send", "reply", "recv", "ack", "status", "control", "probe":
		return true
	}
	return false
}

// ServeHTTP routes one request through the shared local HTTP boundary.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.http.ServeHTTP(w, r) }

func (s *Server) lookup(name string) http.HandlerFunc {
	if !knownMethod(name) {
		return nil
	}
	return func(w http.ResponseWriter, r *http.Request) { s.serve(w, r, name) }
}

// serve keeps recovery and credential checks ahead of reading the body. The
// engine authenticates the endpoint in the same command that acts for it.
func (s *Server) serve(w http.ResponseWriter, r *http.Request, name string) {
	if name != "probe" && !s.enabled.Load() {
		s.http.Fail(w, apiwire.New(apiwire.CategoryUnavailable, "probe_only", "the node is recovering and accepts only tool probes"))
		return
	}
	token, err := bearerToken(r.Header)
	if err != nil {
		s.http.Fail(w, err)
		return
	}
	body, err := localhttp.ReadBody(w, r)
	if err != nil {
		s.http.Fail(w, err)
		return
	}
	if name == "probe" {
		var in struct {
			Frontend adapterproto.Frontend `json:"frontend"`
		}
		if err := strictjson.Decode(body, &in); err != nil {
			s.http.Fail(w, err)
			return
		}
		if !in.Frontend.Valid() {
			s.http.Fail(w, apiwire.Malformed("the front end is not cli or mcp", "frontend"))
			return
		}
		target, err := s.svc.PresentProbeToken(r.Context(), token, in.Frontend)
		if err != nil {
			s.http.Fail(w, err)
			return
		}
		s.http.JSON(w, 200, ProbeResult{OK: true, Preset: target.Preset, Frontend: target.Frontend})
		return
	}
	out, err := s.method(r.Context(), engine.ByToken(token), name, body)
	if err != nil {
		s.http.Fail(w, err)
		return
	}
	if name == "recv" {
		s.http.RawJSON(w, http.StatusOK, out.([]byte))
		return
	}
	s.http.JSON(w, http.StatusOK, out)
}

func bearerToken(h http.Header) (adapterproto.Token, error) {
	values := h.Values("Authorization")
	if len(values) == 0 || values[0] == "" {
		return "", apiwire.New(apiwire.CategoryUnauthenticated, "token_missing", "an endpoint or probe token is required")
	}
	text, ok := strings.CutPrefix(values[0], "Bearer ")
	t := adapterproto.Token(text)
	if len(values) != 1 || !ok || t.Validate() != nil {
		return "", apiwire.New(apiwire.CategoryUnauthenticated, "token_malformed", "the token is not in its required form")
	}
	return t, nil
}
