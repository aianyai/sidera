package peer_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/peer"
)

const wait = 5 * time.Second

// wireLogEnv names a file that collects every line a peer under test writes, for
// a test in the repository's root module that checks each line against the
// protocol's message schema (the validator is a dependency of that module only).
const wireLogEnv = "SIDERA_PEER_WIRE_LOG"

var wireLog struct {
	sync.Mutex
	f *os.File
}

// captured wraps the writer of a peer under test so that the lines it writes are
// copied to the file named by wireLogEnv, when there is one.
func captured(w io.WriteCloser) io.WriteCloser {
	if os.Getenv(wireLogEnv) == "" {
		return w
	}
	return &capturingWriter{WriteCloser: w}
}

type capturingWriter struct{ io.WriteCloser }

func (c *capturingWriter) Write(p []byte) (int, error) {
	wireLog.Lock()
	if wireLog.f == nil {
		f, err := os.OpenFile(os.Getenv(wireLogEnv), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			panic(err)
		}
		wireLog.f = f
	}
	// One Write is one line, and O_APPEND keeps lines whole.
	_, err := wireLog.f.Write(p)
	wireLog.Unlock()
	if err != nil {
		panic(err)
	}
	return c.WriteCloser.Write(p)
}

// peerGoroutines returns the stacks of live goroutines that belong to a Peer.
func peerGoroutines() []string {
	var buf bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
	var found []string
	for _, block := range strings.Split(buf.String(), "\n\n") {
		if strings.Contains(block, "sdk/go/peer.(*Peer).") {
			found = append(found, block)
		}
	}
	return found
}

// settledPeerGoroutines waits up to wait for every goroutine of a Peer to end
// and returns the stacks of those still alive. Peer.Close returns once the
// reader and the writer have ended; its request workers and its notification
// loop notice the end of the connection on their own schedule, so right after
// a Close the goroutines of a finished peer are still on their way out.
func settledPeerGoroutines() []string {
	deadline := time.Now().Add(wait)
	for {
		left := peerGoroutines()
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// leakGuard fails the test if any goroutine of a Peer is still alive after
// the test and its cleanups. Register it before anything that creates a peer.
// Tests using it must not run in parallel.
//
// Peers that an earlier test closed without waiting for their goroutines (the
// tests of package peer itself do not use the guard) may still be winding down
// when the test starts, so the starting check waits for them like the final
// check does; only goroutines that stay alive fail the test.
func leakGuard(t *testing.T) {
	t.Helper()
	if left := settledPeerGoroutines(); len(left) != 0 {
		t.Fatalf("%d peer goroutines alive before the test started:\n%s", len(left), strings.Join(left, "\n\n"))
	}
	t.Cleanup(func() {
		if left := settledPeerGoroutines(); len(left) != 0 {
			t.Errorf("%d peer goroutines leaked:\n%s", len(left), strings.Join(left, "\n\n"))
		}
	})
}

// logBuf collects log output so tests can assert what was and was not logged.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *logBuf) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// frame is a decoded outbound line of the peer under test.
type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

func (f frame) errCode() int {
	if f.Error == nil {
		return 0
	}
	return f.Error.Code
}

// raw is the test's end of a connection to a peer: it writes arbitrary bytes
// to the peer and reads whatever the peer writes.
type raw struct {
	t        *testing.T
	toPeer   *io.PipeWriter
	fromPeer *io.PipeReader
	br       *bufio.Reader
	lines    chan lineResult
}

// newRaw creates an unstarted peer connected to a raw end. setup registers
// handlers; the peer is started afterwards.
func newRaw(t *testing.T, params peer.Params, setup func(*peer.Peer)) (*peer.Peer, *raw, *logBuf) {
	t.Helper()
	return newRawDrain(t, params, setup, true)
}

// newRawUndrained is newRaw, except that nothing reads the peer's output until
// the test calls pump.
func newRawUndrained(t *testing.T, params peer.Params) (*peer.Peer, *raw, *logBuf) {
	t.Helper()
	return newRawDrain(t, params, nil, false)
}

func newRawDrain(t *testing.T, params peer.Params, setup func(*peer.Peer), drain bool) (*peer.Peer, *raw, *logBuf) {
	t.Helper()
	toPeerR, toPeerW := io.Pipe()
	fromPeerR, fromPeerW := io.Pipe()
	logs := &logBuf{}
	p, err := peer.New(toPeerR, captured(fromPeerW), peer.Config{Params: params, Logger: logs.logger()})
	if err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(p)
	}
	p.Start()
	r := &raw{t: t, toPeer: toPeerW, fromPeer: fromPeerR, br: bufio.NewReader(fromPeerR), lines: make(chan lineResult, 4096)}
	if drain {
		go r.pump()
	}
	t.Cleanup(func() {
		p.Close()
		toPeerW.Close()
		fromPeerR.Close()
	})
	return p, r, logs
}

// sendBytes writes b to the peer, failing the test if the peer does not read
// it in time.
func (r *raw) sendBytes(b []byte) {
	r.t.Helper()
	errc := make(chan error, 1)
	go func() { _, err := r.toPeer.Write(b); errc <- err }()
	select {
	case err := <-errc:
		if err != nil {
			r.t.Fatalf("write to peer: %v", err)
		}
	case <-time.After(wait):
		r.t.Fatal("timed out writing to the peer")
	}
}

func (r *raw) send(line string) { r.t.Helper(); r.sendBytes([]byte(line + "\n")) }

type lineResult struct {
	line []byte
	err  error
}

// pump reads lines from the peer until the pipe ends.
func (r *raw) pump() {
	for {
		line, err := r.br.ReadBytes('\n')
		r.lines <- lineResult{line, err}
		if err != nil {
			return
		}
	}
}

// readLine returns the next line from the peer, or ok=false after d.
func (r *raw) readLine(d time.Duration) (line []byte, err error, ok bool) {
	select {
	case x := <-r.lines:
		return x.line, x.err, true
	case <-time.After(d):
		return nil, nil, false
	}
}

// recv returns the next frame from the peer.
func (r *raw) recv() frame {
	r.t.Helper()
	line, err, ok := r.readLine(wait)
	if !ok {
		r.t.Fatal("timed out waiting for a line from the peer")
	}
	if err != nil {
		r.t.Fatalf("read from peer: %v", err)
	}
	var f frame
	if err := json.Unmarshal(line, &f); err != nil {
		r.t.Fatalf("peer wrote an invalid line %q: %v", line, err)
	}
	if f.JSONRPC != "2.0" {
		r.t.Fatalf("peer wrote a line without jsonrpc 2.0: %q", line)
	}
	return f
}

// expectEOF asserts that the peer closed its end without writing anything more.
func (r *raw) expectEOF() {
	r.t.Helper()
	line, err, ok := r.readLine(wait)
	if !ok {
		r.t.Fatal("timed out waiting for the peer to close")
	}
	if err != io.EOF || len(line) != 0 {
		r.t.Fatalf("expected EOF, got line %q err %v", line, err)
	}
}

// expectSilence asserts that the peer writes nothing for d.
func (r *raw) expectSilence(d time.Duration) {
	r.t.Helper()
	line, _, ok := r.readLine(d)
	if ok {
		r.t.Fatalf("unexpected line from the peer: %q", line)
	}
}

// pingHandler answers {} to every request; used to prove a connection still
// works after a bad message.
func pingHandler(context.Context, json.RawMessage) (any, error) { return struct{}{}, nil }

func waitDone(t *testing.T, p *peer.Peer) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(wait):
		t.Fatal("peer did not end in time")
	}
}

// pair connects two peers through in-memory pipes. Writes by each side go
// through tap (when non-nil) before reaching the other.
func newPair(t *testing.T, pa, pb peer.Params, tap func(from string, line []byte)) (a, b *peer.Peer) {
	t.Helper()
	aToBR, aToBW := io.Pipe()
	bToAR, bToAW := io.Pipe()
	var wa io.WriteCloser = captured(aToBW)
	var wb io.WriteCloser = captured(bToAW)
	if tap != nil {
		wa = &tapWriter{WriteCloser: wa, from: "a", tap: tap}
		wb = &tapWriter{WriteCloser: wb, from: "b", tap: tap}
	}
	var err error
	if a, err = peer.New(bToAR, wa, peer.Config{Params: pa}); err != nil {
		t.Fatal(err)
	}
	if b, err = peer.New(aToBR, wb, peer.Config{Params: pb}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

type tapWriter struct {
	io.WriteCloser
	from string
	tap  func(from string, line []byte)
}

func (w *tapWriter) Write(p []byte) (int, error) {
	w.tap(w.from, bytes.Clone(p))
	return w.WriteCloser.Write(p)
}

func newReader(r io.Reader) *bufio.Reader { return bufio.NewReader(r) }

// expectNoLineFor asserts that the peer writes nothing for d.
func (r *raw) expectNoLineFor(d time.Duration) { r.t.Helper(); r.expectSilence(d) }
