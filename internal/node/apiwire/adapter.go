package apiwire

import (
	"context"
	"errors"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// AdapterEngine puts the engine behind the interface the adapter host asks of it,
// with what the engine refuses a request of the adapter for turned into the error
// object the adapter is answered with. The host sits below the engine and hands a
// *peer.Error to the adapter as it is, and anything else as an internal error, so
// the words of the adapter protocol are chosen here, as the words of the other
// interfaces are chosen in FromError.
//
// Only the methods that answer a request of the adapter map their errors. The
// others are the host's own calls: an error of one of them ends the connection and
// is never sent to the adapter. Nothing else of the engine is promoted onto the
// returned value: the host answers endpoint.send and its kin through optional
// interfaces it finds by asking the value, and a method the engine gained that
// slipped through would answer with the engine's words.
func AdapterEngine(e *engine.Engine) adapterhost.Engine { return adapterEngine{e} }

type adapterEngine struct{ e *engine.Engine }

func (a adapterEngine) AdapterAdmitted(ctx context.Context, instance string, adm adapterreg.Admission, info adapterproto.InitializeResult) (adapterproto.StartParams, error) {
	return a.e.AdapterAdmitted(ctx, instance, adm, info)
}

func (a adapterEngine) AdapterStarted(ctx context.Context, instance string, r adapterproto.StartResult) error {
	return a.e.AdapterStarted(ctx, instance, r)
}

func (a adapterEngine) ProbeTokens(ctx context.Context, instance string) ([]adapterproto.ToolProbeRequest, error) {
	return a.e.ProbeTokens(ctx, instance)
}

func (a adapterEngine) AdapterProbed(ctx context.Context, instance string, r adapterproto.ProbeResult) error {
	return a.e.AdapterProbed(ctx, instance, r)
}

func (a adapterEngine) AdapterGone(ctx context.Context, instance string, cause adapterhost.GoneCause) error {
	return a.e.AdapterGone(ctx, instance, cause)
}

func (a adapterEngine) AdapterNotified(ctx context.Context, instance string, n adapterhost.Notification) error {
	return a.e.AdapterNotified(ctx, instance, n)
}

func (a adapterEngine) RegisterDirect(ctx context.Context, instance string, p adapterproto.RegisterParams) (adapterproto.RegisterResult, error) {
	res, err := a.e.RegisterDirect(ctx, instance, p)
	return res, adapterError(err)
}

func (a adapterEngine) UnregisterDirect(ctx context.Context, instance string, p adapterproto.UnregisterParams) error {
	return adapterError(a.e.UnregisterDirect(ctx, instance, p))
}

func (a adapterEngine) CheckpointPut(ctx context.Context, instance string, p adapterproto.CheckpointPutParams) error {
	return adapterError(a.e.CheckpointPut(ctx, instance, p))
}

// adapterCode is what an adapter is told for one reason of the engine.
type adapterCode struct {
	code    int
	message string
	// data adds the data object of the error, for the refusals whose code does not
	// say enough: the limit that was hit, the reason of a back pressure.
	data func(*engine.Refusal) map[string]any
}

// Back pressure and the faults of the disk that mean "not now, nothing was
// recorded" have no code in the adapter protocol: 1007 is the limit of direct
// endpoints and -32001 is the peer's own refusal before a handler ran. Until the
// protocol has one they are an internal error that names the reason, so that an
// adapter can log what happened and treat it as it treats any failed request.
var backPressure = adapterCode{
	code:    peer.CodeInternalError,
	message: "the node could not take the request now",
	data:    func(r *engine.Refusal) map[string]any { return map[string]any{"reason": r.Reason.String()} },
}

var internalCode = adapterCode{code: peer.CodeInternalError, message: "internal error"}

// adapterCodes has one row for each reason of the engine; a test fails when a
// reason has none.
var adapterCodes = map[engine.Reason]adapterCode{
	engine.ReasonEndpointUnknown:   {code: adapterproto.CodeUnknownEndpoint, message: "the endpoint does not exist or is not one of this instance"},
	engine.ReasonWrongEndpointKind: {code: adapterproto.CodeWrongEndpointKind, message: "the endpoint's kind cannot be used with this method"},
	engine.ReasonEndpointLimit: {code: adapterproto.CodeEndpointLimit, message: "no more endpoints can be opened", data: func(r *engine.Refusal) map[string]any {
		if r.Limit == 0 {
			return nil
		}
		return map[string]any{"limit": r.Limit.String()}
	}},
	engine.ReasonInvalidArgument: {code: peer.CodeInvalidParams, message: "a value of the request is not acceptable"},
	engine.ReasonShuttingDown:    {code: peer.CodeShuttingDown, message: "the node is stopping"},

	// The adapter is told nothing it could act on: an instance the engine does not
	// know and a node that is starting are the daemon's to sort out, and a caller
	// that is not an endpoint or an operation identifier used twice cannot be a
	// request of an adapter at all.
	engine.ReasonInstanceUnknown:   internalCode,
	engine.ReasonUnauthenticated:   internalCode,
	engine.ReasonOperationIDReused: internalCode,
	engine.ReasonRecovering:        internalCode,
	// Whether the request took effect is not known, which is what -32603 says.
	engine.ReasonStorageFatal: internalCode,
	// The refusals of the messages, of an alert and of a retired identity answer
	// requests an adapter does not make through this door: the adapter protocol has
	// no code for them, and what the adapter would do with one is the protocol's to
	// say when it carries such a request.
	engine.ReasonIdempotencyConflict: internalCode,
	engine.ReasonMessageConflict:     internalCode,
	engine.ReasonMessageUnknown:      internalCode,
	engine.ReasonTooLarge:            internalCode,
	engine.ReasonAlertUnknown:        internalCode,
	engine.ReasonIdentityRetired:     internalCode,

	engine.ReasonNodeQueueFull:      backPressure,
	engine.ReasonStorageFull:        backPressure,
	engine.ReasonStorageUnavailable: backPressure,
	engine.ReasonClockRollback:      backPressure,
	engine.ReasonLoopBusy:           backPressure,
}

// adapterError turns a refusal of the engine into the error object an adapter is
// answered with, and returns every other error as it is: the host answers those
// as internal errors and keeps their text off the wire. The cause a refusal
// carries (a path, an identifier) is never put in the error.
func adapterError(err error) error {
	var ref *engine.Refusal
	if !errors.As(err, &ref) {
		return err
	}
	row, ok := adapterCodes[ref.Reason]
	if !ok {
		row = internalCode
	}
	message := row.message
	if ref.Reason == engine.ReasonInvalidArgument && ref.Field != "" {
		message += ": " + ref.Field
	}
	pe := peer.NewError(row.code, message)
	if row.data != nil {
		if data := row.data(ref); data != nil {
			pe = pe.WithData(data)
		}
	}
	return pe
}
