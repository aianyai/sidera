package adapterproto

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// The values below are what a control signal carries and what its reply says.
// They are defined here, once, in the form the future signed records need: every
// enum is a small integer from 1, and 0 is never valid. The integers are the
// numbers a deterministic binary encoding uses; the names are the JSON text a
// message_status result uses. A reply field that does not apply is absent, never
// zero.

// enumText converts between a 1-based small integer and its name.
type enumText[E ~uint8] struct{ names []string }

func (t enumText[E]) valid(v E) bool { return v >= 1 && int(v) <= len(t.names) }

func (t enumText[E]) name(v E) string {
	if !t.valid(v) {
		return fmt.Sprintf("invalid(%d)", uint8(v))
	}
	return t.names[v-1]
}

func (t enumText[E]) marshal(v E) ([]byte, error) {
	if !t.valid(v) {
		return nil, bad("invalid enum value %d", uint8(v))
	}
	return []byte(t.names[v-1]), nil
}

func (t enumText[E]) unmarshal(text []byte, dst *E) error {
	i := slices.Index(t.names, string(text))
	if i < 0 {
		return bad("unknown value %q", text)
	}
	*dst = E(i + 1)
	return nil
}

// ControlCreateOutcome is the outcome of a control.session.create reply.
type ControlCreateOutcome uint8

const (
	// CreateReplyCreated: the session exists and showed evidence it is ready.
	CreateReplyCreated ControlCreateOutcome = iota + 1
	// CreateReplyUnconfirmed: the session may have been created but no evidence
	// of readiness came in time.
	CreateReplyUnconfirmed
	// CreateReplyFailed: no session will be attached to the channel. Certainly
	// none was created, except for the reason CreateReasonControlUnknown, where a
	// native session or process may remain and the adapter leaves it alone.
	CreateReplyFailed
)

var createOutcomeText = enumText[ControlCreateOutcome]{[]string{"created", "unconfirmed", "failed"}}

func (v ControlCreateOutcome) String() string               { return createOutcomeText.name(v) }
func (v ControlCreateOutcome) Valid() bool                  { return createOutcomeText.valid(v) }
func (v ControlCreateOutcome) MarshalText() ([]byte, error) { return createOutcomeText.marshal(v) }
func (v *ControlCreateOutcome) UnmarshalText(b []byte) error {
	return createOutcomeText.unmarshal(b, v)
}

// ControlCreateReason says why a creation failed (values 1 to 6 and 9) or is
// unconfirmed (7 and 8). The values are persisted in signed records: a new one
// takes the next number, whichever group it belongs to.
type ControlCreateReason uint8

const (
	CreateReasonPresetUnknown ControlCreateReason = iota + 1
	CreateReasonPresetUnavailable
	CreateReasonAdapterOffline
	CreateReasonRuntimeAuth
	CreateReasonNativeUnavailable
	CreateReasonNativeError
	// CreateReasonReadyTimeout: no readiness evidence within the reply deadline.
	CreateReasonReadyTimeout
	// CreateReasonAdapterLost: the adapter was lost before readiness was shown.
	CreateReasonAdapterLost
	// CreateReasonControlUnknown: no session will be attached, but the adapter
	// cannot tell who controls the native session it was creating (two writers,
	// an outside writer, or a start that timed out with its process still
	// running). The daemon notifies the local owner.
	CreateReasonControlUnknown
)

var createReasonText = enumText[ControlCreateReason]{[]string{
	"preset_unknown", "preset_unavailable", "adapter_offline", "runtime_auth", "native_unavailable", "native_error",
	"ready_timeout", "adapter_lost", "control_unknown",
}}

func (v ControlCreateReason) String() string                { return createReasonText.name(v) }
func (v ControlCreateReason) Valid() bool                   { return createReasonText.valid(v) }
func (v ControlCreateReason) MarshalText() ([]byte, error)  { return createReasonText.marshal(v) }
func (v *ControlCreateReason) UnmarshalText(b []byte) error { return createReasonText.unmarshal(b, v) }

// FailureReason reports whether the reason belongs to a failed creation.
func (v ControlCreateReason) FailureReason() bool {
	return v >= CreateReasonPresetUnknown && v <= CreateReasonNativeError || v == CreateReasonControlUnknown
}

// UnconfirmedReason reports whether the reason belongs to an unconfirmed creation.
func (v ControlCreateReason) UnconfirmedReason() bool {
	return v == CreateReasonReadyTimeout || v == CreateReasonAdapterLost
}

// ControlCreateReply is the reply to control.session.create. The parameter of
// the signal is the preset name, 1 to 64 characters of [a-z0-9][a-z0-9._-]*.
type ControlCreateReply struct {
	Outcome ControlCreateOutcome `json:"outcome"`
	// SessionEndpoint is the endpoint allocated for the session; present when the
	// outcome is created or unconfirmed. A retry gets the same endpoint.
	SessionEndpoint EndpointRef `json:"session_endpoint,omitempty"`
	// Reason is present when the outcome is unconfirmed or failed, and belongs to
	// the matching group.
	Reason ControlCreateReason `json:"reason,omitempty"`
}

// Validate checks that the fields fit the outcome.
func (r ControlCreateReply) Validate() error {
	var c checker
	c.ok(r.Outcome.Valid(), "outcome: unknown value %d", uint8(r.Outcome))
	if r.SessionEndpoint != "" {
		c.field("session_endpoint", r.SessionEndpoint.Validate())
	}
	c.ok(r.Reason == 0 || r.Reason.Valid(), "reason: unknown value %d", uint8(r.Reason))
	if c.err != nil {
		return c.err
	}
	switch r.Outcome {
	case CreateReplyCreated:
		c.ok(r.SessionEndpoint != "", "session_endpoint: a created session has an endpoint")
		c.ok(r.Reason == 0, "reason: a created session has none")
	case CreateReplyUnconfirmed:
		c.ok(r.SessionEndpoint != "", "session_endpoint: an unconfirmed session has an endpoint")
		c.ok(r.Reason.UnconfirmedReason(), "reason: an unconfirmed creation needs ready_timeout or adapter_lost")
	case CreateReplyFailed:
		c.ok(r.SessionEndpoint == "", "session_endpoint: a failed creation has none")
		c.ok(r.Reason.FailureReason(), "reason: a failed creation needs one of the failure reasons")
	}
	return c.err
}

// ControlCloseOutcome is the outcome of a control.session.close reply.
type ControlCloseOutcome uint8

const (
	// CloseReplyClosed: the adapter confirmed the native session is released and no
	// turn was running.
	CloseReplyClosed ControlCloseOutcome = iota + 1
	// CloseReplyUnconfirmed: the endpoint is closed in the channel but the native
	// side could not be confirmed.
	CloseReplyUnconfirmed
	// CloseReplyUnsupported: the endpoint does not take this signal.
	CloseReplyUnsupported
)

var closeOutcomeText = enumText[ControlCloseOutcome]{[]string{"closed", "close_unconfirmed", "unsupported"}}

func (v ControlCloseOutcome) String() string                { return closeOutcomeText.name(v) }
func (v ControlCloseOutcome) Valid() bool                   { return closeOutcomeText.valid(v) }
func (v ControlCloseOutcome) MarshalText() ([]byte, error)  { return closeOutcomeText.marshal(v) }
func (v *ControlCloseOutcome) UnmarshalText(b []byte) error { return closeOutcomeText.unmarshal(b, v) }

// ControlCloseReason says why a close is unconfirmed.
type ControlCloseReason uint8

const (
	CloseReasonTurnStillRunning ControlCloseReason = iota + 1
	CloseReasonAdapterOffline
	CloseReasonNativeUnreachable
)

var closeReasonText = enumText[ControlCloseReason]{[]string{"turn_still_running", "adapter_offline", "native_unreachable"}}

func (v ControlCloseReason) String() string                { return closeReasonText.name(v) }
func (v ControlCloseReason) Valid() bool                   { return closeReasonText.valid(v) }
func (v ControlCloseReason) MarshalText() ([]byte, error)  { return closeReasonText.marshal(v) }
func (v *ControlCloseReason) UnmarshalText(b []byte) error { return closeReasonText.unmarshal(b, v) }

// ControlCloseReply is the reply to control.session.close, which has no
// parameters. The daemon asks the adapter to release the native session first,
// then makes the reply and closes the endpoint in the channel in one step (the
// token is revoked, no more prompts, queued messages are settled). The endpoint
// is closed whatever the adapter answered, close_unconfirmed included.
type ControlCloseReply struct {
	Outcome ControlCloseOutcome `json:"outcome"`
	// Reason is present exactly when the outcome is close_unconfirmed.
	Reason ControlCloseReason `json:"reason,omitempty"`
}

// Validate checks that a reason appears exactly with an unconfirmed close.
func (r ControlCloseReply) Validate() error {
	var c checker
	c.ok(r.Outcome.Valid(), "outcome: unknown value %d", uint8(r.Outcome))
	c.ok(r.Reason == 0 || r.Reason.Valid(), "reason: unknown value %d", uint8(r.Reason))
	if c.err != nil {
		return c.err
	}
	c.ok((r.Outcome == CloseReplyUnconfirmed) == (r.Reason != 0), "reason: present exactly when the close is unconfirmed")
	return c.err
}

// ControlInterruptOutcome is the outcome of a control.session.interrupt reply.
type ControlInterruptOutcome uint8

const (
	// InterruptReplyStopped: the turn ended within the confirmation wait.
	InterruptReplyStopped ControlInterruptOutcome = iota + 1
	// InterruptReplyRequested: the request was made and not confirmed in time.
	InterruptReplyRequested
	InterruptReplyNotRunning
	InterruptReplyFailed
	InterruptReplyUnsupported
)

var interruptOutcomeText = enumText[ControlInterruptOutcome]{[]string{"stopped", "requested", "not_running", "failed", "unsupported"}}

func (v ControlInterruptOutcome) String() string { return interruptOutcomeText.name(v) }
func (v ControlInterruptOutcome) Valid() bool    { return interruptOutcomeText.valid(v) }
func (v ControlInterruptOutcome) MarshalText() ([]byte, error) {
	return interruptOutcomeText.marshal(v)
}
func (v *ControlInterruptOutcome) UnmarshalText(b []byte) error {
	return interruptOutcomeText.unmarshal(b, v)
}

// ControlInterruptReason says why an interrupt failed.
type ControlInterruptReason uint8

const (
	InterruptReasonAdapterOffline ControlInterruptReason = iota + 1
	InterruptReasonNativeUnreachable
	InterruptReasonNativeError
)

var interruptReasonText = enumText[ControlInterruptReason]{[]string{"adapter_offline", "native_unreachable", "native_error"}}

func (v ControlInterruptReason) String() string { return interruptReasonText.name(v) }
func (v ControlInterruptReason) Valid() bool    { return interruptReasonText.valid(v) }
func (v ControlInterruptReason) MarshalText() ([]byte, error) {
	return interruptReasonText.marshal(v)
}
func (v *ControlInterruptReason) UnmarshalText(b []byte) error {
	return interruptReasonText.unmarshal(b, v)
}

// ControlInterruptReply is the reply to control.session.interrupt, which has no
// parameters. Stopping a turn does not undo the side effects it already had.
type ControlInterruptReply struct {
	Outcome ControlInterruptOutcome `json:"outcome"`
	// Reason is present exactly when the outcome is failed.
	Reason ControlInterruptReason `json:"reason,omitempty"`
}

// Validate checks that a reason appears exactly with a failure.
func (r ControlInterruptReply) Validate() error {
	var c checker
	c.ok(r.Outcome.Valid(), "outcome: unknown value %d", uint8(r.Outcome))
	c.ok(r.Reason == 0 || r.Reason.Valid(), "reason: unknown value %d", uint8(r.Reason))
	if c.err != nil {
		return c.err
	}
	c.ok((r.Outcome == InterruptReplyFailed) == (r.Reason != 0), "reason: present exactly when the interrupt failed")
	return c.err
}

// ControlActivity is whether a session is working.
type ControlActivity uint8

const (
	ActivityIdle ControlActivity = iota + 1
	ActivityBusy
	// ActivityUnknown: the adapter is offline and the last report cannot be
	// checked.
	ActivityUnknown
)

var activityText = enumText[ControlActivity]{[]string{"idle", "busy", "unknown"}}

func (v ControlActivity) String() string                { return activityText.name(v) }
func (v ControlActivity) Valid() bool                   { return activityText.valid(v) }
func (v ControlActivity) MarshalText() ([]byte, error)  { return activityText.marshal(v) }
func (v *ControlActivity) UnmarshalText(b []byte) error { return activityText.unmarshal(b, v) }

// ControlCondition is something wrong with an endpoint that a status reply
// reports.
type ControlCondition uint8

const (
	ControlConditionAdapterOffline ControlCondition = iota + 1
	ControlConditionRuntimeAuth
	ControlConditionNativeUnavailable
	// ControlConditionServerRequestRejected: the adapter rejected a request of the native
	// runtime recently.
	ControlConditionServerRequestRejected
	// ControlConditionControlUnknown: the outcome of an earlier control action is unknown.
	ControlConditionControlUnknown
	// ControlConditionVersionUntested: the runtime's version is outside the tested
	// ranges. It does not stop the endpoint working.
	ControlConditionVersionUntested
)

var conditionText = enumText[ControlCondition]{[]string{
	"adapter_offline", "runtime_auth", "native_unavailable", "server_request_rejected", "control_unknown", "version_untested",
}}

func (v ControlCondition) String() string                { return conditionText.name(v) }
func (v ControlCondition) Valid() bool                   { return conditionText.valid(v) }
func (v ControlCondition) MarshalText() ([]byte, error)  { return conditionText.marshal(v) }
func (v *ControlCondition) UnmarshalText(b []byte) error { return conditionText.unmarshal(b, v) }

// PresetAvailability is whether one preset can be used now.
type PresetAvailability struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

// Validate checks the name.
func (p PresetAvailability) Validate() error { return at("name", ValidatePresetName(p.Name)) }

// ControlStatusReply is the reply to control.endpoint.status, which has no
// parameters. The daemon answers it alone, from what it has observed, without
// calling the adapter.
type ControlStatusReply struct {
	Activity ControlActivity `json:"activity"`
	// Queued counts the messages waiting, not those paused by authorization.
	Queued int `json:"queued"`
	// Conditions is in ascending order without repeats; absent when empty.
	Conditions []ControlCondition `json:"conditions,omitempty"`
	// Presets is sorted by name and present only for runtime endpoints.
	Presets []PresetAvailability `json:"presets,omitempty"`
	// AsOf is when the daemon produced the reply, in Unix seconds.
	AsOf int64 `json:"as_of"`
}

// Validate checks the fields, the order of the lists and the absence of repeats.
func (r ControlStatusReply) Validate() error {
	var c checker
	c.ok(r.Activity.Valid(), "activity: unknown value %d", uint8(r.Activity))
	c.field("queued", intRange(r.Queued, 0, 1<<31-1))
	for i, v := range r.Conditions {
		c.ok(v.Valid(), "conditions%s: unknown value %d", index(i), uint8(v))
	}
	c.field("conditions", unique(r.Conditions))
	c.beyond(slices.IsSorted(r.Conditions), "conditions: not in ascending order")
	list(&c, "presets", r.Presets)
	names := make([]string, len(r.Presets))
	for i, p := range r.Presets {
		names[i] = p.Name
	}
	c.beyond(len(duplicates(names)) == 0, "presets: two entries share a name")
	c.beyond(slices.IsSorted(names), "presets: not sorted by name")
	c.field("as_of", intRange(r.AsOf, 1, MaxTimestampS))
	return c.err
}

// ControlReply is the reply to a control signal as a message_status result shows
// it: the signal and the fields of its reply side by side, in JSON.
type ControlReply struct {
	Signal    ControlSignal
	Create    *ControlCreateReply
	Close     *ControlCloseReply
	Interrupt *ControlInterruptReply
	Status    *ControlStatusReply
}

func (r ControlReply) body() (any, bool) {
	switch r.Signal {
	case SignalSessionCreate:
		return r.Create, r.Create != nil
	case SignalSessionClose:
		return r.Close, r.Close != nil
	case SignalSessionInterrupt:
		return r.Interrupt, r.Interrupt != nil
	case SignalEndpointStatus:
		return r.Status, r.Status != nil
	}
	return nil, false
}

// Validate checks that exactly the reply of the signal is set and valid.
func (r ControlReply) Validate() error {
	if err := enumOK(r.Signal, controlSignals); err != nil {
		return at("signal", err)
	}
	set := 0
	for _, p := range []bool{r.Create != nil, r.Close != nil, r.Interrupt != nil, r.Status != nil} {
		if p {
			set++
		}
	}
	body, ok := r.body()
	if !ok || set != 1 {
		return bad("exactly the reply for %s must be set", r.Signal)
	}
	return body.(interface{ Validate() error }).Validate()
}

// MarshalJSON writes the reply's fields and "signal" in one object.
func (r ControlReply) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	body, _ := r.body()
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	sig, _ := json.Marshal(r.Signal)
	m["signal"] = sig
	return json.Marshal(m)
}

// UnmarshalJSON reads "signal" and decodes the rest strictly as that signal's
// reply.
func (r *ControlReply) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return bad("%v", err)
	}
	rawSig, ok := m["signal"]
	if !ok {
		return bad("signal is missing")
	}
	var sig ControlSignal
	if err := json.Unmarshal(rawSig, &sig); err != nil {
		return bad("signal: %v", err)
	}
	delete(m, "signal")
	rest, err := json.Marshal(m)
	if err != nil {
		return err
	}
	out := ControlReply{Signal: sig}
	var target any
	switch sig {
	case SignalSessionCreate:
		out.Create = new(ControlCreateReply)
		target = out.Create
	case SignalSessionClose:
		out.Close = new(ControlCloseReply)
		target = out.Close
	case SignalSessionInterrupt:
		out.Interrupt = new(ControlInterruptReply)
		target = out.Interrupt
	case SignalEndpointStatus:
		out.Status = new(ControlStatusReply)
		target = out.Status
	default:
		return bad("signal: unknown value %q", string(sig))
	}
	if err := strictjson.Decode(rest, target); err != nil {
		return err
	}
	*r = out
	return nil
}
