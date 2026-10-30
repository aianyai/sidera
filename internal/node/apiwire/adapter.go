package apiwire

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

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

func (a adapterEngine) RecvObserved(ctx context.Context, instance string, p adapterproto.RecvObservedParams) (adapterproto.RecvObservedResult, error) {
	res, err := a.e.RecvObserved(ctx, instance, p)
	return res, adapterError(err)
}

// EndpointSend maps only the direct intake's errors; older adapter methods keep
// their existing error contract.
func (a adapterEngine) EndpointSend(ctx context.Context, instance string, p adapterproto.SendParams) (adapterproto.SendResult, error) {
	res, err := a.e.SendDirect(ctx, instance, p)
	return adapterproto.SendResult{MessageID: adapterproto.MessageID(res.Message)}, directSendError(err)
}

func (a adapterEngine) EndpointRecv(ctx context.Context, instance string, p adapterproto.RecvParams) (adapterproto.RecvResult, error) {
	r := engine.RecvRequest{Msg: p.MessageID, Part: p.Part}
	if p.WaitS != nil {
		r.Wait = time.Duration(*p.WaitS) * time.Second
	}
	raw, err := a.e.Recv(ctx, engine.ByAdapter(instance, p.Endpoint), r)
	if err != nil {
		return adapterproto.RecvResult{}, adapterError(err)
	}
	var page adapterproto.RecvPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return adapterproto.RecvResult{}, err
	}
	return adapterproto.RecvResult{Page: page}, nil
}

func (a adapterEngine) EndpointAck(ctx context.Context, instance string, p adapterproto.AckParams) (adapterproto.AckResult, error) {
	items, err := a.e.Ack(ctx, engine.ByAdapter(instance, p.Endpoint), engine.AckRequest{Lease: p.Lease, Items: p.Items})
	return adapterproto.AckResult{Items: items}, adapterError(err)
}

func (a adapterEngine) EndpointMessageStatus(ctx context.Context, instance string, p adapterproto.MessageStatusParams) (adapterproto.MessageStatusResult, error) {
	res, err := a.e.MessageStatus(ctx, engine.ByAdapter(instance, p.Endpoint), p.MessageID)
	var ref *engine.Refusal
	if errors.As(err, &ref) && ref.Reason == engine.ReasonMessageUnknown {
		return res, peer.NewError(peer.CodeInvalidParams, "the message_id is not available")
	}
	return res, adapterError(err)
}

func directSendError(err error) error {
	var ref *engine.Refusal
	if !errors.As(err, &ref) {
		return err
	}
	switch ref.Reason {
	case engine.ReasonInvalidArgument:
		if strings.HasPrefix(ref.Field, "to[") {
			return peer.NewError(adapterproto.CodeInvalidAddress, "an address is not valid").WithData(map[string]any{"field": ref.Field})
		}
		fallthrough
	case engine.ReasonTooLarge, engine.ReasonIdempotencyConflict, engine.ReasonMessageUnknown:
		return peer.NewError(adapterproto.CodeSendRefused, "the message was not accepted").WithData(map[string]any{"reason": ref.Reason.String()})
	default:
		return adapterError(err)
	}
}

// adapterCode is what an adapter is told for one reason of the engine.
type adapterCode struct {
	code    int
	message string
	// data adds the data object of the error, for the refusals whose code does not
	// say enough: the limit that was hit, the reason of a back pressure.
	data func(*engine.Refusal) map[string]any
}

// The refusals that mean "not now, nothing was recorded" (back pressure, the
// faults of the disk a retry may cure, a node that is still starting) are
// node_busy: the adapter may send the request again later, and the reason, which
// is the engine's own name for it, is in the data. The protocol lists the reasons
// this version sends, so a reason of the engine that is added here must be added
// there too; a test holds the two lists together.
var backPressure = adapterCode{
	code:    adapterproto.CodeNodeBusy,
	message: "the node could not take the request now",
	data:    func(r *engine.Refusal) map[string]any { return map[string]any{"reason": r.Reason.String()} },
}

var internalCode = adapterCode{code: peer.CodeInternalError, message: "internal error"}

// adapterCodes has one row for each reason of the engine; a test fails when a
// reason has none.
var adapterCodes = map[engine.Reason]adapterCode{
	engine.ReasonEndpointUnknown:   {code: adapterproto.CodeUnknownEndpoint, message: "the endpoint does not exist or is not one of this instance"},
	engine.ReasonAttemptUnknown:    {code: adapterproto.CodeUnknownAttempt, message: "the attempt is not in flight for this instance"},
	engine.ReasonWrongEndpointKind: {code: adapterproto.CodeWrongEndpointKind, message: "the endpoint's kind cannot be used with this method"},
	engine.ReasonEndpointLimit: {code: adapterproto.CodeEndpointLimit, message: "no more endpoints can be opened", data: func(r *engine.Refusal) map[string]any {
		if r.Limit == 0 {
			return nil
		}
		return map[string]any{"limit": r.Limit.String()}
	}},
	engine.ReasonLeaseUnknown:    {code: adapterproto.CodeLeaseUnknown, message: "the endpoint was handed no page under this lease"},
	engine.ReasonInvalidArgument: {code: peer.CodeInvalidParams, message: "a value of the request is not acceptable"},
	engine.ReasonShuttingDown:    {code: peer.CodeShuttingDown, message: "the node is stopping"},

	// The adapter is told nothing it could act on: an instance the engine does not
	// know is the daemon's to sort out, and a caller that is not an endpoint or an
	// operation identifier used twice cannot be a request of an adapter at all.
	engine.ReasonInstanceUnknown:   internalCode,
	engine.ReasonUnauthenticated:   internalCode,
	engine.ReasonOperationIDReused: internalCode,
	// Whether the request took effect is not known, which is what -32603 says.
	engine.ReasonStorageFatal: internalCode,
	// These reasons keep the historical mapping of the older adapter methods.
	// Direct send and status have their own intake-specific mapping above.
	engine.ReasonIdempotencyConflict: internalCode,
	engine.ReasonMessageConflict:     internalCode,
	engine.ReasonMessageUnknown:      internalCode,
	engine.ReasonTooLarge:            internalCode,
	engine.ReasonAlertUnknown:        internalCode,
	engine.ReasonIdentityRetired:     internalCode,
	// The installation of a package and the presenting of a probe token are the
	// owner's and the agent's requests; an adapter makes neither.
	engine.ReasonInstallInProgress:   internalCode,
	engine.ReasonInstallRepeated:     internalCode,
	engine.ReasonPlatformUnsupported: internalCode,
	engine.ReasonProbeTokenUsed:      internalCode,
	engine.ReasonProbeTokenExpired:   internalCode,

	engine.ReasonNodeQueueFull:      backPressure,
	engine.ReasonStorageFull:        backPressure,
	engine.ReasonStorageUnavailable: backPressure,
	engine.ReasonClockRollback:      backPressure,
	engine.ReasonLoopBusy:           backPressure,
	engine.ReasonRecovering:         backPressure,
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
