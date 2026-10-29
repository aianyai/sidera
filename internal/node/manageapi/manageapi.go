// Package manageapi is the management interface of the daemon: HTTP over the
// local socket (a named pipe on Windows) that the owner's commands talk to. It
// reads a request strictly, judges it by the administrative rules, hands what
// changes state to the engine's one door (engine.Execute), answers in the words
// apiwire gives, and has no rule of its own about who may do what. See README.md.
package manageapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/localhttp"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

const (
	// pathPrefix is where the methods are: POST /v1/manage/<method>.
	pathPrefix = "/v1/manage/"
	// fingerprintDomain begins every fingerprint, so that one made for another
	// purpose or another version of the rule is never the same.
	fingerprintDomain = "sidera-manage-v1"
)

// Config is what the composition root gives the server.
type Config struct {
	TestConfig
	// Service is the engine, as far as the management interface may ask of it.
	Service engine.ManageService
	// Node is this node's identifier: an address of another node is not one of
	// this node's endpoints.
	Node ident.NodeID
	// Clock is the injected time source, read once per request for the
	// administrative rules.
	Clock clock.Clock
	// Stop asks the daemon to shut down. It is called without waiting, once the
	// answer to a request whose operation stops the daemon has been written.
	Stop func(reason string)
	// Version is the version of the program, for the status to show; empty shows
	// none.
	Version string
	// Log receives what the server observed; nil discards it. An error that was
	// not expected is logged whole here and answered as an internal error.
	Log *slog.Logger
}

// Server is the management interface. Its methods are a table in this package:
// the owner of an operation adds a row, and the handling of a request is the same
// for every row.
type Server struct {
	cfg     Config
	list    admin.List
	methods map[string]*method
	http    *localhttp.Server
}

// New builds the server. It listens on nothing until Serve.
func New(c Config) (*Server, error) {
	switch {
	case c.Service == nil:
		return nil, errors.New("manageapi: no service")
	case c.Clock == nil:
		return nil, errors.New("manageapi: no clock")
	case c.Node.IsZero():
		return nil, errors.New("manageapi: no node identifier")
	}
	methods, err := defaultMethods()
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: c, methods: methods}
	// The management interface is the local owner's alone in this stage: the list
	// of authorized persons is empty, and Authorize still decides by the
	// catalogue, which denies an operation it does not know to everyone.
	s.list, _ = admin.NewList(nil)
	s.http, err = localhttp.New(localhttp.Config{
		Kind: ipc.KindManage, PathPrefix: pathPrefix, Lookup: s.lookup, Log: c.Log,
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Listener is a listener of the management interface. Only Listen makes one, and
// Listen refuses the address of the Agent interface: a connection that this
// server accepts is the local owner, so the server may serve no other listener.
type Listener struct{ l *localhttp.Listener }

// Listen listens on the address of the management interface. The caller holds the
// instance lock, as ipc.Listen asks.
func Listen(addr ipc.Address, lock ipc.InstanceLock, opts ipc.Options) (*Listener, error) {
	l, err := localhttp.Listen(ipc.KindManage, addr, lock, opts)
	if err != nil {
		return nil, err
	}
	return &Listener{l: l}, nil
}

// Close closes a listener that no server took.
func (l *Listener) Close() error { return l.l.Close() }

// Serve answers the requests of the listener until Shutdown or Close, and returns
// nil then. It closes the listener.
func (s *Server) Serve(l *Listener) error {
	if l == nil {
		return s.http.Serve(nil)
	}
	return s.http.Serve(l.l)
}

// Shutdown stops accepting requests and waits within ctx, closing what remains.
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

// Close ends the serving at once.
func (s *Server) Close() error { return s.http.Close() }

// actor is the one place the local owner is made. Whoever is served here came in
// by Listen, which takes only the address of the management interface, so the
// peer was checked to be the user's own by ipc, and the interface identifies the
// owner by that alone.
func (s *Server) actor() admin.Actor { return admin.LocalOwner() }

func (s *Server) lookup(name string) http.HandlerFunc {
	m := s.methods[name]
	if m == nil {
		return nil
	}
	return func(w http.ResponseWriter, r *http.Request) { s.serve(w, r, name, m) }
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request, name string, m *method) {
	// Authorize comes first, by the catalogue: the local owner is denied only an
	// operation the catalogue does not know, which is a defect of the program. A
	// test row is no operation of the catalogue and is not authorized by it (see
	// method.test).
	actor := s.actor()
	if m.test == nil {
		if v := admin.Authorize(s.list, actor, m.op, s.cfg.Clock.Now()); !v.Allowed {
			s.http.Fail(w, apiwire.FromAdmin(v.Reason))
			return
		}
	}

	body, err := localhttp.ReadBody(w, r)
	if err != nil {
		s.http.Fail(w, err)
		return
	}
	if m.test != nil {
		s.serveTest(w, r, m, body)
		return
	}
	d, err := s.decode(name, body)
	if err != nil {
		s.http.Fail(w, err)
		return
	}
	if err := m.checkPlan(d.plan); err != nil {
		s.http.Fail(w, err)
		return
	}

	if d.plan.read != nil {
		result, err := d.plan.read(r.Context(), s.cfg.Service)
		if err != nil {
			s.http.Fail(w, err)
			return
		}
		s.write(w, struct {
			Result any `json:"result"`
		}{result})
		return
	}

	// KeyFor only for an actor that Authorize allowed, as the rules ask.
	res, err := s.cfg.Service.Execute(r.Context(), engine.ManagementCommand{
		Key: admin.KeyFor(actor, d.id), Fingerprint: d.fingerprint, Expected: d.expected,
		Name: name, Operation: d.plan.operation,
	})
	if err != nil {
		s.http.Fail(w, err)
		return
	}
	s.write(w, struct {
		OperationID string          `json:"operation_id"`
		Replayed    bool            `json:"replayed"`
		Result      json.RawMessage `json:"result"`
	}{hex.EncodeToString(d.id[:]), res.Replayed, res.Result})
	if res.Stop && s.cfg.Stop != nil {
		// The answer is on its way: flush it, so that the client has it before the
		// daemon starts to go away.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		s.cfg.Stop(name)
	}
}

// serveTest answers a request for a test row: the envelope of a read, the params
// handed to the row, and its result as the result of a read. No operation
// identifier, no expected version, no stored result.
func (s *Server) serveTest(w http.ResponseWriter, r *http.Request, m *method, body []byte) {
	var env readEnvelope
	if err := strictjson.Decode(body, &env); err != nil {
		s.http.Fail(w, err)
		return
	}
	params := env.Params
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	result, err := m.test(s, r.Context(), params)
	if err != nil {
		s.http.Fail(w, err)
		return
	}
	s.write(w, struct {
		Result any `json:"result"`
	}{result})
}

// decoded is a request that was read and found well formed.
type decoded struct {
	m           *method
	id          admin.OperationID
	expected    admin.Version
	plan        *plan
	fingerprint admin.Fingerprint
}

// writeEnvelope and readEnvelope are the two shapes of a request. The identifier
// and the version are optional to the decoder, so that the administrative rules
// say which of them a request lacks.
type writeEnvelope struct {
	OperationID string `json:"operation_id,omitempty"`
	// ExpectedVersion is a pointer so that its presence can be told from its value:
	// a request that carries the member at all, zero included, is refused where the
	// operation takes none, because the sender meant it to do something.
	ExpectedVersion *uint64         `json:"expected_version,omitempty"`
	Params          json.RawMessage `json:"params,omitempty"`
}

type readEnvelope struct {
	Params json.RawMessage `json:"params,omitempty"`
}

// decode reads the request of a method strictly: the envelope, the identifier, the
// version and the params, and judges its shape by the administrative rules. It is
// the one entry that reads untrusted bytes of the interface.
func (s *Server) decode(name string, body []byte) (*decoded, error) {
	m := s.methods[name]
	if m == nil {
		return nil, apiwire.New(apiwire.CategoryNotFound, apiwire.CodeMethodUnknown, "there is no such method")
	}
	d := &decoded{m: m}
	var params json.RawMessage
	if m.op.SideEffecting() {
		var env writeEnvelope
		if err := strictjson.Decode(body, &env); err != nil {
			return nil, err
		}
		id, err := parseOperationID(env.OperationID)
		if err != nil {
			return nil, err
		}
		d.id, params = id, env.Params
		if env.ExpectedVersion != nil {
			if !m.op.IsConfigChange() {
				return nil, apiwire.New(apiwire.CategoryInvalidArgument, apiwire.CodeUnexpectedVersion,
					"this operation changes no versioned setting and takes no expected version")
			}
			d.expected = admin.Version(*env.ExpectedVersion)
		}
		if reason := admin.ValidateRequest(admin.Request{ID: d.id, Op: m.op, ExpectedVersion: d.expected}); reason != admin.ReasonOK {
			return nil, apiwire.FromAdmin(reason)
		}
	} else {
		var env readEnvelope
		if err := strictjson.Decode(body, &env); err != nil {
			return nil, err
		}
		params = env.Params
	}
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	p, err := m.plan(s, params)
	if err != nil {
		return nil, err
	}
	d.plan = p
	d.fingerprint = fingerprint(m.name, p.canonical, d.expected)
	return d, nil
}

// parseOperationID reads the identifier of a request: 32 lower-case hexadecimal
// digits that are not all zero. An absent identifier is the zero value, which the
// administrative rules refuse for a request that changes something.
func parseOperationID(s string) (admin.OperationID, error) {
	var id admin.OperationID
	if s == "" {
		return id, nil
	}
	bad := func(why string) error {
		return apiwire.Malformed("the operation identifier is "+why, "operation_id")
	}
	if len(s) != 2*len(id) || s != strings.ToLower(s) {
		return id, bad("32 lower-case hexadecimal digits")
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return id, bad("32 lower-case hexadecimal digits")
	}
	copy(id[:], raw)
	if id.IsZero() {
		return id, bad("all zero")
	}
	return id, nil
}

// fingerprint is the digest of a whole request: a version of the rule, the method,
// the params in their canonical encoding and the expected version, each followed
// by a NUL. It is the rule of the interface alone: the engine takes the value, and
// two requests that mean the same have one fingerprint however they were written.
func fingerprint(method string, canonicalParams []byte, expected admin.Version) admin.Fingerprint {
	h := sha256.New()
	for _, part := range [][]byte{[]byte(fingerprintDomain), []byte(method), canonicalParams} {
		h.Write(part)
		h.Write([]byte{0})
	}
	h.Write([]byte(strconv.FormatUint(uint64(expected), 10)))
	var fp admin.Fingerprint
	copy(fp[:], h.Sum(nil))
	return fp
}

// write answers 200 with the value as JSON.
func (s *Server) write(w http.ResponseWriter, v any) { s.http.JSON(w, http.StatusOK, v) }
