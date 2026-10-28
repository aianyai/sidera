package engineagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// New returns the agent of the endpoint the caller names, calling svc. The engine
// resolves the caller in the command that does the work, so an endpoint closed
// between two calls is refused and not acted for.
func New(svc engine.AgentService, caller engine.Caller) fakeagent.Agent {
	return &agent{svc: svc, caller: caller}
}

type agent struct {
	svc    engine.AgentService
	caller engine.Caller
}

var _ fakeagent.Agent = (*agent)(nil)

// Declare changes the caller's declaration. A patch field that is nil changes
// nothing; AcceptFrom, when set, replaces the whole list.
func (a *agent) Declare(ctx context.Context, in fakeagent.DeclareInput) error {
	p := engine.DeclarationPatch{
		Paused: in.Paused, RefuseMultiRecipient: in.RefuseMultiRecipient,
		RestrictSenders: in.RestrictSenders, HiddenFromDirectory: in.HiddenFromDirectory,
	}
	if in.AcceptFrom != nil {
		list := make([]ident.Address, 0, len(*in.AcceptFrom))
		for _, s := range *in.AcceptFrom {
			addr, err := ident.ParseAddress(s)
			if err != nil {
				return &fakeagent.Error{Category: fakeagent.CategoryInvalidArgument, Code: apiwire.CodeMalformedRequest, Message: "accept_from: " + err.Error()}
			}
			list = append(list, addr)
		}
		p.AcceptFrom = &list
	}
	_, err := a.svc.Declare(ctx, a.caller, p)
	return MapError(err)
}

// Send sends a message as the caller's endpoint. A content type left empty is
// engine.DefaultContentType, as the front ends of the interface take it; the
// engine requires one.
func (a *agent) Send(ctx context.Context, in fakeagent.SendInput) (fakeagent.SendResult, error) {
	to, err := parseAddresses(in.To)
	if err != nil {
		return fakeagent.SendResult{}, err
	}
	ct := in.ContentType
	if ct == "" {
		ct = engine.DefaultContentType
	}
	res, err := a.svc.Send(ctx, a.caller, engine.SendRequest{
		To: to, Body: []byte(in.Body), ContentType: ct, Thread: in.Thread, DiscloseRecipients: in.DiscloseRecipients,
		AcceptBefore: time.Duration(in.AcceptBeforeS) * time.Second, IdempotencyKey: in.IdemKey,
	})
	return sendResult(res, err)
}

// Status is the state of each delivery of a message the caller's endpoint sent.
func (a *agent) Status(ctx context.Context, in fakeagent.StatusInput) (fakeagent.StatusResult, error) {
	res, err := a.svc.MessageStatus(ctx, a.caller, in.MessageID)
	if err != nil {
		return fakeagent.StatusResult{}, MapError(err)
	}
	return res, nil
}

// Reply answers a message the caller's endpoint received.
func (a *agent) Reply(ctx context.Context, in fakeagent.ReplyInput) (fakeagent.SendResult, error) {
	ct := in.ContentType
	if ct == "" {
		ct = engine.DefaultContentType
	}
	res, err := a.svc.Reply(ctx, a.caller, engine.ReplyRequest{
		Message: delivery.MessageID(in.MessageID), Body: []byte(in.Body), ContentType: ct, Thread: in.Thread, IdempotencyKey: in.IdemKey,
	})
	return sendResult(res, err)
}

// Control sends a control signal as the caller's endpoint. The body is the initial
// message of a session creation.
func (a *agent) Control(ctx context.Context, in fakeagent.ControlInput) (fakeagent.SendResult, error) {
	to, err := ident.ParseAddress(in.To)
	if err != nil {
		return fakeagent.SendResult{}, &fakeagent.Error{Category: fakeagent.CategoryInvalidArgument, Code: apiwire.CodeMalformedRequest, Message: "to: " + err.Error()}
	}
	res, err := a.svc.Control(ctx, a.caller, engine.ControlRequest{To: to, Signal: in.Signal, Preset: in.Preset, Body: []byte(in.Body), IdempotencyKey: in.IdemKey})
	return sendResult(res, err)
}

// Recv asks for a page. The page is kept as printed, which is what the model sees, and
// decoded strictly, so that a page that is not the shape of the protocol fails here.
func (a *agent) Recv(ctx context.Context, in fakeagent.RecvInput) (fakeagent.RecvResult, error) {
	raw, err := a.svc.Recv(ctx, a.caller, engine.RecvRequest{
		Frontend: in.Frontend, Wait: time.Duration(in.WaitS) * time.Second, Msg: in.Msg, Part: in.Part, AckLease: in.AckLease,
	})
	if err != nil {
		return fakeagent.RecvResult{}, MapError(err)
	}
	var page adapterproto.RecvPage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&page); err != nil {
		return fakeagent.RecvResult{}, fmt.Errorf("engineagent: the page is not JSON of the shape of the protocol: %w", err)
	}
	if err := page.Validate(); err != nil {
		return fakeagent.RecvResult{}, fmt.Errorf("engineagent: the page does not validate: %w", err)
	}
	return fakeagent.RecvResult{Raw: raw, Page: page}, nil
}

// Ack acknowledges parts of a page; no items means every part the lease handed out.
func (a *agent) Ack(ctx context.Context, in fakeagent.AckInput) (fakeagent.AckResult, error) {
	items, err := a.svc.Ack(ctx, a.caller, engine.AckRequest{Lease: in.Lease, Items: in.Items})
	if err != nil {
		return fakeagent.AckResult{}, MapError(err)
	}
	return fakeagent.AckResult{Items: items}, nil
}

func parseAddresses(list []string) ([]ident.Address, error) {
	out := make([]ident.Address, 0, len(list))
	for _, s := range list {
		addr, err := ident.ParseAddress(s)
		if err != nil {
			return nil, &fakeagent.Error{Category: fakeagent.CategoryInvalidArgument, Code: apiwire.CodeMalformedRequest, Message: "to: " + err.Error()}
		}
		out = append(out, addr)
	}
	return out, nil
}

func sendResult(res engine.SendResult, err error) (fakeagent.SendResult, error) {
	if err != nil {
		return fakeagent.SendResult{}, MapError(err)
	}
	out := fakeagent.SendResult{MessageID: adapterproto.MessageID(res.Message), Replayed: res.Replayed}
	for _, a := range res.To {
		out.To = append(out.To, a.String())
	}
	return out, nil
}

func (a *agent) Me(ctx context.Context) (fakeagent.MeResult, error) {
	v, err := a.svc.Me(ctx, a.caller)
	if err != nil {
		return fakeagent.MeResult{}, MapError(err)
	}
	d := v.Declaration
	out := fakeagent.MeResult{
		Address: v.Address.String(), Node: v.Address.Node.String(), Kind: v.Kind.String(), DisplayName: v.DisplayName,
		Declaration: fakeagent.Declaration{
			Paused: d.Paused, RefuseMultiRecipient: d.RefuseMultiRecipient, RestrictSenders: d.RestrictSenders,
			HiddenFromDirectory: d.HiddenFromDirectory, AcceptFrom: make([]string, 0, len(d.AcceptFrom)),
		},
		Frontends: make(map[adapterproto.Frontend]fakeagent.FrontendLimits, len(v.Frontends)),
	}
	for _, from := range d.AcceptFrom {
		out.Declaration.AcceptFrom = append(out.Declaration.AcceptFrom, from.String())
	}
	for frontend, limits := range v.Frontends {
		unit, err := apiwire.BudgetUnitName(limits.Budget.Unit)
		if err != nil {
			return fakeagent.MeResult{}, MapError(err)
		}
		out.Frontends[frontend] = fakeagent.FrontendLimits{
			RecvWaitMaxS: int(limits.RecvWaitMax / time.Second), Budget: fakeagent.Budget{Unit: unit, Limit: limits.Budget.Limit},
		}
	}
	return out, nil
}
func (a *agent) Peers(ctx context.Context) (fakeagent.PeersResult, error) {
	nodes, err := a.svc.Peers(ctx, a.caller)
	if err != nil {
		return fakeagent.PeersResult{}, MapError(err)
	}
	out := fakeagent.PeersResult{Nodes: make([]fakeagent.PeerNode, 0, len(nodes))}
	for _, node := range nodes {
		out.Nodes = append(out.Nodes, fakeagent.PeerNode{Node: node.Node.String(), Self: node.Self})
	}
	return out, nil
}
func (a *agent) Directory(ctx context.Context, in fakeagent.DirectoryInput) (fakeagent.DirectoryResult, error) {
	q := engine.DirectoryQuery{Cursor: in.Cursor, Limit: in.Limit}
	if in.Node != "" {
		node, err := ident.ParseNodeID(in.Node)
		if err != nil {
			return fakeagent.DirectoryResult{}, &fakeagent.Error{Category: fakeagent.CategoryInvalidArgument, Code: apiwire.CodeMalformedRequest, Message: "node: " + err.Error()}
		}
		q.Node = node
	}
	page, err := a.svc.Directory(ctx, a.caller, q)
	if err != nil {
		return fakeagent.DirectoryResult{}, MapError(err)
	}
	out := fakeagent.DirectoryResult{NextCursor: page.NextCursor, Endpoints: make([]fakeagent.DirectoryEndpoint, 0, len(page.Endpoints))}
	for _, ep := range page.Endpoints {
		entry := fakeagent.DirectoryEndpoint{
			Address: ep.Address.String(), Kind: ep.Kind.String(), DisplayName: ep.DisplayName,
			Description: ep.Description, Accepts: make([]string, 0, len(ep.Accepts)),
		}
		for _, action := range ep.Accepts {
			entry.Accepts = append(entry.Accepts, action.String())
		}
		out.Endpoints = append(out.Endpoints, entry)
	}
	return out, nil
}

// Probe is unavailable in this caller-bound backend: Caller intentionally
// exposes no credential, and a tool probe needs its separate raw probe token.
// The HTTP backend exercises that credential path instead.
func (a *agent) Probe(context.Context, fakeagent.ProbeInput) (fakeagent.ProbeResult, error) {
	return fakeagent.ProbeResult{}, NotSupported("probe")
}

// NotSupported is the error of a capability this backend does not offer. Its category
// and code are those the Agent interface gives a method the server does not know,
// which is what the ledger records; it is also fakeagent.ErrNotAvailable, which
// tells it from the answer of a real server that does not know the method.
func NotSupported(method string) error {
	return &notSupported{answer: &fakeagent.Error{
		Category: fakeagent.CategoryNotFound, Code: apiwire.CodeMethodUnknown,
		Message: "the caller-bound engine backend does not offer " + method,
	}}
}

type notSupported struct{ answer *fakeagent.Error }

func (e *notSupported) Error() string { return e.answer.Error() }

// Unwrap gives the answer, for fakeagent.CategoryOf and CodeOf, and the sentinel.
func (e *notSupported) Unwrap() []error { return []error{e.answer, fakeagent.ErrNotAvailable} }

// IsNotSupported reports whether err is what NotSupported returns.
func IsNotSupported(err error) bool { return errors.Is(err, fakeagent.ErrNotAvailable) }

// ErrProcessEnded is what a call to a process that ended before the call returned
// carries: the request was taken and no answer could be read, so the client
// cannot know whether it took effect. MapError gives it the category a client
// decides for itself.
var ErrProcessEnded = errors.New("the process the call was made to ended before the call returned")

// ErrNodeNotRunning is what a call to a node that is down carries: nothing is
// listening, as for a client that finds no socket. MapError gives it the category
// the agent interface has for a daemon that is not running.
var ErrNodeNotRunning = errors.New("the node is not running")

// MapError maps what an engine method returned to the error of the fake agent: a
// call whose process ended to outcome_unknown, a call to a node that is down to
// unavailable, and everything else as the interface does (apiwire.FromError), so
// that the words of an error have one home. It is nil for nil.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNodeNotRunning) {
		return &fakeagent.Error{Category: fakeagent.CategoryUnavailable, Code: "daemon_not_running", Message: err.Error()}
	}
	if errors.Is(err, ErrProcessEnded) {
		return &fakeagent.Error{Category: fakeagent.CategoryOutcomeUnknown, Code: "connection_lost", Message: err.Error()}
	}
	we := apiwire.FromError(err)
	return &fakeagent.Error{Category: fakeagent.Category(we.Category), Code: we.Code, Message: we.Message}
}
