package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

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
func (a *directAgent) Peers(ctx context.Context) (fakeagent.PeersResult, error) {
	ctl, err := a.controller(ctx)
	if err != nil {
		return fakeagent.PeersResult{}, err
	}
	res, err := ctl.Peers(ctx, adapterproto.PeersParams{Endpoint: a.endpoint.ref})
	if err != nil {
		return fakeagent.PeersResult{}, directError(err)
	}
	out := fakeagent.PeersResult{Nodes: make([]fakeagent.PeerNode, len(res.Nodes))}
	for i, n := range res.Nodes {
		out.Nodes[i] = fakeagent.PeerNode{Node: n.Node, Self: n.Self, Trust: string(n.Trust), Connected: n.Connected}
	}
	return out, nil
}

// Directory reads one page. A read that got no page is a result of the protocol
// and an error of the Agent interface, so it is returned as the error that
// interface answers with: the category of the outcome and the reason as its code.
func (a *directAgent) Directory(ctx context.Context, in fakeagent.DirectoryInput) (fakeagent.DirectoryResult, error) {
	ctl, err := a.controller(ctx)
	if err != nil {
		return fakeagent.DirectoryResult{}, err
	}
	res, err := ctl.Directory(ctx, adapterproto.DirectoryParams{Endpoint: a.endpoint.ref, Node: in.Node, Cursor: in.Cursor, Limit: in.Limit})
	if err != nil {
		return fakeagent.DirectoryResult{}, directError(err)
	}
	switch res.Outcome {
	case adapterproto.DirectoryUnavailable:
		return fakeagent.DirectoryResult{}, &fakeagent.Error{Category: fakeagent.CategoryUnavailable, Code: string(res.Reason), Message: "the directory could not be reached in time"}
	case adapterproto.DirectoryRefused:
		return fakeagent.DirectoryResult{}, &fakeagent.Error{Category: fakeagent.CategoryRefused, Code: string(res.Reason), Message: "the node asked refused the read"}
	}
	out := fakeagent.DirectoryResult{Endpoints: make([]fakeagent.DirectoryEndpoint, len(res.Endpoints)), NextCursor: res.NextCursor}
	for i, e := range res.Endpoints {
		accepts := make([]string, len(e.Accepts))
		for j, act := range e.Accepts {
			accepts[j] = string(act)
		}
		out.Endpoints[i] = fakeagent.DirectoryEndpoint{Address: string(e.Address), Kind: string(e.Kind), DisplayName: e.DisplayName, Description: e.Description, Accepts: accepts}
	}
	return out, nil
}
func (*directAgent) Probe(context.Context, fakeagent.ProbeInput) (fakeagent.ProbeResult, error) {
	return fakeagent.ProbeResult{}, scenario.NotAvailable("direct endpoint Probe")
}
func (a *directAgent) Declare(ctx context.Context, in fakeagent.DeclareInput) error {
	ctl, err := a.controller(ctx)
	if err != nil {
		return err
	}
	p := adapterproto.DeclareParams{
		Endpoint: a.endpoint.ref, Paused: in.Paused, RefuseMultiRecipient: in.RefuseMultiRecipient,
		RestrictSenders: in.RestrictSenders, HiddenFromDirectory: in.HiddenFromDirectory,
	}
	if in.AcceptFrom != nil {
		list := make([]adapterproto.Address, len(*in.AcceptFrom))
		for i, s := range *in.AcceptFrom {
			list[i] = adapterproto.Address(s)
		}
		p.AcceptFrom = &list
	}
	_, err = ctl.Declare(ctx, p)
	return directError(err)
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
	ctl, err := a.controller(ctx)
	if err != nil {
		return fakeagent.SendResult{}, err
	}
	return sendDirect(ctx, ctl, p)
}

func sendDirect(ctx context.Context, ctl fakeadapter.Controller, p adapterproto.SendParams) (fakeagent.SendResult, error) {
	sent, err := ctl.Send(ctx, p)
	if err != nil {
		return fakeagent.SendResult{}, directError(err)
	}
	res := fakeagent.SendResult{MessageID: sent.MessageID, Replay: fakeagent.ReplayUnknown}
	if p.IdempotencyKey == "" {
		res.Replay = fakeagent.ReplayFresh
	}
	// A successful endpoint.send confirms these canonical recipients in order;
	// the daemon rejects aliases and never rewrites the accepted addresses.
	for _, to := range p.To {
		res.To = append(res.To, string(to))
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
		// The reason of the data is the code the Agent interface answers with.
		category, code = fakeagent.CategoryInvalidArgument, "malformed_request"
		var data struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal(p.Data, &data) == nil && slices.Contains(adapterproto.ParamsReasons(), data.Reason) {
			code = data.Reason
		}
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
