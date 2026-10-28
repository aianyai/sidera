// Package localhttp serves the shared HTTP boundary of the node's local
// interfaces. Interface-specific authorization and decoding stay in the handler.
package localhttp

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
	"github.com/aianyai/sidera/internal/node/ipc"
)

const maxBody = 1 << 20

// Config binds a server to one interface and its exact method lookup.
type Config struct {
	Kind       ipc.Kind
	PathPrefix string
	// Lookup returns nil for an unknown method. It does not read the request.
	Lookup func(string) http.HandlerFunc
	// Log receives internal causes and net/http diagnostics; nil discards them.
	Log *slog.Logger
}

// Server owns the HTTP transport and responses, not the interface's authority.
type Server struct {
	cfg  Config
	log  *slog.Logger
	http *http.Server
}

// New constructs a server without opening a listener.
func New(c Config) (*Server, error) {
	switch {
	case c.Kind != ipc.KindAgent && c.Kind != ipc.KindManage:
		return nil, fmt.Errorf("localhttp: unknown interface kind %q", c.Kind)
	case !strings.HasPrefix(c.PathPrefix, "/") || !strings.HasSuffix(c.PathPrefix, "/"):
		return nil, errors.New("localhttp: method path prefix must begin and end with a slash")
	case c.Lookup == nil:
		return nil, errors.New("localhttp: no method lookup")
	}
	log := c.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{cfg: c, log: log.With("interface", string(c.Kind))}
	s.http = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    8 << 10,
		// A request may wait. Its context ends when its client goes away.
		ErrorLog: slog.NewLogLogger(s.log.Handler(), slog.LevelError),
	}
	return s, nil
}

// Listener holds the checked IPC listener and the interface it belongs to.
// Only Listen constructs one; its underlying net.Listener is never exposed.
type Listener struct {
	kind ipc.Kind
	l    net.Listener
}

// Listen checks the interface kind before opening the address through ipc.
// The caller holds the instance lock and owns the listener until Serve takes it.
func Listen(kind ipc.Kind, addr ipc.Address, lock ipc.InstanceLock, opts ipc.Options) (*Listener, error) {
	if kind != ipc.KindAgent && kind != ipc.KindManage {
		return nil, fmt.Errorf("localhttp: unknown interface kind %q", kind)
	}
	if addr.Kind() != kind {
		return nil, fmt.Errorf("localhttp: %q is not an address of the %s interface", addr.Kind(), kind)
	}
	l, err := ipc.Listen(addr, lock, opts)
	if err != nil {
		return nil, err
	}
	return &Listener{kind: kind, l: l}, nil
}

// Close closes a listener that no server took.
func (l *Listener) Close() error { return l.l.Close() }

// Serve takes ownership of a matching listener, closes it on return, and returns
// nil after Shutdown or Close. A refused listener stays with its caller.
func (s *Server) Serve(l *Listener) error {
	if l == nil || l.l == nil {
		return errors.New("localhttp: no listener")
	}
	if l.kind != s.cfg.Kind {
		return fmt.Errorf("localhttp: cannot serve a %s listener as %s", l.kind, s.cfg.Kind)
	}
	if err := s.http.Serve(l.l); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown waits for active requests within ctx, then closes remaining
// connections on failure and returns the original shutdown error.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.http.Shutdown(ctx)
	if err != nil {
		s.http.Close()
	}
	return err
}

// Close ends serving immediately.
func (s *Server) Close() error { return s.http.Close() }

// ServeHTTP selects the exact method before checking POST. The selected handler
// decides when authorization and recovery permit reading the request body.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutPrefix(r.URL.Path, s.cfg.PathPrefix)
	var h http.HandlerFunc
	if ok {
		h = s.cfg.Lookup(name)
	}
	if h == nil {
		s.Fail(w, apiwire.New(apiwire.CategoryNotFound, apiwire.CodeMethodUnknown, "there is no such method"))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		s.JSON(w, http.StatusMethodNotAllowed, apiwire.Body{Error: *apiwire.New(apiwire.CategoryInvalidArgument, apiwire.CodeMethodNotAllowed, "a method is called with POST")})
		return
	}
	h(w, r)
}

// ReadBody reads at most 1 MiB. Handlers call it after their authorization gates.
func ReadBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err == nil {
		return body, nil
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return nil, apiwire.New(apiwire.CategoryInvalidArgument, apiwire.CodeRequestTooLarge, "the request is larger than a method takes")
	}
	return nil, apiwire.Malformed("the request could not be read", "")
}

// JSON encodes the whole response before publishing headers. Encoding failure
// writes a fixed answer directly, so even unencodable error details cannot recur.
func (s *Server) JSON(w http.ResponseWriter, status int, v any) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		s.log.Error("local HTTP answer could not be encoded", "error", err.Error())
		s.RawJSON(w, http.StatusInternalServerError, []byte(`{"error":{"category":"internal","code":"internal","message":"internal error"}}`+"\n"))
		return
	}
	s.RawJSON(w, status, b.Bytes())
}

// RawJSON sends complete JSON bytes unchanged, including any existing newline.
func (s *Server) RawJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	// The client can finish reading before the handler returns (and starts a stop).
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		s.log.Debug("local HTTP answer was not delivered", "error", err.Error())
	}
}

// Fail maps an error once. Its internal cause belongs in the diagnostic log,
// while the public response keeps apiwire's fixed internal-error message.
func (s *Server) Fail(w http.ResponseWriter, err error) {
	e := apiwire.FromError(err)
	if e.Category == apiwire.CategoryInternal {
		s.log.Error("local HTTP request failed", "code", e.Code, "error", err.Error())
	}
	s.JSON(w, e.Category.HTTPStatus(), apiwire.Body{Error: *e})
}
