package network

import (
	"runtime"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/nodeproto"
	"github.com/aianyai/sidera/internal/preauth"
)

// dialBackPair starts two members of each other, a connected to b as a member
// on both sides, with b's address book holding what the row says for a: book
// returns the addresses of a that b holds.
func dialBackPair(t *testing.T, quic bool, book func(a *peerNode) []string) (a, b *peerNode) {
	t.Helper()
	// A dial-back to an address nothing answers on takes the whole connect
	// budget, so b's is short; the rows that reach a do not use it up.
	a, b = pairTuned(t, quic, true, true, nil, func(c *Config) { c.ConnectTimeout = 500 * time.Millisecond })
	entry := map[ident.NodeID][]string{}
	if addrs := book(a); len(addrs) > 0 {
		entry[a.id] = addrs
	}
	if err := b.net.SetAddressBook(entry); err != nil {
		t.Fatal(err)
	}
	a.dialRaw(b)
	eventually(t, "the exchange to complete", func() bool { return b.stateOf(a) == preauth.StateMember && a.net.Connected(b.id) })
	return a, b
}

// portOf is the port of a multiaddr of the transport: tcp or udp.
func portOf(t *testing.T, addr string, quic bool) string {
	t.Helper()
	m, err := ma.NewMultiaddr(addr)
	if err != nil {
		t.Fatal(err)
	}
	code := ma.P_TCP
	if quic {
		code = ma.P_UDP
	}
	port, err := m.ValueForProtocol(code)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// TestDialBackAnswers asks a member whether it can dial this node back and
// checks the answer for each thing the member's address book can hold for it,
// over each transport: the address the listener is open on, an address nothing
// answers on (what a listener that refuses fresh inbound connections looks like
// from outside) and none at all. When the dial-back is made, it must come from a
// port that is not the member's listening port: from that port the packets of
// the connection the asker made to the member would be the same flow, which a
// stateful firewall lets through whatever it does with a cold inbound
// connection, and the answer "reached" would be wrong in exactly the case the
// question is for.
func TestDialBackAnswers(t *testing.T) {
	for _, quic := range []bool{false, true} {
		transport := "tcp"
		if quic {
			transport = "quic"
		}
		for _, row := range []struct {
			name string
			book func(a *peerNode) []string
			want dialBackOutcome
		}{
			{"the listener is open on the address held", func(a *peerNode) []string { return []string{a.contact(!quic)} }, dialBackReached},
			{"nothing answers on the address held", func(a *peerNode) []string {
				if quic {
					return []string{"/ip4/127.0.0.1/udp/1/quic-v1/p2p/" + a.net.PeerID()}
				}
				return []string{"/ip4/127.0.0.1/tcp/1/p2p/" + a.net.PeerID()}
			}, dialBackNotReached},
			{"no address is held", func(*peerNode) []string { return nil }, dialBackNoAddress},
		} {
			t.Run(transport+"/"+row.name, func(t *testing.T) {
				a, b := dialBackPair(t, quic, row.book)
				got, err := a.net.askDialBack(ctx5(t), b.id)
				if err != nil {
					t.Fatal(err)
				}
				if got.Outcome != row.want {
					t.Fatalf("outcome %d, want %d", got.Outcome, row.want)
				}
				if row.want != dialBackReached {
					if got.Address != nil {
						t.Errorf("address %s on an answer that did not reach", got.Address)
					}
					return
				}
				listen := a.contact(!quic)
				if reached := got.Address.String() + "/p2p/" + a.net.PeerID(); reached != listen {
					t.Errorf("reached %s, want the listening address %s", reached, listen)
				}
				// The port the dial-back leaves from is the dialer's to show: the
				// asker may close the connection before it has taken it up.
				asker, err := peer.Decode(a.net.PeerID())
				if err != nil {
					t.Fatal(err)
				}
				conn, err := b.net.lib.dialBack.dial(ctx5(t), asker, b.net.dialBackAddrs(asker))
				if err != nil {
					t.Fatal(err)
				}
				if from, bPort := portOf(t, conn.Local.String(), quic), portOf(t, b.contact(!quic), quic); from == bPort {
					t.Errorf("the dial-back left from %s, the member's listening port", conn.Local)
				}
			})
		}
	}
}

// TestDialBackToAPeerBeforeTheProtocol asks a member that does not serve the
// protocol, as a node before it does: the multistream negotiation answers
// "not supported", which is its own outcome (the question was never put, so
// nothing is known), and it is no fault of the connection.
func TestDialBackToAPeerBeforeTheProtocol(t *testing.T) {
	a, b := dialBackPair(t, false, func(a *peerNode) []string { return []string{a.contact(true)} })
	b.net.lib.host.RemoveStreamHandler(protocol.ID(nodeproto.ProtocolDialBack))
	for i := 0; i < 2; i++ {
		got, err := a.net.askDialBack(ctx5(t), b.id)
		if err != nil {
			t.Fatalf("ask %d: %v", i, err)
		}
		if got.Outcome != dialBackCannotAsk {
			t.Fatalf("ask %d: outcome %d, want cannot ask (%d)", i, got.Outcome, dialBackCannotAsk)
		}
	}
	if a.net.memberConn(b.id) == nil {
		t.Error("the connection was dropped")
	}
}

// TestDialBackLeavesNothingBehind makes dial-backs over QUIC and closes both
// nodes, then waits for the goroutine count to fall back to what it was before
// the dial-backs. A QUIC connection manager that does not reuse sockets opens a
// UDP socket for every dial and the library never closes it, so the dialer has to,
// and what the socket's transport runs would stay behind.
func TestDialBackLeavesNothingBehind(t *testing.T) {
	run := func() {
		a, b := dialBackPair(t, true, func(a *peerNode) []string { return []string{a.contact(false)} })
		for i := 0; i < 5; i++ {
			got, err := a.net.askDialBack(ctx5(t), b.id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Outcome != dialBackReached {
				t.Fatalf("ask %d: outcome %d, want reached (%d)", i, got.Outcome, dialBackReached)
			}
		}
		a.net.Close()
		b.net.Close()
	}
	run() // the library starts a few goroutines of its own once per process
	time.Sleep(200 * time.Millisecond)
	before := runtime.NumGoroutine()
	run()
	var after int
	eventually(t, "the goroutines to end", func() bool {
		after = runtime.NumGoroutine()
		return after <= before
	})
	t.Logf("goroutines: %d before, %d after", before, after)
}
