package network

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/nodeproto"
)

// advancing is eventually on the fake clock: every poll moves the clock on by
// step, since what the test waits for is made by the network's tick.
func advancing(t testing.TB, clk *clocktest.Fake, what string, step time.Duration, cond func() bool) {
	t.Helper()
	eventually(t, what, func() bool {
		if cond() {
			return true
		}
		clk.Advance(step)
		return false
	})
}

// keepPair starts two nodes that are members of each other and hold each
// other's certificates, with the address book of each holding the other's
// listening address or nothing: a node whose book holds none for the other
// cannot be dialed by it.
func keepPair(t *testing.T, aKnowsB, bKnowsA bool) (a, b *peerNode) {
	t.Helper()
	clk := newClock()
	a = newPeerNode(t, clk, 0x11, 0x91)
	b = newPeerNode(t, clk, 0x12, 0x92)
	short := func(c *Config) { c.ConnectTimeout = 500 * time.Millisecond }
	// The two nodes dial each other when both book the other's address, and two
	// dials that cross on the loopback both fail. The fake clock's step wakes the
	// ticks of both sides at once, and the random wait before a dial is shorter
	// than a tick, so the ticks of one are out of step with the other's to keep
	// the dials from starting together.
	a.start(false, short)
	b.start(false, func(c *Config) { short(c); c.TickEvery = 1300 * time.Millisecond })
	for _, p := range []struct{ self, other *peerNode }{{a, b}, {b, a}} {
		if err := p.self.net.SetMembership(p.self.membership(1, true, p.other)); err != nil {
			t.Fatal(err)
		}
	}
	if aKnowsB {
		a.knows(b)
	}
	if bKnowsA {
		b.knows(a)
	}
	return a, b
}

// restart replaces the node's network with a new one of the same key, as a
// daemon restart does, listening on the addresses given, trusting the other
// node and holding its address when knowsOther says so. The new network presents a certificate of the next serial, as a node
// that was restarted after it renewed does: the fake clock has moved, so the
// certificate it would issue at the old serial would be another one, which the
// peers refuse as a conflict.
func (p *peerNode) restart(listen []string, other *peerNode, knowsOther bool) {
	p.t.Helper()
	p.net.Close()
	n, err := New(Config{
		Key:            p.netKey,
		Clock:          p.clk,
		Rand:           &countingReader{},
		Log:            slog.New(slog.NewTextHandler(p.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
		ConnectTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		p.t.Fatal(err)
	}
	p.t.Cleanup(func() { n.Close() })
	p.net = n
	n.SetNode(p.node)
	if err := n.SetMembership(p.membership(2, true, other)); err != nil {
		p.t.Fatal(err)
	}
	if knowsOther {
		p.knows(other)
	}
	if err := n.Listen(listen); err != nil {
		p.t.Fatal(err)
	}
}

func (p *peerNode) listenAddr() []string {
	return []string{strings.TrimSuffix(p.contact(true), "/p2p/"+p.net.PeerID())}
}

// reachOf is what the node has recorded of whether the other can dial it.
func (p *peerNode) reachOf(other *peerNode) reachState {
	p.net.mu.Lock()
	defer p.net.mu.Unlock()
	return p.net.reachabilityLocked(other.id, p.clk.Now())
}

func (p *peerNode) connectedTo(other *peerNode) bool { return p.net.Connected(other.id) }

// closeConns closes every connection of the node.
func (p *peerNode) closeConns() {
	p.net.mu.Lock()
	var conns []interface{ Close() error }
	for c := range p.net.conns {
		conns = append(conns, c)
	}
	p.net.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

// settle lets the ticks run on the fake clock for a while and checks cond still
// holds at the end of it: a way to see that something is not done.
func settle(clk *clocktest.Fake, step time.Duration, rounds int) {
	for i := 0; i < rounds; i++ {
		clk.Advance(step)
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRetryBackoff(t *testing.T) {
	want := map[int]time.Duration{
		0: 5 * time.Second, 1: 5 * time.Second, 2: 10 * time.Second, 3: 20 * time.Second,
		4: 40 * time.Second, 5: 60 * time.Second, 6: 60 * time.Second, 40: 60 * time.Second,
	}
	for failures, d := range want {
		if got := retryBackoff(failures); got != d {
			t.Errorf("after %d failures: %v, want %v", failures, got, d)
		}
	}
}

// TestKeptConnectionComesBackAfterARestart restarts each side of a pair in which
// one node cannot be dialed (the other's book has no address for it), and shows
// that the connection is made again with nothing sent: by the node that cannot
// be dialed when it is the one that restarts, and again by it when the other
// does, which is down for a while and so is dialed on the backoff.
func TestKeptConnectionComesBackAfterARestart(t *testing.T) {
	for _, row := range []struct {
		name    string
		restart func(a, b *peerNode)
	}{
		{"the node that cannot be dialed restarts", func(a, b *peerNode) {
			a.restart([]string{"/ip4/127.0.0.1/tcp/0"}, b, true)
		}},
		{"the node it keeps the connection to restarts", func(a, b *peerNode) {
			listen := b.listenAddr()
			b.net.Close()
			// The node ends its connections to the one that closed, as it does when
			// it sees them lost: see below for why the test does not wait for it to
			// see them. What follows is the dials after a loss.
			a.closeConns()
			// Down for a few attempts of the backoff.
			settle(a.clk, 10*time.Second, 10)
			b.restart(listen, a, false)
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			a, b := keepPair(t, true, false)
			t.Cleanup(func() {
				if t.Failed() {
					t.Logf("a:\n%s\nb:\n%s", a.log, b.log)
				}
			})
			// Both questions are answered before a side restarts: a dial-back
			// still in flight when a node closes its connections leaves a second
			// connection to reset at the same moment. The library closes a TCP
			// connection with linger 0, which sends a single RST that is not
			// retransmitted, and when it coincides with the dial-back connection
			// being handshaken and reset on the same listening port, the peer's
			// kernel was observed (macOS loopback; with a FIN close it never
			// happened) never to process it, which leaves the other end of that
			// connection open. The product tolerates such a half-open connection
			// (the next call fails on it and clears it, and the yamux keepalive
			// clears it within about 30 seconds), but the test cannot wait for
			// that on a fake clock, so where the other side restarts it ends the
			// connections of the node that stays itself.
			advancing(t, a.clk, "the connection to come up and both questions to be answered", time.Second, func() bool {
				return a.connectedTo(b) && b.connectedTo(a) && a.reachOf(b) == reachUnreachable && b.reachOf(a) != reachUnknown
			})

			row.restart(a, b)

			advancing(t, a.clk, "the connection to come back", time.Second, func() bool {
				return a.connectedTo(b) && b.connectedTo(a)
			})
			// The node that dials is the one that cannot be dialed, so the other can
			// only answer over that connection.
			if _, err := b.net.Send(ctx5(t), a.id, endpointOf(1), []byte("envelope")); err != nil {
				t.Fatalf("a call from the node that could not dial: %v", err)
			}
		})
	}
}

// TestMemberThatCanDialIsNotKept shows what a member that can dial this node is
// spared: after its connection is gone nobody dials it again, until the answer
// has expired or this node's addresses have changed, when the question is asked
// anew.
func TestMemberThatCanDialIsNotKept(t *testing.T) {
	a, b := keepPair(t, true, true)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("a:\n%s\nb:\n%s", a.log, b.log)
		}
	})
	both := func() bool { return a.reachOf(b) == reachReachable && b.reachOf(a) == reachReachable }
	advancing(t, a.clk, "each to learn it can be dialed", time.Second, both)

	reset := func() {
		a.closeConns()
		b.closeConns()
		eventually(t, "the connections to end", func() bool { return a.connCount() == 0 && b.connCount() == 0 })
	}
	reset()
	settle(a.clk, 10*time.Second, 20)
	if a.connCount() != 0 || b.connCount() != 0 {
		t.Fatalf("connections three minutes after: a %d, b %d, want none", a.connCount(), b.connCount())
	}

	t.Run("the answer expires", func(t *testing.T) {
		a.clk.Advance(reachabilityTTL)
		advancing(t, a.clk, "the member to be dialed and asked again", time.Second, func() bool {
			return a.connectedTo(b) && both()
		})
	})
	t.Run("the addresses change", func(t *testing.T) {
		reset()
		a.net.localAddressesChanged()
		if got := a.reachOf(b); got != reachUnknown {
			t.Fatalf("after an address change a records %d, want unknown", got)
		}
		advancing(t, a.clk, "the member to be dialed and asked again", time.Second, func() bool {
			return a.connectedTo(b) && both()
		})
	})
}

// TestMemberBeforeTheQuestionIsKept shows a member that does not serve the
// dial-back question, as a node of an earlier release does not, is kept: its
// connection is made again when it is gone.
func TestMemberBeforeTheQuestionIsKept(t *testing.T) {
	a, b := keepPair(t, true, false)
	b.net.lib.host.RemoveStreamHandler(protocol.ID(nodeproto.ProtocolDialBack))
	advancing(t, a.clk, "the member to be asked", time.Second, func() bool {
		connected := a.connectedTo(b)
		a.net.mu.Lock()
		defer a.net.mu.Unlock()
		r := a.net.reach[b.id]
		return connected && r != nil && r.cannotAsk
	})
	if got := a.reachOf(b); got != reachUnknown {
		t.Fatalf("recorded %d for a member that cannot be asked, want unknown", got)
	}
	a.closeConns()
	advancing(t, a.clk, "the connection to be made again", time.Second, func() bool { return a.connectedTo(b) })
}

// TestKeepSetIsTheMembersThatCannotBeRelied checks who is in the set: not a
// member the node has no address for, not one that can dial the node.
func TestKeepSetIsTheMembersThatCannotBeRelied(t *testing.T) {
	a, b := keepPair(t, false, false)
	size := func() int {
		a.net.mu.Lock()
		defer a.net.mu.Unlock()
		return len(a.net.keepSetLocked(a.clk.Now()))
	}
	if got := size(); got != 0 {
		t.Fatalf("the set holds %d members while the book has no address for them", got)
	}
	a.knows(b)
	if got := size(); got != 1 {
		t.Fatalf("the set holds %d members, want the one with an address", got)
	}
	a.net.mu.Lock()
	a.net.reach[b.id] = &reachRecord{state: reachReachable, decided: a.clk.Now()}
	a.net.mu.Unlock()
	if got := size(); got != 0 {
		t.Fatalf("the set holds %d members while the only one can dial the node", got)
	}
}
