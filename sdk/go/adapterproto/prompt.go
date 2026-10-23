package adapterproto

import "encoding/json"

// PromptDeliverParams asks the adapter to put a prompt into a session. The
// daemon records the attempt as dispatching before it sends this request.
type PromptDeliverParams struct {
	Endpoint EndpointRef `json:"endpoint"`
	Attempt  AttemptID   `json:"attempt"`
	// Text is the prompt exactly as the daemon composed it. The adapter delivers
	// it unchanged, except that it may append one line holding the attempt
	// identifier, as a fallback for reconciliation.
	Text string `json:"text"`
}

// Validate checks the parameters.
func (p PromptDeliverParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	c.field("attempt", p.Attempt.Validate())
	c.field("text", strLen(p.Text, 1, maxPromptText))
	return c.err
}

// PromptDeliverResult is the adapter's answer to prompt.deliver.
//
//   - Accepted: the runtime accepted the prompt but has not made it durable; the
//     adapter must send prompt.persisted later, or the daemon reconciles.
//   - Persisted: the prompt is in the runtime's durable record.
//   - Refused: certainly nothing was delivered; Reason says why.
//
// No answer, a timeout, CodeInternalError, CodeRequestCancelled or a closed
// connection leave the outcome unknown; the daemon then reconciles.
type PromptDeliverResult struct {
	Stage  PromptStage   `json:"stage"`
	Reason PromptRefusal `json:"reason,omitempty"`
}

// Validate checks that a reason appears exactly with a refusal.
func (r PromptDeliverResult) Validate() error {
	var c checker
	c.field("stage", enumOK(r.Stage, promptStages))
	if c.err != nil {
		return c.err
	}
	if r.Stage == PromptRefused {
		c.field("reason", enumOK(r.Reason, promptRefusals))
	} else {
		c.ok(r.Reason == "", "reason: only a refusal has one")
	}
	return c.err
}

// PromptPersistedParams tells the daemon that a prompt it was told was only
// accepted is now in the runtime's durable record.
type PromptPersistedParams struct {
	Endpoint EndpointRef `json:"endpoint"`
	Attempt  AttemptID   `json:"attempt"`
}

// Validate checks the parameters.
func (p PromptPersistedParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	c.field("attempt", p.Attempt.Validate())
	return c.err
}

// TurnStartedParams reports that a turn began. Every turn, including ones the
// runtime starts itself, such as an automatic continuation or work triggered by
// background activity, is reported with its own started and ended pair.
type TurnStartedParams struct {
	Endpoint EndpointRef `json:"endpoint"`
	Turn     TurnRef     `json:"turn"`
	// Attempt is the prompt attempt that caused the turn; required when one did.
	// It also proves that attempt was delivered.
	Attempt AttemptID        `json:"attempt,omitempty"`
	Process *ProcessIdentity `json:"process,omitempty"`
	AtMS    int64            `json:"at_ms"`
}

// Validate checks the parameters.
func (p TurnStartedParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	c.field("turn", p.Turn.Validate())
	if p.Attempt != "" {
		c.field("attempt", p.Attempt.Validate())
	}
	if p.Process != nil {
		c.field("process", p.Process.Validate())
	}
	c.field("at_ms", timestampMS(p.AtMS))
	return c.err
}

// TurnEndedParams reports that a turn ended.
type TurnEndedParams struct {
	Endpoint EndpointRef `json:"endpoint"`
	Turn     TurnRef     `json:"turn"`
	Final    TurnFinal   `json:"final"`
	AtMS     int64       `json:"at_ms"`
}

// Validate checks the parameters.
func (p TurnEndedParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	c.field("turn", p.Turn.Validate())
	c.field("final", enumOK(p.Final, turnFinals))
	c.field("at_ms", timestampMS(p.AtMS))
	return c.err
}

// ReconcileParams asks the adapter whether a prompt attempt reached the runtime.
type ReconcileParams struct {
	Endpoint EndpointRef `json:"endpoint"`
	Attempt  AttemptID   `json:"attempt"`
	// Checkpoint is the stored state of the attempt, if any.
	Checkpoint json.RawMessage `json:"checkpoint,omitempty"`
}

// Validate checks the parameters.
func (p ReconcileParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	c.field("attempt", p.Attempt.Validate())
	if p.Checkpoint != nil {
		c.field("checkpoint", validCheckpoint(p.Checkpoint))
	}
	return c.err
}

// TurnReport describes the turn a found attempt started.
type TurnReport struct {
	Ref   TurnRef   `json:"ref"`
	State TurnState `json:"state"`
}

// Validate checks the fields.
func (t TurnReport) Validate() error {
	var c checker
	c.field("ref", t.Ref.Validate())
	c.field("state", enumOK(t.State, turnStates))
	return c.err
}

// ReconcileResult is the adapter's answer to prompt.reconcile. The verdict and
// the code of Grounds belong together:
//
//   - Found goes with GroundsAttemptInRecord and carries Turn.
//   - ProvablyAbsent goes with GroundsRecordCompleteWithoutAttempt or
//     GroundsNeverDispatched.
//   - Indeterminate goes with GroundsTurnRunning, GroundsRecordUnreadable,
//     GroundsNativeUnreachable or GroundsRuntimeVersionUntested.
type ReconcileResult struct {
	Verdict ReconcileVerdict `json:"verdict"`
	Turn    *TurnReport      `json:"turn,omitempty"`
	Grounds Grounds          `json:"grounds"`
}

// Validate checks that verdict, turn and grounds fit together.
func (r ReconcileResult) Validate() error {
	var c checker
	c.field("verdict", enumOK(r.Verdict, reconcileVerdicts))
	c.field("grounds", r.Grounds.Validate())
	if r.Turn != nil {
		c.field("turn", r.Turn.Validate())
	}
	if c.err != nil {
		return c.err
	}
	switch r.Verdict {
	case VerdictFound:
		c.ok(r.Turn != nil, "turn: a found attempt reports its turn")
		c.field("grounds.code", enumIn(r.Grounds.Code, GroundsAttemptInRecord))
	case VerdictProvablyAbsent:
		c.ok(r.Turn == nil, "turn: an absent attempt started no turn")
		c.field("grounds.code", enumIn(r.Grounds.Code, GroundsRecordCompleteWithoutAttempt, GroundsNeverDispatched))
	case VerdictIndeterminate:
		c.ok(r.Turn == nil, "turn: an undecided attempt reports no turn")
		c.field("grounds.code", enumIn(r.Grounds.Code, indeterminateCodes...))
	}
	return c.err
}

// NativeRequestHandledParams reports how the adapter answered a request that the
// native runtime sent it. The adapter answers every such request locally, from
// the preset, within one second, and never asks the daemon: approvals do not go
// through the channel. Unknown methods are refused and reported.
type NativeRequestHandledParams struct {
	Endpoint EndpointRef `json:"endpoint"`
	// Method is the runtime's method name, opaque to the daemon.
	Method      string      `json:"method"`
	Disposition Disposition `json:"disposition"`
	AtMS        int64       `json:"at_ms"`
}

// Validate checks the parameters.
func (p NativeRequestHandledParams) Validate() error {
	var c checker
	c.field("endpoint", p.Endpoint.Validate())
	c.field("method", strLen(p.Method, 1, MaxIdentLen))
	c.field("disposition", enumOK(p.Disposition, dispositions))
	c.field("at_ms", timestampMS(p.AtMS))
	return c.err
}
