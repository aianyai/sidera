package manageapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/store"
)

var t0 = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

const socketPathLimit = 103

// newHome returns a data directory with the directory of the management socket,
// short enough for a socket path.
func newHome(t testing.TB) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	for _, base := range []string{"", "/tmp"} {
		dir, err := os.MkdirTemp(base, "ma")
		if err != nil {
			continue
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		if len(filepath.Join(dir, "manage", "manage.sock")) > socketPathLimit {
			continue
		}
		for _, d := range []string{"manage", "run"} {
			if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(dir, d), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	t.Skip("no temporary directory short enough for a Unix socket path")
	return ""
}

type held struct{}

func (held) Held() bool { return true }

// fakeService is the engine as the management interface sees it: it records what
// it is asked and answers what the test says.
type fakeService struct {
	mu        sync.Mutex
	commands  []engine.ManagementCommand
	endpoints []engine.EndpointView
	execute   func(ctx context.Context, c engine.ManagementCommand) (engine.StoredResult, error)
	results   map[admin.RequestKey][]byte // what OperationResult finds
	lookups   []admin.RequestKey          // every key OperationResult was asked for
	lookupErr error
	// alerts, status and export are what the reads of the same names answer.
	alerts []store.AlertRecord
	status engine.NodeStatus
	node   config.Node
	export engine.RevocationExport
}

func (f *fakeService) Endpoints(context.Context) ([]engine.EndpointView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.endpoints, nil
}
func (f *fakeService) Counts(context.Context) (engine.Counts, error) { return engine.Counts{}, nil }
func (f *fakeService) Alerts(context.Context) ([]store.AlertRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.AlertRecord(nil), f.alerts...), nil
}
func (f *fakeService) Status(context.Context) (engine.NodeStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}
func (f *fakeService) NodeSettings(context.Context) (config.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.node, nil
}
func (f *fakeService) ExportRevocation(context.Context) (engine.RevocationExport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.export, nil
}
func (f *fakeService) Execute(ctx context.Context, c engine.ManagementCommand) (engine.StoredResult, error) {
	f.mu.Lock()
	f.commands = append(f.commands, c)
	run := f.execute
	f.mu.Unlock()
	if run == nil {
		return engine.StoredResult{Result: []byte(`{}`)}, nil
	}
	return run(ctx, c)
}

func (f *fakeService) OperationResult(_ context.Context, key admin.RequestKey) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups = append(f.lookups, key)
	r, ok := f.results[key]
	return r, ok, f.lookupErr
}

func (f *fakeService) asked() []admin.RequestKey {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]admin.RequestKey(nil), f.lookups...)
}

func (f *fakeService) calls() []engine.ManagementCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]engine.ManagementCommand(nil), f.commands...)
}

type stops struct {
	mu      sync.Mutex
	reasons []string
	ch      chan struct{}
	// hold, when set, makes a stop wait until it is closed, for at most five
	// seconds, and says in timedOut whether it had to give up waiting.
	hold     chan struct{}
	timedOut atomic.Bool
}

func newStops() *stops { return &stops{ch: make(chan struct{}, 8)} }

func (s *stops) stop(reason string) {
	if s.hold != nil {
		select {
		case <-s.hold:
		case <-time.After(5 * time.Second):
			s.timedOut.Store(true)
		}
	}
	s.mu.Lock()
	s.reasons = append(s.reasons, reason)
	s.mu.Unlock()
	s.ch <- struct{}{}
}

func (s *stops) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reasons)
}

// rig is a server on a real socket of a temporary data directory.
type rig struct {
	t    testing.TB
	home string
	svc  *fakeService
	stop *stops
	srv  *Server
	log  *captured
	done chan error
}

// captured is a log that keeps what is written to it.
type captured struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *captured) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}
func (c *captured) String() string { c.mu.Lock(); defer c.mu.Unlock(); return c.buf.String() }

func newRig(t testing.TB, mutate ...func(*Server)) *rig {
	t.Helper()
	r := &rig{t: t, home: newHome(t), svc: &fakeService{}, stop: newStops(), log: &captured{}, done: make(chan error, 1)}
	node, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{7}, ident.NodeIDLen))
	if err != nil {
		t.Fatal(err)
	}
	r.srv, err = New(Config{
		Service: r.svc, Node: node, Clock: clocktest.NewFake(t0), Stop: r.stop.stop,
		Log: slog.New(slog.NewTextHandler(r.log, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mutate {
		m(r.srv)
	}
	addr, err := ipc.Locate(r.home, ipc.KindManage)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Listen(addr, held{}, ipc.Options{})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { r.done <- r.srv.Serve(l) }()
	t.Cleanup(r.shutdown)
	return r
}

func (r *rig) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := r.srv.Shutdown(ctx); err != nil {
		r.t.Errorf("Shutdown: %v", err)
	}
	select {
	case err := <-r.done:
		if err != nil {
			r.t.Errorf("Serve returned %v", err)
		}
	case <-time.After(20 * time.Second):
		r.t.Error("Serve did not return after Shutdown")
	}
}

func (r *rig) client() *http.Client {
	addr, err := ipc.Locate(r.home, ipc.KindManage)
	if err != nil {
		r.t.Fatal(err)
	}
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return ipc.Dial(ctx, addr, ipc.Options{}) },
	}, Timeout: 30 * time.Second}
}

// post sends a body to a method the way a client does and returns the status and
// the answer.
func (r *rig) post(method string, body any) (int, []byte) {
	r.t.Helper()
	var raw []byte
	switch b := body.(type) {
	case string:
		raw = []byte(b)
	case []byte:
		raw = b
	default:
		var err error
		if raw, err = json.Marshal(body); err != nil {
			r.t.Fatal(err)
		}
	}
	c := r.client()
	defer c.CloseIdleConnections()
	resp, err := c.Post("http://sidera/v1/manage/"+method, "application/json", bytes.NewReader(raw))
	if err != nil {
		r.t.Fatalf("POST %s: %v", method, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		r.t.Fatalf("reading the answer to %s: %v", method, err)
	}
	return resp.StatusCode, out
}

type wireError struct {
	Error struct {
		Category string         `json:"category"`
		Code     string         `json:"code"`
		Message  string         `json:"message"`
		Details  map[string]any `json:"details"`
	} `json:"error"`
}

func (r *rig) wantError(status int, category, code string, gotStatus int, body []byte) wireError {
	r.t.Helper()
	var e wireError
	if err := json.Unmarshal(body, &e); err != nil {
		r.t.Fatalf("the answer is not an error body: %v\n%s", err, body)
	}
	if gotStatus != status || e.Error.Category != category || e.Error.Code != code || e.Error.Message == "" {
		r.t.Errorf("answered %d %s/%s %q, want %d %s/%s\n%s", gotStatus, e.Error.Category, e.Error.Code, e.Error.Message, status, category, code, body)
	}
	return e
}

func opID(b byte) string {
	return strings.Repeat("0", 30) + string("0123456789abcdef"[b>>4]) + string("0123456789abcdef"[b&15])
}

var _ = admin.OpRead
