package adapterproto

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/aianyai/sidera/sdk/go/peer"
)

// MaxTimestampMS bounds a millisecond timestamp: the largest integer every JSON
// implementation represents exactly. Timestamps an adapter reports are for
// display and diagnosis only; the daemon judges time by its own clock.
const MaxTimestampMS = 1<<53 - 1

func timestampMS(v int64) error { return intRange(v, 1, MaxTimestampMS) }

// Limits of the lifecycle messages.
const (
	maxNameLen       = 64
	maxVersionLen    = 64
	maxDependencyLen = 128
	maxPathLen       = 4096
	maxEnvEntries    = 64
	maxEnvValueLen   = 4096
	maxConfigEntries = 64
	maxConfigString  = 4096
	maxConfigList    = 256
	maxProblems      = 32
	maxProbes        = 256
	// MaxDirectEndpoints bounds max_endpoints in the capabilities.
	MaxDirectEndpoints = 1000
	// MaxCheckpointBytes bounds a checkpoint state, measured as compact JSON.
	MaxCheckpointBytes = 16 * 1024
)

// DaemonInfo identifies the daemon to the adapter.
type DaemonInfo struct {
	Version string `json:"version"`
}

// Validate checks the version text.
func (d DaemonInfo) Validate() error { return at("version", strLen(d.Version, 1, maxVersionLen)) }

// InstanceInfo identifies the adapter instance the daemon started.
type InstanceInfo struct {
	// ID is the instance's name.
	ID string `json:"id"`
	// LockPath is the file the adapter locks exclusively before it answers
	// initialize, so that a hung old process and its replacement never control
	// sessions at once. The lock is released when the process dies.
	LockPath string `json:"lock_path"`
}

// Validate checks both fields.
func (i InstanceInfo) Validate() error {
	var c checker
	c.field("id", strLen(i.ID, 1, MaxIdentLen))
	c.field("lock_path", strLen(i.LockPath, 1, maxPathLen))
	return c.err
}

// InitializeParams is the first request, from the daemon.
type InitializeParams struct {
	// Protocol is the range of versions the daemon supports.
	Protocol Range        `json:"protocol"`
	Daemon   DaemonInfo   `json:"daemon"`
	Instance InstanceInfo `json:"instance"`
}

// Validate checks the parameters.
func (p InitializeParams) Validate() error {
	var c checker
	c.field("protocol", p.Protocol.Validate())
	c.field("daemon", p.Daemon.Validate())
	c.field("instance", p.Instance.Validate())
	return c.err
}

// AdapterInfo is the adapter's own identity, which must equal its manifest.
type AdapterInfo struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// Validate checks the identifier and the version.
func (a AdapterInfo) Validate() error {
	var c checker
	c.field("id", ValidateAdapterID(a.ID))
	c.field("version", ValidateSemVer(a.Version))
	return c.err
}

// RuntimeCapabilities declares that the adapter drives a native agent runtime:
// it provides one runtime endpoint and the session endpoints created under it.
type RuntimeCapabilities struct {
	// Interrupt: the adapter implements session.interrupt.
	Interrupt bool `json:"interrupt"`
	// AckOnBehalf: the adapter calls recv.observed to acknowledge messages the
	// model has been shown.
	AckOnBehalf bool `json:"ack_on_behalf"`
	// ServerRequests: the native runtime sends requests of its own, which the
	// adapter must answer locally and report with native_request.handled.
	ServerRequests bool `json:"server_requests"`
	// Frontends are the agent tool front ends a session can be set up with.
	Frontends []Frontend `json:"frontends"`
}

// Validate checks the front-end list: at least one, no repeats.
func (r RuntimeCapabilities) Validate() error {
	var c checker
	c.ok(len(r.Frontends) >= 1, "frontends: a runtime needs at least one front end")
	for i, f := range r.Frontends {
		c.field("frontends"+index(i), enumOK(f, frontends))
	}
	c.field("frontends", unique(r.Frontends))
	return c.err
}

// DirectCapabilities declares that the adapter owns endpoints it reads itself,
// such as a chat bridge that stands for people.
type DirectCapabilities struct {
	// MaxEndpoints is the most direct endpoints the adapter registers.
	MaxEndpoints int `json:"max_endpoints"`
}

// Validate checks the limit.
func (d DirectCapabilities) Validate() error {
	return at("max_endpoints", intRange(d.MaxEndpoints, 1, MaxDirectEndpoints))
}

// Capabilities are what an adapter declares it supports. Declaring is separate
// from being available: availability comes from probing and from snapshots.
type Capabilities struct {
	// Runtime is absent when the adapter provides no runtime and session endpoints.
	Runtime *RuntimeCapabilities `json:"runtime,omitempty"`
	// Direct is absent when the adapter provides no direct endpoints.
	Direct *DirectCapabilities `json:"direct,omitempty"`
}

// Validate checks each declared part.
func (cp Capabilities) Validate() error {
	var c checker
	if cp.Runtime != nil {
		c.field("runtime", cp.Runtime.Validate())
	}
	if cp.Direct != nil {
		c.field("direct", cp.Direct.Validate())
	}
	return c.err
}

// InitializeResult is the adapter's answer to initialize.
type InitializeResult struct {
	// ProtocolVersion is the version chosen by Negotiate.
	ProtocolVersion int          `json:"protocol_version"`
	Adapter         AdapterInfo  `json:"adapter"`
	Capabilities    Capabilities `json:"capabilities"`
}

// Validate checks the result.
func (r InitializeResult) Validate() error {
	var c checker
	c.field("protocol_version", intRange(r.ProtocolVersion, 1, MaxVersion))
	c.field("adapter", r.Adapter.Validate())
	c.field("capabilities", r.Capabilities.Validate())
	return c.err
}

// Container says what isolates a native process from the adapter.
type Container struct {
	Kind ContainerKind `json:"kind"`
	// Name is the unit, process group, job or task name. It is present for every
	// kind except ContainerNone.
	Name string `json:"name,omitempty"`
}

// Validate checks the kind and, except for none, the name.
func (ct Container) Validate() error {
	var c checker
	c.field("kind", enumOK(ct.Kind, containerKinds))
	if ct.Kind == ContainerNone {
		c.ok(ct.Name == "", "name: a process with no container has no name")
	} else if ct.Kind != "" {
		c.field("name", strLen(ct.Name, 1, 256))
	}
	return c.err
}

// ProcessIdentity names one native process in a way that survives PID reuse and
// a restart of the adapter or the daemon. The daemon never reads the process
// table: it stores and shows these fields and hands them back to an adapter that
// reconnects.
type ProcessIdentity struct {
	// PID finds the process: to tell whether it is busy, to signal it and to take
	// it over after a restart.
	PID int64 `json:"pid"`
	// StartKey together with PID identifies one process instance. It is an opaque
	// string compared only for equality, built from the platform's raw start
	// value and the boot identity, in the same format across adapter versions.
	StartKey string `json:"start_key"`
	// StartedAtMS is an approximate start time for people to read; it is never
	// used in a decision.
	StartedAtMS int64 `json:"started_at_ms,omitempty"`
	// Container says what isolates the process from the adapter.
	Container *Container `json:"container,omitempty"`
}

// Validate checks the identity.
func (p ProcessIdentity) Validate() error {
	var c checker
	c.field("pid", intRange(p.PID, 1, 1<<32-1))
	c.field("start_key", strLen(p.StartKey, 1, 64))
	if p.StartedAtMS != 0 {
		c.field("started_at_ms", timestampMS(p.StartedAtMS))
	}
	if p.Container != nil {
		c.field("container", p.Container.Validate())
	}
	return c.err
}

// ToolAccess is what a session needs to reach the channel's tools: the
// environment variables the adapter injects into the agent. The daemon mints the
// endpoint token and chooses the variable names; the adapter passes them on
// without interpreting them.
type ToolAccess struct {
	Env map[string]string `json:"env"`
}

func validateEnv(env map[string]string) error {
	if env == nil {
		return bad("required map is missing; use an empty map for none")
	}
	if len(env) > maxEnvEntries {
		return bad("%d entries, at most %d", len(env), maxEnvEntries)
	}
	for k, v := range env {
		if err := strLen(k, 1, 128); err != nil {
			return at(k, err)
		}
		if err := matches(envNameRE, k); err != nil {
			return at(k, err)
		}
		if err := strLen(v, 0, maxEnvValueLen); err != nil {
			return at(k, err)
		}
	}
	return nil
}

// Validate checks the variables.
func (t ToolAccess) Validate() error { return at("env", validateEnv(t.Env)) }

// ConfigValues are configuration values keyed by field key. A value is a string,
// an integer, a boolean or a list of strings; the daemon validated it against
// the manifest's field description before sending it.
type ConfigValues map[string]json.RawMessage

// Validate checks the keys and the kinds of the values.
func (v ConfigValues) Validate() error {
	if v == nil {
		return bad("required map is missing; use an empty map for none")
	}
	if len(v) > maxConfigEntries {
		return bad("%d entries, at most %d", len(v), maxConfigEntries)
	}
	for k, raw := range v {
		if err := matches(configKeyRE, k); err != nil {
			return at(k, err)
		}
		if err := validConfigValue(raw); err != nil {
			return at(k, err)
		}
	}
	return nil
}

func validConfigValue(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return bad("missing value")
	}
	switch c := raw[0]; {
	case c == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return bad("%v", err)
		}
		return strLen(s, 0, maxConfigString)
	case c == 't' || c == 'f':
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return bad("%v", err)
		}
		return nil
	case c == '-' || (c >= '0' && c <= '9'):
		n, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			return bad("%q is not a plain integer", raw)
		}
		return intRange(n, -MaxTimestampMS, MaxTimestampMS)
	case c == '[':
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return bad("%v", err)
		}
		if len(items) > maxConfigList {
			return bad("%d items, at most %d", len(items), maxConfigList)
		}
		for i, it := range items {
			it = bytes.TrimSpace(it)
			if len(it) == 0 || it[0] != '"' {
				return at(index(i), bad("a list holds strings only"))
			}
			var s string
			if err := json.Unmarshal(it, &s); err != nil {
				return at(index(i), bad("%v", err))
			}
			if err := strLen(s, 0, maxConfigString); err != nil {
				return at(index(i), err)
			}
		}
		return nil
	}
	return bad("a value is a string, an integer, a boolean or a list of strings")
}

// validCheckpoint checks a stored state: a JSON object that fits the limit.
func validCheckpoint(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return bad("missing state")
	}
	if raw[0] != '{' {
		return bad("a state is a JSON object")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return bad("%v", err)
	}
	if buf.Len() > MaxCheckpointBytes {
		return beyond("state is %d bytes, at most %d", buf.Len(), MaxCheckpointBytes)
	}
	return nil
}

// PresetConfig is a runtime preset: a name and its configuration values.
type PresetConfig struct {
	Name   string       `json:"name"`
	Config ConfigValues `json:"config"`
}

// Validate checks the name and the values.
func (p PresetConfig) Validate() error {
	var c checker
	c.field("name", ValidatePresetName(p.Name))
	c.field("config", p.Config.Validate())
	return c.err
}

// SessionAttach is a session the daemon asks the adapter to take back control of.
type SessionAttach struct {
	Endpoint  EndpointRef `json:"endpoint"`
	Preset    string      `json:"preset"`
	NativeRef NativeRef   `json:"native_ref"`
	// Process is the native process as last reported.
	Process *ProcessIdentity `json:"process,omitempty"`
	// TurnActive is whether the daemon last heard that a turn was running.
	TurnActive bool       `json:"turn_active"`
	ToolAccess ToolAccess `json:"tool_access"`
	// Checkpoint is the session's stored state.
	Checkpoint json.RawMessage `json:"checkpoint,omitempty"`
}

// Validate checks the entry.
func (s SessionAttach) Validate() error {
	var c checker
	c.field("endpoint", s.Endpoint.Validate())
	c.field("preset", ValidatePresetName(s.Preset))
	c.field("native_ref", s.NativeRef.Validate())
	if s.Process != nil {
		c.field("process", s.Process.Validate())
	}
	c.field("tool_access", s.ToolAccess.Validate())
	if s.Checkpoint != nil {
		c.field("checkpoint", validCheckpoint(s.Checkpoint))
	}
	return c.err
}

// DirectAttach is a direct endpoint the adapter registered before.
type DirectAttach struct {
	Endpoint EndpointRef `json:"endpoint"`
	Key      string      `json:"key"`
}

// Validate checks the entry.
func (d DirectAttach) Validate() error {
	var c checker
	c.field("endpoint", d.Endpoint.Validate())
	c.field("key", strLen(d.Key, 1, MaxIdentLen))
	return c.err
}

// StartParams is sent once per connection, after initialize, with everything
// the adapter needs to take over: the node, its endpoints and the sessions to
// reconnect. It is a full re-attachment, so an adapter keeps no memory of an
// earlier connection that it relies on.
type StartParams struct {
	// Node names this node. It is opaque to the adapter.
	Node string `json:"node"`
	// RuntimeEndpoint is the runtime endpoint the daemon allocated; present
	// exactly when the adapter declared runtime capabilities.
	RuntimeEndpoint EndpointRef `json:"runtime_endpoint,omitempty"`
	// Config holds the instance configuration values.
	Config          ConfigValues    `json:"config"`
	Presets         []PresetConfig  `json:"presets"`
	Sessions        []SessionAttach `json:"sessions"`
	DirectEndpoints []DirectAttach  `json:"direct_endpoints"`
}

// Validate checks the parameters.
func (p StartParams) Validate() error {
	var c checker
	c.field("node", strLen(p.Node, 1, MaxIdentLen))
	if p.RuntimeEndpoint != "" {
		c.field("runtime_endpoint", p.RuntimeEndpoint.Validate())
	}
	c.field("config", p.Config.Validate())
	c.field("presets", present(p.Presets))
	list(&c, "presets", p.Presets)
	c.field("sessions", present(p.Sessions))
	list(&c, "sessions", p.Sessions)
	c.field("direct_endpoints", present(p.DirectEndpoints))
	list(&c, "direct_endpoints", p.DirectEndpoints)
	return c.err
}

// SessionState is the adapter's report on one session it was asked to attach.
type SessionState struct {
	Endpoint   EndpointRef      `json:"endpoint"`
	State      AttachState      `json:"state"`
	TurnActive bool             `json:"turn_active"`
	Process    *ProcessIdentity `json:"process,omitempty"`
}

// Validate checks the entry.
func (s SessionState) Validate() error {
	var c checker
	c.field("endpoint", s.Endpoint.Validate())
	c.field("state", enumOK(s.State, attachStates))
	if s.Process != nil {
		c.field("process", s.Process.Validate())
	}
	return c.err
}

// StartResult reports each session's state after the adapter attached it. The
// daemon takes TurnActive as the new busy state for the session.
type StartResult struct {
	Sessions []SessionState `json:"sessions"`
}

// Validate checks the result.
func (r StartResult) Validate() error {
	var c checker
	c.field("sessions", present(r.Sessions))
	list(&c, "sessions", r.Sessions)
	return c.err
}

// ToolProbeRequest asks the adapter to check that one preset's front end reaches
// the channel. The token is single-use and valid for five minutes; only the
// daemon seeing it arrive on its agent interface proves the path works.
type ToolProbeRequest struct {
	Preset   string            `json:"preset"`
	Frontend Frontend          `json:"frontend"`
	Token    Token             `json:"token"`
	Env      map[string]string `json:"env"`
}

// Validate checks the request.
func (t ToolProbeRequest) Validate() error {
	var c checker
	c.field("preset", ValidatePresetName(t.Preset))
	c.field("frontend", enumOK(t.Frontend, frontends))
	c.field("token", t.Token.Validate())
	c.field("env", validateEnv(t.Env))
	return c.err
}

// ProbeParams is the parameter of adapter.probe.
type ProbeParams struct {
	ToolProbes []ToolProbeRequest `json:"tool_probes"`
}

// Validate checks the parameters.
func (p ProbeParams) Validate() error {
	var c checker
	c.field("tool_probes", present(p.ToolProbes))
	c.ok(len(p.ToolProbes) <= maxProbes, "tool_probes: %d entries, at most %d", len(p.ToolProbes), maxProbes)
	list(&c, "tool_probes", p.ToolProbes)
	return c.err
}

// RuntimeInfo is what a probe found out about the native runtime.
type RuntimeInfo struct {
	// Name is the runtime's product name, matching a "runtime" of the manifest's
	// tested ranges.
	Name    string `json:"name"`
	Version string `json:"version"`
	// Tested says the version is inside one of the manifest's tested ranges.
	Tested bool `json:"tested"`
}

// Validate checks the fields.
func (r RuntimeInfo) Validate() error {
	var c checker
	c.field("name", strLen(r.Name, 1, maxNameLen))
	c.field("version", strLen(r.Version, 1, maxVersionLen))
	return c.err
}

// Dependency is the state of one program or service the adapter needs.
type Dependency struct {
	Name   string          `json:"name"`
	State  DependencyState `json:"state"`
	Detail string          `json:"detail,omitempty"`
}

// Validate checks the entry.
func (d Dependency) Validate() error {
	var c checker
	c.field("name", strLen(d.Name, 1, maxDependencyLen))
	c.field("state", enumOK(d.State, dependencyStates))
	if d.Detail != "" {
		c.field("detail", strLen(d.Detail, 1, MaxDetailLen))
	}
	return c.err
}

// PresetStatus is whether a preset can be used right now.
type PresetStatus struct {
	Name     string   `json:"name"`
	Usable   bool     `json:"usable"`
	Problems []string `json:"problems,omitempty"`
}

// Validate checks the entry.
func (p PresetStatus) Validate() error {
	var c checker
	c.field("name", ValidatePresetName(p.Name))
	c.ok(len(p.Problems) <= maxProblems, "problems: %d entries, at most %d", len(p.Problems), maxProblems)
	for i, s := range p.Problems {
		c.field("problems"+index(i), strLen(s, 1, MaxDetailLen))
	}
	return c.err
}

// ToolProbeResult is the outcome of one requested tool probe.
type ToolProbeResult struct {
	Preset   string       `json:"preset"`
	Frontend Frontend     `json:"frontend"`
	Outcome  ProbeOutcome `json:"outcome"`
	Method   ProbeMethod  `json:"method,omitempty"`
	Detail   string       `json:"detail,omitempty"`
}

// Validate checks the entry.
func (t ToolProbeResult) Validate() error {
	var c checker
	c.field("preset", ValidatePresetName(t.Preset))
	c.field("frontend", enumOK(t.Frontend, frontends))
	c.field("outcome", enumOK(t.Outcome, probeOutcomes))
	if t.Method != "" {
		c.field("method", enumOK(t.Method, probeMethods))
	}
	if t.Detail != "" {
		c.field("detail", strLen(t.Detail, 1, MaxDetailLen))
	}
	return c.err
}

// ProbeResult is what adapter.probe found. Reporting "ran" for a tool probe is
// necessary but not sufficient: the daemon counts a front end as reachable only
// when it sees the probe token itself.
type ProbeResult struct {
	// Runtime is absent when the adapter declared no runtime capabilities.
	Runtime      *RuntimeInfo      `json:"runtime,omitempty"`
	Auth         AuthState         `json:"auth"`
	Dependencies []Dependency      `json:"dependencies"`
	Presets      []PresetStatus    `json:"presets"`
	ToolProbes   []ToolProbeResult `json:"tool_probes"`
}

// Validate checks the result.
func (r ProbeResult) Validate() error {
	var c checker
	if r.Runtime != nil {
		c.field("runtime", r.Runtime.Validate())
	}
	c.field("auth", enumOK(r.Auth, authStates))
	c.field("dependencies", present(r.Dependencies))
	list(&c, "dependencies", r.Dependencies)
	c.field("presets", present(r.Presets))
	list(&c, "presets", r.Presets)
	c.field("tool_probes", present(r.ToolProbes))
	c.ok(len(r.ToolProbes) <= maxProbes, "tool_probes: %d entries, at most %d", len(r.ToolProbes), maxProbes)
	list(&c, "tool_probes", r.ToolProbes)
	return c.err
}

// Availability is one condition in an availability snapshot.
type Availability struct {
	Condition AvailabilityCondition `json:"condition"`
	// Endpoints limits the condition to these endpoints; absent means every
	// endpoint of the adapter.
	Endpoints []EndpointRef `json:"endpoints,omitempty"`
	// SinceMS is when the condition began, if known. The daemon uses it only when
	// it is not later than the moment the daemon received the snapshot.
	SinceMS int64  `json:"since_ms,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Validate checks the entry.
func (a Availability) Validate() error {
	var c checker
	c.field("condition", enumOK(a.Condition, availabilityConditions))
	for i, e := range a.Endpoints {
		c.field("endpoints"+index(i), e.Validate())
	}
	if a.SinceMS != 0 {
		c.field("since_ms", timestampMS(a.SinceMS))
	}
	if a.Detail != "" {
		c.field("detail", strLen(a.Detail, 1, MaxDetailLen))
	}
	return c.err
}

// AvailabilityParams is a complete snapshot of what currently keeps the adapter
// from working; an empty list means everything is fine. Each notification
// replaces the previous one.
type AvailabilityParams struct {
	Conditions []Availability `json:"conditions"`
}

// Validate checks the snapshot.
func (p AvailabilityParams) Validate() error {
	var c checker
	c.field("conditions", present(p.Conditions))
	list(&c, "conditions", p.Conditions)
	return c.err
}

// Empty is the parameters or result of a method that carries none.
type Empty struct{}

// Validate always succeeds.
func (Empty) Validate() error { return nil }

// CancelRequestParams asks the peer to stop working on a request it received.
// The peer layer handles the notification itself and owns its params and the
// request id rule; the type is listed here so the method table is complete.
type CancelRequestParams = peer.CancelRequestParams
