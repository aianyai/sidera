package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// directAgent is the chat bridge side of the actual adapter protocol. The only
// cached receive facts are routing and lease parts; none prove delivery or ack.
type directAgent struct {
	env      *environment
	endpoint directEndpoint
	routes   map[adapterproto.MessageID]adapterproto.Address
	leases   map[adapterproto.LeaseRef][]adapterproto.PartRef
}

var _ fakeagent.Agent = (*directAgent)(nil)

func (a *directAgent) controller(ctx context.Context) (fakeadapter.Controller, error) {
	if err := a.env.check(); err != nil {
		return nil, err
	}
	// Resolve on every call: a controller holds one generation's connection.
	return a.env.fixture.Adapter(ctx, a.endpoint.instance)
}
func (*directAgent) Me(context.Context) (fakeagent.MeResult, error) {
	return fakeagent.MeResult{}, scenario.NotAvailable("direct endpoint Me")
}
func (*directAgent) Peers(context.Context) (fakeagent.PeersResult, error) {
	return fakeagent.PeersResult{}, scenario.NotAvailable("direct endpoint Peers")
}
func (*directAgent) Directory(context.Context, fakeagent.DirectoryInput) (fakeagent.DirectoryResult, error) {
	return fakeagent.DirectoryResult{}, scenario.NotAvailable("direct endpoint Directory")
}
func (*directAgent) Probe(context.Context, fakeagent.ProbeInput) (fakeagent.ProbeResult, error) {
	return fakeagent.ProbeResult{}, scenario.NotAvailable("direct endpoint Probe")
}
func (*directAgent) Declare(context.Context, fakeagent.DeclareInput) error {
	return scenario.NotAvailable("direct endpoint Declare")
}

func (a *directAgent) Send(ctx context.Context, in fakeagent.SendInput) (fakeagent.SendResult, error) {
	to := make([]adapterproto.Address, len(in.To))
	for i, v := range in.To {
		to[i] = adapterproto.Address(v)
	}
	return a.send(ctx, adapterproto.SendParams{Endpoint: a.endpoint.ref, To: to, Body: in.Body, ContentType: contentType(in.ContentType, in.Body), Thread: in.Thread, DiscloseRecipients: in.DiscloseRecipients, AcceptBeforeS: in.AcceptBeforeS, IdempotencyKey: in.IdemKey})
}
func contentType(value, body string) string {
	if value == "" && body != "" {
		return "text/plain"
	}
	return value
}
func (a *directAgent) send(ctx context.Context, p adapterproto.SendParams) (fakeagent.SendResult, error) {
	if err := a.env.check(); err != nil {
		return fakeagent.SendResult{}, err
	}
	if p.IdempotencyKey != "" {
		return fakeagent.SendResult{}, scenario.NotAvailable("direct protocol replay result")
	}
	ctl, err := a.controller(ctx)
	if err != nil {
		return fakeagent.SendResult{}, err
	}
	sent, err := ctl.Send(ctx, p)
	if err != nil {
		return fakeagent.SendResult{}, directError(err)
	}
	// endpoint.send reports only the id. The actual status supplies resolved
	// recipients; the protocol exposes no replay bit, so none is inferred locally.
	status, err := ctl.MessageStatus(ctx, adapterproto.MessageStatusParams{Endpoint: a.endpoint.ref, MessageID: sent.MessageID})
	if err != nil {
		return fakeagent.SendResult{}, directError(err)
	}
	res := fakeagent.SendResult{MessageID: sent.MessageID}
	for _, r := range status.Recipients {
		res.To = append(res.To, string(r.To))
	}
	return res, nil
}
func (a *directAgent) Control(ctx context.Context, in fakeagent.ControlInput) (fakeagent.SendResult, error) {
	control := &adapterproto.Control{Signal: in.Signal}
	if in.Signal == adapterproto.SignalSessionCreate {
		control.Params = &adapterproto.ControlParams{Preset: in.Preset}
	} else if in.Preset != "" {
		return fakeagent.SendResult{}, fmt.Errorf("acceptance: only session.create takes a preset")
	}
	return a.send(ctx, adapterproto.SendParams{Endpoint: a.endpoint.ref, To: []adapterproto.Address{adapterproto.Address(in.To)}, Body: in.Body, ContentType: contentType("", in.Body), Control: control, IdempotencyKey: in.IdemKey})
}
func (a *directAgent) Reply(ctx context.Context, in fakeagent.ReplyInput) (fakeagent.SendResult, error) {
	if err := a.env.check(); err != nil {
		return fakeagent.SendResult{}, err
	}
	from, ok := a.routes[in.MessageID]
	if !ok {
		return fakeagent.SendResult{}, &fakeagent.Error{Category: fakeagent.CategoryNotFound, Code: "message_unknown", Message: "this direct endpoint has not received that message"}
	}
	return a.send(ctx, adapterproto.SendParams{Endpoint: a.endpoint.ref, To: []adapterproto.Address{from}, ReplyTo: in.MessageID, Body: in.Body, ContentType: contentType(in.ContentType, in.Body), Thread: in.Thread, IdempotencyKey: in.IdemKey})
}
func (a *directAgent) Recv(ctx context.Context, in fakeagent.RecvInput) (fakeagent.RecvResult, error) {
	if err := a.env.check(); err != nil {
		return fakeagent.RecvResult{}, err
	}
	// Direct endpoints always use their direct budget, as the daemon also does
	// for a direct caller of the engine. CLI names the shared script's reader;
	// it does not select a runtime budget for a chat bridge.
	if in.Frontend != "" && in.Frontend != adapterproto.FrontendCLI {
		return fakeagent.RecvResult{}, scenario.NotAvailable("direct endpoint runtime frontend")
	}
	if in.AckLease != "" {
		if _, err := a.Ack(ctx, fakeagent.AckInput{Lease: in.AckLease}); err != nil {
			return fakeagent.RecvResult{}, err
		}
	}
	ctl, err := a.controller(ctx)
	if err != nil {
		return fakeagent.RecvResult{}, err
	}
	res, err := ctl.Recv(ctx, adapterproto.RecvParams{Endpoint: a.endpoint.ref, WaitS: &in.WaitS, MessageID: in.Msg, Part: in.Part})
	if err != nil {
		return fakeagent.RecvResult{}, directError(err)
	}
	parts := make([]adapterproto.PartRef, 0, len(res.Page.Messages))
	for _, m := range res.Page.Messages {
		a.routes[m.ID] = m.From
		part := 1
		if m.Part != nil {
			part = m.Part.Index
		}
		parts = append(parts, adapterproto.PartRef{MessageID: m.ID, Part: part})
	}
	a.leases[res.Page.Lease] = parts
	// The bridge renders its typed protocol page for its own reader. These bytes
	// are that display, not a claim to preserve the daemon's JSON representation.
	raw, err := json.Marshal(res.Page)
	return fakeagent.RecvResult{Raw: raw, Page: res.Page}, err
}

func (a *directAgent) Ack(ctx context.Context, in fakeagent.AckInput) (fakeagent.AckResult, error) {
	if err := a.env.check(); err != nil {
		return fakeagent.AckResult{}, err
	}
	items := in.Items
	if len(items) == 0 {
		var ok bool
		items, ok = a.leases[in.Lease]
		if !ok {
			return fakeagent.AckResult{}, &fakeagent.Error{Category: fakeagent.CategoryNotFound, Code: "lease_unknown", Message: "this direct endpoint has not received that lease"}
		}
	}
	ctl, err := a.controller(ctx)
	if err != nil {
		return fakeagent.AckResult{}, err
	}
	res, err := ctl.Ack(ctx, adapterproto.AckParams{Endpoint: a.endpoint.ref, Lease: in.Lease, Items: items})
	return fakeagent.AckResult{Items: res.Items}, directError(err)
}
func (a *directAgent) Status(ctx context.Context, in fakeagent.StatusInput) (fakeagent.StatusResult, error) {
	ctl, err := a.controller(ctx)
	if err != nil {
		return fakeagent.StatusResult{}, err
	}
	res, err := ctl.MessageStatus(ctx, adapterproto.MessageStatusParams{Endpoint: a.endpoint.ref, MessageID: in.MessageID})
	return res, directError(err)
}

// directError translates the protocol's public error vocabulary. Transport
// failures stay errors rather than becoming fabricated daemon replies.
func directError(err error) error {
	if err == nil {
		return nil
	}
	var p *peer.Error
	if !errors.As(err, &p) {
		return err
	}
	category, code := fakeagent.CategoryInternal, "internal"
	switch p.Code {
	case adapterproto.CodeUnknownEndpoint:
		category, code = fakeagent.CategoryNotFound, "endpoint_unknown"
	case adapterproto.CodeLeaseUnknown:
		category, code = fakeagent.CategoryNotFound, "lease_unknown"
	case adapterproto.CodeInvalidAddress:
		category, code = fakeagent.CategoryInvalidArgument, "invalid_address"
	case adapterproto.CodeWrongEndpointKind:
		category, code = fakeagent.CategoryInvalidArgument, "wrong_endpoint_kind"
	case peer.CodeInvalidParams:
		category, code = fakeagent.CategoryInvalidArgument, "malformed_request"
	case adapterproto.CodeEndpointLimit:
		category, code = fakeagent.CategoryRefused, "endpoint_limit"
	case adapterproto.CodeSendRefused:
		var data adapterproto.SendRefusedData
		if e := json.Unmarshal(p.Data, &data); e != nil {
			return e
		}
		category, code = fakeagent.CategoryRefused, data.Reason
	case adapterproto.CodeNodeBusy:
		var data adapterproto.BusyData
		if e := json.Unmarshal(p.Data, &data); e != nil {
			return e
		}
		category, code = fakeagent.CategoryBackpressure, string(data.Reason)
	case peer.CodeShuttingDown:
		category, code = fakeagent.CategoryUnavailable, "shutting_down"
	default:
		if peer.ResultUnknown(err) {
			category, code = fakeagent.CategoryOutcomeUnknown, "outcome_unknown"
		}
	}
	return &fakeagent.Error{Category: category, Code: code, Message: p.Message}
}
