package engineagent

import (
	"context"
	"errors"
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

// Declare is the one method the engine's service interface offers. A patch field
// that is nil changes nothing; AcceptFrom, when set, replaces the whole list.
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

// The methods below are not offered by the engine yet. Each answers as the agent
// interface answers a method the server does not know.

func (a *agent) Me(context.Context) (fakeagent.MeResult, error) {
	return fakeagent.MeResult{}, NotSupported("me")
}
func (a *agent) Peers(context.Context) (fakeagent.PeersResult, error) {
	return fakeagent.PeersResult{}, NotSupported("peers")
}
func (a *agent) Directory(context.Context, fakeagent.DirectoryInput) (fakeagent.DirectoryResult, error) {
	return fakeagent.DirectoryResult{}, NotSupported("directory")
}
func (a *agent) Probe(context.Context, fakeagent.ProbeInput) (fakeagent.ProbeResult, error) {
	return fakeagent.ProbeResult{}, NotSupported("probe")
}
func (a *agent) Recv(context.Context, fakeagent.RecvInput) (fakeagent.RecvResult, error) {
	return fakeagent.RecvResult{}, NotSupported("recv")
}
func (a *agent) Ack(context.Context, fakeagent.AckInput) (fakeagent.AckResult, error) {
	return fakeagent.AckResult{}, NotSupported("ack")
}
func (a *agent) Control(context.Context, fakeagent.ControlInput) (fakeagent.SendResult, error) {
	return fakeagent.SendResult{}, NotSupported("control")
}

// NotSupported is the error of a method the engine does not offer yet. Its category
// and code are those the agent interface gives a method the server does not know,
// which is what the ledger records; it is also fakeagent.ErrNotAvailable, which
// tells it from the answer of a real server that does not know the method.
func NotSupported(method string) error {
	return &notSupported{answer: &fakeagent.Error{
		Category: fakeagent.CategoryNotFound, Code: apiwire.CodeMethodUnknown,
		Message: "the engine does not offer " + method + " yet",
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
