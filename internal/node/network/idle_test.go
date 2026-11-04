package network

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/aianyai/sidera/internal/nodeproto"
)

// canDialOf reports whether the node knows it can dial the other, from the other's
// own question: the other asked, and the node reached it.
func (p *peerNode) canDialOf(other *peerNode) bool {
	p.net.mu.Lock()
	defer p.net.mu.Unlock()
	r := p.net.reach[other.id]
	return r != nil && r.canDial(p.clk.Now())
}

// TestIdleMemberConnection leaves a connection between two members unused and
// shows which are closed after five minutes: only the ones that neither end
// needs, which is when each end has learned that the other can dial it, and has
// shown the other that it can dial. A connection a node keeps for the sake of
// its own reachability, one the other end keeps for it, and one to a member that
// cannot be asked (a node of an earlier release) stay. An answer on its way is
// not cut off by this close, which is the same close-when-idle as a renewal's and
// is shown there.
func TestIdleMemberConnection(t *testing.T) {
	for _, row := range []struct {
		name             string
		aKnowsB, bKnowsA bool
		oldPeer          bool // b does not serve the question
		closed           bool
	}{
		{"both can dial each other", true, true, false, true},
		{"one cannot dial the other", true, false, false, false},
		{"the member does not serve the question", true, true, true, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			a, b := keepPair(t, row.aKnowsB, row.bKnowsA)
			t.Cleanup(func() {
				if t.Failed() {
					t.Logf("a:\n%s\nb:\n%s", a.log, b.log)
				}
			})
			if row.oldPeer {
				b.net.lib.host.RemoveStreamHandler(protocol.ID(nodeproto.ProtocolDialBack))
			}
			// The first questions are answered before the connection can be idle
			// long enough to be closed.
			advancing(t, a.clk, "the questions to be answered", time.Second, func() bool {
				if row.oldPeer {
					connected := a.net.Connected(b.id)
					a.net.mu.Lock()
					defer a.net.mu.Unlock()
					r := a.net.reach[b.id]
					return connected && r != nil && r.cannotAsk
				}
				known := a.reachOf(b) != reachUnknown && b.reachOf(a) != reachUnknown
				return known && (!row.aKnowsB || a.canDialOf(b)) && (!row.bKnowsA || b.canDialOf(a))
			})

			// Not before five minutes.
			settle(a.clk, 10*time.Second, 25)
			if a.connCount() == 0 || b.connCount() == 0 {
				t.Fatalf("a connection was closed before it was idle for five minutes: a %d, b %d", a.connCount(), b.connCount())
			}

			settle(a.clk, 10*time.Second, 40)
			if row.closed {
				eventually(t, "the unneeded connection to be closed", func() bool { return a.connCount() == 0 && b.connCount() == 0 })
				// And nobody dials it again.
				settle(a.clk, 10*time.Second, 30)
				if a.connCount() != 0 || b.connCount() != 0 {
					t.Fatalf("connections after the close: a %d, b %d, want none", a.connCount(), b.connCount())
				}
				return
			}
			if !a.connectedTo(b) || !b.connectedTo(a) {
				t.Fatal("a connection that one end needs was closed")
			}
		})
	}
}
