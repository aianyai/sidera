package adapter

import (
	"context"
	"errors"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func closed() adapterproto.CloseResult {
	return adapterproto.CloseResult{Outcome: adapterproto.CloseClosed}
}

func unconfirmed(reason adapterproto.CloseReason) adapterproto.CloseResult {
	return adapterproto.CloseResult{Outcome: adapterproto.CloseUnconfirmed, Reason: reason}
}

// CloseSession releases a session's thread: it is unloaded, which ends the MCP
// server it started, and archived, which keeps its record. A turn that is running
// is left to end: the close is unconfirmed, and the sender interrupts first.
func (a *App) CloseSession(ctx context.Context, p adapterproto.CloseParams) (adapterproto.CloseResult, error) {
	s := a.sessionOf(p.Endpoint)
	if s != nil && s.attached {
		return adapterproto.CloseResult{}, wrongKind("closing")
	}
	if s != nil {
		return a.closeHeld(ctx, s)
	}
	if p.NativeRef == "" {
		return adapterproto.CloseResult{}, unknownEndpoint(p.Endpoint)
	}
	// Not held: the endpoint was closed in the channel, or the adapter restarted
	// and holds only the open ones. The thread is found by its reference, unless it
	// belongs to a session that is open under another endpoint, which is then left
	// alone: nothing is left to release for this one.
	if other := a.sessionByThread(string(p.NativeRef)); other != nil {
		return closed(), nil
	}
	c := a.liveClient()
	if c == nil {
		return unconfirmed(adapterproto.CloseNativeUnreachable), nil
	}
	return a.archive(ctx, c, string(p.NativeRef)), nil
}

func (a *App) closeHeld(ctx context.Context, s *session) (adapterproto.CloseResult, error) {
	a.mu.Lock()
	busy, creating := s.busy(), s.creating
	a.mu.Unlock()
	if creating {
		// The creation still owns the thread; it ends with its own answer.
		return adapterproto.CloseResult{}, errStillCreating
	}
	if busy {
		return unconfirmed(adapterproto.CloseTurnStillRunning), nil
	}
	c := a.liveClient()
	if c == nil {
		return unconfirmed(adapterproto.CloseNativeUnreachable), nil
	}
	if err := c.ThreadUnsubscribe(ctx, s.threadID); err != nil && !appserver.IsThreadGone(err) {
		if errors.Is(err, appserver.ErrClosed) {
			return unconfirmed(adapterproto.CloseNativeUnreachable), nil
		}
		// A server that will not unsubscribe still archives.
		a.log.Warn("cannot unsubscribe from a thread", "endpoint", s.endpoint, "error", err)
	}
	res := a.archive(ctx, c, s.threadID)
	if res.Outcome == adapterproto.CloseClosed {
		a.forget(s.endpoint)
	}
	return res, nil
}

// archive archives a thread. One the server does not have is as released as one it
// archived: for the node, released and gone are the same fact. A failure that does
// not say the thread is gone leaves it unconfirmed.
func (a *App) archive(ctx context.Context, c *appserver.Client, threadID string) adapterproto.CloseResult {
	err := c.ThreadArchive(ctx, threadID)
	switch {
	case err == nil || appserver.IsThreadGone(err):
		return closed()
	}
	a.log.Warn("cannot archive a thread", "error", err)
	return unconfirmed(adapterproto.CloseNativeUnreachable)
}
