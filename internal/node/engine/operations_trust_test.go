package engine_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/pairing"
	"github.com/aianyai/sidera/internal/revocation"
	"github.com/aianyai/sidera/internal/trust"
)

// The owner's operations on trust are carried out through the one door, so their
// results are stored with their effects, and what is not an execution (a refusal)
// stores nothing.

func encodeExpected(n engine.NodeExpected) ([]byte, error) {
	return []byte(`{"node":"` + n.Node.String() + `","expires_at":"` + n.ExpiresAt.UTC().Format(time.RFC3339) + `"}`), nil
}

func expectCommand(b byte, node ident.NodeID) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b), Fingerprint: fingerprintOf("expect" + node.String()), Name: "trust.expect",
		Operation: engine.ExpectNodeOp(node, encodeExpected),
	}
}

func forgetCommand(b byte, node ident.NodeID) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b), Fingerprint: fingerprintOf("forget" + node.String()), Name: "trust.forget",
		Operation: engine.ForgetNodeOp(node, func(n ident.NodeID) ([]byte, error) {
			return []byte(`{"node":"` + n.String() + `","forgotten":true}`), nil
		}),
	}
}

func encodeCleared(c engine.NodeCleared) ([]byte, error) {
	if c.NewPairingRequired {
		return []byte(`{"node":"` + c.Node.String() + `","new_pairing_required":true}`), nil
	}
	return []byte(`{"node":"` + c.Node.String() + `","new_pairing_required":false}`), nil
}

func clearCommandOf(b byte, node ident.NodeID) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b), Fingerprint: fingerprintOf("clear" + node.String()), Name: "trust.clear",
		Operation: engine.ClearNodeOp(node, encodeCleared),
	}
}

func removeCommand(b byte, node ident.NodeID) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b), Fingerprint: fingerprintOf("remove" + node.String()), Name: "trust.remove",
		Operation: engine.RemoveNodeOp(node, func(r engine.NodeRemoved) ([]byte, error) { return []byte(`{}`), nil }),
	}
}

func wantRefusal(t *testing.T, err error, reason engine.Reason, field string) {
	t.Helper()
	var ref *engine.Refusal
	if !errors.As(err, &ref) || ref.Reason != reason || ref.Field != field {
		t.Fatalf("error %v, want a refusal for %s (field %q)", err, reason, field)
	}
}

// A command on expectations stores its result with the list; a command the pairing
// does not execute is a refusal that stores nothing, so its identifier stays free;
// clearing a node that was removed ends the removal and says whether the node needs a
// new pairing, and clearing one that was not stores the result alone.
func TestTrustOperationsStoreTheirResultsAndRefusalsStoreNothing(t *testing.T) {
	r := trustRig(t)
	peer := newPeerNode(t, 1)
	e := r.start()
	exec := func(c engine.ManagementCommand) (engine.StoredResult, error) {
		return e.Execute(context.Background(), c)
	}

	_, err := exec(expectCommand(0x61, r.node))
	wantRefusal(t, err, engine.ReasonInvalidArgument, "node")
	_, err = exec(forgetCommand(0x62, peer.node))
	wantRefusal(t, err, engine.ReasonInvalidArgument, "node")
	if sc := r.scan(); hasResult(sc, requestKey(0x61)) || hasResult(sc, requestKey(0x62)) {
		t.Error("a refused command stored a result")
	}

	want := `{"node":"` + peer.node.String() + `","expires_at":"` + t0.Add(pairing.DefaultParams().ExpectationTTL).UTC().Format(time.RFC3339) + `"}`
	if res, err := exec(expectCommand(0x63, peer.node)); err != nil || string(res.Result) != want {
		t.Fatalf("expect: %s, %v; want %s", res.Result, err, want)
	}
	if sc := r.scan(); len(sc.PairingExpectations) != 1 || !hasResult(sc, requestKey(0x63)) {
		t.Errorf("expect left %d expectations and result %v", len(sc.PairingExpectations), hasResult(sc, requestKey(0x63)))
	}
	if res, err := exec(expectCommand(0x63, peer.node)); err != nil || !res.Replayed || string(res.Result) != want {
		t.Errorf("the same request again: %+v, %v; want the stored result", res, err)
	}
	if _, err := exec(forgetCommand(0x64, peer.node)); err != nil {
		t.Fatal(err)
	}
	if sc := r.scan(); len(sc.PairingExpectations) != 0 || !hasResult(sc, requestKey(0x64)) {
		t.Errorf("forget left %d expectations and result %v", len(sc.PairingExpectations), hasResult(sc, requestKey(0x64)))
	}

	// A node that was removed with a credential is taken back, and needs a new pairing.
	cred := peer.credentialWith(r.keys.IdentitySigner(), false)
	inLoop(t, e, func(l engine.LoopSide) {
		if err := l.InstallCredential(cred.Record, nil); err != nil {
			t.Error(err)
		}
	})
	if _, err := exec(removeCommand(0x65, peer.node)); err != nil {
		t.Fatal(err)
	}
	wantCleared := func(b byte, node ident.NodeID, required bool) {
		t.Helper()
		res, err := exec(clearCommandOf(b, node))
		want, _ := encodeCleared(engine.NodeCleared{Node: node, NewPairingRequired: required})
		if err != nil || string(res.Result) != string(want) {
			t.Fatalf("clear %s: %s, %v; want %s", node, res.Result, err, want)
		}
	}
	wantCleared(0x66, peer.node, true)
	if sc := r.scan(); len(sc.LocalRevoked) != 0 || !hasResult(sc, requestKey(0x66)) || len(sc.CredRevocations) != 1 {
		t.Errorf("clear left %d entries, %d revocations and result %v", len(sc.LocalRevoked), len(sc.CredRevocations), hasResult(sc, requestKey(0x66)))
	}
	wantCleared(0x67, peer.node, false) // not removed any more: nothing to clear
	if !hasResult(r.scan(), requestKey(0x67)) || r.commits("trust-result") != 1 {
		t.Errorf("clearing a node that was not removed: result %v, %d result-only commits, want one", hasResult(r.scan(), requestKey(0x67)), r.commits("trust-result"))
	}
}

// scriptedStream is the engine's end of a pairing stream of a test: the frames the
// engine sends, in order.
type scriptedStream struct {
	frames chan []byte
	once   sync.Once
	closed chan struct{}
}

func newScriptedStream() *scriptedStream {
	return &scriptedStream{frames: make(chan []byte, 8), closed: make(chan struct{})}
}

func (s *scriptedStream) Send(frame []byte) { s.frames <- slices.Clone(frame) }
func (s *scriptedStream) Close()            { s.once.Do(func() { close(s.closed) }) }

func (s *scriptedStream) next(t *testing.T) []byte {
	t.Helper()
	select {
	case f := <-s.frames:
		return f
	case <-time.After(10 * time.Second):
		t.Fatal("the engine sent no frame")
		return nil
	}
}

// requestScene is a node asked by the node under test, played by the pairing
// package's own target, over a network that dials it.
type requestScene struct {
	t      *testing.T
	r      *rig
	e      *engine.Engine
	net    *fakeNetwork
	peer   peerNode
	stream *scriptedStream
	sid    chan pairing.SessionID

	mu    sync.Mutex
	dials int

	world  pairing.World
	target pairing.State
	conn   pairing.Conn
}

func newRequestScene(t *testing.T, opts ...rigOption) *requestScene {
	t.Helper()
	s := &requestScene{t: t, net: &fakeNetwork{}, peer: newPeerNode(t, 3), stream: newScriptedStream(), sid: make(chan pairing.SessionID, 1)}
	s.r = trustRig(t, append(opts, withNetwork(s.net))...)
	remote := s.peer.certificate(1)
	initiator := partyOf(t, s.r.keys.Certificate(), s.r.keys.NetworkSigner().Public())
	target := partyOf(t, remote.Cert, s.peer.networkSigner(1).Public())
	const protocol = "/sidera/pairing/test"
	s.net.dial = func(_ context.Context, _ ident.NodeID, _ []string, sid pairing.SessionID) (engine.PairingPeer, engine.PairingStream, error) {
		s.mu.Lock()
		s.dials++
		s.mu.Unlock()
		s.sid <- sid
		return engine.PairingPeer{
			Conn: pairing.Conn{Local: initiator, Remote: target, Protocol: protocol},
			Cert: remote.Cert, Record: remote.Rec, Seen: remote.Seen,
		}, s.stream, nil
	}
	s.conn = pairing.Conn{Local: target, Remote: initiator, Protocol: protocol}
	s.world = pairing.World{
		Params: pairing.DefaultParams(), Identity: s.peer.identity, Revocations: revocation.NewSet(),
		Trust: trust.View{
			Local: s.peer.node, Settings: trust.DefaultSettings(), LocallyRevoked: trust.NodeSet{},
			RevokedCredentials: trust.DigestSet{}, DeclaredAcceptsInheritance: map[ident.NodeID]bool{},
		},
	}
	s.target = mustOpen(t, s)
	s.e = s.r.start()
	return s
}

func mustOpen(t *testing.T, s *requestScene) pairing.State {
	t.Helper()
	st, err := pairing.Restore(pairing.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	st, _, err = st.TargetOpen(s.world, 1, t0)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// answer gives the target's end a frame the engine sent and returns the frame it
// answers with.
func (s *requestScene) answer(frame []byte) []byte {
	s.t.Helper()
	next, acts, err := s.target.TargetReceive(s.world, s.conn, 1, frame, rand.Reader, t0)
	if err != nil {
		s.t.Fatal(err)
	}
	s.target = next
	for _, a := range acts {
		if send, ok := a.(pairing.Send); ok {
			return send.Frame
		}
	}
	s.t.Fatal("the target answered nothing")
	return nil
}

func (s *requestScene) frame(sid pairing.SessionID, frame []byte) {
	s.t.Helper()
	if err := s.e.PairingFrame(context.Background(), sid, frame); err != nil {
		s.t.Fatalf("PairingFrame: %v", err)
	}
}

type requestOutcome struct {
	res engine.StoredResult
	err error
}

func encodeRequested(p engine.PairingRequested) ([]byte, error) {
	if p.Saved == nil {
		return []byte(`{"refusal":"` + p.Refusal.String() + `"}`), nil
	}
	id := p.Saved.ID()
	return []byte(`{"id":"` + hex.EncodeToString(id[:]) + `"}`), nil
}

func requestCommand(b byte, node ident.NodeID) engine.ManagementCommand {
	return engine.ManagementCommand{
		Key: requestKey(b), Fingerprint: fingerprintOf("request" + node.String()), Name: "trust.request",
		Operation: engine.RequestPairingOp(node, nil, encodeRequested),
	}
}

// start sends the request and returns the channel its answer comes on.
func (s *requestScene) start(c engine.ManagementCommand) <-chan requestOutcome {
	out := make(chan requestOutcome, 1)
	go func() {
		res, err := s.e.Execute(context.Background(), c)
		out <- requestOutcome{res, err}
	}()
	return out
}

func (s *requestScene) await(out <-chan requestOutcome) requestOutcome {
	s.t.Helper()
	select {
	case o := <-out:
		return o
	case <-time.After(10 * time.Second):
		s.t.Fatal("the request was not answered")
		return requestOutcome{}
	}
}

// The request for a pairing is answered when the target accepts it, with the result
// stored in the transaction that saves the request (no other write of the request
// precedes it); a second request of the same identifier while the first is under way
// is refused; and the same request sent after the answer is answered from the stored
// result and dials nothing.
func TestARequestForAPairingStoresItsResultWithTheSavedRequest(t *testing.T) {
	s := newRequestScene(t)
	cmd := requestCommand(0x71, s.peer.node)
	out := s.start(cmd)

	sid := <-s.sid
	s.frame(sid, s.answer(s.stream.next(t))) // M1 answered by the reveal
	_, err := s.e.Execute(context.Background(), cmd)
	wantRefusal(t, err, engine.ReasonPairingInProgress, "")
	s.frame(sid, s.answer(s.stream.next(t))) // M3 answered by the acceptance

	got := s.await(out)
	if got.err != nil {
		t.Fatalf("the request: %v", got.err)
	}
	sc := s.r.scan()
	if len(sc.PairingInitiators) != 1 {
		t.Fatalf("%d initiator records, want one", len(sc.PairingInitiators))
	}
	id := sc.PairingInitiators[0].ID()
	if want := `{"id":"` + hex.EncodeToString(id[:]) + `"}`; string(got.res.Result) != want {
		t.Errorf("result %s, want %s", got.res.Result, want)
	}
	stored, ok := s.r.adminResultOf(sc, cmd)
	if !ok || string(stored.Result) != string(got.res.Result) {
		t.Errorf("the stored result is %q (found %v), want the answer", stored.Result, ok)
	}
	if s.r.commits("trust-request") != 1 || s.r.commits("pairing-initiator-put") != 0 || s.r.commits("trust-result") != 0 {
		t.Errorf("commits: trust-request %d, pairing-initiator-put %d, trust-result %d; want the request and its result in one transaction",
			s.r.commits("trust-request"), s.r.commits("pairing-initiator-put"), s.r.commits("trust-result"))
	}

	again, err := s.e.Execute(context.Background(), cmd)
	if err != nil || !again.Replayed || string(again.Result) != string(got.res.Result) {
		t.Errorf("the request sent again: %+v, %v; want the stored result", again, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dials != 1 {
		t.Errorf("the node was dialed %d times, want once", s.dials)
	}
}

// A target that refuses the request gives the owner a result, stored alone: no
// request is saved. A node that cannot be reached is no result: nothing is stored and
// the identifier is free for the owner to ask again.
func TestARequestThatIsRefusedOrNotAnsweredStoresTheRightThing(t *testing.T) {
	t.Run("the target refuses", func(t *testing.T) {
		s := newRequestScene(t)
		cmd := requestCommand(0x71, s.peer.node)
		out := s.start(cmd)
		sid := <-s.sid
		s.stream.next(t)
		refusal, err := pairing.RefusalMsg{Reason: pairing.RefusalBusy}.Encode()
		if err != nil {
			t.Fatal(err)
		}
		s.frame(sid, refusal)
		got := s.await(out)
		if want := `{"refusal":"` + pairing.RefusalBusy.String() + `"}`; got.err != nil || string(got.res.Result) != want {
			t.Fatalf("answered %s, %v; want %s", got.res.Result, got.err, want)
		}
		sc := s.r.scan()
		if len(sc.PairingInitiators) != 0 || !hasResult(sc, cmd.Key) || s.r.commits("trust-result") != 1 {
			t.Errorf("%d initiator records, result %v, %d result-only commits; want none, a result, one", len(sc.PairingInitiators), hasResult(sc, cmd.Key), s.r.commits("trust-result"))
		}
	})
	t.Run("the node cannot be reached", func(t *testing.T) {
		s := newRequestScene(t)
		s.net.dial = nil
		cmd := requestCommand(0x71, s.peer.node)
		got := s.await(s.start(cmd))
		wantRefusal(t, got.err, engine.ReasonNodeUnreachable, "")
		if hasResult(s.r.scan(), cmd.Key) {
			t.Error("a request that got no answer stored a result")
		}
		s.net.dial = nil
		if _, err := s.e.Execute(context.Background(), cmd); !isReason(err, engine.ReasonNodeUnreachable) {
			t.Errorf("asked again: %v, want the same refusal and not a conflict", err)
		}
	})
}

func isReason(err error, r engine.Reason) bool {
	got, ok := engine.ReasonOf(err)
	return ok && got == r
}

// The saved request and the result of the owner's request for it are written in
// one transaction, the request first. Wherever the node dies the next start finds
// neither, the request alone, or both; never the result without the request it
// reports, which the owner could not otherwise tell the code of.
func TestARequestForAPairingRecoversFromACrashAtEveryPoint(t *testing.T) {
	noLeaks(t)
	const tx = "trust-request"
	cmd := requestCommand(0x71, newPeerNode(t, 3).node)
	var s *requestScene
	checked, neither, requestOnly, both := 0, 0, 0, 0
	walker := newCrashWalker(func(where string, disk *fsxtest.Mem) {
		checked++
		f := s.r.fork(disk)
		e, _, err := f.open()
		if err != nil {
			t.Errorf("%s: Open: %v", where, err)
			return
		}
		startForked(t, f, e)
		defer f.stop2(e)
		sc := f.scan()
		_, result := f.adminResultOf(sc, cmd)
		switch records := len(sc.PairingInitiators); {
		case records == 0 && result:
			t.Errorf("%s: the result is stored without the request it reports", where)
		case records == 0:
			neither++
		case !result:
			requestOnly++
		default:
			both++
		}
	}, tx)
	s = newRequestScene(t, withObserver(walker))
	walker.rig = s.r
	out := s.start(cmd)
	sid := <-s.sid
	s.frame(sid, s.answer(s.stream.next(t)))
	s.frame(sid, s.answer(s.stream.next(t)))
	if got := s.await(out); got.err != nil {
		t.Fatal(got.err)
	}
	settle(t, s.e)
	if checked == 0 || neither == 0 || requestOnly == 0 || both == 0 {
		t.Fatalf("%d states judged: %d with neither, %d with the request alone, %d with both; the walk must reach every side of the commit", checked, neither, requestOnly, both)
	}
	var missing []string
	for _, k := range s.r.tab.Keys() {
		if k.Tag.Tx != tx {
			continue
		}
		for _, model := range crashKindNames {
			if walker.taken[k.Key][model] == 0 {
				missing = append(missing, fmt.Sprintf("%v under %s", k.Key, model))
			}
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf("crash points never crashed at: %v", missing)
	}
}
