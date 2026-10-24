package adapterreg

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/handoff"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// ErrUnmapped is wrapped by the mapping functions when a value is not one the
// protocol defines for the field.
var ErrUnmapped = errors.New("adapterreg: value has no mapping")

func unmapped(what string, v any) error { return fmt.Errorf("%w: %s %v", ErrUnmapped, what, v) }

// PromptOutcome maps the stage a prompt.deliver result reports onto what handoff
// records for the attempt: accepted and persisted both mean the prompt reached
// the session; refused means certainly not.
func PromptOutcome(r adapterproto.PromptDeliverResult) (handoff.AttemptOutcome, error) {
	switch r.Stage {
	case adapterproto.PromptAccepted, adapterproto.PromptPersisted:
		return handoff.AttemptDelivered, nil
	case adapterproto.PromptRefused:
		return handoff.AttemptFailed, nil
	}
	return 0, unmapped("prompt stage", string(r.Stage))
}

// PromptOutcomeOnError maps the error a peer.Call of prompt.deliver returned onto
// the attempt outcome, by the effect rule of package peer. An error that proves
// the request had no effect (most error responses, and a request refused before
// it was written) means the prompt certainly did not reach the session. Every
// other failure leaves the attempt unknown, to be reconciled: an internal error,
// a cancellation, no response at all, a timeout, a closed connection, and any
// error the rule does not know.
//
// 1005 attempt_conflict is one of those, although it is an application code the
// rule counts as no effect: it says the attempt identifier is already known with
// other parameters, so an earlier request of the same attempt reached the
// adapter and may have delivered the prompt. The conflicting request itself did
// nothing, but the attempt's outcome is exactly what the earlier one left.
//
// A nil error is no failure; it maps to unknown as well, so that a caller that
// mistakenly passes one never records a delivery as failed.
func PromptOutcomeOnError(err error) handoff.AttemptOutcome {
	var remote *peer.Error
	if errors.As(err, &remote) && remote.Code == adapterproto.CodeAttemptConflict {
		return handoff.AttemptUnknown
	}
	if peer.NoEffect(err) {
		return handoff.AttemptFailed
	}
	return handoff.AttemptUnknown
}

// ReconcileOutcome maps a reconciliation verdict onto the corrected attempt
// outcome: found means delivered, provably absent means failed, and an
// indeterminate verdict keeps the attempt unknown.
func ReconcileOutcome(r adapterproto.ReconcileResult) (handoff.AttemptOutcome, error) {
	switch r.Verdict {
	case adapterproto.VerdictFound:
		return handoff.AttemptDelivered, nil
	case adapterproto.VerdictProvablyAbsent:
		return handoff.AttemptFailed, nil
	case adapterproto.VerdictIndeterminate:
		return handoff.AttemptUnknown, nil
	}
	return 0, unmapped("reconcile verdict", string(r.Verdict))
}

// Signals maps an availability snapshot onto the causes handoff judges blocked
// delivery by, for one endpoint.
//
//   - runtime_auth is AuthInvalid;
//   - native_unavailable and dependency_missing are ServiceUnavailable.
//
// A runtime version outside the tested ranges is no cause, and no condition of
// a snapshot: the runtime still works, and RuntimeVersionUntested judges it.
//
// A condition names endpoints or, with none named, applies to all of them.
// adapterOffline is the daemon's own observation (the connection dropped, or
// pings failed) and offlineSince the moment it began; it comes first when several
// causes are active, as handoff orders them.
//
// Signals.Since is when the winning cause began, from the adapter's since_ms
// for the conditions of that cause. It is zero, meaning unknown, when any of
// them gives none or gives a moment later than received, the moment the daemon
// got the snapshot: an adapter's clock is for reference only, and handoff then
// measures from the oldest waiting message, which can only overstate the
// duration.
func Signals(conditions []adapterproto.Availability, endpoint adapterproto.EndpointRef, received time.Time, adapterOffline bool, offlineSince time.Time) handoff.Signals {
	var s handoff.Signals
	var auth, service []adapterproto.Availability
	for _, c := range conditions {
		if !appliesTo(c, endpoint) {
			continue
		}
		switch c.Condition {
		case adapterproto.ConditionRuntimeAuth:
			s.AuthInvalid = true
			auth = append(auth, c)
		case adapterproto.ConditionNativeUnavailable, adapterproto.ConditionDependencyMissing:
			s.ServiceUnavailable = true
			service = append(service, c)
		}
	}
	switch {
	case adapterOffline:
		s.AdapterOffline = true
		s.Since = offlineSince
	case s.AuthInvalid:
		s.Since = earliest(auth, received)
	case s.ServiceUnavailable:
		s.Since = earliest(service, received)
	}
	return s
}

func appliesTo(c adapterproto.Availability, endpoint adapterproto.EndpointRef) bool {
	return len(c.Endpoints) == 0 || slices.Contains(c.Endpoints, endpoint)
}

// earliest is the moment the union of the conditions began: the earliest of their
// reported starts, or zero when any start is unknown or lies after received.
func earliest(cs []adapterproto.Availability, received time.Time) time.Time {
	var first time.Time
	for _, c := range cs {
		if c.SinceMS == 0 {
			return time.Time{}
		}
		t := time.UnixMilli(c.SinceMS).UTC()
		if t.After(received) {
			return time.Time{}
		}
		if first.IsZero() || t.Before(first) {
			first = t
		}
	}
	return first
}

// RuntimeVersionUntested reports whether the runtime a probe found runs a version
// that no tested range of the manifest contains. It is the only place that
// judges this: the adapter reports the runtime's name and version and nothing
// about the ranges, so the manifest the daemon admitted cannot be contradicted
// by its adapter. The verdict is the status condition version_untested
// (adapterproto.ControlConditionVersionUntested), which the daemon writes into
// the status of the endpoints and shows the owner; it blocks nothing.
//
// A nil runtime, as for an adapter with no runtime or one that has not been
// probed, makes no claim and is not untested. A manifest with no tested range
// at all contains no version, so every probed version is untested.
func RuntimeVersionUntested(m manifest.Manifest, runtime *adapterproto.RuntimeInfo) bool {
	return runtime != nil && !m.Tests(runtime.Name, runtime.Version)
}

// StatusConditions builds the conditions list of a status reply for one endpoint
// from the snapshot and the daemon's own observations: ascending, without repeats.
// dependency_missing is shown as native_unavailable, as handoff counts it.
// versionUntested is the verdict of RuntimeVersionUntested, which only the daemon
// can give; it adds the sixth condition.
func StatusConditions(conditions []adapterproto.Availability, endpoint adapterproto.EndpointRef, adapterOffline, serverRequestRejected, controlUnknown, versionUntested bool) []adapterproto.ControlCondition {
	set := map[adapterproto.ControlCondition]bool{}
	if adapterOffline {
		set[adapterproto.ControlConditionAdapterOffline] = true
	}
	if serverRequestRejected {
		set[adapterproto.ControlConditionServerRequestRejected] = true
	}
	if controlUnknown {
		set[adapterproto.ControlConditionControlUnknown] = true
	}
	if versionUntested {
		set[adapterproto.ControlConditionVersionUntested] = true
	}
	for _, c := range conditions {
		if !appliesTo(c, endpoint) {
			continue
		}
		switch c.Condition {
		case adapterproto.ConditionRuntimeAuth:
			set[adapterproto.ControlConditionRuntimeAuth] = true
		case adapterproto.ConditionNativeUnavailable, adapterproto.ConditionDependencyMissing:
			set[adapterproto.ControlConditionNativeUnavailable] = true
		}
	}
	var out []adapterproto.ControlCondition
	for c := range set {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// CreateReply maps a session.create result onto the reply to the control signal.
// final is false for an indeterminate result: the daemon keeps reconciling and,
// when the reply deadline passes, writes CreateUnconfirmed. A created result needs
// its endpoint, the one the daemon allocated before it asked.
func CreateReply(r adapterproto.SessionCreateResult, session adapterproto.EndpointRef) (reply adapterproto.ControlCreateReply, final bool, err error) {
	switch r.Outcome {
	case adapterproto.CreateCreated:
		return adapterproto.ControlCreateReply{Outcome: adapterproto.CreateReplyCreated, SessionEndpoint: session}, true, nil
	case adapterproto.CreateFailed:
		var reason adapterproto.ControlCreateReason
		switch r.Reason {
		case adapterproto.CreateFailPresetUnknown:
			reason = adapterproto.CreateReasonPresetUnknown
		case adapterproto.CreateFailPresetInvalid:
			reason = adapterproto.CreateReasonPresetUnavailable
		case adapterproto.CreateFailRuntimeAuth:
			reason = adapterproto.CreateReasonRuntimeAuth
		case adapterproto.CreateFailNativeUnavailable, adapterproto.CreateFailDependencyMissing:
			reason = adapterproto.CreateReasonNativeUnavailable
		case adapterproto.CreateFailNativeError:
			reason = adapterproto.CreateReasonNativeError
		case adapterproto.CreateFailControlUnknown:
			reason = adapterproto.CreateReasonControlUnknown
		default:
			return adapterproto.ControlCreateReply{}, false, unmapped("creation failure", string(r.Reason))
		}
		return adapterproto.ControlCreateReply{Outcome: adapterproto.CreateReplyFailed, Reason: reason}, true, nil
	case adapterproto.CreateIndeterminate:
		return adapterproto.ControlCreateReply{}, false, nil
	}
	return adapterproto.ControlCreateReply{}, false, unmapped("creation outcome", string(r.Outcome))
}

// DaemonCreateReason is why the daemon itself refuses a session.create before
// the adapter is called. The set is closed: a refusal that is not listed here has
// no reply, so it cannot be added without deciding what the sender is told.
type DaemonCreateReason uint8

// The reasons the daemon refuses a creation by itself. The zero value is no
// reason.
const (
	// DaemonCreatePresetUnknown: no preset has that name.
	DaemonCreatePresetUnknown DaemonCreateReason = iota + 1
	// DaemonCreatePresetClosedToLocalSources: the preset exists, but declares
	// that endpoints on this node may not start a session with it.
	DaemonCreatePresetClosedToLocalSources
	// DaemonCreateAdapterOffline: the adapter that owns the runtime is not
	// connected.
	DaemonCreateAdapterOffline
)

// DaemonCreateFailure is the reply to a session.create the daemon refuses
// without calling the adapter. It is a failed creation with no session endpoint:
// none was allocated and nothing was created. A preset closed to local sources
// is preset_unavailable, as an adapter's preset_invalid is: the preset exists and
// cannot be used for this creation. A value that is no reason gives the zero
// reply, which does not validate, so it cannot be sent by mistake.
func DaemonCreateFailure(r DaemonCreateReason) adapterproto.ControlCreateReply {
	var reason adapterproto.ControlCreateReason
	switch r {
	case DaemonCreatePresetUnknown:
		reason = adapterproto.CreateReasonPresetUnknown
	case DaemonCreatePresetClosedToLocalSources:
		reason = adapterproto.CreateReasonPresetUnavailable
	case DaemonCreateAdapterOffline:
		reason = adapterproto.CreateReasonAdapterOffline
	default:
		return adapterproto.ControlCreateReply{}
	}
	return adapterproto.ControlCreateReply{Outcome: adapterproto.CreateReplyFailed, Reason: reason}
}

// CreateUnconfirmed is the reply written when the reply deadline passes, or the
// adapter is lost, before readiness was shown. The session endpoint is the one
// the daemon allocated, so a retry gets the same one.
func CreateUnconfirmed(session adapterproto.EndpointRef, adapterLost bool) adapterproto.ControlCreateReply {
	reason := adapterproto.CreateReasonReadyTimeout
	if adapterLost {
		reason = adapterproto.CreateReasonAdapterLost
	}
	return adapterproto.ControlCreateReply{Outcome: adapterproto.CreateReplyUnconfirmed, SessionEndpoint: session, Reason: reason}
}

// InterruptReply maps a session.interrupt result onto the reply to the control
// signal. turnEnded says whether the session's turn.ended arrived within the
// confirmation wait; a requested interrupt becomes stopped when it did and stays
// requested when it did not.
func InterruptReply(r adapterproto.InterruptResult, turnEnded bool) (adapterproto.ControlInterruptReply, error) {
	switch r.Stage {
	case adapterproto.InterruptStopped:
		return adapterproto.ControlInterruptReply{Outcome: adapterproto.InterruptReplyStopped}, nil
	case adapterproto.InterruptRequested:
		if turnEnded {
			return adapterproto.ControlInterruptReply{Outcome: adapterproto.InterruptReplyStopped}, nil
		}
		return adapterproto.ControlInterruptReply{Outcome: adapterproto.InterruptReplyRequested}, nil
	case adapterproto.InterruptNotRunning:
		return adapterproto.ControlInterruptReply{Outcome: adapterproto.InterruptReplyNotRunning}, nil
	case adapterproto.InterruptRefused:
		switch r.Reason {
		case adapterproto.InterruptNativeUnreachable:
			return adapterproto.ControlInterruptReply{Outcome: adapterproto.InterruptReplyFailed, Reason: adapterproto.InterruptReasonNativeUnreachable}, nil
		case adapterproto.InterruptNativeError:
			return adapterproto.ControlInterruptReply{Outcome: adapterproto.InterruptReplyFailed, Reason: adapterproto.InterruptReasonNativeError}, nil
		}
		return adapterproto.ControlInterruptReply{}, unmapped("interrupt refusal", string(r.Reason))
	}
	return adapterproto.ControlInterruptReply{}, unmapped("interrupt stage", string(r.Stage))
}

// CloseReply maps a session.close result onto the reply to the control signal.
func CloseReply(r adapterproto.CloseResult) (adapterproto.ControlCloseReply, error) {
	switch r.Outcome {
	case adapterproto.CloseClosed:
		return adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyClosed}, nil
	case adapterproto.CloseUnconfirmed:
		switch r.Reason {
		case adapterproto.CloseTurnStillRunning:
			return adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyUnconfirmed, Reason: adapterproto.CloseReasonTurnStillRunning}, nil
		case adapterproto.CloseNativeUnreachable:
			return adapterproto.ControlCloseReply{Outcome: adapterproto.CloseReplyUnconfirmed, Reason: adapterproto.CloseReasonNativeUnreachable}, nil
		}
		return adapterproto.ControlCloseReply{}, unmapped("close reason", string(r.Reason))
	}
	return adapterproto.ControlCloseReply{}, unmapped("close outcome", string(r.Outcome))
}

// ItemAck maps the verdict of delivery.Record.Ack onto the name recv.observed and
// endpoint.ack report. A refused token has no name: the daemon passes the token
// as matching because the connection owns the endpoint, so it cannot occur.
func ItemAck(v delivery.AckVerdict) (adapterproto.ItemAck, error) {
	switch v {
	case delivery.AckRecorded:
		return adapterproto.ItemRecorded, nil
	case delivery.AckCompleted:
		return adapterproto.ItemCompleted, nil
	case delivery.AckDuplicate:
		return adapterproto.ItemDuplicate, nil
	case delivery.AckRefusedNoHandout:
		return adapterproto.ItemRefusedNoHandout, nil
	case delivery.AckRefusedLease:
		return adapterproto.ItemRefusedLease, nil
	case delivery.AckTooLate:
		return adapterproto.ItemTooLate, nil
	}
	return "", unmapped("acknowledgement verdict", uint8(v))
}

// AckReason maps handoff's reason for allowing or refusing an acknowledgement onto
// the name recv.observed reports. An unknown lease is the daemon's own reason,
// adapterproto.AckUnknownLease, and has no handoff counterpart.
func AckReason(r handoff.AckReason) (adapterproto.AckReason, error) {
	switch r {
	case handoff.AckSeenInFull:
		return adapterproto.AckSeenInFull, nil
	case handoff.AckReplaced:
		return adapterproto.AckReplaced, nil
	case handoff.AckTruncated:
		return adapterproto.AckTruncated, nil
	case handoff.AckContentMismatch:
		return adapterproto.AckContentMismatch, nil
	}
	return "", unmapped("acknowledgement reason", uint8(r))
}

// HandshakeReason says why an initialize result does not fit the manifest or the
// request.
type HandshakeReason uint8

const (
	// HandshakeAdapterMismatch: the adapter's id differs from the manifest's.
	HandshakeAdapterMismatch HandshakeReason = iota + 1
	// HandshakeVersionMismatch: the adapter's version differs from the manifest's.
	HandshakeVersionMismatch
	// HandshakeProtocolOutsideRequest: the chosen protocol version is outside the
	// range the daemon announced.
	HandshakeProtocolOutsideRequest
	// HandshakeProtocolOutsideManifest: the chosen protocol version is outside the
	// range the manifest declares.
	HandshakeProtocolOutsideManifest
)

var handshakeNames = [...]string{
	HandshakeAdapterMismatch: "adapter_mismatch", HandshakeVersionMismatch: "version_mismatch",
	HandshakeProtocolOutsideRequest: "protocol_outside_request", HandshakeProtocolOutsideManifest: "protocol_outside_manifest",
}

// String returns the name of the reason.
func (r HandshakeReason) String() string {
	if r >= HandshakeAdapterMismatch && int(r) < len(handshakeNames) {
		return handshakeNames[r]
	}
	return fmt.Sprintf("invalid(%d)", uint8(r))
}

// HandshakeError is returned by CheckHandshake.
type HandshakeError struct{ Reason HandshakeReason }

func (e *HandshakeError) Error() string { return "adapterreg: initialize result: " + e.Reason.String() }

// CheckHandshake checks an initialize result against the request the daemon sent
// and the manifest of the adapter it started: the adapter reports the id and the
// version the manifest declares, and the protocol version it chose lies in the
// range the daemon asked for and in the range the manifest declares.
func CheckHandshake(sent adapterproto.Range, res adapterproto.InitializeResult, m manifest.Manifest) error {
	switch {
	case res.Adapter.ID != m.ID:
		return &HandshakeError{HandshakeAdapterMismatch}
	case res.Adapter.Version != m.Version:
		return &HandshakeError{HandshakeVersionMismatch}
	case !sent.Contains(res.ProtocolVersion):
		return &HandshakeError{HandshakeProtocolOutsideRequest}
	case !m.Protocol.Contains(res.ProtocolVersion):
		return &HandshakeError{HandshakeProtocolOutsideManifest}
	}
	return nil
}

// ActionOf maps a control signal onto the authorization action of the same
// meaning.
func ActionOf(s adapterproto.ControlSignal) (authz.Action, error) {
	switch s {
	case adapterproto.SignalSessionCreate:
		return authz.ActionSessionCreate, nil
	case adapterproto.SignalSessionClose:
		return authz.ActionSessionClose, nil
	case adapterproto.SignalSessionInterrupt:
		return authz.ActionSessionInterrupt, nil
	case adapterproto.SignalEndpointStatus:
		return authz.ActionEndpointStatus, nil
	}
	return 0, unmapped("control signal", string(s))
}
