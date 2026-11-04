package network

import (
	"time"

	lnet "github.com/libp2p/go-libp2p/core/network"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/preauth"
)

// The host has no connection manager, so a connection, once made, lives until a
// restart, a renewal or a fault. Left like that, a network of members that can all
// dial each other ends as a mesh of connections nobody uses, which grows with the
// square of the members. A member connection is therefore marked to close when
// idle once neither end needs it and nobody has used it for five minutes, and it
// is closed by the table's own close-when-idle (see release), so an answer on its
// way is never cut off. There is no count limit and no second closer: a limit, if
// one is ever needed, is one more reason to choose among the connections here.

// idleCloseAfter is how long a connection nobody needs is left idle before it is
// closed. Making it again costs a certificate exchange, and two agents talk at
// intervals of minutes, so it is longer than the gap of a conversation.
const idleCloseAfter = 5 * time.Minute

// connNeededLocked reports whether either end needs a connection between this
// node and the member, whoever opened it:
//
//   - this node needs it unless the member is known to be able to dial this node;
//   - the member needs it unless this node has shown the member it can dial the
//     member (the member cannot know that for itself).
//
// Both ends reach the same conclusion from the same two questions, so one does
// not close a connection the other dials again.
func (n *Network) connNeededLocked(node ident.NodeID, now time.Time) bool {
	r := n.reach[node]
	return r == nil || r.stateAt(now) != reachReachable || !r.canDial(now)
}

// closeUnneeded marks the member connections that neither end needs and that
// have been idle for idleCloseAfter to close when idle, and closes those that are
// idle now.
func (n *Network) closeUnneeded(now time.Time) {
	var closing []lnet.Conn
	n.mu.Lock()
	for _, e := range n.conns {
		if e.state != preauth.StateMember || e.closeIdle || e.busy != 0 || now.Sub(e.idleSince) < idleCloseAfter {
			continue
		}
		if n.connNeededLocked(e.node, now) {
			continue
		}
		e.closeIdle = true
		closing = append(closing, e.conn)
	}
	n.mu.Unlock()

	for _, c := range closing {
		n.log.Debug("closing a member connection that neither end needs", "peer", c.RemotePeer())
		c.Close()
	}
}
