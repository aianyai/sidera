package adapterproto

import "slices"

// Every enum of the wire protocol is a string type. The zero value, the empty
// string, is "unset" and never valid. Each type lists its values once, in the
// table its Valid method and EnumTables use; the schema states the same lists.

// Frontend is a way an agent reaches the channel's tools: the command line or an
// MCP server.
type Frontend string

const (
	FrontendCLI Frontend = "cli"
	FrontendMCP Frontend = "mcp"
)

var frontends = []Frontend{FrontendCLI, FrontendMCP}

// Valid reports whether f is a known front end.
func (f Frontend) Valid() bool { return slices.Contains(frontends, f) }

// ContainerKind is the isolation a native process was started under, so that it
// survives the adapter.
type ContainerKind string

const (
	ContainerSystemdUnit   ContainerKind = "systemd_unit"
	ContainerProcessGroup  ContainerKind = "process_group"
	ContainerLaunchdJob    ContainerKind = "launchd_job"
	ContainerWindowsJob    ContainerKind = "windows_job"
	ContainerScheduledTask ContainerKind = "scheduled_task"
	ContainerNone          ContainerKind = "none"
)

var containerKinds = []ContainerKind{ContainerSystemdUnit, ContainerProcessGroup, ContainerLaunchdJob, ContainerWindowsJob, ContainerScheduledTask, ContainerNone}

// Valid reports whether k is a known container kind.
func (k ContainerKind) Valid() bool { return slices.Contains(containerKinds, k) }

// AuthState is what a probe found about the runtime's credentials.
type AuthState string

const (
	AuthValid   AuthState = "valid"
	AuthInvalid AuthState = "invalid"
	AuthUnknown AuthState = "unknown"
)

var authStates = []AuthState{AuthValid, AuthInvalid, AuthUnknown}

// Valid reports whether a is a known state.
func (a AuthState) Valid() bool { return slices.Contains(authStates, a) }

// DependencyState is what a probe found about one program or service the
// adapter needs.
type DependencyState string

const (
	DependencyOK           DependencyState = "ok"
	DependencyMissing      DependencyState = "missing"
	DependencyWrongVersion DependencyState = "wrong_version"
)

var dependencyStates = []DependencyState{DependencyOK, DependencyMissing, DependencyWrongVersion}

// Valid reports whether s is a known state.
func (s DependencyState) Valid() bool { return slices.Contains(dependencyStates, s) }

// ProbeOutcome is the result of one tool-connectivity probe.
type ProbeOutcome string

const (
	ProbeRan    ProbeOutcome = "ran"
	ProbeNotRun ProbeOutcome = "not_run"
	ProbeFailed ProbeOutcome = "failed"
)

var probeOutcomes = []ProbeOutcome{ProbeRan, ProbeNotRun, ProbeFailed}

// Valid reports whether o is a known outcome.
func (o ProbeOutcome) Valid() bool { return slices.Contains(probeOutcomes, o) }

// ProbeMethod is how a tool probe was run.
type ProbeMethod string

const (
	ProbeSandboxed   ProbeMethod = "sandboxed"
	ProbeUnsandboxed ProbeMethod = "unsandboxed"
	ProbeNativeTurn  ProbeMethod = "native_turn"
)

var probeMethods = []ProbeMethod{ProbeSandboxed, ProbeUnsandboxed, ProbeNativeTurn}

// Valid reports whether m is a known method.
func (m ProbeMethod) Valid() bool { return slices.Contains(probeMethods, m) }

// AvailabilityCondition is something that keeps a runtime from working, as the
// adapter reports it in an availability snapshot. A runtime version outside the
// manifest's tested ranges is not one: the runtime still works, and the daemon
// alone decides whether to show it (adapterreg.RuntimeVersionUntested).
type AvailabilityCondition string

const (
	// ConditionRuntimeAuth: the runtime refuses the credentials.
	ConditionRuntimeAuth AvailabilityCondition = "runtime_auth"
	// ConditionNativeUnavailable: the runtime's native service does not answer.
	ConditionNativeUnavailable AvailabilityCondition = "native_unavailable"
	// ConditionDependencyMissing: a program or service the adapter needs is missing.
	ConditionDependencyMissing AvailabilityCondition = "dependency_missing"
)

var availabilityConditions = []AvailabilityCondition{ConditionRuntimeAuth, ConditionNativeUnavailable, ConditionDependencyMissing}

// Valid reports whether c is a known condition.
func (c AvailabilityCondition) Valid() bool { return slices.Contains(availabilityConditions, c) }

// AttachState is what the adapter found when it took a session back at start.
type AttachState string

const (
	// AttachAttached: the native session exists and the adapter controls it again.
	AttachAttached AttachState = "attached"
	// AttachUnreachable: the native session may exist but cannot be reached now.
	AttachUnreachable AttachState = "unreachable"
	// AttachLost: the native session no longer exists.
	AttachLost AttachState = "lost"
)

var attachStates = []AttachState{AttachAttached, AttachUnreachable, AttachLost}

// Valid reports whether s is a known state.
func (s AttachState) Valid() bool { return slices.Contains(attachStates, s) }

// CreateOutcome is the adapter's answer to a session creation.
type CreateOutcome string

const (
	// CreateCreated: the session exists and the first prompt is in its native
	// record, which is the proof of readiness.
	CreateCreated CreateOutcome = "created"
	// CreateFailed: certainly no session was created.
	CreateFailed CreateOutcome = "failed"
	// CreateIndeterminate: a session may or may not exist; the daemon reconciles.
	CreateIndeterminate CreateOutcome = "indeterminate"
)

var createOutcomes = []CreateOutcome{CreateCreated, CreateFailed, CreateIndeterminate}

// Valid reports whether o is a known outcome.
func (o CreateOutcome) Valid() bool { return slices.Contains(createOutcomes, o) }

// CreateFailure says why a session was certainly not created.
type CreateFailure string

const (
	CreateFailPresetUnknown     CreateFailure = "preset_unknown"
	CreateFailPresetInvalid     CreateFailure = "preset_invalid"
	CreateFailRuntimeAuth       CreateFailure = "runtime_auth"
	CreateFailNativeUnavailable CreateFailure = "native_unavailable"
	CreateFailDependencyMissing CreateFailure = "dependency_missing"
	CreateFailNativeError       CreateFailure = "native_error"
)

var createFailures = []CreateFailure{CreateFailPresetUnknown, CreateFailPresetInvalid, CreateFailRuntimeAuth, CreateFailNativeUnavailable, CreateFailDependencyMissing, CreateFailNativeError}

// Valid reports whether f is a known failure.
func (f CreateFailure) Valid() bool { return slices.Contains(createFailures, f) }

// CheckpointScope says what a checkpoint belongs to.
type CheckpointScope string

const (
	// ScopeAttempt: the state of one creation or prompt attempt, keyed by its AttemptID.
	ScopeAttempt CheckpointScope = "attempt"
	// ScopeSession: the state of one session, keyed by its EndpointRef.
	ScopeSession CheckpointScope = "session"
)

var checkpointScopes = []CheckpointScope{ScopeAttempt, ScopeSession}

// Valid reports whether s is a known scope.
func (s CheckpointScope) Valid() bool { return slices.Contains(checkpointScopes, s) }

// PromptStage is how far a prompt got when the adapter answered.
type PromptStage string

const (
	// PromptAccepted: the runtime accepted the prompt but it is not yet durable.
	PromptAccepted PromptStage = "accepted"
	// PromptPersisted: the prompt is in the runtime's durable record.
	PromptPersisted PromptStage = "persisted"
	// PromptRefused: certainly nothing was delivered.
	PromptRefused PromptStage = "refused"
)

var promptStages = []PromptStage{PromptAccepted, PromptPersisted, PromptRefused}

// Valid reports whether s is a known stage.
func (s PromptStage) Valid() bool { return slices.Contains(promptStages, s) }

// PromptRefusal says why a prompt was certainly not delivered.
type PromptRefusal string

const (
	// RefusalBusy: the runtime has a single writer and it is occupied.
	RefusalBusy              PromptRefusal = "busy"
	RefusalRuntimeAuth       PromptRefusal = "runtime_auth"
	RefusalNativeUnavailable PromptRefusal = "native_unavailable"
	RefusalSessionLost       PromptRefusal = "session_lost"
	RefusalControlUnknown    PromptRefusal = "control_unknown"
)

var promptRefusals = []PromptRefusal{RefusalBusy, RefusalRuntimeAuth, RefusalNativeUnavailable, RefusalSessionLost, RefusalControlUnknown}

// Valid reports whether r is a known refusal.
func (r PromptRefusal) Valid() bool { return slices.Contains(promptRefusals, r) }

// TurnFinal is how a turn ended.
type TurnFinal string

const (
	TurnCompleted   TurnFinal = "completed"
	TurnInterrupted TurnFinal = "interrupted"
	TurnFailed      TurnFinal = "failed"
)

var turnFinals = []TurnFinal{TurnCompleted, TurnInterrupted, TurnFailed}

// Valid reports whether f is a known ending.
func (f TurnFinal) Valid() bool { return slices.Contains(turnFinals, f) }

// InterruptStage is how far an interrupt got when the adapter answered.
type InterruptStage string

const (
	InterruptRequested  InterruptStage = "requested"
	InterruptStopped    InterruptStage = "stopped"
	InterruptNotRunning InterruptStage = "not_running"
	InterruptRefused    InterruptStage = "refused"
)

var interruptStages = []InterruptStage{InterruptRequested, InterruptStopped, InterruptNotRunning, InterruptRefused}

// Valid reports whether s is a known stage.
func (s InterruptStage) Valid() bool { return slices.Contains(interruptStages, s) }

// InterruptRefusal says why an interrupt could not be issued.
type InterruptRefusal string

const (
	InterruptNativeUnreachable InterruptRefusal = "native_unreachable"
	InterruptNativeError       InterruptRefusal = "native_error"
)

var interruptRefusals = []InterruptRefusal{InterruptNativeUnreachable, InterruptNativeError}

// Valid reports whether r is a known refusal.
func (r InterruptRefusal) Valid() bool { return slices.Contains(interruptRefusals, r) }

// CloseOutcome is the adapter's answer to closing a session.
type CloseOutcome string

const (
	CloseClosed      CloseOutcome = "closed"
	CloseUnconfirmed CloseOutcome = "close_unconfirmed"
)

var closeOutcomes = []CloseOutcome{CloseClosed, CloseUnconfirmed}

// Valid reports whether o is a known outcome.
func (o CloseOutcome) Valid() bool { return slices.Contains(closeOutcomes, o) }

// CloseReason says why a close could not be confirmed.
type CloseReason string

const (
	CloseTurnStillRunning  CloseReason = "turn_still_running"
	CloseNativeUnreachable CloseReason = "native_unreachable"
)

var closeReasons = []CloseReason{CloseTurnStillRunning, CloseNativeUnreachable}

// Valid reports whether r is a known reason.
func (r CloseReason) Valid() bool { return slices.Contains(closeReasons, r) }

// ReconcileVerdict is what reconciliation found about a prompt attempt.
type ReconcileVerdict string

const (
	// VerdictFound: the attempt is in the native record.
	VerdictFound ReconcileVerdict = "found"
	// VerdictProvablyAbsent: the attempt certainly did not reach the runtime.
	VerdictProvablyAbsent ReconcileVerdict = "provably_absent"
	// VerdictIndeterminate: it cannot be told yet.
	VerdictIndeterminate ReconcileVerdict = "indeterminate"
)

var reconcileVerdicts = []ReconcileVerdict{VerdictFound, VerdictProvablyAbsent, VerdictIndeterminate}

// Valid reports whether v is a known verdict.
func (v ReconcileVerdict) Valid() bool { return slices.Contains(reconcileVerdicts, v) }

// TurnState is the state of the turn a found attempt started.
type TurnState string

const (
	TurnStateRunning     TurnState = "running"
	TurnStateCompleted   TurnState = "completed"
	TurnStateInterrupted TurnState = "interrupted"
	TurnStateFailed      TurnState = "failed"
)

var turnStates = []TurnState{TurnStateRunning, TurnStateCompleted, TurnStateInterrupted, TurnStateFailed}

// Valid reports whether s is a known state.
func (s TurnState) Valid() bool { return slices.Contains(turnStates, s) }

// GroundsCode is the closed set of reasons behind a reconciliation verdict.
type GroundsCode string

const (
	// GroundsAttemptInRecord (found): the attempt is in the native record.
	GroundsAttemptInRecord GroundsCode = "attempt_in_record"
	// GroundsRecordCompleteWithoutAttempt (provably_absent): the writer has stopped
	// and the whole raw record was scanned.
	GroundsRecordCompleteWithoutAttempt GroundsCode = "record_complete_without_attempt"
	// GroundsNeverDispatched (provably_absent): the checkpoint shows the native
	// interface was never called.
	GroundsNeverDispatched GroundsCode = "never_dispatched"
	// GroundsTurnRunning (indeterminate): a turn is running; retry after it ends.
	GroundsTurnRunning GroundsCode = "turn_running"
	// GroundsRecordUnreadable (indeterminate).
	GroundsRecordUnreadable GroundsCode = "record_unreadable"
	// GroundsNativeUnreachable (indeterminate).
	GroundsNativeUnreachable GroundsCode = "native_unreachable"
	// GroundsRuntimeVersionUntested (indeterminate).
	GroundsRuntimeVersionUntested GroundsCode = "runtime_version_untested"
)

var groundsCodes = []GroundsCode{
	GroundsAttemptInRecord, GroundsRecordCompleteWithoutAttempt, GroundsNeverDispatched,
	GroundsTurnRunning, GroundsRecordUnreadable, GroundsNativeUnreachable, GroundsRuntimeVersionUntested,
}

// Valid reports whether c is a known code.
func (c GroundsCode) Valid() bool { return slices.Contains(groundsCodes, c) }

// AckVerdict is the daemon's decision on an acknowledgement an adapter makes on
// the agent's behalf.
type AckVerdict string

const (
	AckAllowed AckVerdict = "allowed"
	AckRefused AckVerdict = "refused"
)

var ackVerdicts = []AckVerdict{AckAllowed, AckRefused}

// Valid reports whether v is a known verdict.
func (v AckVerdict) Valid() bool { return slices.Contains(ackVerdicts, v) }

// AckReason says why an acknowledgement was allowed or refused.
type AckReason string

const (
	AckSeenInFull      AckReason = "seen_in_full"
	AckReplaced        AckReason = "replaced"
	AckTruncated       AckReason = "truncated"
	AckContentMismatch AckReason = "content_mismatch"
	// AckUnknownLease: the daemon holds no page for the lease.
	AckUnknownLease AckReason = "unknown_lease"
)

var ackReasons = []AckReason{AckSeenInFull, AckReplaced, AckTruncated, AckContentMismatch, AckUnknownLease}

// Valid reports whether r is a known reason.
func (r AckReason) Valid() bool { return slices.Contains(ackReasons, r) }

// ItemAck is the outcome of acknowledging one message part.
type ItemAck string

const (
	ItemRecorded  ItemAck = "recorded"
	ItemCompleted ItemAck = "completed"
	ItemDuplicate ItemAck = "duplicate"
	// ItemRefusedNoHandout: the part was never handed out to this endpoint.
	// That covers a message the daemon holds nothing of for the endpoint and a
	// part number beyond the message's last part: both are parts nobody was
	// given, so there is nothing to acknowledge.
	ItemRefusedNoHandout ItemAck = "refused_no_handout"
	ItemRefusedLease     ItemAck = "refused_lease"
	ItemTooLate          ItemAck = "too_late"
)

var itemAcks = []ItemAck{ItemRecorded, ItemCompleted, ItemDuplicate, ItemRefusedNoHandout, ItemRefusedLease, ItemTooLate}

// Valid reports whether a is a known outcome.
func (a ItemAck) Valid() bool { return slices.Contains(itemAcks, a) }

// Disposition is how an adapter handled a request the native runtime sent it.
type Disposition string

const (
	// DispositionPreset: answered according to the preset.
	DispositionPreset Disposition = "preset"
	// DispositionRejectedByPreset: the preset says to refuse this request.
	DispositionRejectedByPreset Disposition = "rejected_by_preset"
	// DispositionRejectedUnknown: the method is unknown, so it was refused.
	DispositionRejectedUnknown Disposition = "rejected_unknown"
	// DispositionLate: no answer went out within the time the runtime allows.
	DispositionLate Disposition = "late"
)

var dispositions = []Disposition{DispositionPreset, DispositionRejectedByPreset, DispositionRejectedUnknown, DispositionLate}

// Valid reports whether d is a known disposition.
func (d Disposition) Valid() bool { return slices.Contains(dispositions, d) }

// DeliveryState is the state of one delivery as the sender sees it. The names
// are those of the delivery states.
type DeliveryState string

const (
	DeliveryQueued             DeliveryState = "queued"
	DeliveryAccepted           DeliveryState = "accepted"
	DeliveryDelivered          DeliveryState = "delivered"
	DeliveryRejected           DeliveryState = "rejected"
	DeliveryExpiredUnconfirmed DeliveryState = "expired_unconfirmed"
	DeliveryUnknown            DeliveryState = "delivery_unknown"
	DeliveryClosedUnconfirmed  DeliveryState = "closed_unconfirmed"
)

var deliveryStates = []DeliveryState{DeliveryQueued, DeliveryAccepted, DeliveryDelivered, DeliveryRejected, DeliveryExpiredUnconfirmed, DeliveryUnknown, DeliveryClosedUnconfirmed}

// Valid reports whether s is a known state.
func (s DeliveryState) Valid() bool { return slices.Contains(deliveryStates, s) }

// RejectReason says why a delivery was rejected. The names are those of the
// delivery rejection reasons.
type RejectReason string

const (
	RejectUnauthorized       RejectReason = "unauthorized"
	RejectEndpointMissing    RejectReason = "endpoint_missing"
	RejectEndpointClosed     RejectReason = "endpoint_closed"
	RejectEndpointDeclined   RejectReason = "endpoint_declined"
	RejectTooLarge           RejectReason = "too_large"
	RejectExpired            RejectReason = "expired"
	RejectBacklogExpired     RejectReason = "backlog_expired"
	RejectSecondCheckFailed  RejectReason = "second_check_failed"
	RejectMalformed          RejectReason = "malformed"
	RejectDeadlineOutOfRange RejectReason = "deadline_out_of_range"
)

var rejectReasons = []RejectReason{
	RejectUnauthorized, RejectEndpointMissing, RejectEndpointClosed, RejectEndpointDeclined, RejectTooLarge,
	RejectExpired, RejectBacklogExpired, RejectSecondCheckFailed, RejectMalformed, RejectDeadlineOutOfRange,
}

// Valid reports whether r is a known reason.
func (r RejectReason) Valid() bool { return slices.Contains(rejectReasons, r) }

// ControlSignal names a control signal a message can carry. The names are those
// of the authorization actions of the same meaning.
type ControlSignal string

const (
	SignalSessionCreate    ControlSignal = "session.create"
	SignalSessionClose     ControlSignal = "session.close"
	SignalSessionInterrupt ControlSignal = "session.interrupt"
	SignalEndpointStatus   ControlSignal = "endpoint.status"
)

var controlSignals = []ControlSignal{SignalSessionCreate, SignalSessionClose, SignalSessionInterrupt, SignalEndpointStatus}

// Valid reports whether s is a known signal.
func (s ControlSignal) Valid() bool { return slices.Contains(controlSignals, s) }

// EnumTables returns the values of every wire enum, keyed by the name of its
// Go type, which is also the name of its definition in the schema.
func EnumTables() map[string][]string {
	return map[string][]string{
		"Frontend":              strs(frontends),
		"ContainerKind":         strs(containerKinds),
		"AuthState":             strs(authStates),
		"DependencyState":       strs(dependencyStates),
		"ProbeOutcome":          strs(probeOutcomes),
		"ProbeMethod":           strs(probeMethods),
		"AvailabilityCondition": strs(availabilityConditions),
		"AttachState":           strs(attachStates),
		"CreateOutcome":         strs(createOutcomes),
		"CreateFailure":         strs(createFailures),
		"CheckpointScope":       strs(checkpointScopes),
		"PromptStage":           strs(promptStages),
		"PromptRefusal":         strs(promptRefusals),
		"TurnFinal":             strs(turnFinals),
		"InterruptStage":        strs(interruptStages),
		"InterruptRefusal":      strs(interruptRefusals),
		"CloseOutcome":          strs(closeOutcomes),
		"CloseReason":           strs(closeReasons),
		"ReconcileVerdict":      strs(reconcileVerdicts),
		"TurnState":             strs(turnStates),
		"GroundsCode":           strs(groundsCodes),
		"AckVerdict":            strs(ackVerdicts),
		"AckReason":             strs(ackReasons),
		"ItemAck":               strs(itemAcks),
		"Disposition":           strs(dispositions),
		"DeliveryState":         strs(deliveryStates),
		"RejectReason":          strs(rejectReasons),
		"ControlSignal":         strs(controlSignals),
	}
}
