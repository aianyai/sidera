package engine_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/pairing"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
)

// fakePairingStream notes what the engine does to a stream, in order.
type fakePairingStream struct {
	mu  sync.Mutex
	log []string
}

func (s *fakePairingStream) Send(frame []byte) { s.note("send " + string(frame)) }
func (s *fakePairingStream) Close()            { s.note("close") }

func (s *fakePairingStream) note(what string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, what)
}

func (s *fakePairingStream) done() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.log)
}

// The persists of a list of actions come first and in order, then the rest, and a
// list that has a persist after something that reaches outside is refused whole:
// nothing is written and nothing is sent.
func TestPairingActionsArePerformedInOrderOrNotAtAll(t *testing.T) {
	limits, err := pairing.NewLimits([]time.Time{t0}, nil)
	if err != nil {
		t.Fatal(err)
	}
	expectations := []pairing.Expectation{{Node: mustNode(t, 9), Author: pairing.LocalAuthor(), Operation: admin.OperationID{1}, CreatedAt: t0}}
	for _, tc := range []struct {
		name       string
		acts       func(sid pairing.SessionID) []pairing.Action
		refused    bool
		wantLimits bool
		wantExpect bool
		wantStream []string
	}{
		{"persists then the rest", func(sid pairing.SessionID) []pairing.Action {
			return []pairing.Action{
				pairing.PersistLimits{Limits: limits}, pairing.PersistExpectations{Expectations: expectations},
				pairing.Send{Session: sid, Frame: []byte("a")}, pairing.Send{Session: sid, Frame: []byte("b")},
				pairing.Notify{}, pairing.Close{Session: sid},
			}
		}, false, true, true, []string{"send a", "send b", "close"}},
		{"only what reaches outside", func(sid pairing.SessionID) []pairing.Action {
			return []pairing.Action{pairing.Send{Session: sid, Frame: []byte("a")}, pairing.Close{Session: sid}}
		}, false, false, false, []string{"send a", "close"}},
		{"only persists", func(pairing.SessionID) []pairing.Action {
			return []pairing.Action{pairing.PersistLimits{Limits: limits}}
		}, false, true, false, nil},
		{"a persist after a send", func(sid pairing.SessionID) []pairing.Action {
			return []pairing.Action{pairing.Send{Session: sid, Frame: []byte("a")}, pairing.PersistLimits{Limits: limits}}
		}, true, false, false, nil},
		{"a persist between a persist and a close", func(sid pairing.SessionID) []pairing.Action {
			return []pairing.Action{
				pairing.PersistLimits{Limits: limits}, pairing.Close{Session: sid}, pairing.PersistExpectations{Expectations: expectations},
			}
		}, true, false, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, 10, withKeys())
			e := r.start()
			stream := &fakePairingStream{}
			var performErr error
			err := engine.InLoop(context.Background(), e, func(l engine.LoopSide) {
				performErr = l.PerformPairingActions(tc.acts(l.PairingSession(stream)))
			})
			if err != nil {
				t.Fatal(err)
			}
			if (performErr != nil) != tc.refused {
				t.Fatalf("PerformPairingActions: %v, refused %v", performErr, tc.refused)
			}
			sc, err := r.st.Scan()
			if err != nil {
				t.Fatal(err)
			}
			if got := len(sc.PairingLimits.Reveals()) > 0; got != tc.wantLimits {
				t.Errorf("limits stored: %v, want %v", got, tc.wantLimits)
			}
			if got := len(sc.PairingExpectations) > 0; got != tc.wantExpect {
				t.Errorf("expectations stored: %v, want %v", got, tc.wantExpect)
			}
			if got := stream.done(); !slices.Equal(got, tc.wantStream) {
				t.Errorf("the stream saw %q, want %q", got, tc.wantStream)
			}
		})
	}
}

// partyOf is the end of a pairing that the certificate and the key of a connection
// make.
func partyOf(t *testing.T, cert record.Verified[record.NetworkKeyCertificate], key ident.PublicKey) pairing.Party {
	t.Helper()
	serial := cert.Payload().Serial
	p, err := pairing.NewParty(cert, revocation.NewSet().NetworkKey(cert, serial).Revocation, serial, key, t0)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A result fetch the state machine asks for while the node starts is held until the
// second recovery entry has ended, and only then handed to the network.
func TestPairingFetchIsHeldUntilTheNodeHasStarted(t *testing.T) {
	net := &fakeNetwork{}
	r := newRig(t, 10, withKeys(), withNetwork(net))
	peer := newPeerNode(t, 3)
	remote := peer.certificate(1)

	// The initiator's request is confirmed, signed and released: what it waits for
	// is the target's answer, which the first tick asks for.
	terms := pairing.Terms{
		Protocol:  "/sidera/pairing/test",
		Initiator: partyOf(t, r.keys.Certificate(), r.keys.NetworkSigner().Public()),
		Target:    partyOf(t, remote.Cert, peer.networkSigner(1).Public()),
		CreatedAt: t0, NotAfter: t0.Add(24 * time.Hour),
	}
	nA, nB := pairing.Nonce{1}, pairing.Nonce{2}
	commitment, err := terms.Commit(nA)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := pairing.Open(terms, commitment, nB, nA)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := record.Seal(credential.Descriptor, transcript.Credential(), r.keys.IdentitySigner())
	if err != nil {
		t.Fatal(err)
	}
	rec := pairing.InitiatorRecord{
		Transcript: transcript, ReceivedAt: t0, ExpiresAt: t0.Add(5 * time.Minute), Status: pairing.InitiatorActive,
		Confirmation: &pairing.Confirmation{Method: pairing.ConfirmedByCode, By: pairing.LocalAuthor(), Operation: admin.OperationID{1}, At: t0},
		Signature:    &signed, Released: true,
	}
	if err := r.st.PairingInitiatorPut(rec); err != nil {
		t.Fatal(err)
	}

	e, _, err := r.open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	r.eng = e
	t.Cleanup(func() { r.stop(e) })
	e.Start()
	settle(t, e)
	if got := net.fetched(); len(got) != 0 {
		t.Fatalf("the network was asked for %d fetches before the node had started", len(got))
	}
	if err := e.RecoverDelivery(context.Background()); err != nil {
		t.Fatalf("RecoverDelivery: %v", err)
	}
	settle(t, e)
	if got := net.fetched(); len(got) != 1 || got[0] != peer.node {
		t.Fatalf("fetches after the start: %v, want one, to %s", got, peer.node)
	}
}
