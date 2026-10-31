// Package create decides the next step of creating one Codex session, as a pure
// function of what is stored for the attempt and what was just observed. The
// caller observes, calls Decide, does what it says and observes again; the same
// function serves the first run, a retry and the check after a restart, so there
// is one set of rules.
//
// What is stored (the checkpoint) is only the thread's id and, for a final
// failure, its reason. That the first prompt reached the thread is not stored: the
// thread's record shows it, and the record cannot disagree with itself.
package create

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// CheckpointVersion is the format version of the checkpoint.
const CheckpointVersion = 1

// State is what is stored for an attempt.
type State struct {
	// ThreadID is the thread the attempt started; empty until it did.
	ThreadID string
	// Failed is the reason the attempt ended in failure; empty while it has not.
	// A failure is final: every later request of the attempt gets the same one.
	Failed adapterproto.CreateFailure
}

type checkpoint struct {
	V        int                        `json:"v"`
	ThreadID string                     `json:"thread_id,omitempty"`
	Failed   adapterproto.CreateFailure `json:"failed,omitempty"`
}

// Checkpoint is the state as the daemon stores it.
func (s State) Checkpoint() json.RawMessage {
	raw, _ := json.Marshal(checkpoint{V: CheckpointVersion, ThreadID: s.ThreadID, Failed: s.Failed})
	return raw
}

// ParseCheckpoint reads a checkpoint the daemon handed back. An empty one is the
// state of an attempt that stored nothing.
func ParseCheckpoint(raw json.RawMessage) (State, error) {
	if len(raw) == 0 {
		return State{}, nil
	}
	var c checkpoint
	if err := json.Unmarshal(raw, &c); err != nil {
		return State{}, fmt.Errorf("create: unreadable checkpoint: %w", err)
	}
	if c.V != CheckpointVersion {
		return State{}, fmt.Errorf("create: checkpoint version %d is not %d", c.V, CheckpointVersion)
	}
	return State{ThreadID: c.ThreadID, Failed: c.Failed}, nil
}

// Params holds the numbers the decision uses.
type Params struct {
	// StartTimeout is how long, counted from the latest turn start, the first prompt
	// may take to reach the record while its turn runs. Past it the outcome is
	// indeterminate: the daemon asks again by repeating the attempt.
	StartTimeout time.Duration
	// MaxStarts is how many turns one attempt may start. Each one after the first
	// follows a turn that ended without recording the prompt; past the limit the
	// attempt fails instead of looping on a runtime that does not take the prompt.
	MaxStarts int
}

// DefaultParams returns the values the adapter runs with.
func DefaultParams() Params { return Params{StartTimeout: 2 * time.Minute, MaxStarts: 2} }

// MCP is whether the MCP server of the channel started in the thread.
type MCP int

// The states of the channel's MCP server.
const (
	MCPUnknown MCP = iota
	MCPReady
	MCPFailed
)

// Observed is what the caller found out just before deciding.
type Observed struct {
	// Checkpointed says the thread id is stored with the daemon.
	Checkpointed bool
	// Named says the thread carries the attempt's name, which is also what makes it
	// persistent and findable.
	Named bool
	// MCP is the state of the channel's MCP server. The caller reports MCPReady
	// when the server stayed silent past the time it is waited for: a server that
	// failed then shows as a failed tool call, not as a stuck creation.
	MCP MCP
	// PromptRecorded says the first prompt is in the thread's record.
	PromptRecorded bool
	// TurnRunning says the thread runs a turn.
	TurnRunning bool
	// TurnsStarted is how many turns this attempt has started.
	TurnsStarted int
	// Waited is the time since the latest turn start, or since the attempt began
	// when none was started.
	Waited time.Duration
}

// Action is what the caller does next.
type Action string

// The actions.
const (
	// StartThread starts a thread and records its id in the state.
	StartThread Action = "start_thread"
	// PutCheckpoint stores the state with the daemon before anything else is done
	// to the thread, so that a crash cannot lose it.
	PutCheckpoint Action = "put_checkpoint"
	// NameThread names the thread after the attempt.
	NameThread Action = "name_thread"
	// AwaitMCP waits for the channel's MCP server to start, bounded by the caller.
	AwaitMCP Action = "await_mcp"
	// StartTurn starts a turn with the first prompt, carrying the prompt's attempt id.
	StartTurn Action = "start_turn"
	// AwaitPrompt waits for the prompt to reach the record, or the turn to end.
	AwaitPrompt Action = "await_prompt"
	// Ready answers created: the prompt is in the record.
	Ready Action = "ready"
	// Fail ends the attempt with Decision.Failure. Unless State.Failed is already
	// set the caller stores it first (State.Checkpoint) and then answers.
	Fail Action = "fail"
	// Indeterminate answers that the outcome cannot be told yet: a turn runs and
	// the prompt has not appeared in the time allowed.
	Indeterminate Action = "indeterminate"
)

// Decision is the next step.
type Decision struct {
	Action Action
	// Failure is the reason of a Fail.
	Failure adapterproto.CreateFailure
}

// Decide returns the next step.
func Decide(st State, obs Observed, p Params) Decision {
	switch {
	case st.Failed != "":
		return Decision{Action: Fail, Failure: st.Failed}
	case st.ThreadID == "":
		return Decision{Action: StartThread}
	case !obs.Checkpointed:
		return Decision{Action: PutCheckpoint}
	case obs.PromptRecorded:
		return Decision{Action: Ready}
	case !obs.Named:
		return Decision{Action: NameThread}
	case obs.MCP == MCPFailed:
		return Decision{Action: Fail, Failure: adapterproto.CreateFailDependencyMissing}
	case obs.MCP == MCPUnknown:
		return Decision{Action: AwaitMCP}
	case obs.TurnRunning && obs.Waited > p.StartTimeout:
		return Decision{Action: Indeterminate}
	case obs.TurnRunning:
		return Decision{Action: AwaitPrompt}
	case obs.TurnsStarted >= p.MaxStarts:
		return Decision{Action: Fail, Failure: adapterproto.CreateFailNativeError}
	}
	return Decision{Action: StartTurn}
}
