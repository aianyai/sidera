package adapterproto

import "fmt"

// Method names. The names are verbs on nouns, "noun.verb"; "$/" marks a
// protocol-level method that has no business meaning.
const (
	MethodInitialize            = "initialize"
	MethodPing                  = "ping"
	MethodCancelRequest         = "$/cancel_request"
	MethodShutdown              = "shutdown"
	MethodAdapterStart          = "adapter.start"
	MethodAdapterProbe          = "adapter.probe"
	MethodAdapterAvailability   = "adapter.availability"
	MethodSessionCreate         = "session.create"
	MethodCheckpointPut         = "checkpoint.put"
	MethodPromptDeliver         = "prompt.deliver"
	MethodPromptPersisted       = "prompt.persisted"
	MethodTurnStarted           = "turn.started"
	MethodTurnEnded             = "turn.ended"
	MethodSessionInterrupt      = "session.interrupt"
	MethodSessionClose          = "session.close"
	MethodPromptReconcile       = "prompt.reconcile"
	MethodRecvObserved          = "recv.observed"
	MethodNativeRequestHandled  = "native_request.handled"
	MethodEndpointRegister      = "endpoint.register"
	MethodEndpointUnregister    = "endpoint.unregister"
	MethodEndpointPending       = "endpoint.pending"
	MethodEndpointRecv          = "endpoint.recv"
	MethodEndpointAck           = "endpoint.ack"
	MethodEndpointSend          = "endpoint.send"
	MethodEndpointMessageStatus = "endpoint.message_status"
)

// Direction says which peer may call a method.
type Direction string

const (
	// DaemonToAdapter: the daemon calls, the adapter implements.
	DaemonToAdapter Direction = "daemon_to_adapter"
	// AdapterToDaemon: the adapter calls, the daemon implements.
	AdapterToDaemon Direction = "adapter_to_daemon"
	// Both: either peer may call.
	Both Direction = "both"
)

// Directions lists the values of Direction.
func Directions() []Direction { return []Direction{DaemonToAdapter, AdapterToDaemon, Both} }

// CalledBy reports whether the peer on the given side may send the method.
// fromDaemon is true when the sender is the daemon.
func (d Direction) CalledBy(fromDaemon bool) bool {
	switch d {
	case Both:
		return true
	case DaemonToAdapter:
		return fromDaemon
	case AdapterToDaemon:
		return !fromDaemon
	}
	return false
}

// MethodInfo describes one method: its direction, whether it is a request, which
// has a result, or a notification, which has none, and constructors for fresh
// parameter and result values to decode into.
type MethodInfo struct {
	Name      string
	Direction Direction
	// Notification is true for a method that has no response.
	Notification bool
	newParams    func() Message
	newResult    func() Message
}

// NewParams returns a zero value of the parameter type, ready to decode into.
func (m MethodInfo) NewParams() Message { return m.newParams() }

// NewResult returns a zero value of the result type, or nil for a notification.
func (m MethodInfo) NewResult() Message {
	if m.newResult == nil {
		return nil
	}
	return m.newResult()
}

// DecodeParams strictly decodes and validates the parameters of the method.
func (m MethodInfo) DecodeParams(data []byte) (Message, error) {
	v := m.newParams()
	return v, Decode(data, v)
}

// DecodeResult strictly decodes and validates the result of the method.
func (m MethodInfo) DecodeResult(data []byte) (Message, error) {
	if m.newResult == nil {
		return nil, fmt.Errorf("adapterproto: %s is a notification and has no result", m.Name)
	}
	v := m.newResult()
	return v, Decode(data, v)
}

func request[P, R any, PP interface {
	*P
	Message
}, PR interface {
	*R
	Message
}](name string, d Direction) MethodInfo {
	return MethodInfo{
		Name: name, Direction: d,
		newParams: func() Message { return PP(new(P)) },
		newResult: func() Message { return PR(new(R)) },
	}
}

func notification[P any, PP interface {
	*P
	Message
}](name string, d Direction) MethodInfo {
	return MethodInfo{
		Name: name, Direction: d, Notification: true,
		newParams: func() Message { return PP(new(P)) },
	}
}

// Methods returns every method of the protocol, requests and notifications, in a
// stable order.
func Methods() []MethodInfo {
	return []MethodInfo{
		request[InitializeParams, InitializeResult](MethodInitialize, DaemonToAdapter),
		request[Empty, Empty](MethodPing, Both),
		notification[CancelRequestParams](MethodCancelRequest, Both),
		request[Empty, Empty](MethodShutdown, DaemonToAdapter),
		request[StartParams, StartResult](MethodAdapterStart, DaemonToAdapter),
		request[ProbeParams, ProbeResult](MethodAdapterProbe, DaemonToAdapter),
		notification[AvailabilityParams](MethodAdapterAvailability, AdapterToDaemon),
		request[SessionCreateParams, SessionCreateResult](MethodSessionCreate, DaemonToAdapter),
		request[CheckpointPutParams, Empty](MethodCheckpointPut, AdapterToDaemon),
		request[PromptDeliverParams, PromptDeliverResult](MethodPromptDeliver, DaemonToAdapter),
		notification[PromptPersistedParams](MethodPromptPersisted, AdapterToDaemon),
		notification[TurnStartedParams](MethodTurnStarted, AdapterToDaemon),
		notification[TurnEndedParams](MethodTurnEnded, AdapterToDaemon),
		request[InterruptParams, InterruptResult](MethodSessionInterrupt, DaemonToAdapter),
		request[CloseParams, CloseResult](MethodSessionClose, DaemonToAdapter),
		request[ReconcileParams, ReconcileResult](MethodPromptReconcile, DaemonToAdapter),
		request[RecvObservedParams, RecvObservedResult](MethodRecvObserved, AdapterToDaemon),
		notification[NativeRequestHandledParams](MethodNativeRequestHandled, AdapterToDaemon),
		request[RegisterParams, RegisterResult](MethodEndpointRegister, AdapterToDaemon),
		request[UnregisterParams, Empty](MethodEndpointUnregister, AdapterToDaemon),
		notification[PendingParams](MethodEndpointPending, DaemonToAdapter),
		request[RecvParams, RecvResult](MethodEndpointRecv, AdapterToDaemon),
		request[AckParams, AckResult](MethodEndpointAck, AdapterToDaemon),
		request[SendParams, SendResult](MethodEndpointSend, AdapterToDaemon),
		request[MessageStatusParams, MessageStatusResult](MethodEndpointMessageStatus, AdapterToDaemon),
	}
}

// Lookup finds a method by name.
func Lookup(name string) (MethodInfo, bool) {
	for _, m := range Methods() {
		if m.Name == name {
			return m, true
		}
	}
	return MethodInfo{}, false
}
