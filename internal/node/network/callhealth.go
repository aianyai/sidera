package network

import (
	"context"
	"errors"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/transport"
)

// The health of the calls to a node is logged where the calls are made: the
// engine retries a failed call on its own schedule and says why only at debug
// level, so without this a node that stopped answering leaves no trace at the
// level an owner reads. One line says that calls to a node began to fail and
// why, and one that they work again, so a streak of failures costs two lines
// however long it lasts.

// callStreak is a run of failed calls to one node.
type callStreak struct {
	since  time.Time // the first failure of the run
	failed int
}

// noteCall records the outcome of a finished call to a node, as the caller of
// Send, Pull or QueryDirectory sees it. A call that ended because the caller's
// own context did says nothing about the node. A call refused because the node
// does not serve a feature the call needs reached the node and was answered by
// its negotiation, so it counts as the node working: it neither starts a run of
// failures nor leaves one standing.
func (n *Network) noteCall(ctx context.Context, target ident.NodeID, call string, err error) {
	if err != nil && ctx.Err() != nil {
		return
	}
	if errors.Is(err, transport.ErrFeatureUnsupported) {
		err = nil
	}
	now := n.clock.Now()
	n.mu.Lock()
	streak := n.callStreaks[target]
	if streak == nil && err != nil {
		streak = &callStreak{since: now}
		n.callStreaks[target] = streak
	}
	var failed int
	var since time.Time
	if streak != nil {
		failed, since = streak.failed, streak.since
		if err == nil {
			delete(n.callStreaks, target)
		} else {
			streak.failed++
			failed = streak.failed
		}
	}
	n.mu.Unlock()

	switch {
	case err == nil && failed > 0:
		n.log.Info("calls to a node work again", "node", target, "call", call, "failed", failed, "for", now.Sub(since))
	case err == nil:
	case failed == 1:
		n.log.Info("calls to a node are failing", append([]any{"node", target, "call", call}, failureAttrs(err)...)...)
	default:
		n.log.Debug("a call to a node failed again", append([]any{"node", target, "call", call, "failed", failed, "since", since}, failureAttrs(err)...)...)
	}
}

// failureAttrs says why a call failed, in the transport's terms: the kind of
// failure, the reason name of a refusal, and the error itself, which carries the
// detail of where the call stopped (no address, the memo of a failed dial, the
// error of each address dialed, the exchange, the peer's gate).
func failureAttrs(err error) []any {
	var refused transport.Refused
	switch {
	case errors.As(err, &refused):
		return []any{"kind", "refused", "reason", refused.Reason}
	case errors.Is(err, transport.ErrUnreachable):
		return []any{"kind", "unreachable", "err", err}
	case errors.Is(err, transport.ErrTimeout):
		return []any{"kind", "timeout", "err", err}
	default:
		return []any{"kind", "failed", "err", err}
	}
}
