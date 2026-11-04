package network

import (
	"bufio"
	"context"
	"fmt"
	"testing"
	"time"

	lnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/nodeproto"
	"github.com/aianyai/sidera/internal/preauth"
	"github.com/aianyai/sidera/internal/record"
)

// memberCall is one member call made from a to b, and what it must have got back.
type memberCall struct {
	name string
	// serve installs the handler of the call on the target: it renews the
	// target's certificate, which marks every connection of the target to close
	// as soon as it is idle, and answers.
	serve func(b *peerNode, renew func(), t testing.TB)
	// call makes the call and checks the answer.
	call func(ctx context.Context, a, b *peerNode) error
	// proto is the current identifier of the protocol; request is a request to
	// it as bytes and check says whether the bytes are the answer to it.
	proto   string
	request func() ([]byte, error)
	check   func(answer []byte) error
}

func memberCalls(t testing.TB) []memberCall {
	sender, recipient := endpointOf(1), endpointOf(0x21)
	return []memberCall{
		{name: "send", serve: func(b *peerNode, renew func(), t testing.TB) {
			rep := testRecord(t, b)
			b.node.inbound = func(context.Context, transport.Origin, ident.EndpointID, []byte) (transport.Receipt, error) {
				renew()
				return transport.Receipt{Reports: []record.Record{rep}}, nil
			}
		}, call: func(ctx context.Context, a, b *peerNode) error {
			receipt, err := a.net.Send(ctx, b.id, sender, []byte("an envelope"))
			if err == nil && len(receipt.Reports) != 1 {
				err = fmt.Errorf("receipt %+v", receipt)
			}
			return err
		}, proto: nodeproto.ProtocolSend,
			request: func() ([]byte, error) {
				return nodeproto.SendRequest{Sender: sender, Envelope: []byte("an envelope")}.Encode()
			},
			check: func(answer []byte) error {
				ans, err := nodeproto.DecodeSendAnswer(answer)
				if err == nil && (ans.Outcome != nodeproto.OutcomeOK || len(ans.Reports) != 1) {
					err = fmt.Errorf("answer %+v", ans)
				}
				return err
			}},
		{name: "pull", serve: func(b *peerNode, renew func(), t testing.TB) {
			rep := testRecord(t, b)
			b.node.pull = func(context.Context, transport.Origin, transport.PullRequest) (transport.PullAnswer, error) {
				renew()
				return transport.PullAnswer{Reports: []record.Record{rep}}, nil
			}
		}, call: func(ctx context.Context, a, b *peerNode) error {
			ans, err := a.net.Pull(ctx, b.id, transport.PullRequest{Message: "msg-1", Recipients: []ident.EndpointID{recipient}})
			if err == nil && len(ans.Reports) != 1 {
				err = fmt.Errorf("answer %+v", ans)
			}
			return err
		}, proto: nodeproto.ProtocolPull,
			request: func() ([]byte, error) {
				return nodeproto.PullRequest{Message: "msg-1", Recipients: []ident.EndpointID{recipient}}.Encode()
			},
			check: func(answer []byte) error {
				ans, err := nodeproto.DecodePullAnswer(answer)
				if err == nil && (ans.Outcome != nodeproto.OutcomeOK || len(ans.Reports) != 1) {
					err = fmt.Errorf("answer %+v", ans)
				}
				return err
			}},
		{name: "directory", serve: func(b *peerNode, renew func(), t testing.TB) {
			b.node.dir = func(context.Context, transport.Origin, ident.EndpointID, transport.DirectoryRequest) (transport.DirectoryPage, error) {
				renew()
				return transport.DirectoryPage{Endpoints: []transport.DirectoryEntry{{
					Address: ident.Address{Node: b.id, Endpoint: recipient}, Kind: adapterreg.KindRuntime,
					DisplayName: "runtime", Description: "a runtime", Accepts: []authz.Action{authz.ActionSee},
				}}}, nil
			}
		}, call: func(ctx context.Context, a, b *peerNode) error {
			page, err := a.net.QueryDirectory(ctx, b.id, sender, transport.DirectoryRequest{Limit: 5})
			if err == nil && len(page.Endpoints) != 1 {
				err = fmt.Errorf("page %+v", page)
			}
			return err
		}, proto: nodeproto.ProtocolDirectory,
			request: func() ([]byte, error) { return nodeproto.DirectoryRequest{Sender: sender, Limit: 5}.Encode() },
			check: func(answer []byte) error {
				ans, err := nodeproto.DecodeDirectoryAnswer(answer)
				if err == nil && (ans.Outcome != nodeproto.OutcomeOK || len(ans.Entries) != 1) {
					err = fmt.Errorf("answer %+v", ans)
				}
				return err
			}},
	}
}

// pairOver starts two members that reach each other over one transport. The TCP
// row has no QUIC listener at all, since identify would teach the dialing side
// the QUIC address and a later dial would take it; the QUIC row's address book
// names the QUIC address alone, which the dial prefers to the TCP one it learns.
func pairOver(t testing.TB, quic bool) (a, b *peerNode) {
	t.Helper()
	a, b = pair(t, quic, true, true, nil)
	if quic {
		if err := a.net.SetAddressBook(map[ident.NodeID][]string{b.id: {b.contact(false)}}); err != nil {
			t.Fatal(err)
		}
	}
	return a, b
}

// TestMemberAnswerSurvivesTheRenewal answers a member call in the same handling
// that renews the target's certificate. A renewal marks the target's
// connections to close as soon as they are idle, and the handler's return is
// what makes this one idle: the answer must be out of the connection before it
// is closed, or the caller gets a timeout and sends again. The loss is a race
// between the close and the delivery of the answer, so each cell repeats the
// call on a new pair of nodes, since the connection a round used is closed.
func TestMemberAnswerSurvivesTheRenewal(t *testing.T) {
	const rounds = 20
	for _, quic := range []bool{false, true} {
		for _, mc := range memberCalls(t) {
			name := "tcp"
			if quic {
				name = "quic"
			}
			t.Run(name+"/"+mc.name, func(t *testing.T) {
				lost := 0
				for i := 0; i < rounds; i++ {
					a, b := pairOver(t, quic)
					mc.serve(b, func() {
						if err := b.net.SetMembership(b.membership(2, false, a)); err != nil {
							t.Error(err)
						}
					}, t)
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					err := mc.call(ctx, a, b)
					cancel()
					if err != nil {
						lost++
						t.Logf("round %d: %v", i, err)
					}
					a.net.Close()
					b.net.Close()
				}
				if lost > 0 {
					t.Errorf("%d of %d answers lost", lost, rounds)
				}
			})
		}
	}
}

// withoutCurrentProtocols makes the node one that speaks only the legacy
// identifiers, as a node before 2.0.0 does: the handlers of the current ones are
// removed, so a stream proposing one is answered "not supported" by the
// negotiation, before any handler.
func withoutCurrentProtocols(b *peerNode) {
	for _, proto := range []string{nodeproto.ProtocolSend, nodeproto.ProtocolPull, nodeproto.ProtocolDirectory} {
		b.net.lib.host.RemoveStreamHandler(protocol.ID(proto))
	}
}

// TestCurrentClientFallsBackToTheLegacyProtocol calls a node that serves only
// the legacy identifiers. The first call proposes the current identifier, is told
// it is not served and takes the legacy one; neither it nor the next call, which
// goes the same way, is taken for a fault of the connection.
func TestCurrentClientFallsBackToTheLegacyProtocol(t *testing.T) {
	for _, mc := range memberCalls(t) {
		t.Run(mc.name, func(t *testing.T) {
			a, b := pairOver(t, false)
			mc.serve(b, func() {}, t)
			withoutCurrentProtocols(b)
			for i := 0; i < 2; i++ {
				if err := mc.call(ctx5(t), a, b); err != nil {
					t.Fatalf("call %d: %v", i, err)
				}
			}
			if a.net.memberConn(b.id) == nil {
				t.Error("the connection was dropped")
			}
		})
	}
}

// TestLegacyClientIsServed calls a node that serves both forms in the form of a
// node before 2.0.0: the request is everything up to the client's half-close and
// the answer everything up to the end of the stream.
func TestLegacyClientIsServed(t *testing.T) {
	for _, mc := range memberCalls(t) {
		t.Run(mc.name, func(t *testing.T) {
			a, b := pairOver(t, false)
			mc.serve(b, func() {}, t)
			legacy, ok := map[string]string{
				nodeproto.ProtocolSend:      nodeproto.LegacyProtocolSend,
				nodeproto.ProtocolPull:      nodeproto.LegacyProtocolPull,
				nodeproto.ProtocolDirectory: nodeproto.LegacyProtocolDirectory,
			}[mc.proto]
			if !ok {
				t.Fatalf("%s has no legacy identifier", mc.proto)
			}
			req, err := mc.request()
			if err != nil {
				t.Fatal(err)
			}
			e := a.dialRaw(b)
			eventually(t, "the exchange to complete", func() bool { return b.stateOf(a) == preauth.StateMember && a.net.Connected(b.id) })
			ctx := ctx5(t)
			s, err := openRaw(ctx, e.conn, legacy)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Reset()
			if _, err := s.Write(req); err != nil {
				t.Fatal(err)
			}
			s.CloseWrite()
			answer, err := nodeproto.ReadMessage(s, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if err := mc.check(answer); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestServerKeepsAStreamUntilTheClientClosesIt holds the client's end of a
// framed call open after the answer: the server's handler, and the busy count of
// its connection, stay until the client closes, and are released at the call's
// budget when it never does, so that a client that does not close cannot keep a
// connection from ever closing when idle.
func TestServerKeepsAStreamUntilTheClientClosesIt(t *testing.T) {
	call := memberCalls(t)[0]
	busy := func(p, other *peerNode) int {
		e := p.entryFor(other)
		if e == nil {
			return -1
		}
		p.net.mu.Lock()
		defer p.net.mu.Unlock()
		return e.busy
	}
	open := func(t *testing.T, budget time.Duration) (a, b *peerNode, s lnet.Stream, answer []byte) {
		a, b = pairTuned(t, false, true, true, nil, func(c *Config) { c.CallTimeout = budget })
		call.serve(b, func() {}, t)
		e := a.dialRaw(b)
		eventually(t, "the exchange to complete", func() bool { return b.stateOf(a) == preauth.StateMember && a.net.Connected(b.id) })
		s, err := openRaw(ctx5(t), e.conn, call.proto)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Reset() })
		req, err := call.request()
		if err != nil {
			t.Fatal(err)
		}
		framed, err := nodeproto.AppendFrame(nil, req, 1<<16)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Write(framed); err != nil {
			t.Fatal(err)
		}
		answer, err = nodeproto.ReadFrame(bufio.NewReader(s), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if err := call.check(answer); err != nil {
			t.Fatal(err)
		}
		return a, b, s, answer
	}

	t.Run("held until the client closes", func(t *testing.T) {
		a, b, s, _ := open(t, DefaultCallTimeout)
		time.Sleep(200 * time.Millisecond)
		if got := busy(b, a); got != 1 {
			t.Fatalf("the server released the stream before the client closed it: busy %d", got)
		}
		s.Close()
		eventually(t, "the server to release the connection", func() bool { return busy(b, a) == 0 })
	})
	t.Run("released at the budget when the client never closes", func(t *testing.T) {
		a, b, _, _ := open(t, 300*time.Millisecond)
		eventually(t, "the server to release the connection", func() bool { return busy(b, a) == 0 })
	})
}
