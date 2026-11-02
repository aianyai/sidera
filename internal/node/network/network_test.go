package network

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	lnet "github.com/libp2p/go-libp2p/core/network"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/nodeproto"
	"github.com/aianyai/sidera/internal/preauth"
	"github.com/aianyai/sidera/internal/record"
)

func newClock() *clocktest.Fake { return clocktest.NewFake(time.Unix(1_800_000_000, 0)) }

// pair starts two nodes. aTrustsB and bTrustsA say who holds a direct
// credential with whom; a knows where b is.
func pair(t testing.TB, quic bool, aTrustsB, bTrustsA bool, tune func(*Config)) (a, b *peerNode) {
	t.Helper()
	return pairTuned(t, quic, aTrustsB, bTrustsA, tune, tune)
}

// pairTuned is pair with a configuration of its own for each node.
func pairTuned(t testing.TB, quic bool, aTrustsB, bTrustsA bool, tuneA, tuneB func(*Config)) (a, b *peerNode) {
	t.Helper()
	clk := newClock()
	a = newPeerNode(t, clk, 0x11, 0x91)
	b = newPeerNode(t, clk, 0x12, 0x92)
	a.start(quic, tuneA)
	b.start(quic, tuneB)
	var aTrusts, bTrusts []*peerNode
	if aTrustsB {
		aTrusts = append(aTrusts, b)
	}
	if bTrustsA {
		bTrusts = append(bTrusts, a)
	}
	a.setMembership(1, aTrusts...)
	b.setMembership(1, bTrusts...)
	a.knows(b)
	b.knows(a)
	return a, b
}

func testRecord(t testing.TB, p *peerNode) record.Record {
	t.Helper()
	rec, err := record.Sign("test/report/1", []byte("payload"), p.identity)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func sameRecord(a, b record.Record) bool {
	ab, err1 := a.Encode()
	bb, err2 := b.Encode()
	return err1 == nil && err2 == nil && bytes.Equal(ab, bb)
}

func endpointOf(first byte) ident.EndpointID {
	b := make([]byte, ident.EndpointIDLen)
	for i := range b {
		b[i] = first + byte(i)
	}
	id, err := ident.EndpointIDFromBytes(b)
	if err != nil {
		panic(err)
	}
	return id
}

func ctx5(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestRoundTrips carries each of the three requests to a member over each of the
// two transports and checks what arrived and what came back.
func TestRoundTrips(t *testing.T) {
	for _, transportName := range []string{"tcp", "quic"} {
		t.Run(transportName, func(t *testing.T) {
			a, b := pair(t, true, true, true, nil)
			if transportName == "quic" {
				// Only the QUIC address is known, so the call can only go over it.
				book := map[ident.NodeID][]string{b.id: {b.contact(false)}}
				if err := a.net.SetAddressBook(book); err != nil {
					t.Fatal(err)
				}
			}
			rep := testRecord(t, b)
			sender, recipient := endpointOf(1), endpointOf(0x21)
			b.node.inbound = func(_ context.Context, from transport.Origin, s ident.EndpointID, env []byte) (transport.Receipt, error) {
				if s != sender || string(env) != "an envelope" {
					return transport.Receipt{}, errors.New("unexpected request")
				}
				return transport.Receipt{Reports: []record.Record{rep}, ControlReply: &rep}, nil
			}
			b.node.pull = func(_ context.Context, from transport.Origin, req transport.PullRequest) (transport.PullAnswer, error) {
				if req.Message != "msg-1" || len(req.Recipients) != 1 || req.Recipients[0] != recipient {
					return transport.PullAnswer{}, errors.New("unexpected request")
				}
				return transport.PullAnswer{Reports: []record.Record{rep}, NoRecord: []ident.EndpointID{endpointOf(0x41)}}, nil
			}
			b.node.dir = func(_ context.Context, from transport.Origin, s ident.EndpointID, req transport.DirectoryRequest) (transport.DirectoryPage, error) {
				if s != sender || req.Cursor != "c1" || req.Limit != 5 {
					return transport.DirectoryPage{}, errors.New("unexpected request")
				}
				return transport.DirectoryPage{
					Endpoints: []transport.DirectoryEntry{{
						Address: ident.Address{Node: b.id, Endpoint: recipient}, Kind: adapterreg.KindRuntime,
						DisplayName: "runtime", Description: "a runtime", Accepts: []authz.Action{authz.ActionSee},
					}},
					NextCursor: "c2",
				}, nil
			}

			ctx := ctx5(t)
			receipt, err := a.net.Send(ctx, b.id, sender, []byte("an envelope"))
			if err != nil {
				t.Fatal(err)
			}
			if len(receipt.Reports) != 1 || !sameRecord(receipt.Reports[0], rep) || receipt.ControlReply == nil || !sameRecord(*receipt.ControlReply, rep) {
				t.Fatalf("receipt = %+v", receipt)
			}

			answer, err := a.net.Pull(ctx, b.id, transport.PullRequest{Message: delivery.MessageID("msg-1"), Recipients: []ident.EndpointID{recipient}})
			if err != nil {
				t.Fatal(err)
			}
			if len(answer.Reports) != 1 || len(answer.NoRecord) != 1 || answer.NoRecord[0] != endpointOf(0x41) || answer.ControlReply != nil {
				t.Fatalf("pull answer = %+v", answer)
			}

			page, err := a.net.QueryDirectory(ctx, b.id, sender, transport.DirectoryRequest{Cursor: "c1", Limit: 5})
			if err != nil {
				t.Fatal(err)
			}
			want := transport.DirectoryEntry{
				Address: ident.Address{Node: b.id, Endpoint: recipient}, Kind: adapterreg.KindRuntime,
				DisplayName: "runtime", Description: "a runtime", Accepts: []authz.Action{authz.ActionSee},
			}
			if page.NextCursor != "c2" || len(page.Endpoints) != 1 || page.Endpoints[0].Address != want.Address ||
				page.Endpoints[0].Kind != want.Kind || page.Endpoints[0].DisplayName != want.DisplayName ||
				page.Endpoints[0].Description != want.Description || len(page.Endpoints[0].Accepts) != 1 || page.Endpoints[0].Accepts[0] != authz.ActionSee {
				t.Fatalf("page = %+v", page)
			}

			b.node.mu.Lock()
			defer b.node.mu.Unlock()
			if len(b.node.origins) != 3 {
				t.Fatalf("the target saw %d requests, want 3", len(b.node.origins))
			}
			for _, o := range b.node.origins {
				if o.Local() || o.Node() != a.id {
					t.Fatalf("origin = local %v node %v, want a remote origin of the caller", o.Local(), o.Node())
				}
			}
		})
	}
}

// TestDecisions is the state each side gives the other after the exchange, for
// every combination of who trusts whom.
func TestDecisions(t *testing.T) {
	rows := []struct {
		name               string
		aTrustsB, bTrustsA bool
		aSeesB, bSeesA     preauth.State
	}{
		{"both trust", true, true, preauth.StateMember, preauth.StateMember},
		{"only a trusts b", true, false, preauth.StateMember, preauth.StateNonMemberNode},
		{"only b trusts a", false, true, preauth.StateNonMemberNode, preauth.StateMember},
		{"neither trusts", false, false, preauth.StateNonMemberNode, preauth.StateNonMemberNode},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			a, b := pair(t, false, r.aTrustsB, r.bTrustsA, nil)
			a.dialRaw(b)
			eventually(t, "both sides to decide", func() bool {
				return a.stateOf(b) != preauth.StateUnauthenticated && b.stateOf(a) != preauth.StateUnauthenticated &&
					a.stateOf(b) != preauth.StateUnset && b.stateOf(a) != preauth.StateUnset
			})
			if got := a.stateOf(b); got != r.aSeesB {
				t.Errorf("a judges b %v, want %v", got, r.aSeesB)
			}
			if got := b.stateOf(a); got != r.bSeesA {
				t.Errorf("b judges a %v, want %v", got, r.bSeesA)
			}
			if got := a.net.Connected(b.id); got != (r.aSeesB == preauth.StateMember) {
				t.Errorf("a.Connected(b) = %v", got)
			}
			// The engine is told of a certificate only when its connection is a member's.
			if got, want := len(b.node.noted()) == 1, r.bSeesA == preauth.StateMember; got != want {
				t.Errorf("b was told of a's certificate: %v, want %v", got, want)
			}
		})
	}
}

// TestMemberProtocolsWaitForTheExchange holds the exchange open on the target
// and shows that a member protocol is reset until it completes and served after.
func TestMemberProtocolsWaitForTheExchange(t *testing.T) {
	a, b := pair(t, false, true, true, nil)
	release := make(chan struct{})
	b.node.setBlock(release)

	e := a.dialRaw(b)
	eventually(t, "the target to hold the exchange", func() bool { return len(b.node.noted()) == 1 })

	callRaw := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s, err := openRaw(ctx, e.conn, nodeproto.ProtocolSend)
		if err != nil {
			return err
		}
		defer s.Reset()
		req, err := nodeproto.SendRequest{Sender: endpointOf(1), Envelope: []byte("x")}.Encode()
		if err != nil {
			t.Fatal(err)
		}
		// A reset can fail the write before the read reports it, and only the
		// read carries the peer's reset code.
		if _, err := s.Write(req); err == nil {
			s.CloseWrite()
		}
		_, err = nodeproto.ReadMessage(s, 1<<16)
		return err
	}
	if err := callRaw(); !errors.Is(err, gatedByPeer) {
		t.Fatalf("a send before the exchange completed ended with %v, want the peer's gate reset", err)
	}
	b.node.mu.Lock()
	served := len(b.node.origins)
	b.node.mu.Unlock()
	if served != 0 {
		t.Fatalf("the handler ran %d times before the exchange completed", served)
	}

	close(release)
	eventually(t, "the exchange to complete", func() bool { return b.stateOf(a) == preauth.StateMember && a.net.Connected(b.id) })
	if err := callRaw(); err != nil {
		t.Fatalf("a send after the exchange: %v", err)
	}
}

// TestNonMemberMayOnlyPair registers a pairing handler through the gate and
// shows that a connection judged a non-member reaches it and nothing else.
func TestNonMemberMayOnlyPair(t *testing.T) {
	c, s := pair(t, false, true, false, nil) // s does not trust c
	s.net.handle(string(preauth.ProtocolPairing), func(_ context.Context, st lnet.Stream, _ *connEntry) {
		st.Write([]byte("pairing"))
		st.Close()
	})
	e := c.dialRaw(s)
	eventually(t, "the target to decide", func() bool { return s.stateOf(c) == preauth.StateNonMemberNode })
	<-e.ready

	open := func(proto string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		st, err := openRaw(ctx, e.conn, proto)
		if err != nil {
			return nil, err
		}
		defer st.Reset()
		st.CloseWrite()
		return nodeproto.ReadMessage(st, 1<<10)
	}
	if got, err := open(string(preauth.ProtocolPairing)); err != nil || string(got) != "pairing" {
		t.Fatalf("pairing: %q, %v", got, err)
	}
	for _, proto := range []string{nodeproto.ProtocolSend, nodeproto.ProtocolPull, nodeproto.ProtocolDirectory} {
		if _, err := open(proto); !errors.Is(err, gatedByPeer) {
			t.Errorf("%s from a non-member ended with %v, want the peer's gate reset", proto, err)
		}
	}
}

// TestUnauthenticatedConnectionIsClosedAtTheDeadline holds a stranger's exchange
// open and moves the injected clock past the exchange deadline.
func TestUnauthenticatedConnectionIsClosedAtTheDeadline(t *testing.T) {
	a, b := pair(t, false, true, true, nil)
	b.node.setBlock(make(chan struct{})) // never released: the exchange stays open
	a.dialRaw(b)
	eventually(t, "the target to hold the exchange", func() bool { return len(b.node.noted()) == 1 })
	eventually(t, "the tick to be armed", func() bool { return b.clk.Timers() > 0 })

	b.clk.Advance(preauth.ExchangeDeadline - time.Second)
	time.Sleep(100 * time.Millisecond)
	if b.connCount() != 1 {
		t.Fatal("the connection was closed before its deadline")
	}
	b.clk.Advance(2 * time.Second)
	eventually(t, "the overdue connection to be closed", func() bool { return b.connCount() == 0 })
}

// TestOwnIdentityCertificateClosesTheConnection connects a second instance of
// the node, whose certificate names the same node under another network key.
func TestOwnIdentityCertificateClosesTheConnection(t *testing.T) {
	clk := newClock()
	a := newPeerNode(t, clk, 0x11, 0x91)
	twin := newPeerNode(t, clk, 0x11, 0x99) // the same identity, another network key
	a.start(false, nil)
	twin.start(false, nil)
	a.setMembership(1, twin)
	twin.setMembership(1, a)
	twin.knows(a)

	// Either side may close the connection as soon as the exchange shows it the
	// other's certificate, before the dial's caller could look the connection up.
	twin.dial(a)
	eventually(t, "the log to name the second instance", func() bool {
		return strings.Contains(a.log.String(), "another instance uses this node's identity")
	})
	eventually(t, "the connection to be closed", func() bool { return a.connCount() == 0 && twin.connCount() == 0 })
	if n := len(a.node.noted()); n != 0 {
		t.Fatalf("the engine was told of %d certificates of the node's own identity", n)
	}
}

// TestCallErrors is one row per way a call ends without a receipt.
func TestCallErrors(t *testing.T) {
	short := func(c *Config) { c.CallTimeout = 300 * time.Millisecond }
	t.Run("members", func(t *testing.T) {
		// Only the caller has a short budget: the timeout row must not race
		// the target's own deadline and the failure answer it can return.
		a, b := pairTuned(t, false, true, true, short, nil)
		send := func(ctx context.Context) error {
			_, err := a.net.Send(ctx, b.id, endpointOf(1), []byte("envelope"))
			return err
		}
		rows := []struct {
			name   string
			handle func(ctx context.Context) (transport.Receipt, error)
			ctx    func() (context.Context, context.CancelFunc)
			check  func(t *testing.T, err error)
		}{
			{"refused: the target refuses with a reason", func(context.Context) (transport.Receipt, error) {
				return transport.Receipt{}, errors.New("conflict")
			}, nil, func(t *testing.T, err error) {
				var refused transport.Refused
				if !errors.As(err, &refused) || refused.Reason != "message_conflict" {
					t.Fatalf("err = %v, want Refused{message_conflict}", err)
				}
			}},
			{"failed: the target cannot handle the call", func(context.Context) (transport.Receipt, error) {
				return transport.Receipt{}, errors.New("disk full")
			}, nil, func(t *testing.T, err error) {
				var refused transport.Refused
				if err == nil || errors.Is(err, transport.ErrTimeout) || errors.Is(err, transport.ErrUnreachable) || errors.As(err, &refused) {
					t.Fatalf("err = %v, want a plain error", err)
				}
			}},
			{"the caller's context ends first", func(ctx context.Context) (transport.Receipt, error) {
				<-ctx.Done()
				return transport.Receipt{}, ctx.Err()
			}, func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(100*time.Millisecond, cancel)
				return ctx, cancel
			}, func(t *testing.T, err error) {
				if !errors.Is(err, context.Canceled) || errors.Is(err, transport.ErrTimeout) {
					t.Fatalf("err = %v, want the caller's context error", err)
				}
			}},
			// Last: a call that times out closes its connection and the node is
			// remembered as unreachable, which the rows after it would see.
			{"timeout: the target does not answer in time", func(ctx context.Context) (transport.Receipt, error) {
				<-ctx.Done()
				return transport.Receipt{}, ctx.Err()
			}, nil, func(t *testing.T, err error) {
				if !errors.Is(err, transport.ErrTimeout) {
					t.Fatalf("err = %v, want ErrTimeout", err)
				}
			}},
		}
		b.node.mu.Lock()
		b.node.refusal = func(err error) (string, bool) {
			if err != nil && err.Error() == "conflict" {
				return "message_conflict", true
			}
			return "", false
		}
		b.node.mu.Unlock()
		for _, r := range rows {
			t.Run(r.name, func(t *testing.T) {
				b.node.mu.Lock()
				b.node.inbound = func(ctx context.Context, _ transport.Origin, _ ident.EndpointID, _ []byte) (transport.Receipt, error) {
					return r.handle(ctx)
				}
				b.node.mu.Unlock()
				ctx, cancel := ctx5(t), context.CancelFunc(func() {})
				if r.ctx != nil {
					ctx, cancel = r.ctx()
				}
				defer cancel()
				r.check(t, send(ctx))
			})
		}
	})

	t.Run("unreachable: the target is not at its address", func(t *testing.T) {
		a, b := pair(t, false, true, true, nil)
		if err := a.net.SetAddressBook(map[ident.NodeID][]string{b.id: {"/ip4/127.0.0.1/tcp/1/p2p/" + b.net.PeerID()}}); err != nil {
			t.Fatal(err)
		}
		_, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope"))
		if !errors.Is(err, transport.ErrUnreachable) {
			t.Fatalf("err = %v, want ErrUnreachable", err)
		}
		// The node is not tried again at once, and is when the memo has run out.
		_, err = a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope"))
		if !errors.Is(err, transport.ErrUnreachable) || !strings.Contains(err.Error(), "a moment ago") {
			t.Fatalf("second err = %v, want the memo's ErrUnreachable", err)
		}
		a.clk.Advance(DefaultUnreachableFor + time.Second)
		_, err = a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope"))
		if !errors.Is(err, transport.ErrUnreachable) || strings.Contains(err.Error(), "a moment ago") {
			t.Fatalf("third err = %v, want a fresh attempt", err)
		}
	})

	t.Run("unreachable: no address is known", func(t *testing.T) {
		a, _ := pair(t, false, true, true, nil)
		stranger := newPeerNode(t, a.clk, 0x13, 0x93)
		_, err := a.net.Send(ctx5(t), stranger.id, endpointOf(1), []byte("envelope"))
		if !errors.Is(err, transport.ErrUnreachable) {
			t.Fatalf("err = %v, want ErrUnreachable", err)
		}
	})

	t.Run("unreachable: the target does not judge the caller a member", func(t *testing.T) {
		a, b := pair(t, false, true, false, nil) // b does not trust a
		var called atomic.Bool
		b.node.inbound = func(context.Context, transport.Origin, ident.EndpointID, []byte) (transport.Receipt, error) {
			called.Store(true)
			return transport.Receipt{}, nil
		}
		_, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope"))
		if !errors.Is(err, transport.ErrUnreachable) || called.Load() {
			t.Fatalf("err = %v, handler called = %v; want ErrUnreachable and no call", err, called.Load())
		}
	})

	t.Run("unreachable: the caller does not judge the target a member", func(t *testing.T) {
		a, b := pair(t, false, false, true, nil) // a does not trust b
		_, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope"))
		if !errors.Is(err, transport.ErrUnreachable) {
			t.Fatalf("err = %v, want ErrUnreachable", err)
		}
	})
}

// TestNewSnapshotClosesWhatNoLongerKeeps revokes a member in a new snapshot and
// shows that its live connection is closed and a new one is not a member's.
func TestNewSnapshotClosesWhatNoLongerKeeps(t *testing.T) {
	a, b := pair(t, false, true, true, nil)
	if _, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope")); err != nil {
		t.Fatal(err)
	}
	if !a.net.Connected(b.id) {
		t.Fatal("no member connection after a call")
	}
	a.setMembership(1) // a trusts nobody now
	eventually(t, "the connection to be closed on both sides", func() bool { return a.connCount() == 0 && b.connCount() == 0 })
	if _, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope")); !errors.Is(err, transport.ErrUnreachable) {
		t.Fatalf("a call to a node that is no longer a member: %v, want ErrUnreachable", err)
	}
}

// TestNodeThatCameBackIsReachedWhenTheMemoRunsOut stops a node, fails a call to
// it, brings it back on the same address and shows that the first call after the
// memo has run out reaches it, though the library's own dial backoff for that
// address, which runs on the real clock, has not.
func TestNodeThatCameBackIsReachedWhenTheMemoRunsOut(t *testing.T) {
	a, b := pair(t, false, true, true, nil)
	addr := b.contact(true)
	listen := strings.TrimSuffix(addr, "/p2p/"+b.net.PeerID())
	b.net.Close()
	if _, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope")); !errors.Is(err, transport.ErrUnreachable) {
		t.Fatalf("a call to a node that is down: %v, want ErrUnreachable", err)
	}

	back, err := New(Config{Key: b.netKey, Clock: b.clk, Log: slog.New(slog.NewTextHandler(b.log, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { back.Close() })
	back.SetNode(b.node)
	if err := back.SetMembership(b.membership(1, false, a)); err != nil {
		t.Fatal(err)
	}
	if err := back.Listen([]string{listen}); err != nil {
		t.Fatal(err)
	}
	a.clk.Advance(DefaultUnreachableFor + time.Second)
	if _, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope")); err != nil {
		t.Fatalf("the first call after the memo ran out: %v", err)
	}
}

// TestPromotedNodeIsCalledOnANewConnection makes a node a member while a
// non-member connection to it is live and shows that the connection is closed,
// the memo of the failed call is gone, and the next call exchanges again and
// goes through.
func TestPromotedNodeIsCalledOnANewConnection(t *testing.T) {
	a, b := pair(t, false, false, true, nil) // a does not trust b yet
	if _, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope")); !errors.Is(err, transport.ErrUnreachable) {
		t.Fatalf("a call to a node that is not a member: %v, want ErrUnreachable", err)
	}
	a.setMembership(1, b)
	eventually(t, "the non-member connection to be closed on both sides", func() bool { return a.connCount() == 0 && b.connCount() == 0 })
	// The clock is not advanced: the memo of the failed call must not be what
	// answers now.
	if _, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope")); err != nil {
		t.Fatalf("a call to the node once it is a member: %v", err)
	}
}

// TestCallThatTimesOutDropsItsConnection shows that a connection a call timed
// out on is closed, so that the next call is answered unreachable at once
// instead of waiting on the same connection.
//
// The peer keeps its default call timeout, far beyond the caller's, so nothing
// comes back on the stream before the caller's own deadline, as with a peer that
// is gone or stuck. With equal timeouts the peer's handler gives up a moment
// after the caller and resets the stream; under load that reset can reach the
// caller before its own deadline is seen, and a reset is the answer of a live
// connection, which is rightly kept.
func TestCallThatTimesOutDropsItsConnection(t *testing.T) {
	a, b := pairTuned(t, false, true, true, func(c *Config) { c.CallTimeout = 300 * time.Millisecond }, nil)
	b.node.inbound = func(ctx context.Context, _ transport.Origin, _ ident.EndpointID, _ []byte) (transport.Receipt, error) {
		<-ctx.Done()
		return transport.Receipt{}, ctx.Err()
	}
	if _, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope")); !errors.Is(err, transport.ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	eventually(t, "the connection to be closed", func() bool { return a.connCount() == 0 })
	begin := time.Now()
	_, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope"))
	if !errors.Is(err, transport.ErrUnreachable) || time.Since(begin) > 200*time.Millisecond {
		t.Fatalf("second err = %v after %v, want ErrUnreachable at once", err, time.Since(begin))
	}
}

// fakeConn is a key of the connection table that nothing calls.
type fakeConn struct {
	lnet.Conn
	id uint64
}

// TestMemberConnPrefersTheNewest picks among two member connections to a node.
func TestMemberConnPrefersTheNewest(t *testing.T) {
	n := &Network{conns: map[lnet.Conn]*connEntry{}}
	node := newPeerNode(t, newClock(), 0x11, 0x91).id
	add := func(seq uint64, closeIdle bool) *connEntry {
		e := &connEntry{conn: &fakeConn{id: seq}, state: preauth.StateMember, node: node, seq: seq, closeIdle: closeIdle}
		n.conns[e.conn] = e
		return e
	}
	older, newer, closing := add(1, false), add(2, false), add(3, true)
	if got := n.memberConn(node); got != newer {
		t.Fatalf("got connection %d, want the newest that is not closing (2)", got.seq)
	}
	delete(n.conns, newer.conn)
	if got := n.memberConn(node); got != older {
		t.Fatalf("got connection %d, want the older one that is not closing (1)", got.seq)
	}
	delete(n.conns, older.conn)
	if got := n.memberConn(node); got != closing {
		t.Fatal("a closing connection is still better than none")
	}
}

// TestRenewedCertificateIsExchangedOnANewConnection changes the serial of the
// node's own certificate and shows that the connection is closed when idle and
// the next call presents the new certificate.
func TestRenewedCertificateIsExchangedOnANewConnection(t *testing.T) {
	a, b := pair(t, false, true, true, nil)
	if _, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope")); err != nil {
		t.Fatal(err)
	}
	if got := b.node.noted(); len(got) != 1 || got[0].Serial != 1 {
		t.Fatalf("b was told %+v, want serial 1", got)
	}
	a.clk.Advance(time.Hour)
	a.setMembership(2, b)
	eventually(t, "the idle connection to be closed", func() bool { return a.connCount() == 0 && b.connCount() == 0 })
	if _, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope")); err != nil {
		t.Fatal(err)
	}
	if got := b.node.noted(); len(got) != 2 || got[1].Serial != 2 {
		t.Fatalf("b was told %+v, want serial 2 second", got)
	}
}

// TestDialByHeldCertificate dials a node whose address has no /p2p/ part: the
// peer comes from the held certificate.
func TestDialByHeldCertificate(t *testing.T) {
	clk := newClock()
	a := newPeerNode(t, clk, 0x11, 0x91)
	b := newPeerNode(t, clk, 0x12, 0x92)
	a.start(false, nil)
	b.start(false, nil)
	if err := a.net.SetMembership(a.membership(1, true, b)); err != nil {
		t.Fatal(err)
	}
	b.setMembership(1, a)
	bare := strings.TrimSuffix(b.contact(true), "/p2p/"+b.net.PeerID())
	if err := a.net.SetAddressBook(map[ident.NodeID][]string{b.id: {bare}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope")); err != nil {
		t.Fatal(err)
	}
}

// TestCloseLeavesNoGoroutine closes two nodes that have talked and waits for the
// goroutine count to fall back to what it was before they started.
func TestCloseLeavesNoGoroutine(t *testing.T) {
	run := func() {
		a, b := pair(t, true, true, true, nil)
		if _, err := a.net.Send(ctx5(t), b.id, endpointOf(1), []byte("envelope")); err != nil {
			t.Fatal(err)
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

type routerRemote struct{ calls []string }

func (r *routerRemote) Send(context.Context, ident.NodeID, ident.EndpointID, []byte) (transport.Receipt, error) {
	r.calls = append(r.calls, "send")
	return transport.Receipt{}, nil
}

func (r *routerRemote) Pull(context.Context, ident.NodeID, transport.PullRequest) (transport.PullAnswer, error) {
	r.calls = append(r.calls, "pull")
	return transport.PullAnswer{}, nil
}

func (r *routerRemote) QueryDirectory(context.Context, ident.NodeID, ident.EndpointID, transport.DirectoryRequest) (transport.DirectoryPage, error) {
	r.calls = append(r.calls, "directory")
	return transport.DirectoryPage{}, nil
}

// TestRouter sends the local node's calls to the loopback, which makes a local
// origin, and every other node's to the remote client.
func TestRouter(t *testing.T) {
	clk := newClock()
	self := newPeerNode(t, clk, 0x11, 0x91)
	other := newPeerNode(t, clk, 0x12, 0x92)
	handler := &fakeNode{}
	remote := &routerRemote{}
	r := NewRouter(self.id, transport.NewLoopback(self.id, handler, nil), remote)
	ctx := context.Background()

	r.Send(ctx, self.id, endpointOf(1), []byte("x"))
	r.Pull(ctx, self.id, transport.PullRequest{})
	r.QueryDirectory(ctx, self.id, endpointOf(1), transport.DirectoryRequest{})
	if len(remote.calls) != 0 || len(handler.origins) != 3 {
		t.Fatalf("local calls: remote %v, handler saw %d", remote.calls, len(handler.origins))
	}
	for _, o := range handler.origins {
		if !o.Local() {
			t.Fatal("a call to this node did not carry a local origin")
		}
	}
	r.Send(ctx, other.id, endpointOf(1), []byte("x"))
	r.Pull(ctx, other.id, transport.PullRequest{})
	r.QueryDirectory(ctx, other.id, endpointOf(1), transport.DirectoryRequest{})
	if got := strings.Join(remote.calls, ","); got != "send,pull,directory" || len(handler.origins) != 3 {
		t.Fatalf("remote calls = %q, handler saw %d", got, len(handler.origins))
	}
}

// TestCheckAddresses is the rule for the addresses entered for a node: without a
// held certificate one of them names the peer; with one, every /p2p/ is the peer
// of the certificate's key and a bare address is fine. The error names the path
// of the offending address.
func TestCheckAddresses(t *testing.T) {
	clk := newClock()
	a := newPeerNode(t, clk, 0x11, 0x91)
	b := newPeerNode(t, clk, 0x12, 0x92)
	c := newPeerNode(t, clk, 0x13, 0x93)
	a.start(false, nil)
	b.start(true, nil)
	c.start(true, nil)
	const bare = "/ip4/10.0.0.2/tcp/7744"
	named := func(n *peerNode) string { return bare + "/p2p/" + n.net.PeerID() }

	cases := []struct {
		name      string
		held      bool
		addrs     []string
		wantField string // empty: accepted
	}{
		{"not held, one names the peer", false, []string{bare, named(b)}, ""},
		{"not held, none names it", false, []string{bare}, "addresses"},
		{"not held, nothing", false, nil, "addresses"},
		{"not held, a bad address", false, []string{"nonsense"}, "addresses[0]"},
		{"held, bare", true, []string{bare}, ""},
		{"held, the certificate's peer", true, []string{named(b), bare}, ""},
		{"held, another peer", true, []string{bare, named(c)}, "addresses[1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := a.membership(1, tc.held, b)
			if err := a.net.SetMembership(m); err != nil {
				t.Fatal(err)
			}
			err := a.net.CheckAddresses(b.id, tc.addrs)
			var fe *config.FieldError
			switch {
			case tc.wantField == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.wantField != "" && (!errors.As(err, &fe) || fe.Field != tc.wantField):
				t.Fatalf("error %v, want a field error at %q", err, tc.wantField)
			}
		})
	}
}

// TestContactAddrsListNoLoopbackAndEndInThePeer: a node listening on loopback has
// no address to give, and one listening on every interface gives the addresses of
// the machine's interfaces, each ending in its peer identifier.
func TestContactAddrsListNoLoopbackAndEndInThePeer(t *testing.T) {
	clk := newClock()
	n := newPeerNode(t, clk, 0x11, 0x91)
	n.start(true, nil)
	if got := n.net.ContactAddrs(); len(got) != 0 {
		t.Fatalf("a node on loopback gives %v", got)
	}
	if err := n.net.Listen([]string{"/ip4/0.0.0.0/tcp/0"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range n.net.ContactAddrs() {
		if strings.HasPrefix(a, "/ip4/127.") || strings.HasPrefix(a, "/ip4/169.254.") || strings.HasPrefix(a, "/ip4/0.0.0.0") || !strings.HasSuffix(a, "/p2p/"+n.net.PeerID()) {
			t.Errorf("contact address %s", a)
		}
	}
}
