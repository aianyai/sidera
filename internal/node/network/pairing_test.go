package network

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"testing"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/pairing"
	"github.com/aianyai/sidera/internal/preauth"
)

// The nodes of these tests are not members of each other: pairing is what the
// access table lets such nodes do.

// netPublic is the node's network key as the connection authenticates it.
func netPublic(t testing.TB, p *peerNode) ident.PublicKey {
	t.Helper()
	k, err := ident.NewPublicKey(ident.AlgorithmEd25519, p.netKey.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// checkPeer checks that the facts of a connection carry the parties its
// certificates and keys give, at the serials the nodes presented.
func checkPeer(t testing.TB, got PairingPeer, local, remote *peerNode, localSerial, remoteSerial uint64) {
	t.Helper()
	for _, c := range []struct {
		name   string
		party  pairing.Party
		node   *peerNode
		serial uint64
	}{
		{"local", got.Conn.Local, local, localSerial},
		{"remote", got.Conn.Remote, remote, remoteSerial},
	} {
		if c.party.Node() != c.node.id || c.party.Identity() != c.node.identity.Public() ||
			c.party.Network() != netPublic(t, c.node) || c.party.Serial() != c.serial {
			t.Errorf("%s party is node %s at serial %d, want node %s at serial %d", c.name, c.party.Node(), c.party.Serial(), c.node.id, c.serial)
		}
	}
	if got.Conn.Protocol != string(preauth.ProtocolPairing) {
		t.Errorf("protocol %q", got.Conn.Protocol)
	}
	if got.Cert.Payload().Node != remote.id || got.Cert.Payload().Serial != remoteSerial {
		t.Errorf("certificate of node %s at serial %d, want node %s at serial %d", got.Cert.Payload().Node, got.Cert.Payload().Serial, remote.id, remoteSerial)
	}
	if got.Seen.Node != remote.id || got.Seen.Serial != remoteSerial || got.Seen.Digest != got.Cert.Digest() {
		t.Errorf("serial state %+v does not match the certificate", got.Seen)
	}
	if got.Record.Digest() != got.Cert.Digest() {
		t.Error("the record is not the one the certificate was verified from")
	}
}

// eventLog writes the events of a fake engine as comparable lines.
func eventLog(evs []pairEvent) []string {
	var out []string
	for _, e := range evs {
		if e.kind == "frame" {
			out = append(out, fmt.Sprintf("frame %d %s", e.sid, e.frame))
		} else {
			out = append(out, fmt.Sprintf("%s %d", e.kind, e.sid))
		}
	}
	return out
}

func waitEvents(t testing.TB, p *peerNode, want ...string) {
	t.Helper()
	eventually(t, fmt.Sprintf("the events %q", want), func() bool { return slices.Equal(eventLog(p.node.pairing()), want) })
}

// TestPairingStream opens a pairing stream between two nodes that are not
// members of each other, sends frames both ways, and ends it from either side.
// The engine that ended the stream is not told of it; the other is, once.
func TestPairingStream(t *testing.T) {
	for _, closer := range []string{"initiator", "target"} {
		t.Run(closer+" closes", func(t *testing.T) {
			// QUIC, where closing a stream also ends its reading.
			a, b := pair(t, true, false, false, nil)
			a.setMembership(2)
			b.setMembership(3)
			b.node.pairOpenedAs = 100
			b.node.onPairFrame = func(_ pairing.SessionID, frame []byte, s PairingStream) {
				switch string(frame) {
				case "m1":
					s.Send([]byte("m2"))
				case "m3":
					s.Send([]byte("m4"))
					if closer == "target" {
						s.Close()
					}
				}
			}

			peer, ps, err := a.net.StartPairing(ctx5(t), b.id, []string{b.contact(false)}, 7)
			if err != nil {
				t.Fatal(err)
			}
			a.node.keepStream(7, ps)
			checkPeer(t, peer, a, b, 2, 3)
			ps.Send([]byte("m1"))
			ps.Send([]byte("m3"))

			targetSaw := []string{"opened 100", "frame 100 m1", "frame 100 m3"}
			initiatorSaw := []string{"frame 7 m2", "frame 7 m4"}
			if closer == "target" {
				initiatorSaw = append(initiatorSaw, "closed 7")
			} else {
				targetSaw = append(targetSaw, "closed 100")
			}
			eventually(t, "the initiator's two answers", func() bool { return len(a.node.pairing()) >= 2 })
			if closer == "initiator" {
				ps.Close()
			}
			waitEvents(t, b, targetSaw...)
			waitEvents(t, a, initiatorSaw...)
			b.node.mu.Lock()
			opened := b.node.pairPeers[0]
			b.node.mu.Unlock()
			checkPeer(t, opened, b, a, 3, 2)
			// Closing the networks waits for every goroutine of the streams; what
			// the engines were told does not change while the streams wind down.
			a.net.Close()
			b.net.Close()
			if got := eventLog(a.node.pairing()); !slices.Equal(got, initiatorSaw) {
				t.Errorf("the initiator's engine saw %q after Close, want %q", got, initiatorSaw)
			}
			if got := eventLog(b.node.pairing()); !slices.Equal(got, targetSaw) {
				t.Errorf("the target's engine saw %q after Close, want %q", got, targetSaw)
			}
		})
	}
}

// TestPairingFetch makes one round trip on a connection to a node that is not a
// member, dialed on a hint alone, and returns the answer with the responder's
// facts. The engine that answered and closed is not told of the end.
func TestPairingFetch(t *testing.T) {
	a, b := pair(t, false, false, false, nil)
	a.setMembership(2)
	b.setMembership(3)
	if err := a.net.SetAddressBook(nil); err != nil {
		t.Fatal(err)
	}
	b.node.pairOpenedAs = 200
	b.node.onPairFrame = func(_ pairing.SessionID, frame []byte, s PairingStream) {
		s.Send(append([]byte("answer to "), frame...))
		s.Close()
	}

	answer, peer, err := a.net.PairingFetch(ctx5(t), b.id, []string{b.contact(true)}, []byte("f1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(answer) != "answer to f1" {
		t.Errorf("answer %q", answer)
	}
	checkPeer(t, peer, a, b, 2, 3)
	waitEvents(t, b, "opened 200", "frame 200 f1")
	a.net.Close()
	b.net.Close()
	if got := eventLog(b.node.pairing()); !slices.Equal(got, []string{"opened 200", "frame 200 f1"}) {
		t.Errorf("the responder's engine saw %q after the fetch", got)
	}
}

// TestPairingFetchAnswerSurvivesThePromotion is the target that completes a
// pairing on a fetch: it answers, and the same handling makes the initiator a
// member, which marks their non-member connection to be closed as soon as it is
// idle. The handler must keep the connection busy until the initiator has taken
// the answer: an idle close right after the handler returned discards a reply
// still in flight on QUIC.
//
// The window is the time the initiator's reader takes to wake after the answer
// arrives, and the connection's close that follows it lands in that window only
// on a busy machine: one processor makes it the usual outcome.
func TestPairingFetchAnswerSurvivesThePromotion(t *testing.T) {
	previous := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previous) })
	a, b := pair(t, true, false, false, nil)
	a.setMembership(2)
	b.setMembership(3)
	if err := a.net.SetAddressBook(nil); err != nil {
		t.Fatal(err)
	}
	b.node.pairOpenedAs = 200
	b.node.onPairFrame = func(_ pairing.SessionID, frame []byte, s PairingStream) {
		s.Send(append([]byte("answer to "), frame...))
		s.Close()
		if err := b.net.SetMembership(b.membership(3, false, a)); err != nil {
			t.Error(err)
		}
	}

	answer, _, err := a.net.PairingFetch(ctx5(t), b.id, []string{b.contact(false)}, []byte("f1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(answer) != "answer to f1" {
		t.Errorf("answer %q", answer)
	}
	eventually(t, "the promoted connection to be closed once idle", func() bool { return b.connCount() == 0 })
}

// TestStartPairingRefusesAnotherNode dials an address that reaches a node other
// than the one asked for.
func TestStartPairingRefusesAnotherNode(t *testing.T) {
	a, b := pair(t, false, false, false, nil)
	other := newPeerNode(t, a.clk, 0x13, 0x93)
	_, _, err := a.net.StartPairing(ctx5(t), other.id, []string{b.contact(true)}, 7)
	if !errors.Is(err, transport.ErrUnreachable) {
		t.Fatalf("got %v, want ErrUnreachable", err)
	}
	if got := eventLog(b.node.pairing()); len(got) != 0 {
		t.Errorf("a stream reached the other node: %q", got)
	}
}

// The online timeout of a pairing stream is the one the connection accounting
// keeps non-member connections open for.
func TestPairingOnlineTimeoutIsTheAccountings(t *testing.T) {
	if got, want := DefaultParams().PairingOnlineTimeout, pairing.DefaultParams().OnlineTimeout; got != want {
		t.Fatalf("the accounting allows %v for the online segment, the pairing protocol %v", got, want)
	}
}
