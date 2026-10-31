package adapterkit

import (
	"context"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// Application is what an adapter implements; the kit calls it for the requests of
// the daemon that are not the kit's own (initialize, ping, shutdown and
// the gating around them). Every call has been admitted by the kit: the connection
// is initialized, shutdown has not begun, the parameters decoded strictly and
// passed their Validate, and the capability the method needs was declared. The
// result is validated before it is sent, so an adapter never writes a message the
// protocol would refuse; a result that fails is answered as an internal error and
// logged.
//
// An error is answered as it is when it is a *peer.Error (use peer.NewError with a
// code of the protocol), and as an internal error otherwise. Calls run
// concurrently, up to the limit of the peer, and each receives a context that is
// cancelled by $/cancel_request or when the connection ends.
//
// Embed Base to implement only the methods the adapter has.
type Application interface {
	// Initialize is called once, after the version is negotiated and the instance
	// lock is held, before the answer to initialize is sent. The adapter may keep
	// conn to send its own requests and notifications. An error is the answer to
	// initialize and the lock is released.
	Initialize(ctx context.Context, conn *Conn, instance adapterproto.InstanceInfo) error
	// Shutdown is called when the daemon asks the adapter to end, after the kit has
	// begun to refuse every request but ping and before it answers shutdown. The
	// adapter stops its work and returns; the process ends when the stream closes.
	Shutdown(ctx context.Context)

	// Start takes the adapter on: it attaches the sessions and the direct endpoints
	// the daemon lists.
	Start(ctx context.Context, p adapterproto.StartParams) (adapterproto.StartResult, error)
	// Probe judges what the adapter depends on.
	Probe(ctx context.Context, p adapterproto.ProbeParams) (adapterproto.ProbeResult, error)

	// The five methods of a runtime adapter. The kit answers them with
	// capability_not_declared, without calling these, when the adapter declared no
	// runtime.
	CreateSession(ctx context.Context, p adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error)
	DeliverPrompt(ctx context.Context, p adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error)
	Reconcile(ctx context.Context, p adapterproto.ReconcileParams) (adapterproto.ReconcileResult, error)
	Interrupt(ctx context.Context, p adapterproto.InterruptParams) (adapterproto.InterruptResult, error)
	CloseSession(ctx context.Context, p adapterproto.CloseParams) (adapterproto.CloseResult, error)

	// EndpointPending is the daemon's best-effort notice that messages wait for a
	// direct endpoint; the adapter must not depend on receiving it.
	EndpointPending(ctx context.Context, p adapterproto.PendingParams)
}

// Base implements Application with the answers of an adapter that does nothing:
// no state at initialize and shutdown, an empty start and probe, and
// capability_not_declared for the methods of a runtime. An adapter embeds it and defines the methods it
// has.
type Base struct{}

var _ Application = Base{}

// Initialize accepts the instance.
func (Base) Initialize(context.Context, *Conn, adapterproto.InstanceInfo) error { return nil }

// Shutdown does nothing.
func (Base) Shutdown(context.Context) {}

// Start answers that the adapter holds none of the sessions it is asked to take
// back, which is true of an adapter that has none.
func (Base) Start(_ context.Context, p adapterproto.StartParams) (adapterproto.StartResult, error) {
	res := adapterproto.StartResult{Sessions: make([]adapterproto.SessionState, 0, len(p.Sessions))}
	for _, s := range p.Sessions {
		res.Sessions = append(res.Sessions, adapterproto.SessionState{Endpoint: s.Endpoint, State: adapterproto.AttachLost})
	}
	return res, nil
}

// Probe reports what an adapter with nothing to check reports: unknown
// credentials, no dependencies, no presets and no tools.
func (Base) Probe(context.Context, adapterproto.ProbeParams) (adapterproto.ProbeResult, error) {
	return adapterproto.ProbeResult{
		Auth: adapterproto.AuthUnknown, Dependencies: []adapterproto.Dependency{},
		Presets: []adapterproto.PresetStatus{}, ToolProbes: []adapterproto.ToolProbeResult{},
	}, nil
}

// CreateSession is not declared.
func (Base) CreateSession(context.Context, adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error) {
	return adapterproto.SessionCreateResult{}, notDeclared("session.create")
}

// DeliverPrompt is not declared.
func (Base) DeliverPrompt(context.Context, adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error) {
	return adapterproto.PromptDeliverResult{}, notDeclared("prompt.deliver")
}

// Reconcile is not declared.
func (Base) Reconcile(context.Context, adapterproto.ReconcileParams) (adapterproto.ReconcileResult, error) {
	return adapterproto.ReconcileResult{}, notDeclared("prompt.reconcile")
}

// Interrupt is not declared.
func (Base) Interrupt(context.Context, adapterproto.InterruptParams) (adapterproto.InterruptResult, error) {
	return adapterproto.InterruptResult{}, notDeclared("session.interrupt")
}

// CloseSession is not declared.
func (Base) CloseSession(context.Context, adapterproto.CloseParams) (adapterproto.CloseResult, error) {
	return adapterproto.CloseResult{}, notDeclared("session.close")
}

// EndpointPending ignores the notice.
func (Base) EndpointPending(context.Context, adapterproto.PendingParams) {}

func notDeclared(method string) error {
	return peer.NewError(adapterproto.CodeCapabilityNotDeclared, method+" needs a capability this adapter did not declare")
}
