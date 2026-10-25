package fakeruntime

import (
	"fmt"
	"regexp"
	"slices"
	"unicode/utf8"

	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// The link between an adapter and a runtime that runs as a process of its own:
// requests the adapter makes, notifications the runtime sends it, and the knobs
// the fixture turns. They travel over the control channel (package fakectl).
const (
	MethodDeliver   = "runtime.deliver"
	MethodInterrupt = "runtime.interrupt"
	MethodClose     = "runtime.close"
	MethodReconcile = "runtime.reconcile"
	MethodState     = "runtime.state"
	// MethodWatch makes the caller the adapter that is in charge of the session: its
	// first turn starts, and every adapter connected hears the turn events.
	MethodWatch = "runtime.watch"

	// The three requests the runtime makes of the adapter. They are requests and
	// not notifications: the adapter answers only after it has handed the event to
	// the daemon, so that the runtime counts as busy until the daemon's side of it
	// is on its way. A notification would let the runtime turn idle while the event
	// was still unhandled, and a fixture that waits for quiet would advance the clock
	// before the daemon had heard of the turn.
	MethodTurnStarted = "runtime.turn.started"
	MethodTurnEnded   = "runtime.turn.ended"
	MethodToolResult  = "runtime.tool.result"

	KnobPlan      = "plan"
	KnobTurnStart = "turn.start"
	KnobTurnEnd   = "turn.end"
	KnobKill      = "kill"
	KnobIdle      = "idle"
	// KnobFirstTurn is sent to an adapter, not to a runtime process: it sets the
	// plan of the first turn of the sessions the adapter creates from then on.
	KnobFirstTurn = "first-turn"
)

// Error codes of the link: a refusal a session made, so the other side can answer
// with the same error value.
const (
	CodeBusy     = 2001
	CodeNotAlive = 2002
	CodeNoTurn   = 2003
)

var (
	attemptPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	refPattern     = regexp.MustCompile(`^n-[0-9a-f]{8}$`)
)

// DeliverParams puts a prompt into the session.
type DeliverParams struct {
	Attempt string `json:"attempt"`
	Text    string `json:"text"`
}

// ReconcileParams asks what the record says about an attempt.
type ReconcileParams struct {
	Attempt string `json:"attempt"`
}

// InterruptReply is the answer to an interrupt: "stopped" or "not_running".
type InterruptReply struct {
	Result string `json:"result"`
}

// StateReply is the state of a session, with the identity of the process that
// answers: a caller that connected to an address checks that it reached the
// session it meant, because a port that was released can be taken by the next
// process that asks for one.
type StateReply struct {
	Ref      string `json:"ref"`
	PID      int64  `json:"pid"`
	StartKey string `json:"start_key"`
	Alive    bool   `json:"alive"`
	Active   bool   `json:"active"`
	Turn     string `json:"turn,omitempty"`
}

// ReconcileReply is a Reconciliation on the wire.
type ReconcileReply struct {
	Found         bool   `json:"found"`
	Turn          string `json:"turn,omitempty"`
	TurnState     string `json:"turn_state,omitempty"`
	TurnRunning   bool   `json:"turn_running"`
	WriterStopped bool   `json:"writer_stopped"`
}

// TurnEndKnob is the argument of the knob that ends a turn.
type TurnEndKnob struct {
	Final string `json:"final"`
}

// IdleReply is the answer of the idle knob.
type IdleReply struct {
	Idle bool `json:"idle"`
}

// PlanKnob is the argument of the knob that sets the plan of the next turn.
type PlanKnob struct {
	ManualStart bool         `json:"manual_start,omitempty"`
	Actions     []TurnAction `json:"actions,omitempty"`
	EndAfter    bool         `json:"end_after,omitempty"`
}

// DecodePlanKnob reads the arguments of a knob that carries a plan: absent for the
// zero plan.
func DecodePlanKnob(name string, args []byte) (PlanKnob, error) {
	if args == nil {
		return PlanKnob{}, nil
	}
	v, err := DecodeLink("knob."+name, args)
	if err != nil {
		return PlanKnob{}, err
	}
	return v.(PlanKnob), nil
}

// KnobOf is the knob that carries a plan.
func KnobOf(p TurnPlan) PlanKnob {
	return PlanKnob{ManualStart: p.ManualStart, Actions: p.Actions, EndAfter: p.EndAfter}
}

// Plan returns the plan the knob carries.
func (k PlanKnob) Plan() TurnPlan {
	return TurnPlan{ManualStart: k.ManualStart, Actions: k.Actions, EndAfter: k.EndAfter}
}

// ProcessInfo is what a runtime process writes about itself, in
// "<native ref>.json" beside the session record, so that an adapter that was
// restarted finds the process that holds its session.
type ProcessInfo struct {
	Ref      string `json:"ref"`
	Endpoint string `json:"endpoint"`
	Addr     string `json:"addr"`
	PID      int64  `json:"pid"`
	StartKey string `json:"start_key"`
	// CreatedSeq is the sequence number of the runtime's ledger entry that says the
	// session exists (writer "runtime/<ref>"), for the adapter to name in its own.
	CreatedSeq int64 `json:"created_seq"`
}

func (p ProcessInfo) validate() error {
	switch {
	case !refPattern.MatchString(p.Ref):
		return fmt.Errorf("ref %q is not a native reference", p.Ref)
	case p.Endpoint == "" || p.Addr == "" || p.StartKey == "":
		return fmt.Errorf("endpoint, addr and start_key are required")
	case p.PID < 1:
		return fmt.Errorf("pid %d is not a process id", p.PID)
	case p.CreatedSeq < 1:
		return fmt.Errorf("created_seq %d is not a sequence number", p.CreatedSeq)
	}
	return nil
}

// DecodeProcessInfo reads the info file of a runtime process strictly.
func DecodeProcessInfo(data []byte) (ProcessInfo, error) {
	var p ProcessInfo
	if err := strictjson.Decode(data, &p); err != nil {
		return ProcessInfo{}, err
	}
	return p, p.validate()
}

func (p DeliverParams) validate() error {
	if !attemptPattern.MatchString(p.Attempt) {
		return fmt.Errorf("attempt %q is not a UUID version 4", p.Attempt)
	}
	if !utf8.ValidString(p.Text) {
		return fmt.Errorf("text is not UTF-8")
	}
	return nil
}

func (p ReconcileParams) validate() error {
	if !attemptPattern.MatchString(p.Attempt) {
		return fmt.Errorf("attempt %q is not a UUID version 4", p.Attempt)
	}
	return nil
}

func (r InterruptReply) validate() error {
	if r.Result != "stopped" && r.Result != "not_running" {
		return fmt.Errorf("result %q is neither stopped nor not_running", r.Result)
	}
	return nil
}

func (s StateReply) validate() error {
	if !refPattern.MatchString(s.Ref) || s.PID < 1 || s.StartKey == "" {
		return fmt.Errorf("a state needs the identity of the session: ref, pid and start_key")
	}
	if s.Active != (s.Turn != "") {
		return fmt.Errorf("active and turn disagree")
	}
	return nil
}

func (r ReconcileReply) validate() error {
	if r.TurnState != "" && !slices.Contains([]string{"running", "completed", "interrupted", "failed"}, r.TurnState) {
		return fmt.Errorf("turn_state %q is not a turn state", r.TurnState)
	}
	if (r.Turn == "") != (r.TurnState == "") {
		return fmt.Errorf("turn and turn_state go together")
	}
	if r.Turn != "" && !r.Found {
		return fmt.Errorf("a turn without a found attempt")
	}
	return nil
}

func (k TurnEndKnob) validate() error {
	if !slices.Contains([]string{FinalCompleted, FinalInterrupted, FinalFailed}, k.Final) {
		return fmt.Errorf("final %q is not completed, interrupted or failed", k.Final)
	}
	return nil
}

func (IdleReply) validate() error { return nil }

func (k PlanKnob) validate() error { return k.Plan().validate() }

// Validate checks that every action of the plan is one a turn can take.
func (k PlanKnob) Validate() error { return k.validate() }

// TurnStartedParams, TurnEndedParams and ToolResultParams are the requests a
// runtime process makes of the adapters that watch its session.
type TurnStartedParams struct {
	Turn    string `json:"turn"`
	Attempt string `json:"attempt,omitempty"`
	AtMS    int64  `json:"at_ms"`
}

// TurnEndedParams reports the end of a turn.
type TurnEndedParams struct {
	Turn  string `json:"turn"`
	Final string `json:"final"`
	AtMS  int64  `json:"at_ms"`
}

// ToolResultParams reports the result of a recv the model received.
type ToolResultParams struct {
	Lease    string `json:"lease"`
	Text     string `json:"text"`
	Frontend string `json:"frontend"`
}

func (n TurnStartedParams) validate() error {
	if n.Turn == "" || n.AtMS < 0 {
		return fmt.Errorf("a turn start needs a turn and a time")
	}
	return nil
}

func (n TurnEndedParams) validate() error {
	if n.Turn == "" || n.AtMS < 0 {
		return fmt.Errorf("a turn end needs a turn and a time")
	}
	return TurnEndKnob{Final: n.Final}.validate()
}

func (n ToolResultParams) validate() error {
	if n.Lease == "" || n.Frontend == "" {
		return fmt.Errorf("a tool result needs a lease and a front end")
	}
	return nil
}

type validated interface{ validate() error }

func decode[T validated](data []byte) (any, error) {
	var v T
	if err := strictjson.Decode(data, &v); err != nil {
		return nil, err
	}
	if err := v.validate(); err != nil {
		return nil, err
	}
	return v, nil
}

// DecodeLink strictly decodes the params or the result of a method of the link:
// for a request, the params of the request; the answers of
// the requests have the names of their method followed by ".reply", and the
// knob arguments are the names of the knobs prefixed "knob.". The value is the
// type itself, not a pointer.
func DecodeLink(name string, data []byte) (any, error) {
	switch name {
	case MethodDeliver:
		return decode[DeliverParams](data)
	case MethodReconcile:
		return decode[ReconcileParams](data)
	case MethodInterrupt + ".reply":
		return decode[InterruptReply](data)
	case MethodState + ".reply":
		return decode[StateReply](data)
	case MethodReconcile + ".reply":
		return decode[ReconcileReply](data)
	case "knob." + KnobPlan:
		return decode[PlanKnob](data)
	case "knob." + KnobTurnEnd:
		return decode[TurnEndKnob](data)
	case "knob." + KnobIdle + ".reply":
		return decode[IdleReply](data)
	case "knob." + KnobFirstTurn:
		return decode[PlanKnob](data)
	case MethodTurnStarted:
		return decode[TurnStartedParams](data)
	case MethodTurnEnded:
		return decode[TurnEndedParams](data)
	case MethodToolResult:
		return decode[ToolResultParams](data)
	}
	return nil, fmt.Errorf("fakeruntime: unknown link message %q", name)
}
