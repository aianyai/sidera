package fakeruntime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

// Errors a session reports. They are values, not strings, so the adapter that
// drives a session maps each to the protocol's own answer.
var (
	// ErrBusy: a prompt arrived while a turn was running. A real runtime has one
	// writer per session, and so does this one.
	ErrBusy = errors.New("fakeruntime: a turn is running")
	// ErrNotAlive: the session was closed or its native process was killed.
	ErrNotAlive = errors.New("fakeruntime: the session is not alive")
	// ErrUnknownSession: no session has this native reference.
	ErrUnknownSession = errors.New("fakeruntime: unknown session")
	// ErrNoTurn: no turn is running.
	ErrNoTurn = errors.New("fakeruntime: no turn is running")
)

// The actions a model can take inside a turn.
const (
	ActionRecv     = "recv"
	ActionRecvPart = "recv_part"
	ActionAck      = "ack"
	ActionReply    = "reply"
	ActionSend     = "send"
	ActionControl  = "control"
	ActionDeclare  = "declare"
	// ActionExec runs a command line, for runners that start real programs; its
	// Args hold the argument vector.
	ActionExec = "exec"
)

var actionKinds = []string{ActionRecv, ActionRecvPart, ActionAck, ActionReply, ActionSend, ActionControl, ActionDeclare, ActionExec}

// TurnAction is one thing the model does inside a turn. The arguments are the
// runner's: the fake runtime passes them on unread, except that for ActionRecv it
// reads "frontend" (cli or mcp, default cli) to say how the result reached the
// model.
type TurnAction struct {
	Kind string            `json:"kind"`
	Args map[string]string `json:"args,omitempty"`
}

// TurnPlan says how the next turn goes. It is consumed when that turn starts.
type TurnPlan struct {
	// ManualStart holds back the start of a turn that a prompt would start by
	// itself: the turn then starts when the script calls StartTurn. The zero
	// value starts a turn as soon as a prompt arrives.
	ManualStart bool `json:"manual_start,omitempty"`
	// Actions are what the model does in the turn, in order.
	Actions []TurnAction `json:"actions,omitempty"`
	// EndAfter ends the turn, completed, when the last action is done. The zero
	// value leaves the end to the script.
	EndAfter bool `json:"end_after,omitempty"`
}

func (p TurnPlan) validate() error {
	for _, a := range p.Actions {
		if !slices.Contains(actionKinds, a.Kind) {
			return fmt.Errorf("fakeruntime: unknown action kind %q", a.Kind)
		}
	}
	return nil
}

// SessionInfo tells an ActionRunner which session an action belongs to.
type SessionInfo struct {
	Ref      string
	Endpoint string
	Preset   string
	// Env is the tool access the session was created with: the variables a real
	// runtime would put in the environment of the agent's tools.
	Env map[string]string
}

// ActionResult is what an action produced. Output is the text the model was
// shown: for ActionRecv, the page as the command or tool printed it.
type ActionResult struct {
	Output []byte
}

// ActionRunner carries out one action of a turn on behalf of the model.
type ActionRunner interface {
	RunAction(ctx context.Context, session SessionInfo, action TurnAction) (ActionResult, error)
}

// TurnStartedEvent and the two events below are what a session tells the adapter
// that watches it. Times are Unix milliseconds on the shared test clock.
type TurnStartedEvent struct {
	Turn    string
	Attempt string // the prompt attempt that caused the turn, or empty
	AtMS    int64
}

// TurnEndedEvent reports the end of a turn: Final is one of the Final constants.
type TurnEndedEvent struct {
	Turn  string
	Final string
	AtMS  int64
}

// ToolResultEvent reports the result of a recv the model received: the page as
// the model saw it, with the lease it names.
type ToolResultEvent struct {
	Lease    string
	Text     string
	Frontend string
}

// Watcher receives the events of a session, one at a time and in order.
type Watcher interface {
	TurnStarted(TurnStartedEvent)
	TurnEnded(TurnEndedEvent)
	ToolResult(ToolResultEvent)
}

// InterruptResult is how an interrupt ended.
type InterruptResult int

// The results of an interrupt.
const (
	InterruptStopped InterruptResult = iota + 1
	InterruptNotRunning
)

// Reconciliation is what the record says about a prompt attempt.
type Reconciliation struct {
	// Found: the attempt is in the record.
	Found bool
	// Turn and TurnState: the turn the attempt started. Both are empty for a
	// prompt that is in the record and waits for a turn. TurnState is running,
	// completed, interrupted or failed.
	Turn      string
	TurnState string
	// TurnRunning: a turn is running now, so an attempt that is not in the record
	// may still arrive.
	TurnRunning bool
	// WriterStopped: the session's process has ended, so the record is complete.
	WriterStopped bool
}

// CreateRequest asks for a native session.
type CreateRequest struct {
	// Attempt is the creation attempt. The same attempt gets the same session.
	Attempt string
	// Endpoint is the session endpoint the daemon allocated.
	Endpoint string
	Preset   string
	// FirstAttempt and FirstPrompt are the first prompt, which is in the record
	// when the session is ready.
	FirstAttempt string
	FirstPrompt  string
	// Env is the tool access of the session.
	Env map[string]string
}

func (r CreateRequest) validate() error {
	for name, v := range map[string]string{"attempt": r.Attempt, "endpoint": r.Endpoint, "preset": r.Preset, "first attempt": r.FirstAttempt, "first prompt": r.FirstPrompt} {
		if v == "" {
			return fmt.Errorf("fakeruntime: create request has no %s", name)
		}
	}
	return nil
}

// Session is a native session as an adapter drives it.
type Session interface {
	Ref() string
	Endpoint() string
	PID() int64
	StartKey() string
	// Alive is false after a close and after the native process was killed.
	Alive() bool
	// TurnActive says whether a turn is running, and which.
	TurnActive() (turn string, active bool)
	// Deliver puts a prompt into the session. The same attempt is delivered at
	// most once. ErrBusy while a turn runs, ErrNotAlive for a dead session.
	Deliver(ctx context.Context, attempt, text string) error
	// Interrupt ends a running turn.
	Interrupt(ctx context.Context) (InterruptResult, error)
	// Close releases the session; a running turn is interrupted first.
	Close(ctx context.Context) error
	// Reconcile reads the record for an attempt.
	Reconcile(attempt string) (Reconciliation, error)
	// Watch sets the one watcher of the session's events.
	Watch(Watcher)
}

// Launcher starts or finds native sessions. A Runtime is one, in process; a
// ProcessLauncher starts a program per session.
type Launcher interface {
	Create(ctx context.Context, req CreateRequest) (Session, error)
	Find(ref string) (Session, bool)
}

// Controller is the script's hold on the turns of the sessions: the fixture
// starts and ends turns and sets the plan of the next one. A Runtime is one, in
// process; RemoteController drives a runtime process.
type Controller interface {
	SetPlan(ref string, plan TurnPlan) error
	StartTurn(ref string) error
	EndTurn(ref string, final string) error
	KillNative(ref string) error
	// Idle is true when no turn action is running or waiting to start.
	Idle() bool
}

// Config configures a Runtime.
type Config struct {
	// Now is the shared test clock.
	Now func() time.Time
	// Ledgers opens the ledger writer of each session ("runtime/<native ref>").
	Ledgers ledger.Factory
	// Dir holds the session records, "<native ref>.jsonl".
	Dir string
	// Seed makes native references and process identities deterministic.
	Seed uint64
	// Runner carries out the actions of turns. Without one an action fails.
	Runner ActionRunner
	// Feedback classifies a message of a page as feedback from the node itself and
	// names its kind; nil classifies nothing. The format of such a message is the
	// engine's, so the classifier is the engine side's to supply.
	Feedback func(from, body string) (kind string, ok bool)
	// PID is the process id the ledger writers record: 0 in the test process, the
	// real id in a runtime process.
	PID int
}
