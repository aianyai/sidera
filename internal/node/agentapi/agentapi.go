// Package agentapi serves an endpoint's Agent interface over the node's local
// socket. The token identifies the endpoint in the engine command that acts for it.
package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

const pathPrefix = "/v1/agent/"
const maxBody = 1 << 20

// Config gives the probe interface its engine and diagnostic log.
type Config struct {
	Service engine.AgentService
	Log     *slog.Logger
}

// Server serves the Agent interface in probe-only mode, so an adapter can verify
// its tools while the engine is recovering. Other Agent methods are unavailable.
type Server struct {
	svc  engine.AgentService
	log  *slog.Logger
	http *http.Server
}

// New makes a server without opening a listener.
func New(c Config) (*Server, error) {
	if c.Service == nil {
		return nil, errors.New("agentapi: no service")
	}
	s := &Server{svc: c.Service, log: c.Log}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.http = &http.Server{
		Handler: s, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8 << 10,
		// A recv may wait longer than an ordinary response, so it has no global
		// write timeout. Its context ends when its client disconnects.
	}
	return s, nil
}

// Listener is an Agent listener that Listen checked and opened through ipc.
type Listener struct{ l net.Listener }

// Close closes a listener that no server took.
func (l *Listener) Close() error { return l.l.Close() }

// Listen opens only an Agent address. The caller holds the node's instance lock.
func Listen(addr ipc.Address, lock ipc.InstanceLock, opts ipc.Options) (*Listener, error) {
	if addr.Kind() != ipc.KindAgent {
		return nil, fmt.Errorf("agentapi: %q is not an Agent address", addr.Kind())
	}
	l, err := ipc.Listen(addr, lock, opts)
	if err != nil {
		return nil, err
	}
	return &Listener{l}, nil
}

// Serve owns the listener until Shutdown or Close.
func (s *Server) Serve(l *Listener) error {
	if l == nil || l.l == nil {
		return errors.New("agentapi: no listener")
	}
	if err := s.http.Serve(l.l); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops accepting requests and waits within ctx, closing what remains.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.http.Shutdown(ctx)
	if err != nil {
		s.http.Close()
	}
	return err
}

// Close ends serving immediately.
func (s *Server) Close() error { return s.http.Close() }

func knownMethod(name string) bool {
	switch name {
	case "me", "declare", "peers", "directory", "send", "reply", "recv", "ack", "status", "control", "probe":
		return true
	}
	return false
}

// ServeHTTP reads one request and leaves the endpoint's authentication and action
// in one engine command. No value received from the client is written to the log.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutPrefix(r.URL.Path, pathPrefix)
	if !ok || !knownMethod(name) {
		s.fail(w, apiwire.New(apiwire.CategoryNotFound, apiwire.CodeMethodUnknown, "there is no such method"), 0)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		s.fail(w, apiwire.New(apiwire.CategoryInvalidArgument, apiwire.CodeMethodNotAllowed, "a method is called with POST"), http.StatusMethodNotAllowed)
		return
	}
	if name != "probe" {
		s.fail(w, apiwire.New(apiwire.CategoryUnavailable, "probe_only", "the node is recovering and accepts only tool probes"), 0)
		return
	}
	token, err := bearerToken(r.Header)
	if err != nil {
		s.fail(w, err, 0)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			err = apiwire.New(apiwire.CategoryInvalidArgument, apiwire.CodeRequestTooLarge, "the request is larger than a method takes")
		} else {
			err = apiwire.Malformed("the request could not be read", "")
		}
		s.fail(w, err, 0)
		return
	}
	if name == "probe" {
		var in struct {
			Frontend adapterproto.Frontend `json:"frontend"`
		}
		if err := strictjson.Decode(body, &in); err != nil {
			s.fail(w, err, 0)
			return
		}
		if !in.Frontend.Valid() {
			s.fail(w, apiwire.Malformed("the front end is not cli or mcp", "frontend"), 0)
			return
		}
		target, err := s.svc.PresentProbeToken(r.Context(), token, in.Frontend)
		if err != nil {
			s.fail(w, err, 0)
			return
		}
		s.send(w, 200, struct {
			OK       bool                  `json:"ok"`
			Preset   string                `json:"preset"`
			Frontend adapterproto.Frontend `json:"frontend"`
		}{true, target.Preset, target.Frontend})
		return
	}
	s.fail(w, apiwire.New(apiwire.CategoryNotFound, apiwire.CodeMethodUnknown, "there is no such method"), 0)
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

func (s *Server) send(w http.ResponseWriter, status int, v any) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		s.fail(w, err, 0)
		return
	}
	s.write(w, status, out.Bytes())
}

func (s *Server) write(w http.ResponseWriter, status int, out []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(status)
	if _, err := w.Write(out); err != nil {
		s.log.Debug("an Agent answer was not delivered")
	}
}

func (s *Server) fail(w http.ResponseWriter, err error, status int) {
	e := apiwire.FromError(err)
	if e.Category == apiwire.CategoryInternal {
		s.log.Error("an Agent request failed", "code", e.Code)
	}
	if status == 0 {
		status = e.Category.HTTPStatus()
	}
	s.send(w, status, apiwire.Body{Error: *e})
}
