package engine

import (
	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Announcing. A direct endpoint is read by its adapter with recv, and is told with
// endpoint.pending that a message is ready to be handed out to it. The notification is
// level-triggered and may be coalesced: it says that something waits and how many
// messages do, never how many notifications were sent. It is best effort. Nothing is
// recorded, nothing is sent again and nothing waits for it, because an adapter that
// misses it reads on its own, with a recv that may wait.
//
// A message becoming ready (taken in, or queued again when its lease ran out) marks
// the endpoint in pendingNotices. After the command, once the waiting recv has been
// handed what it could take (serveReady), servePending tells the adapter once for each
// endpoint marked, however many messages became ready in the command, and only if a
// message still waits: one that a waiting recv took needs no announcement.

// notePending marks a direct endpoint that a message became ready for.
func (e *Engine) notePending(id ident.EndpointID) {
	if st := e.view.endpoints[id]; st != nil && st.rec.Kind == adapterreg.KindDirect && st.rec.Status == store.EndpointOpen {
		e.pendingNotices[id] = true
	}
}

// servePending tells the adapter of each marked endpoint how many messages wait for it.
// It is a step of the loop like serveReady, and the call to the adapter does not block.
func (e *Engine) servePending() {
	if len(e.pendingNotices) == 0 {
		return
	}
	marked := e.pendingNotices
	e.pendingNotices = map[ident.EndpointID]bool{}
	// A notification not sent while the node does not serve, is paused for a clock in
	// doubt (the hand-out of a message judges its authorization at the time) or is
	// retired is lost, as any may be.
	if e.Phase() != PhaseRunning || e.fatal != nil || e.paused || e.retired || e.d.Adapters == nil {
		return
	}
	now := e.d.Clock.Now()
	for id := range marked {
		st := e.view.endpoints[id]
		if st == nil || st.rec.Status != store.EndpointOpen {
			continue
		}
		if n := len(e.pendingOf(now, st)); n > 0 {
			e.d.Adapters.NotifyPending(st.rec.Instance, adapterproto.PendingParams{Endpoint: adapterproto.EndpointRef(id.String()), Count: n})
		}
	}
}
