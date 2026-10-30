package fakeruntime

import (
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
	// process joins. It is required: a process that nobody can end would be left
	// behind when its test ends.
	Control string
	// Seed is the seed of native references.
	Seed uint64
	// TestSeed is the base of the seeds the runtime processes derive for the
	// commands their models run; empty derives none.
	TestSeed string
	// Now is the shared test clock: the runtime process starts at its reading.
	Now func() time.Time
	// Env is added to the environment of the runtime process.
	Env []string

	mu        sync.Mutex
	clients   []*fakectl.Client
	firstTurn TurnPlan
}

var _ Launcher = (*ProcessLauncher)(nil)

// SetFirstTurn sets the plan of the first turn of the sessions this launcher
// creates from now on.
func (l *ProcessLauncher) SetFirstTurn(plan TurnPlan) error {
	if err := plan.validate(); err != nil {
		return err
	}
	l.mu.Lock()
	l.firstTurn = plan
	l.mu.Unlock()
	return nil
}

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
	if l.Binary == "" || l.Dir == "" || l.LedgerDir == "" || l.Control == "" || l.Now == nil {
		return nil, errors.New("fakeruntime: a process launcher needs a binary, two directories, the control address and the test clock")
	}
	ref := nativeRef(l.Seed, req.Attempt)
	if info, err := l.readProcessInfo(ref); err == nil {
		return l.connect(ctx, info, true)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	claimed, err := l.recordExists(ref)
	if err != nil {
		return nil, err
	}
	if claimed {
		return l.waitForProcessInfo(ctx, ref, nil)
	}
	if req.Refusal != "" {
		return nil, &CreateRefusedError{Reason: req.Refusal}
	}
	l.mu.Lock()
	first := l.firstTurn
	l.mu.Unlock()
	spec := ProcessSpec{
		Create: req, Dir: l.Dir, LedgerDir: l.LedgerDir, Seed: l.Seed, ClockStartMS: l.Now().UnixMilli(), Control: l.Control,
		FirstTurn: KnobOf(first), TestSeed: l.TestSeed,
	}
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
	// Nil standard streams use the null device. In particular, readiness must
	// not write to a pipe whose reader dies with the original adapter.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// This is the sole Wait owner for this candidate, even if Create is
	// cancelled. The fixture's control connection owns the native's lifetime;
	// cancellation cannot kill a process that may already have created a session.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	return l.waitForProcessInfo(ctx, ref, exited)
}

func (l *ProcessLauncher) recordExists(ref string) (bool, error) {
	_, err := os.Stat(filepath.Join(l.Dir, ref+".jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (l *ProcessLauncher) readProcessInfo(ref string) (ProcessInfo, error) {
	raw, err := os.ReadFile(filepath.Join(l.Dir, ref+".json"))
	if err != nil {
		return ProcessInfo{}, err
	}
	info, err := DecodeProcessInfo(raw)
	if err != nil {
		return ProcessInfo{}, err
	}
	if info.Ref != ref {
		return ProcessInfo{}, errWrongSession
	}
	return info, nil
}

// waitForProcessInfo observes one native's existing atomic publication. The
// timer only paces file reads: it supplies neither readiness nor a deadline.
func (l *ProcessLauncher) waitForProcessInfo(ctx context.Context, ref string, exited <-chan error) (Session, error) {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	var candidateExited bool
	var exitErr error
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if info, err := l.readProcessInfo(ref); err == nil {
			return l.connect(ctx, info, true)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if candidateExited {
			claimed, err := l.recordExists(ref)
			if err != nil {
				return nil, err
			}
			if !claimed {
				if exitErr != nil {
					return nil, fmt.Errorf("fakeruntime: the runtime process ended before it was ready: %w", exitErr)
				}
				return nil, errors.New("fakeruntime: the runtime process ended before it was ready")
			}
			// A candidate can lose the exclusive record create to another
			// owner. A claim without info cannot prove either life or death;
			// wait for publication or cancellation, never spawn a replacement.
			candidateExited = false
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case exitErr = <-exited:
			exited = nil
			candidateExited = true
		case <-timer.C:
			timer.Reset(10 * time.Millisecond)
		}
	}
}

// Find returns the session with the reference: a live one over a connection to its
// process, or a dead one that reads its record. A reference without an info file
// is no session.
func (l *ProcessLauncher) Find(ref string) (Session, bool) {
	info, err := l.readProcessInfo(ref)
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if s, err := l.connect(ctx, info, false); err == nil {
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

// errWrongSession: the address answered, but not as the session it was written
// for. A port that was released can be taken by the next process that asks for
// one, so an info file that outlived its process may point at another session.
var errWrongSession = errors.New("fakeruntime: the process at the address is not the session the info file names")

func (l *ProcessLauncher) connect(ctx context.Context, info ProcessInfo, requireAlive bool) (Session, error) {
	c, err := fakectl.Dial(ctx, info.Addr)
	if err != nil {
		return nil, err
	}
	s := &remoteSession{info: info, client: c, record: filepath.Join(l.Dir, info.Ref+".jsonl")}
	// The runtime makes these requests of the adapter; the adapter answers once the
	// event has been handed on (see MethodTurnStarted).
	c.Handle(MethodTurnStarted, func(_ context.Context, params json.RawMessage) (any, error) {
		v, err := DecodeLink(MethodTurnStarted, params)
		if err != nil {
			return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
		}
		n := v.(TurnStartedParams)
		s.dispatch(func(w Watcher) { w.TurnStarted(TurnStartedEvent{Turn: n.Turn, Attempt: n.Attempt, AtMS: n.AtMS}) })
		return struct{}{}, nil
	})
	c.Handle(MethodTurnEnded, func(_ context.Context, params json.RawMessage) (any, error) {
		v, err := DecodeLink(MethodTurnEnded, params)
		if err != nil {
			return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
		}
		n := v.(TurnEndedParams)
		s.dispatch(func(w Watcher) { w.TurnEnded(TurnEndedEvent{Turn: n.Turn, Final: n.Final, AtMS: n.AtMS}) })
		return struct{}{}, nil
	})
	c.Handle(MethodToolResult, func(_ context.Context, params json.RawMessage) (any, error) {
		v, err := DecodeLink(MethodToolResult, params)
		if err != nil {
			return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
		}
		n := v.(ToolResultParams)
		s.dispatch(func(w Watcher) { w.ToolResult(ToolResultEvent{Lease: n.Lease, Text: n.Text, Frontend: n.Frontend}) })
		return struct{}{}, nil
	})
	if err := c.Start(ctx, fakectl.HelloParams{Role: fakectl.RoleAdapter, ID: "adapter", PID: int64(os.Getpid())}); err != nil {
		_ = c.Close()
		return nil, err
	}
	state, err := s.checkIdentity(ctx)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	if requireAlive && !state.Alive {
		_ = c.Close()
		return nil, ErrNotAlive
	}
	l.mu.Lock()
	l.clients = append(l.clients, c)
	l.mu.Unlock()
	return s, nil
}

// remoteSession is a live session in a runtime process.
type remoteSession struct {
	info   ProcessInfo
	client *fakectl.Client
	record string

	mu      sync.Mutex
	watcher Watcher
}

func (s *remoteSession) Ref() string      { return s.info.Ref }
func (s *remoteSession) Endpoint() string { return s.info.Endpoint }
func (s *remoteSession) PID() int64       { return s.info.PID }
func (s *remoteSession) StartKey() string { return s.info.StartKey }

// Watch sets the watcher and tells the process that this adapter is in charge of
// the session, which starts the session's first turn if it has not started.
func (s *remoteSession) Watch(w Watcher) {
	s.mu.Lock()
	s.watcher = w
	s.mu.Unlock()
	if w == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A process that is gone has no first turn to start; the error says nothing
	// the next call on the session would not.
	_ = s.client.Call(ctx, MethodWatch, nil, nil)
}

func (s *remoteSession) Created() LedgerRef {
	return LedgerRef{Writer: "runtime/" + s.info.Ref, Seq: s.info.CreatedSeq}
}

// checkIdentity asks the process who it is and compares it with the info file.
func (s *remoteSession) checkIdentity(ctx context.Context) (StateReply, error) {
	var st StateReply
	if err := s.client.Call(ctx, MethodState, nil, &st); err != nil {
		return StateReply{}, err
	}
	if st.Ref != s.info.Ref || st.PID != s.info.PID || st.StartKey != s.info.StartKey {
		return StateReply{}, fmt.Errorf("%w: wanted %s pid %d key %s, got %s pid %d key %s", errWrongSession, s.info.Ref, s.info.PID, s.info.StartKey, st.Ref, st.PID, st.StartKey)
	}
	return st, nil
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
		if errors.Is(err, peer.ErrClosed) {
			return ErrNotAlive // the process is gone: its connection ended
		}
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
		if errors.Is(remoteError(err), ErrNotAlive) {
			// The process ended since the connection was made: its record is
			// complete, and is what a native runtime leaves behind.
			return (&deadSession{info: s.info, record: s.record}).Reconcile(attempt)
		}
		return Reconciliation{}, remoteError(err)
	}
	return Reconciliation{Found: r.Found, Turn: r.Turn, TurnState: r.TurnState, TurnRunning: r.TurnRunning, WriterStopped: r.WriterStopped}, nil
}

// deadSession is a session whose process is gone: only its record is left.
type deadSession struct {
	info   ProcessInfo
	record string
}

func (s *deadSession) Ref() string      { return s.info.Ref }
func (s *deadSession) Endpoint() string { return s.info.Endpoint }
func (s *deadSession) PID() int64       { return s.info.PID }
func (s *deadSession) StartKey() string { return s.info.StartKey }
func (s *deadSession) Alive() bool      { return false }
func (s *deadSession) Created() LedgerRef {
	return LedgerRef{Writer: "runtime/" + s.info.Ref, Seq: s.info.CreatedSeq}
}
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
