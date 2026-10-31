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
	nodekeys "github.com/aianyai/sidera/internal/node/keys"
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

// The persists of a list of actions are made durable in order before anything of the
// list reaches outside, and what reaches outside is then done in the order of the
// list, wherever the persists stood among it: the state machine's lists interleave
// them (a tick that ends two requests gives a persist and a notice for each, a
// recovery a credential, a record, a notice and a revocation), and a list of that
// shape is performed and does not wedge the node. Only a list with an action the
// engine does not know is refused whole.
func TestPairingActionsAreStoredFirstAndThenSentInOrder(t *testing.T) {
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
		}, false, true, false, []string{"send a"}},
		// The shape of a tick that ends two requests: a persist and a notice for each.
		{"a persist, a notice, a persist and a notice", func(sid pairing.SessionID) []pairing.Action {
			return []pairing.Action{
				pairing.PersistLimits{Limits: limits}, pairing.Notify{}, pairing.PersistExpectations{Expectations: expectations}, pairing.Notify{},
				pairing.Send{Session: sid, Frame: []byte("a")},
			}
		}, false, true, true, []string{"send a"}},
		// The shape of a recovery: persists, a notice, and a persist that follows it.
		{"persists, a notice and a persist after it", func(sid pairing.SessionID) []pairing.Action {
			return []pairing.Action{
				pairing.PersistLimits{Limits: limits}, pairing.Notify{}, pairing.Close{Session: sid}, pairing.PersistExpectations{Expectations: expectations},
			}
		}, false, true, true, []string{"close"}},
		{"an action the engine does not know", func(sid pairing.SessionID) []pairing.Action {
			return []pairing.Action{pairing.PersistLimits{Limits: limits}, pairing.Send{Session: sid, Frame: []byte("a")}, nil}
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

// While the node may not sign (its clock is paused) it starts no pairing and takes no
// incoming stream, with the refusal the rest of the engine gives for it; once the
// owner clears the pause both go on.
func TestPairingIsRefusedWhileSigningIsRefused(t *testing.T) {
	net := &fakeNetwork{}
	r := trustRig(t, withNetwork(net))
	if err := r.keys.SaveClock(nodekeys.ClockState{HighWater: t0, RollbackPausedSince: t0}); err != nil {
		t.Fatal(err)
	}
	r.paused = true
	e := r.start()
	ctx := context.Background()
	peer := newPeerNode(t, 3)

	_, err := e.Execute(ctx, requestCommand(0x91, peer.node))
	wantRefusal(t, err, engine.ReasonClockRollback, "")
	stream := &fakePairingStream{}
	_, err = e.PairingOpened(ctx, engine.PairingPeer{}, stream)
	wantRefusal(t, err, engine.ReasonClockRollback, "")

	if _, err := e.Execute(ctx, clearCommand(0x92, "clear")); err != nil {
		t.Fatal(err)
	}
	_, err = e.Execute(ctx, requestCommand(0x93, peer.node))
	wantRefusal(t, err, engine.ReasonNodeUnreachable, "") // asked for the node, which the fake network cannot reach
	if _, err := e.PairingOpened(ctx, engine.PairingPeer{}, stream); err != nil {
		t.Errorf("PairingOpened after the pause was cleared: %v", err)
	}
}

// transcriptBetween is the transcript of a request between the node of the rig and
// the peer, with the node as the initiator or as the target. salt makes the identifier
// of one request differ from another's.
func transcriptBetween(t *testing.T, r *rig, peer peerNode, nodeInitiates bool, salt byte) pairing.Transcript {
	t.Helper()
	own := partyOf(t, r.keys.Certificate(), r.keys.NetworkSigner().Public())
	other := partyOf(t, peer.certificate(1).Cert, peer.networkSigner(1).Public())
	terms := pairing.Terms{Protocol: "/sidera/pairing/test", Initiator: own, Target: other, CreatedAt: t0, NotAfter: t0.Add(24 * time.Hour)}
	if !nodeInitiates {
		terms.Initiator, terms.Target = other, own
	}
	nA, nB := pairing.Nonce{salt}, pairing.Nonce{salt + 1}
	commitment, err := terms.Commit(nA)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := pairing.Open(terms, commitment, nB, nA)
	if err != nil {
		t.Fatal(err)
	}
	return transcript
}

// waitingInitiatorRecord is a request the node started that waits for its owner.
func waitingInitiatorRecord(t *testing.T, r *rig, peer peerNode, salt byte) pairing.InitiatorRecord {
	t.Helper()
	return pairing.InitiatorRecord{
		Transcript: transcriptBetween(t, r, peer, true, salt), ReceivedAt: t0, ExpiresAt: t0.Add(5 * time.Minute), Status: pairing.InitiatorActive,
	}
}

// signedInitiatorRecord is a request the node started that its owner confirmed: the
// node signed it and released the signature, so it waits for the target's answer.
func signedInitiatorRecord(t *testing.T, r *rig, peer peerNode) pairing.InitiatorRecord {
	t.Helper()
	rec := waitingInitiatorRecord(t, r, peer, 1)
	signed, err := record.Seal(credential.Descriptor, rec.Transcript.Credential(), r.keys.IdentitySigner())
	if err != nil {
		t.Fatal(err)
	}
	rec.Confirmation = &pairing.Confirmation{Method: pairing.ConfirmedByCode, By: pairing.LocalAuthor(), Operation: admin.OperationID{1}, At: t0}
	rec.Signature, rec.Released = &signed, true
	return rec
}

// pendingTargetRecord is a request the peer made of the node that waits for its owner.
func pendingTargetRecord(t *testing.T, r *rig, peer peerNode, salt byte) pairing.TargetRecord {
	t.Helper()
	return pairing.TargetRecord{
		Transcript: transcriptBetween(t, r, peer, false, salt), ReceivedAt: t0, ExpiresAt: t0.Add(5 * time.Minute), Status: pairing.TargetPending,
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

	// The initiator's request is confirmed, signed and released: what it waits for
	// is the target's answer, which the first tick asks for.
	rec := signedInitiatorRecord(t, r, peer)
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
