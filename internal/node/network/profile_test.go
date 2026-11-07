package network

import (
	"context"
	"errors"
	"testing"

	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/nodeproto"
	"github.com/aianyai/sidera/internal/preauth"
	"github.com/aianyai/sidera/internal/record"
)

var errRefused = errors.New("refused")

// connectedPair is two members of each other with a connection up between them.
func connectedPair(t *testing.T, quic bool) (a, b *peerNode) {
	t.Helper()
	a, b = pairOver(t, quic)
	a.dialRaw(b)
	eventually(t, "the exchange to complete", func() bool {
		return b.stateOf(a) == preauth.StateMember && a.net.Connected(b.id) && b.net.Connected(a.id)
	})
	return a, b
}

// TestFetchProfile asks a member for its profile over both transports: the record
// comes back as the target's engine gave it, and the engine was told who asked.
func TestFetchProfile(t *testing.T) {
	for _, quic := range []bool{false, true} {
		name := map[bool]string{false: "tcp", true: "quic"}[quic]
		t.Run(name, func(t *testing.T) {
			a, b := connectedPair(t, quic)
			want := testRecord(t, b)
			b.node.setProfile(func(_ context.Context, from transport.Origin) (r record.Record, err error) {
				if from.Local() || from.Node() != a.id {
					t.Errorf("the engine was told the request came from %v (local %v)", from.Node(), from.Local())
				}
				return want, nil
			})
			got, served, err := a.net.FetchProfile(ctx5(t), b.id)
			if err != nil || !served || !sameRecord(got, want) {
				t.Fatalf("FetchProfile = %v, served %v, %v", got, served, err)
			}
		})
	}
}

// A refusal names its reason, as the answers of the other protocols do, and a
// failure says nothing more.
func TestFetchProfileMapsTheTargetsRefusalAndFailure(t *testing.T) {
	a, b := connectedPair(t, false)
	b.node.setRefusal(func(err error) (string, bool) { return "not_permitted", errors.Is(err, errRefused) })
	b.node.setProfile(func(context.Context, transport.Origin) (record.Record, error) { return record.Record{}, errRefused })
	_, _, err := a.net.FetchProfile(ctx5(t), b.id)
	if got := (transport.Refused{}); !errors.As(err, &got) || got.Reason != "not_permitted" {
		t.Fatalf("a refusal = %v", err)
	}
	b.node.setProfile(func(context.Context, transport.Origin) (record.Record, error) {
		return record.Record{}, errors.New("boom")
	})
	if _, served, err := a.net.FetchProfile(ctx5(t), b.id); err == nil || errors.As(err, new(transport.Refused)) || served {
		t.Fatalf("a failure = %v, served %v", err, served)
	}
}

// A node that does not serve the protocol, as a node of an earlier release does
// not, is not an error and does not cost the connection.
func TestFetchProfileFromANodeThatDoesNotServeIt(t *testing.T) {
	a, b := connectedPair(t, false)
	b.net.lib.host.RemoveStreamHandler(protocol.ID(nodeproto.ProtocolProfile))
	for i := 0; i < 2; i++ {
		if _, served, err := a.net.FetchProfile(ctx5(t), b.id); err != nil || served {
			t.Fatalf("call %d: served %v, %v", i, served, err)
		}
	}
	if a.net.memberConn(b.id) == nil {
		t.Error("the connection was dropped")
	}
}

// Fetching dials nothing: with no member connection up the call was not seen.
func TestFetchProfileWithoutAMemberConnection(t *testing.T) {
	a, b := pairOver(t, false)
	if _, _, err := a.net.FetchProfile(ctx5(t), b.id); !errors.Is(err, transport.ErrUnreachable) {
		t.Fatalf("FetchProfile = %v, want ErrUnreachable", err)
	}
}

// The engine is told when a connection becomes a member's, at both ends, and not
// when the peer is no member.
func TestTheEngineIsToldOfEveryMemberConnection(t *testing.T) {
	a, b := connectedPair(t, false)
	eventually(t, "both engines told", func() bool {
		return len(a.node.memberConnections()) > 0 && len(b.node.memberConnections()) > 0
	})
	if got := a.node.memberConnections()[0]; got != b.id {
		t.Errorf("a was told of %v, want b", got)
	}
	if got := b.node.memberConnections()[0]; got != a.id {
		t.Errorf("b was told of %v, want a", got)
	}
}

func TestTheEngineIsNotToldOfANonMember(t *testing.T) {
	a, b := pair(t, false, true, false, nil)
	a.dialRaw(b)
	eventually(t, "both exchanges to complete", func() bool {
		return a.stateOf(b) == preauth.StateMember && b.stateOf(a) == preauth.StateNonMemberNode
	})
	eventually(t, "a told of b", func() bool { return len(a.node.memberConnections()) > 0 })
	if got := b.node.memberConnections(); len(got) != 0 {
		t.Errorf("b was told of %v, a node it does not trust", got)
	}
}

// The node that read the exchange first is told of the member connection before
// the dialer has applied its side of it, and a profile it asks for at once is reset
// by the dialer's gate. The failure is an unreachable node, not a refusal and not a
// node that does not serve the protocol, so the engine can tell it is worth asking
// again; the same fetch after the dialer has applied the exchange is answered.
func TestFetchProfileBeforeThePeerHasAppliedTheExchange(t *testing.T) {
	a, b := pairOver(t, false)
	release := make(chan struct{})
	a.node.setBlock(release) // a holds its side of the exchange open
	a.dialRaw(b)
	eventually(t, "b to be told of the member connection", func() bool { return len(b.node.memberConnections()) == 1 })
	want := testRecord(t, a)
	a.node.setProfile(func(context.Context, transport.Origin) (record.Record, error) { return want, nil })

	_, served, err := b.net.FetchProfile(ctx5(t), a.id)
	if served || !errors.Is(err, transport.ErrUnreachable) || errors.As(err, new(transport.Refused)) {
		t.Fatalf("a fetch before the peer applied the exchange: served %v, %v; want an unreachable error", served, err)
	}

	close(release)
	eventually(t, "a to apply the exchange", func() bool { return a.net.Connected(b.id) })
	if got, served, err := b.net.FetchProfile(ctx5(t), a.id); err != nil || !served || !sameRecord(got, want) {
		t.Fatalf("a fetch after the exchange: %v, served %v, %v", got, served, err)
	}
}
