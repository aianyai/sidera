package adapterproto

import "encoding/json"

// maxPromptText bounds the text of a prompt. A prompt is a short wake-up line.
const maxPromptText = 8192

// FirstPrompt is the first prompt of a new session. It is an ordinary prompt
// attempt: once it is in the native record it is both the proof the session is
// ready and a delivered prompt.
type FirstPrompt struct {
	Attempt AttemptID `json:"attempt"`
	Text    string    `json:"text"`
}

// Validate checks the attempt and the text.
func (f FirstPrompt) Validate() error {
	var c checker
	c.field("attempt", f.Attempt.Validate())
	c.field("text", strLen(f.Text, 1, maxPromptText))
	return c.err
}

// SessionCreateParams asks the adapter to start a native session.
//
// The same Attempt may be sent again after an unknown outcome; the adapter then
// continues the earlier attempt instead of creating a second session, and
// answers CodeAttemptConflict when the parameters differ.
type SessionCreateParams struct {
	Attempt         AttemptID   `json:"attempt"`
	RuntimeEndpoint EndpointRef `json:"runtime_endpoint"`
	// SessionEndpoint is the endpoint the daemon allocated for the new session.
	SessionEndpoint EndpointRef `json:"session_endpoint"`
	Preset          string      `json:"preset"`
	FirstPrompt     FirstPrompt `json:"first_prompt"`
	ToolAccess      ToolAccess  `json:"tool_access"`
	// Checkpoint is the stored state of the attempt, when an earlier try left one.
	Checkpoint json.RawMessage `json:"checkpoint,omitempty"`
}

// Validate checks the parameters.
func (p SessionCreateParams) Validate() error {
	var c checker
	c.field("attempt", p.Attempt.Validate())
	c.field("runtime_endpoint", p.RuntimeEndpoint.Validate())
	c.field("session_endpoint", p.SessionEndpoint.Validate())
	c.field("preset", ValidatePresetName(p.Preset))
	c.field("first_prompt", p.FirstPrompt.Validate())
	c.field("tool_access", p.ToolAccess.Validate())
	if p.Checkpoint != nil {
		c.field("checkpoint", validCheckpoint(p.Checkpoint))
	}
	return c.err
}

// Grounds is the evidence behind a verdict that is not a plain yes.
type Grounds struct {
	Code   GroundsCode `json:"code"`
	Detail string      `json:"detail,omitempty"`
}

// Validate checks the code and detail.
func (g Grounds) Validate() error {
	var c checker
	c.field("code", enumOK(g.Code, groundsCodes))
	if g.Detail != "" {
		c.field("detail", strLen(g.Detail, 1, MaxDetailLen))
	}
	return c.err
}

// indeterminateCodes are the grounds of a verdict that cannot be decided yet.
var indeterminateCodes = []GroundsCode{GroundsTurnRunning, GroundsRecordUnreadable, GroundsNativeUnreachable, GroundsRuntimeVersionUntested}

// SessionCreateResult is the adapter's answer to session.create.
//
//   - Created: the session exists and its first prompt is in the native record
//     (for a runtime that keeps its record on disk, the record is written). It
//     carries NativeRef and Frontends, the front ends actually set up, which
//     decide the output budget of recv.
//   - Failed: certainly no session was created; Reason says why.
//   - Indeterminate: a session may exist. Grounds says why it cannot be told yet
//     and NativeRef may already be known; the daemon asks again by repeating the
//     attempt.
type SessionCreateResult struct {
	Outcome   CreateOutcome    `json:"outcome"`
	NativeRef NativeRef        `json:"native_ref,omitempty"`
	Process   *ProcessIdentity `json:"process,omitempty"`
	Frontends []Frontend       `json:"frontends,omitempty"`
	Reason    CreateFailure    `json:"reason,omitempty"`
	Grounds   *Grounds         `json:"grounds,omitempty"`
	Detail    string           `json:"detail,omitempty"`
}

// Validate checks the fields the outcome requires and forbids.
func (r SessionCreateResult) Validate() error {
	var c checker
	c.field("outcome", enumOK(r.Outcome, createOutcomes))
	if c.err != nil {
		return c.err
	}
	if r.NativeRef != "" {
		c.field("native_ref", r.NativeRef.Validate())
	}
	if r.Process != nil {
		c.field("process", r.Process.Validate())
	}
	for i, f := range r.Frontends {
		c.field("frontends"+index(i), enumOK(f, frontends))
	}
	c.field("frontends", unique(r.Frontends))
	if r.Reason != "" {
		c.field("reason", enumOK(r.Reason, createFailures))
	}
	if r.Grounds != nil {
		c.field("grounds", r.Grounds.Validate())
	}
	if r.Detail != "" {
		c.field("detail", strLen(r.Detail, 1, MaxDetailLen))
	}
	if c.err != nil {
		return c.err
	}
	switch r.Outcome {
	case CreateCreated:
		c.ok(r.NativeRef != "", "native_ref: a created session names its native session")
		c.ok(len(r.Frontends) >= 1, "frontends: a created session reports the front ends it set up")
		c.ok(r.Reason == "", "reason: only a failure has one")
		c.ok(r.Grounds == nil, "grounds: only an indeterminate outcome has them")
	case CreateFailed:
		c.ok(r.Reason != "", "reason: a failure says why")
		c.ok(r.NativeRef == "" && r.Process == nil && len(r.Frontends) == 0, "a failure has no session to describe")
		c.ok(r.Grounds == nil, "grounds: only an indeterminate outcome has them")
	case CreateIndeterminate:
		c.ok(r.Grounds != nil, "grounds: an indeterminate outcome says why")
		if r.Grounds != nil {
			c.field("grounds.code", enumIn(r.Grounds.Code, indeterminateCodes...))
		}
		c.ok(r.Reason == "", "reason: only a failure has one")
		c.ok(len(r.Frontends) == 0, "frontends: only a created session reports them")
	}
	return c.err
}

// CheckpointPutParams stores adapter state in the daemon's state directory. The
// daemon treats the state as opaque and keeps one value per (scope, id): a
// later put replaces the earlier one.
type CheckpointPutParams struct {
	Scope CheckpointScope `json:"scope"`
	// ID is an AttemptID for ScopeAttempt and an EndpointRef for ScopeSession.
	ID string `json:"id"`
	// State is a JSON object of at most MaxCheckpointBytes bytes in compact form.
	State json.RawMessage `json:"state"`
}

// Validate checks the scope, the key that goes with it and the state.
func (p CheckpointPutParams) Validate() error {
	var c checker
	c.field("scope", enumOK(p.Scope, checkpointScopes))
	if c.err == nil {
		if p.Scope == ScopeAttempt {
			c.field("id", AttemptID(p.ID).Validate())
		} else {
			c.field("id", EndpointRef(p.ID).Validate())
		}
	}
	c.field("state", validCheckpoint(p.State))
	return c.err
}

// InterruptParams asks the adapter to stop the running turn of a session.
type InterruptParams struct {
	Endpoint EndpointRef `json:"endpoint"`
}

// Validate checks the endpoint.
func (p InterruptParams) Validate() error { return at("endpoint", p.Endpoint.Validate()) }

// InterruptResult is the adapter's answer to session.interrupt. Stopping a turn
// does not undo the side effects it already had.
//
//   - Requested: the request was made; the daemon waits for turn.ended.
//   - Stopped: the adapter confirmed the turn is over.
//   - NotRunning: there was no turn to stop.
//   - Refused: the request could not be made; Reason says why.
type InterruptResult struct {
	Stage  InterruptStage   `json:"stage"`
	Reason InterruptRefusal `json:"reason,omitempty"`
}

// Validate checks that a reason appears exactly with a refusal.
func (r InterruptResult) Validate() error {
	var c checker
	c.field("stage", enumOK(r.Stage, interruptStages))
	if c.err != nil {
		return c.err
	}
	if r.Stage == InterruptRefused {
		c.field("reason", enumOK(r.Reason, interruptRefusals))
	} else {
		c.ok(r.Reason == "", "reason: only a refusal has one")
	}
	return c.err
}

// CloseParams asks the adapter to release a session's native resources. A turn
// that is still running is interrupted first.
type CloseParams struct {
	Endpoint EndpointRef `json:"endpoint"`
}

// Validate checks the endpoint.
func (p CloseParams) Validate() error { return at("endpoint", p.Endpoint.Validate()) }

// CloseResult is the adapter's answer to session.close.
type CloseResult struct {
	Outcome CloseOutcome `json:"outcome"`
	Reason  CloseReason  `json:"reason,omitempty"`
}

// Validate checks that a reason appears exactly when the close is unconfirmed.
func (r CloseResult) Validate() error {
	var c checker
	c.field("outcome", enumOK(r.Outcome, closeOutcomes))
	if c.err != nil {
		return c.err
	}
	if r.Outcome == CloseUnconfirmed {
		c.field("reason", enumOK(r.Reason, closeReasons))
	} else {
		c.ok(r.Reason == "", "reason: only an unconfirmed close has one")
	}
	return c.err
}
