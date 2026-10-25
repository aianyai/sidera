package fakeruntime

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Runtime holds native sessions in process. It is safe for concurrent use.
type Runtime struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	sessions  map[string]*session
	byAttempt map[string]*session
	nextPID   int64
	busy      int // turns with actions that are not finished
}

var (
	_ Launcher   = (*Runtime)(nil)
	_ Controller = (*Runtime)(nil)
)

// New returns a Runtime. The record directory is created.
func New(cfg Config) (*Runtime, error) {
	switch {
	case cfg.Now == nil:
		return nil, errors.New("fakeruntime: a runtime needs the test clock")
	case cfg.Ledgers == nil:
		return nil, errors.New("fakeruntime: a runtime needs a ledger")
	case cfg.Dir == "":
		return nil, errors.New("fakeruntime: a runtime needs a record directory")
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runtime{cfg: cfg, ctx: ctx, cancel: cancel, sessions: map[string]*session{}, byAttempt: map[string]*session{}, nextPID: 100000}, nil
}

// Close stops every running action and waits for them. Sessions keep their
// records; nothing is deleted.
func (r *Runtime) Close() error {
	r.cancel()
	r.wg.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	for _, s := range r.sessions {
		errs = append(errs, s.closeFile())
	}
	return errors.Join(errs...)
}

// nativeRef derives the reference of the session made for a creation attempt
// from the seed and the attempt: the same attempt always gets the same session.
func nativeRef(seed uint64, attempt string) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], seed)
	sum := sha256.Sum256(append(b[:], attempt...))
	return "n-" + hex.EncodeToString(sum[:4])
}

// Create makes a session for the attempt, or returns the one an earlier call of
// the same attempt made. The ledger entry that says the session exists is
// written before Create returns.
func (r *Runtime) Create(ctx context.Context, req CreateRequest) (Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := req.validate(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.byAttempt[req.Attempt]; ok {
		return s, nil
	}
	ref := nativeRef(r.cfg.Seed, req.Attempt)
	if _, dup := r.sessions[ref]; dup {
		return nil, fmt.Errorf("fakeruntime: native reference %s is taken", ref)
	}
	w, err := r.cfg.Ledgers.Open("runtime/"+ref, r.cfg.PID, r.cfg.Now)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(r.cfg.Dir, ref+".jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	r.nextPID++
	sum := sha256.Sum256([]byte(ref))
	s := &session{
		rt: r, ref: ref, endpoint: req.Endpoint, preset: req.Preset, env: req.Env,
		pid: r.nextPID, startKey: "k-" + hex.EncodeToString(sum[:6]),
		ledger: w, file: f, alive: true,
		seen: map[string]bool{}, attemptTurn: map[string]string{}, turnState: map[string]string{},
	}
	if err := s.record(Event{Kind: EventSession, Endpoint: req.Endpoint, Attempt: req.Attempt}); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := s.ledger.Append(ledger.KindRuntimeSessionCreated, ledger.Attrs{Attempt: req.Attempt, NativeRef: ref, PID: s.pid}); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := s.acceptPromptLocked(req.FirstAttempt, req.FirstPrompt); err != nil {
		_ = f.Close()
		return nil, err
	}
	r.sessions[ref] = s
	r.byAttempt[req.Attempt] = s
	return s, nil
}

// Find returns the session with a native reference, dead or alive: a native
// session's record outlives its process.
func (r *Runtime) Find(ref string) (Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[ref]
	if !ok {
		return nil, false
	}
	return s, true
}

func (r *Runtime) session(ref string) (*session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[ref]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownSession, ref)
	}
	return s, nil
}

// SetPlan sets the plan of the session's next turn.
func (r *Runtime) SetPlan(ref string, plan TurnPlan) error {
	if err := plan.validate(); err != nil {
		return err
	}
	s, err := r.session(ref)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.plan = plan
	s.mu.Unlock()
	return nil
}

// StartTurn starts a turn now, with the plan that was set. It belongs to the
// latest prompt that waits for one.
func (r *Runtime) StartTurn(ref string) error {
	s, err := r.session(ref)
	if err != nil {
		return err
	}
	return s.startTurn()
}

// EndTurn ends the running turn.
func (r *Runtime) EndTurn(ref, final string) error {
	s, err := r.session(ref)
	if err != nil {
		return err
	}
	return s.endTurn(final, false)
}

// KillNative ends the session's native process: it stops reporting, never ends
// its turn, and refuses prompts, and its record stays readable.
func (r *Runtime) KillNative(ref string) error {
	s, err := r.session(ref)
	if err != nil {
		return err
	}
	return s.kill()
}

// Idle is true when no turn has an action running or waiting to run. The fixture
// waits for it before it judges the state of the system.
func (r *Runtime) Idle() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.busy == 0
}

func (r *Runtime) addBusy(n int) {
	r.mu.Lock()
	r.busy += n
	r.mu.Unlock()
}

// --- session ---

type session struct {
	rt       *Runtime
	ref      string
	endpoint string
	preset   string
	env      map[string]string
	pid      int64
	startKey string
	ledger   *ledger.Writer

	evMu sync.Mutex // serialises events to the watcher
	mu   sync.Mutex
	file *os.File
	seq  int

	alive       bool
	watcher     Watcher
	plan        TurnPlan
	seen        map[string]bool   // prompt attempts in the record
	pending     []string          // prompt attempts that wait for a turn
	attemptTurn map[string]string // attempt -> turn
	turnState   map[string]string // turn -> running, completed, ...
	turn        string            // the running turn, or empty
	turnN       int
	turnCancel  context.CancelFunc
}

func (s *session) Ref() string      { return s.ref }
func (s *session) Endpoint() string { return s.endpoint }
func (s *session) PID() int64       { return s.pid }
func (s *session) StartKey() string { return s.startKey }

func (s *session) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive
}

func (s *session) TurnActive() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.alive || s.turn == "" {
		return "", false
	}
	return s.turn, true
}

func (s *session) Watch(w Watcher) {
	s.evMu.Lock()
	s.watcher = w
	s.evMu.Unlock()
}

// record appends an event to the session's record file, stamping it. The caller
// holds s.mu or is the only user of the session.
func (s *session) record(e Event) error {
	s.seq++
	e.Seq = s.seq
	e.AtMS = s.rt.cfg.Now().UnixMilli()
	line, err := EncodeEvent(e)
	if err != nil {
		s.seq--
		return err
	}
	if _, err := s.file.Write(append(line, '\n')); err != nil {
		s.seq--
		return err
	}
	return nil
}

func (s *session) closeFile() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// acceptPromptLocked records a prompt: ledger first, then the record. The caller
// holds s.mu, or owns a session nobody else can see yet.
func (s *session) acceptPromptLocked(attempt, text string) error {
	digest := ledger.Digest([]byte(text))
	if err := s.ledger.Append(ledger.KindRuntimePromptReceived, ledger.Attrs{Attempt: attempt, TextDigest: digest}); err != nil {
		return err
	}
	if err := s.record(Event{Kind: EventPrompt, Attempt: attempt, TextDigest: digest}); err != nil {
		return err
	}
	s.seen[attempt] = true
	s.pending = append(s.pending, attempt)
	return nil
}

func (s *session) Deliver(ctx context.Context, attempt, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if !s.alive {
		s.mu.Unlock()
		return ErrNotAlive
	}
	if s.seen[attempt] {
		s.mu.Unlock()
		return nil
	}
	if s.turn != "" {
		s.mu.Unlock()
		return ErrBusy
	}
	if err := s.acceptPromptLocked(attempt, text); err != nil {
		s.mu.Unlock()
		return err
	}
	manual := s.plan.ManualStart
	s.mu.Unlock()
	if manual {
		return nil
	}
	return s.startTurn()
}

// startTurn starts a turn with the plan that was set, for the latest prompt that
// waits for one.
func (s *session) startTurn() error {
	s.mu.Lock()
	if !s.alive {
		s.mu.Unlock()
		return ErrNotAlive
	}
	if s.turn != "" {
		s.mu.Unlock()
		return ErrBusy
	}
	plan := s.plan
	s.plan = TurnPlan{}
	s.turnN++
	turn := fmt.Sprintf("t%d", s.turnN)
	attempt := ""
	if n := len(s.pending); n > 0 {
		attempt = s.pending[n-1]
	}
	for _, a := range s.pending {
		s.attemptTurn[a] = turn
	}
	s.pending = nil
	s.turn = turn
	s.turnState[turn] = "running"
	ctx, cancel := context.WithCancel(s.rt.ctx)
	s.turnCancel = cancel
	if err := s.ledger.Append(ledger.KindRuntimeTurnStarted, ledger.Attrs{Turn: turn, Attempt: attempt}); err != nil {
		s.mu.Unlock()
		cancel()
		return err
	}
	if err := s.record(Event{Kind: EventTurnStarted, Turn: turn, Attempt: attempt}); err != nil {
		s.mu.Unlock()
		cancel()
		return err
	}
	atMS := s.rt.cfg.Now().UnixMilli()
	withActions := len(plan.Actions) > 0 || plan.EndAfter
	if withActions {
		s.rt.addBusy(1)
	}
	s.mu.Unlock()

	s.notify(func(w Watcher) { w.TurnStarted(TurnStartedEvent{Turn: turn, Attempt: attempt, AtMS: atMS}) })
	if withActions {
		s.rt.wg.Add(1)
		go func() {
			defer s.rt.wg.Done()
			defer s.rt.addBusy(-1)
			s.runActions(ctx, turn, plan)
		}()
	}
	return nil
}

// notify hands an event to the watcher, one event at a time, outside s.mu.
func (s *session) notify(f func(Watcher)) {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	if s.watcher != nil {
		f(s.watcher)
	}
}

// endTurn ends the running turn. interrupted marks the end as an interruption
// that is recorded as one.
func (s *session) endTurn(final string, interrupted bool) error {
	s.mu.Lock()
	if !s.alive {
		s.mu.Unlock()
		return ErrNotAlive
	}
	if s.turn == "" {
		s.mu.Unlock()
		return ErrNoTurn
	}
	turn := s.turn
	s.turnCancel()
	s.turn, s.turnCancel = "", nil
	s.turnState[turn] = final
	if interrupted {
		if err := s.ledger.Append(ledger.KindRuntimeInterrupted, ledger.Attrs{}); err != nil {
			s.mu.Unlock()
			return err
		}
		if err := s.record(Event{Kind: EventInterrupted}); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	if err := s.ledger.Append(ledger.KindRuntimeTurnEnded, ledger.Attrs{Turn: turn}); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.record(Event{Kind: EventTurnEnded, Turn: turn, Final: final}); err != nil {
		s.mu.Unlock()
		return err
	}
	atMS := s.rt.cfg.Now().UnixMilli()
	s.mu.Unlock()
	s.notify(func(w Watcher) { w.TurnEnded(TurnEndedEvent{Turn: turn, Final: final, AtMS: atMS}) })
	return nil
}

func (s *session) Interrupt(ctx context.Context) (InterruptResult, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	err := s.endTurn(FinalInterrupted, true)
	switch {
	case err == nil:
		return InterruptStopped, nil
	case errors.Is(err, ErrNoTurn):
		return InterruptNotRunning, nil
	}
	return 0, err
}

func (s *session) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.Alive() {
		return nil // already closed, or killed: there is nothing left to release
	}
	if err := s.endTurn(FinalInterrupted, true); err != nil && !errors.Is(err, ErrNoTurn) {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.alive {
		return nil
	}
	if err := s.ledger.Append(ledger.KindRuntimeClosed, ledger.Attrs{}); err != nil {
		return err
	}
	if err := s.record(Event{Kind: EventClosed}); err != nil {
		return err
	}
	s.alive = false
	return nil
}

func (s *session) kill() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.alive {
		return ErrNotAlive
	}
	if err := s.ledger.Append(ledger.KindRuntimeKilled, ledger.Attrs{}); err != nil {
		return err
	}
	if err := s.record(Event{Kind: EventKilled}); err != nil {
		return err
	}
	s.alive = false
	if s.turnCancel != nil {
		s.turnCancel()
	}
	return nil
}

func (s *session) Reconcile(attempt string) (Reconciliation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := Reconciliation{WriterStopped: !s.alive}
	if !s.seen[attempt] {
		rec.TurnRunning = s.alive && s.turn != ""
		return rec, nil
	}
	rec.Found = true
	if turn, ok := s.attemptTurn[attempt]; ok {
		rec.Turn, rec.TurnState = turn, s.turnState[turn]
	}
	return rec, nil
}

// runActions is what the model does in a turn: the actions in order, then, if the
// plan says so, the end of the turn. It stops at once when the turn is ended,
// interrupted or killed.
func (s *session) runActions(ctx context.Context, turn string, plan TurnPlan) {
	info := SessionInfo{Ref: s.ref, Endpoint: s.endpoint, Preset: s.preset, Env: s.env}
	for _, action := range plan.Actions {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		err := s.record(Event{Kind: EventToolCall, Turn: turn, Action: action.Kind})
		s.mu.Unlock()
		if err != nil {
			return
		}
		var res ActionResult
		if s.rt.cfg.Runner == nil {
			err = errors.New("no action runner")
		} else {
			res, err = s.rt.cfg.Runner.RunAction(ctx, info, action)
		}
		if ctx.Err() != nil {
			return
		}
		s.finishAction(turn, action, res, err)
	}
	if plan.EndAfter && ctx.Err() == nil {
		_ = s.endTurn(FinalCompleted, false)
	}
}

// finishAction records the result of an action and, for a recv, what the model
// saw and what the adapter may observe.
func (s *session) finishAction(turn string, action TurnAction, res ActionResult, runErr error) {
	ev := Event{Kind: EventToolResult, Turn: turn, Action: action.Kind}
	var page adapterproto.RecvPage
	isPage := false
	switch {
	case runErr != nil:
		ev.Error = runErr.Error()
	case len(res.Output) > 0:
		ev.Text = string(res.Output)
		// A receive is a recv action, or a command whose action names the front
		// end it was received through.
		if action.Kind == ActionRecv || (action.Kind == ActionExec && action.Args["frontend"] != "") {
			if err := adapterproto.Decode(res.Output, &page); err == nil {
				isPage = true
				ev.Lease = string(page.Lease)
				ev.Frontend = action.Args["frontend"]
				if ev.Frontend == "" {
					ev.Frontend = string(adapterproto.FrontendCLI)
				}
			}
		}
	}
	if isPage {
		for _, attrs := range ModelSaw(s.endpoint, page, s.rt.cfg.Feedback) {
			if err := s.ledger.Append(ledger.KindModelSaw, attrs); err != nil {
				return
			}
		}
	}
	s.mu.Lock()
	err := s.record(ev)
	s.mu.Unlock()
	if err != nil {
		return
	}
	if isPage {
		s.notify(func(w Watcher) { w.ToolResult(ToolResultEvent{Lease: ev.Lease, Text: ev.Text, Frontend: ev.Frontend}) })
	}
}

// ModelSaw returns the ledger attributes of what the model saw on a page, one
// entry per message or part: where it came from, a digest of its body (never the
// body), which part of how many, whether the page marked it as a possible
// duplicate, and, when feedback classifies the message as one of the node's own
// notices, which kind. Whoever hands a page to a model records it with this one
// function, so that the oracle reads the same entries however the page arrived.
func ModelSaw(endpoint string, page adapterproto.RecvPage, feedback func(from, body string) (string, bool)) []ledger.Attrs {
	out := make([]ledger.Attrs, 0, len(page.Messages))
	for _, m := range page.Messages {
		dup := m.PossiblyDuplicate
		attrs := ledger.Attrs{
			Endpoint: endpoint, MessageID: string(m.ID), From: string(m.From),
			BodyDigest: ledger.Digest([]byte(m.Body)), PossiblyDuplicate: &dup,
		}
		if m.Part != nil {
			attrs.Part, attrs.Parts = m.Part.Index, m.Part.Total
		}
		if feedback != nil {
			if kind, ok := feedback(string(m.From), m.Body); ok {
				attrs.Feedback = kind
			}
		}
		out = append(out, attrs)
	}
	return out
}
