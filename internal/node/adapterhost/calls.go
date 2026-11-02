package adapterhost

import (
	"context"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The calls the engine makes on an adapter. Each runs on the caller's goroutine,
// bounded by the caller's context, and none waits for anything the host does. A
// call to an instance that has no connection past adapter.start, or whose
// connection is being ended, returns ErrNotConnected at once and sends nothing.
// An error of a call that was sent is returned as the peer returned it, for the
// caller to judge with the effect rule; a result that does not decode or validate
// is returned as an error that is not a *peer.Error, which the effect rule counts
// as an unknown outcome.

// CreateSession sends session.create.
func (h *Host) CreateSession(ctx context.Context, instance string, p adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error) {
	return callAdapter[adapterproto.SessionCreateResult](ctx, h, instance, adapterproto.MethodSessionCreate, p)
}

// DeliverPrompt sends prompt.deliver.
func (h *Host) DeliverPrompt(ctx context.Context, instance string, p adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error) {
	return callAdapter[adapterproto.PromptDeliverResult](ctx, h, instance, adapterproto.MethodPromptDeliver, p)
}

// Reconcile sends prompt.reconcile.
func (h *Host) Reconcile(ctx context.Context, instance string, p adapterproto.ReconcileParams) (adapterproto.ReconcileResult, error) {
	return callAdapter[adapterproto.ReconcileResult](ctx, h, instance, adapterproto.MethodPromptReconcile, p)
}

// Interrupt sends session.interrupt.
func (h *Host) Interrupt(ctx context.Context, instance string, p adapterproto.InterruptParams) (adapterproto.InterruptResult, error) {
	return callAdapter[adapterproto.InterruptResult](ctx, h, instance, adapterproto.MethodSessionInterrupt, p)
}

// Close sends session.close.
func (h *Host) Close(ctx context.Context, instance string, p adapterproto.CloseParams) (adapterproto.CloseResult, error) {
	return callAdapter[adapterproto.CloseResult](ctx, h, instance, adapterproto.MethodSessionClose, p)
}

// NotifyPending sends endpoint.pending. It is best effort: it never blocks, and a
// notification that cannot be queued, or an instance that is not in service, is
// only logged.
func (h *Host) NotifyPending(instance string, p adapterproto.PendingParams) {
	c, err := h.serviceConn(instance)
	if err != nil {
		return
	}
	if err := p.Validate(); err != nil {
		c.log.Warn("endpoint.pending was not sent: the parameters are not valid", "err", err)
		return
	}
	if err := c.p.Notify(context.Background(), adapterproto.MethodEndpointPending, p); err != nil {
		c.log.Debug("endpoint.pending was not sent", "err", err)
	}
}

// Attach sends session.attach. The wake information it carries is never logged.
func (h *Host) Attach(ctx context.Context, instance string, p adapterproto.SessionAttachParams) (adapterproto.SessionAttachResult, error) {
	return callAdapter[adapterproto.SessionAttachResult](ctx, h, instance, adapterproto.MethodSessionAttach, p)
}

// NotifyDetached sends session.detached. It is best effort, as NotifyPending is.
func (h *Host) NotifyDetached(instance string, p adapterproto.SessionDetachedParams) {
	c, err := h.serviceConn(instance)
	if err != nil {
		return
	}
	if err := p.Validate(); err != nil {
		c.log.Warn("session.detached was not sent: the parameters are not valid", "err", err)
		return
	}
	if err := c.p.Notify(context.Background(), adapterproto.MethodSessionDetached, p); err != nil {
		c.log.Debug("session.detached was not sent", "err", err)
	}
}

func callAdapter[R any, PR interface {
	*R
	adapterproto.Message
}](ctx context.Context, h *Host, instance, method string, params adapterproto.Message) (R, error) {
	var res R
	c, err := h.serviceConn(instance)
	if err != nil {
		return res, err
	}
	if err := c.call(ctx, method, params, PR(&res)); err != nil {
		var zero R
		return zero, err
	}
	return res, nil
}

// serviceConn is the connection of an instance that is in service.
func (h *Host) serviceConn(instance string) (*conn, error) {
	h.mu.Lock()
	var s *supervisor
	if h.phase == phaseRunning {
		s = h.sups[instance]
	}
	h.mu.Unlock()
	if s == nil {
		return nil, ErrNotConnected
	}
	s.mu.Lock()
	c := s.service
	s.mu.Unlock()
	if c == nil || c.closing.Load() {
		return nil, ErrNotConnected
	}
	return c, nil
}
