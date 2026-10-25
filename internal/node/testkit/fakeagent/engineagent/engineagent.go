package engineagent

import (
	"context"
	"errors"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
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
				return &fakeagent.Error{Category: fakeagent.CategoryInvalidArgument, Code: codeMalformed, Message: "accept_from: " + err.Error()}
			}
			list = append(list, addr)
		}
		p.AcceptFrom = &list
	}
	_, err := a.svc.Declare(ctx, a.caller, p)
	return MapError(err)
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
func (a *agent) Send(context.Context, fakeagent.SendInput) (fakeagent.SendResult, error) {
	return fakeagent.SendResult{}, NotSupported("send")
}
func (a *agent) Reply(context.Context, fakeagent.ReplyInput) (fakeagent.SendResult, error) {
	return fakeagent.SendResult{}, NotSupported("reply")
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
func (a *agent) Status(context.Context, fakeagent.StatusInput) (fakeagent.StatusResult, error) {
	return fakeagent.StatusResult{}, NotSupported("status")
}

const (
	codeMethodUnknown = "method_unknown"
	codeMalformed     = "malformed_request"
)

// NotSupported is the error of a method the engine does not offer yet. Its category
// and code are those the agent interface gives a method the server does not know,
// which is what the ledger records; it is also fakeagent.ErrNotAvailable, which
// tells it from the answer of a real server that does not know the method.
func NotSupported(method string) error {
	return &notSupported{answer: &fakeagent.Error{
		Category: fakeagent.CategoryNotFound, Code: codeMethodUnknown,
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

// MapError maps what an engine method returned to the error of the fake agent:
// a typed refusal to its category and code, a call whose process ended to
// outcome_unknown, anything else to an internal error. It is nil for nil.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrProcessEnded) {
		return &fakeagent.Error{Category: fakeagent.CategoryOutcomeUnknown, Code: "connection_lost", Message: err.Error()}
	}
	var ref *engine.Refusal
	if !errors.As(err, &ref) {
		return &fakeagent.Error{Category: fakeagent.CategoryInternal, Code: "internal", Message: err.Error()}
	}
	category, code := classify(ref)
	return &fakeagent.Error{Category: category, Code: code, Message: ref.Error()}
}

func classify(r *engine.Refusal) (fakeagent.Category, string) {
	switch r.Reason {
	case engine.ReasonUnauthenticated:
		return fakeagent.CategoryUnauthenticated, "token_unknown"
	case engine.ReasonEndpointUnknown:
		return fakeagent.CategoryNotFound, "endpoint_unknown"
	case engine.ReasonInstanceUnknown:
		return fakeagent.CategoryNotFound, "instance_unknown"
	case engine.ReasonWrongEndpointKind:
		return fakeagent.CategoryRefused, "endpoint_kind"
	case engine.ReasonEndpointLimit:
		return fakeagent.CategoryRefused, "endpoint_limit"
	case engine.ReasonInvalidArgument:
		if r.Detail == engine.DetailEmptyPatch {
			return fakeagent.CategoryInvalidArgument, r.Detail.String()
		}
		return fakeagent.CategoryInvalidArgument, codeMalformed
	case engine.ReasonOperationIDReused:
		return fakeagent.CategoryConflict, "operation_id_reused"
	case engine.ReasonNodeQueueFull, engine.ReasonStorageFull, engine.ReasonStorageUnavailable,
		engine.ReasonClockRollback, engine.ReasonLoopBusy:
		return fakeagent.CategoryBackpressure, r.Reason.String()
	case engine.ReasonRecovering, engine.ReasonShuttingDown:
		return fakeagent.CategoryUnavailable, r.Reason.String()
	case engine.ReasonStorageFatal:
		// The command met a fatal storage error and the engine has stopped: whether
		// it took effect is not known, which is what outcome_unknown says.
		return fakeagent.CategoryOutcomeUnknown, r.Reason.String()
	}
	return fakeagent.CategoryInternal, "unclassified_reason"
}
