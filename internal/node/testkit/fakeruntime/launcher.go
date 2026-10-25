package fakeruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// ProcessLauncher starts a runtime process for each session and drives it over the
// control channel, the way an adapter drives a native runtime that is a program
// of its own. The runtime process is detached: it outlives the adapter that
// started it, which is what lets a restarted adapter take its session back.
type ProcessLauncher struct {
	// Binary is the runtime program, built from cmd/sidera-fake-runtime.
	Binary string
	// Dir is the directory of the session records and info files; LedgerDir the
	// directory of the file ledger. Both are shared with the fixture.
	Dir       string
	LedgerDir string
	// Control is the address of the fixture's control server, which the runtime
	// process joins; empty for none.
	Control string
	// Seed is the seed of native references.
	Seed uint64
	// Now is the shared test clock: the runtime process starts at its reading.
	Now func() time.Time
	// Env is added to the environment of the runtime process.
	Env []string

	mu      sync.Mutex
	clients []*fakectl.Client
}

var _ Launcher = (*ProcessLauncher)(nil)

// Name of the environment variable that carries the ProcessSpec.
const SpecEnv = "SIDERA_FAKE_RUNTIME_SPEC"

// Create starts a runtime process for the attempt, waits until its session exists,
// and connects to it. The same attempt, asked again, finds the process that its
// first ask started.
func (l *ProcessLauncher) Create(ctx context.Context, req CreateRequest) (Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := req.validate(); err != nil {
		return nil, err
	}
	if l.Binary == "" || l.Dir == "" || l.LedgerDir == "" || l.Now == nil {
		return nil, errors.New("fakeruntime: a process launcher needs a binary, two directories and the test clock")
	}
	if s, ok := l.Find(nativeRef(l.Seed, req.Attempt)); ok && s.Alive() {
		return s, nil
	}
	spec := ProcessSpec{Create: req, Dir: l.Dir, LedgerDir: l.LedgerDir, Seed: l.Seed, ClockStartMS: l.Now().UnixMilli(), Control: l.Control}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return nil, err
	}
	cmd := exec.Command(l.Binary)
	cmd.Env = append(append(os.Environ(), l.Env...), SpecEnv+"="+string(raw))
	detach(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	type result struct {
		info ProcessInfo
		err  error
	}
	got := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(out).ReadBytes('\n')
		if err != nil {
			got <- result{err: fmt.Errorf("fakeruntime: the runtime process ended before it was ready: %w", err)}
			return
		}
		info, err := DecodeProcessInfo(line)
		got <- result{info: info, err: err}
	}()
	var info ProcessInfo
	select {
	case r := <-got:
		if r.err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, r.err
		}
		info = r.info
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, ctx.Err()
	}
	// The process is meant to outlive this one. Its handle is released; it is not
	// waited for.
	_ = cmd.Process.Release()
	return l.connect(ctx, info)
}

// Find returns the session with the reference: a live one over a connection to its
// process, or a dead one that reads its record. A reference without an info file
// is no session.
func (l *ProcessLauncher) Find(ref string) (Session, bool) {
	raw, err := os.ReadFile(filepath.Join(l.Dir, ref+".json"))
	if err != nil {
		return nil, false
	}
	info, err := DecodeProcessInfo(raw)
	if err != nil || info.Ref != ref {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s, err := l.connect(ctx, info); err == nil {
		return s, true
	}
	return &deadSession{info: info, record: filepath.Join(l.Dir, ref+".jsonl")}, true
}

// Close ends the connections this launcher opened to runtime processes. The
// processes themselves go on: they are meant to outlive their adapter.
func (l *ProcessLauncher) Close() error {
	l.mu.Lock()
	clients := l.clients
	l.clients = nil
	l.mu.Unlock()
	var errs []error
	for _, c := range clients {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

func (l *ProcessLauncher) connect(ctx context.Context, info ProcessInfo) (Session, error) {
	c, err := fakectl.Dial(ctx, info.Addr)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.clients = append(l.clients, c)
	l.mu.Unlock()
	s := &remoteSession{info: info, client: c}
	c.HandleNotification(NoteTurnStarted, func(_ context.Context, params json.RawMessage) {
		if v, err := DecodeLink(NoteTurnStarted, params); err == nil {
			n := v.(TurnStartedNote)
			s.dispatch(func(w Watcher) { w.TurnStarted(TurnStartedEvent{Turn: n.Turn, Attempt: n.Attempt, AtMS: n.AtMS}) })
		}
	})
	c.HandleNotification(NoteTurnEnded, func(_ context.Context, params json.RawMessage) {
		if v, err := DecodeLink(NoteTurnEnded, params); err == nil {
			n := v.(TurnEndedNote)
			s.dispatch(func(w Watcher) { w.TurnEnded(TurnEndedEvent{Turn: n.Turn, Final: n.Final, AtMS: n.AtMS}) })
		}
	})
	c.HandleNotification(NoteToolResult, func(_ context.Context, params json.RawMessage) {
		if v, err := DecodeLink(NoteToolResult, params); err == nil {
			n := v.(ToolResultNote)
			s.dispatch(func(w Watcher) { w.ToolResult(ToolResultEvent{Lease: n.Lease, Text: n.Text, Frontend: n.Frontend}) })
		}
	})
	if err := c.Start(ctx, fakectl.HelloParams{Role: fakectl.RoleAdapter, ID: "adapter", PID: int64(os.Getpid())}); err != nil {
		_ = c.Close()
		return nil, err
	}
	return s, nil
}

// remoteSession is a live session in a runtime process.
type remoteSession struct {
	info   ProcessInfo
	client *fakectl.Client

	mu      sync.Mutex
	watcher Watcher
}

func (s *remoteSession) Ref() string      { return s.info.Ref }
func (s *remoteSession) Endpoint() string { return s.info.Endpoint }
func (s *remoteSession) PID() int64       { return s.info.PID }
func (s *remoteSession) StartKey() string { return s.info.StartKey }

func (s *remoteSession) Watch(w Watcher) {
	s.mu.Lock()
	s.watcher = w
	s.mu.Unlock()
}

func (s *remoteSession) dispatch(f func(Watcher)) {
	s.mu.Lock()
	w := s.watcher
	s.mu.Unlock()
	if w != nil {
		f(w)
	}
}

// remoteError turns the error a runtime process answered with into the value the
// session would have returned in process.
func remoteError(err error) error {
	var pe *peer.Error
	if !errors.As(err, &pe) {
		return err
	}
	switch pe.Code {
	case CodeBusy:
		return ErrBusy
	case CodeNotAlive:
		return ErrNotAlive
	case CodeNoTurn:
		return ErrNoTurn
	}
	return err
}

func (s *remoteSession) state() StateReply {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var st StateReply
	if err := s.client.Call(ctx, MethodState, nil, &st); err != nil {
		return StateReply{}
	}
	return st
}

func (s *remoteSession) Alive() bool { return s.state().Alive }

func (s *remoteSession) TurnActive() (string, bool) {
	st := s.state()
	return st.Turn, st.Alive && st.Active
}

func (s *remoteSession) Deliver(ctx context.Context, attempt, text string) error {
	return remoteError(s.client.Call(ctx, MethodDeliver, DeliverParams{Attempt: attempt, Text: text}, nil))
}

func (s *remoteSession) Interrupt(ctx context.Context) (InterruptResult, error) {
	var r InterruptReply
	if err := s.client.Call(ctx, MethodInterrupt, nil, &r); err != nil {
		return 0, remoteError(err)
	}
	if r.Result == "not_running" {
		return InterruptNotRunning, nil
	}
	return InterruptStopped, nil
}

func (s *remoteSession) Close(ctx context.Context) error {
	return remoteError(s.client.Call(ctx, MethodClose, nil, nil))
}

func (s *remoteSession) Reconcile(attempt string) (Reconciliation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var r ReconcileReply
	if err := s.client.Call(ctx, MethodReconcile, ReconcileParams{Attempt: attempt}, &r); err != nil {
		return Reconciliation{}, remoteError(err)
	}
	return Reconciliation{Found: r.Found, Turn: r.Turn, TurnState: r.TurnState, TurnRunning: r.TurnRunning, WriterStopped: r.WriterStopped}, nil
}

// deadSession is a session whose process is gone: only its record is left.
type deadSession struct {
	info   ProcessInfo
	record string
}

func (s *deadSession) Ref() string                                   { return s.info.Ref }
func (s *deadSession) Endpoint() string                              { return s.info.Endpoint }
func (s *deadSession) PID() int64                                    { return s.info.PID }
func (s *deadSession) StartKey() string                              { return s.info.StartKey }
func (s *deadSession) Alive() bool                                   { return false }
func (s *deadSession) TurnActive() (string, bool)                    { return "", false }
func (s *deadSession) Watch(Watcher)                                 {}
func (s *deadSession) Close(context.Context) error                   { return nil }
func (s *deadSession) Deliver(context.Context, string, string) error { return ErrNotAlive }
func (s *deadSession) Interrupt(context.Context) (InterruptResult, error) {
	return 0, ErrNotAlive
}

func (s *deadSession) Reconcile(attempt string) (Reconciliation, error) {
	raw, err := os.ReadFile(s.record)
	if err != nil {
		return Reconciliation{}, err
	}
	events, err := ParseRecord(raw)
	if err != nil {
		return Reconciliation{}, err
	}
	return reconcileFromRecord(events, attempt), nil
}
