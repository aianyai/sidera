package adapterhost

import (
	"context"
	"fmt"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Engine is what the host asks of the engine. The methods of one connection are
// called in this order: AdapterAdmitted, AdapterStarted, ProbeTokens,
// AdapterProbed, and from then on AdapterNotified and the requests of the
// adapter, until AdapterGone. A notification reaches AdapterNotified only after
// AdapterAdmitted has returned, in the order the adapter sent it.
//
// An error from a method that answers a request of the adapter (RegisterDirect,
// UnregisterDirect, CheckpointPut and the methods of the optional interfaces) is
// handed to the adapter as it is when it is a *peer.Error, and as an internal
// error (-32603) otherwise; what a refusal of the engine is worth on the wire is
// decided by whoever puts the engine behind this interface, not by the host. An
// error from any other method means the connection cannot be used: the host
// ends it and starts the instance again after a backoff.
type Engine interface {
	// AdapterAdmitted is told that the adapter answered initialize with info and
	// that its declaration was admitted as a. It makes the instance's runtime
	// endpoint exist when the adapter declared a runtime, and returns what
	// adapter.start carries.
	AdapterAdmitted(ctx context.Context, instance string, a adapterreg.Admission, info adapterproto.InitializeResult) (adapterproto.StartParams, error)
	// AdapterStarted is told what the adapter answered to adapter.start.
	AdapterStarted(ctx context.Context, instance string, r adapterproto.StartResult) error
	// ProbeTokens issues one single-use token for each front end of each preset
	// the adapter is asked to probe, and returns the adapter.probe parameters.
	ProbeTokens(ctx context.Context, instance string) ([]adapterproto.ToolProbeRequest, error)
	// AdapterProbed is told what the adapter answered to adapter.probe.
	AdapterProbed(ctx context.Context, instance string, r adapterproto.ProbeResult) error
	// AdapterGone is told that the connection of the instance ended or never came
	// to be, and why. It may be called for an instance the engine already knows
	// to be offline.
	AdapterGone(ctx context.Context, instance string, cause GoneCause) error

	// AdapterNotified is told a notification of the adapter, in the order the
	// adapter sent it. A notification about an endpoint the engine does not know
	// is not an error: a session can report a turn before its creation has
	// returned.
	AdapterNotified(ctx context.Context, instance string, n Notification) error

	// RegisterDirect, UnregisterDirect and CheckpointPut answer the requests of
	// the adapter of the same names.
	RegisterDirect(ctx context.Context, instance string, p adapterproto.RegisterParams) (adapterproto.RegisterResult, error)
	UnregisterDirect(ctx context.Context, instance string, p adapterproto.UnregisterParams) error
	CheckpointPut(ctx context.Context, instance string, p adapterproto.CheckpointPutParams) error
}

// EndpointCalls is the part of the adapter protocol that a direct endpoint reads
// and writes its messages with. An engine that offers it, which the host finds
// by asking the value it was given, answers endpoint.send, endpoint.recv,
// endpoint.ack and endpoint.message_status through it; for an engine that does
// not, those methods are answered as an unknown method.
type EndpointCalls interface {
	EndpointSend(ctx context.Context, instance string, p adapterproto.SendParams) (adapterproto.SendResult, error)
	EndpointRecv(ctx context.Context, instance string, p adapterproto.RecvParams) (adapterproto.RecvResult, error)
	EndpointAck(ctx context.Context, instance string, p adapterproto.AckParams) (adapterproto.AckResult, error)
	EndpointMessageStatus(ctx context.Context, instance string, p adapterproto.MessageStatusParams) (adapterproto.MessageStatusResult, error)
}

// ObservedCalls is the part of the adapter protocol that acknowledges on the
// agent's behalf. An engine that offers it answers recv.observed through it;
// for one that does not, the method is answered as an unknown method.
type ObservedCalls interface {
	RecvObserved(ctx context.Context, instance string, p adapterproto.RecvObservedParams) (adapterproto.RecvObservedResult, error)
}

// Notification is a notification of the adapter, decoded and validated: the
// method name and the parameters as the type of that method, which a reader finds
// with a type switch on Params (adapterproto.AvailabilityParams,
// TurnStartedParams, TurnEndedParams, PromptPersistedParams,
// NativeRequestHandledParams).
type Notification struct {
	Method string
	Params adapterproto.Message
}

// GoneCause says why the host reports an adapter as gone.
type GoneCause uint8

// The causes. The zero value is not a cause.
const (
	// GoneConnectionEnded: the stream ended or the process exited while the
	// adapter was in service.
	GoneConnectionEnded GoneCause = iota + 1
	// GonePingsFailed: OfflineAfterPingFailures pings in a row went unanswered.
	GonePingsFailed
	// GoneStartFailed: the adapter was not taken on: the process did not start,
	// its answers were wrong, it was refused admission, it held no instance lock
	// or the engine could not take it.
	GoneStartFailed

	goneEnd
)

var goneNames = [goneEnd]string{
	GoneConnectionEnded: "connection_ended",
	GonePingsFailed:     "pings_failed",
	GoneStartFailed:     "start_failed",
}

// String returns the name of the cause.
func (c GoneCause) String() string {
	if c > 0 && c < goneEnd {
		return goneNames[c]
	}
	return fmt.Sprintf("invalid(%d)", uint8(c))
}

// GoneCauses lists every cause, in order.
func GoneCauses() []GoneCause {
	out := make([]GoneCause, 0, goneEnd-1)
	for c := GoneCause(1); c < goneEnd; c++ {
		out = append(out, c)
	}
	return out
}
