package inproc

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/internal/pairing"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
	"github.com/aianyai/sidera/internal/trust"
)

// The owner's operations on trust and pairing run through the engine with the
// node's own keys and a network layer. The harness node has no network, which
// a pairing request needs to dial the target, so these samples open the engine
// the way the composition root does, over the benchmark node's real disk.

func transactionBenchmarkEngineTrustCases() []transactionBenchmarkCase {
	return []transactionBenchmarkCase{
		{name: "trust-local-revoke", prepare: prepareTransactionTrustLocalRevoke},
		{name: "trust-expect", prepare: prepareTransactionTrustExpect},
		{name: "trust-forget", prepare: prepareTransactionTrustForget},
		{name: "trust-request", prepare: prepareTransactionTrustRequest},
		{name: "trust-reject", prepare: prepareTransactionTrustReject},
		{name: "trust-clear", prepare: prepareTransactionTrustClear},
		{name: "trust-result", prepare: prepareTransactionTrustResult},
	}
}

// transactionBenchmarkNetwork is the network layer of the node under test. It
// reaches nobody, except that a pairing request dials the node dial plays.
type transactionBenchmarkNetwork struct {
	dial func(sid pairing.SessionID) (engine.PairingPeer, engine.PairingStream, error)
}

func (*transactionBenchmarkNetwork) SetMembership(engine.Membership) error          { return nil }
func (*transactionBenchmarkNetwork) Connected(ident.NodeID) bool                    { return false }
func (*transactionBenchmarkNetwork) SetAddressBook(map[ident.NodeID][]string) error { return nil }
func (*transactionBenchmarkNetwork) CheckAddresses(ident.NodeID, []string) error    { return nil }
func (*transactionBenchmarkNetwork) FetchProfile(context.Context, ident.NodeID) (record.Record, bool, error) {
	return record.Record{}, false, nil
}
func (*transactionBenchmarkNetwork) PeerID() string         { return "benchmark" }
func (*transactionBenchmarkNetwork) ListenAddrs() []string  { return nil }
func (*transactionBenchmarkNetwork) ContactAddrs() []string { return nil }

func (*transactionBenchmarkNetwork) PairingFetch(context.Context, ident.NodeID, []string, []byte) ([]byte, engine.PairingPeer, error) {
	return nil, engine.PairingPeer{}, transport.ErrUnreachable
}

func (n *transactionBenchmarkNetwork) StartPairing(_ context.Context, _ ident.NodeID, _ []string, sid pairing.SessionID) (engine.PairingPeer, engine.PairingStream, error) {
	if n.dial == nil {
		return engine.PairingPeer{}, nil, transport.ErrUnreachable
	}
	return n.dial(sid)
}

// transactionBenchmarkTrustNode is a running engine with the node's own keys.
type transactionBenchmarkTrustNode struct {
	engine *engine.Engine
	store  *store.Store
	key    *keys.Keys
	net    *transactionBenchmarkNetwork
}

// newTransactionBenchmarkTrustNode opens and starts an engine on the benchmark
// node. seed writes the records the engine reads when it opens, through the
// store's own methods. The caller stops the engine.
func newTransactionBenchmarkTrustNode(b *testing.B, observer txn.Observer, seed func(*store.Store, *keys.Keys) error) *transactionBenchmarkTrustNode {
	b.Helper()
	_, w, cfg, key := newTransactionBenchmarkNode(b, observer)
	st := store.New(w)
	if seed != nil {
		if err := seed(st, key); err != nil {
			b.Fatal(err)
		}
	}
	n := &transactionBenchmarkTrustNode{store: st, key: key, net: &transactionBenchmarkNetwork{}}
	ctx := context.Background()
	e, _, err := engine.Open(ctx, engine.Deps{
		Node: key.Node(), Clock: clocktest.NewFake(harness.DefaultStart), HighWater: clock.NewHighWater(time.Time{}),
		Writer: w, Store: st, Config: cfg, Keys: key, Rand: harness.RandReader(300),
		Log: slog.New(slog.DiscardHandler),
	}, engine.Params{})
	if err != nil {
		b.Fatal(err)
	}
	if err := e.SetNetwork(n.net); err != nil {
		b.Fatal(err)
	}
	e.Start()
	if err := e.RecoverDelivery(ctx); err != nil {
		b.Fatal(err)
	}
	e.Serve()
	n.engine = e
	return n
}

func (n *transactionBenchmarkTrustNode) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	n.engine.BeginShutdown()
	if err := n.engine.Stop(ctx); err != nil {
		panic(fmt.Sprintf("stopping the benchmark engine: %v", err))
	}
}

func (n *transactionBenchmarkTrustNode) execute(cmd engine.ManagementCommand) (engine.StoredResult, error) {
	return n.engine.Execute(context.Background(), cmd)
}

func (n *transactionBenchmarkTrustNode) side() transactionBenchmarkEngineSide {
	return transactionBenchmarkEngineSide{execute: n.execute, records: func() (*store.Scan, error) { return transactionBenchmarkScan(n.store) }, cleanup: n.stop}
}

// effect adapts a judgment of the scan to the check of an owner operation.
func (n *transactionBenchmarkTrustNode) effect(what string, ok func(*store.Scan) bool) func() error {
	return func() error {
		sc, err := transactionBenchmarkScan(n.store)
		if err == nil && !ok(sc) {
			return fmt.Errorf("%s did not leave the expected records", what)
		}
		return err
	}
}

// transactionBenchmarkTrustCommand is an owner's command of the local owner.
func transactionBenchmarkTrustCommand(id byte, name string, op engine.Operation) engine.ManagementCommand {
	cmd := transactionBenchmarkEngineCommand(id, name, op)
	cmd.Actor = admin.LocalOwner()
	return cmd
}

func transactionBenchmarkEncode[T any](v T) ([]byte, error) { return json.Marshal(v) }

func transactionBenchmarkExpectCommand(id byte, node ident.NodeID) engine.ManagementCommand {
	return transactionBenchmarkTrustCommand(id, "trust.expect", engine.ExpectNodeOp(node, transactionBenchmarkEncode[engine.NodeExpected]))
}

func transactionBenchmarkRemoveCommand(id byte, node ident.NodeID) engine.ManagementCommand {
	return transactionBenchmarkTrustCommand(id, "trust.remove", engine.RemoveNodeOp(node, transactionBenchmarkEncode[engine.NodeRemoved]))
}

func transactionBenchmarkClearCommand(id byte, node ident.NodeID) engine.ManagementCommand {
	return transactionBenchmarkTrustCommand(id, "trust.clear", engine.ClearNodeOp(node, transactionBenchmarkEncode[engine.NodeCleared]))
}

func prepareTransactionTrustLocalRevoke(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	peer := newTransactionBenchmarkPeer(b, 3)
	var cred store.CredentialRecord
	n := newTransactionBenchmarkTrustNode(b, observer, func(st *store.Store, key *keys.Keys) error {
		cred = transactionBenchmarkCredential(b, key.IdentitySigner(), peer.identity)
		peerRecord := peer.peerRecord(b)
		return st.TrustCredential(cred, &peerRecord)
	})
	return transactionBenchmarkOwnerOperation(n.side(), transactionBenchmarkRemoveCommand(1, peer.node), n.effect("removal of a node", func(sc *store.Scan) bool {
		return len(sc.LocalRevoked) == 1 && sc.LocalRevoked[0].Node == peer.node &&
			len(sc.CredRevocations) == 1 && sc.CredRevocations[0].Revoked == cred.Digest
	}))
}

func prepareTransactionTrustExpect(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	peer := newTransactionBenchmarkPeer(b, 3)
	n := newTransactionBenchmarkTrustNode(b, observer, nil)
	return transactionBenchmarkOwnerOperation(n.side(), transactionBenchmarkExpectCommand(1, peer.node), n.effect("the expectation", func(sc *store.Scan) bool {
		return len(sc.PairingExpectations) == 1 && sc.PairingExpectations[0].Node == peer.node
	}))
}

func prepareTransactionTrustForget(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	peer := newTransactionBenchmarkPeer(b, 3)
	n := newTransactionBenchmarkTrustNode(b, observer, nil)
	if _, err := n.execute(transactionBenchmarkExpectCommand(1, peer.node)); err != nil {
		b.Fatal(err)
	}
	cmd := transactionBenchmarkTrustCommand(2, "trust.forget", engine.ForgetNodeOp(peer.node, transactionBenchmarkEncode[ident.NodeID]))
	return transactionBenchmarkOwnerOperation(n.side(), cmd, n.effect("forgetting the expectation", func(sc *store.Scan) bool {
		return len(sc.PairingExpectations) == 0
	}))
}

func prepareTransactionTrustClear(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	peer := newTransactionBenchmarkPeer(b, 3)
	n := newTransactionBenchmarkTrustNode(b, observer, nil)
	if _, err := n.execute(transactionBenchmarkRemoveCommand(1, peer.node)); err != nil {
		b.Fatal(err)
	}
	return transactionBenchmarkOwnerOperation(n.side(), transactionBenchmarkClearCommand(2, peer.node), n.effect("clearing a removed node", func(sc *store.Scan) bool {
		return len(sc.LocalRevoked) == 0
	}))
}

// Clearing a node that was never removed changes nothing and stores its result
// alone.
func prepareTransactionTrustResult(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	peer := newTransactionBenchmarkPeer(b, 3)
	n := newTransactionBenchmarkTrustNode(b, observer, nil)
	return transactionBenchmarkOwnerOperation(n.side(), transactionBenchmarkClearCommand(1, peer.node), n.effect("clearing a node that was not removed", func(sc *store.Scan) bool {
		return len(sc.LocalRevoked) == 0
	}))
}

// The node rejects a request it holds as a target.
func prepareTransactionTrustReject(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	peer := newTransactionBenchmarkPeer(b, 3)
	var target pairing.TargetRecord
	n := newTransactionBenchmarkTrustNode(b, observer, func(st *store.Store, key *keys.Keys) error {
		own := transactionBenchmarkParty(b, key.Certificate(), key.NetworkSigner().Public())
		target = transactionBenchmarkTargetRecord(transactionBenchmarkTranscript(b, peer.party(b), own, 20))
		return st.PairingTargetPut(target)
	})
	cmd := transactionBenchmarkTrustCommand(1, "trust.reject", engine.RejectPairingOp(target.ID(), func(v engine.PairingRejected) ([]byte, error) {
		return json.Marshal(map[string]string{"status": v.Incoming.Status.String()})
	}))
	return transactionBenchmarkOwnerOperation(n.side(), cmd, n.effect("rejecting the request", func(sc *store.Scan) bool {
		return len(sc.PairingTargets) == 1 && sc.PairingTargets[0].Status == pairing.TargetRejected
	}))
}

// transactionBenchmarkPairingTarget plays the node asked for a pairing with the
// pairing package's own target, answering the frames the engine sends.
type transactionBenchmarkPairingTarget struct {
	world  pairing.World
	conn   pairing.Conn
	state  pairing.State
	frames chan []byte
	dialed chan pairing.SessionID
	peer   engine.PairingPeer
}

func (t *transactionBenchmarkPairingTarget) Send(frame []byte) { t.frames <- slices.Clone(frame) }
func (t *transactionBenchmarkPairingTarget) Close()            {}

func newTransactionBenchmarkPairingTarget(b *testing.B, key *keys.Keys, peer transactionBenchmarkPeer) *transactionBenchmarkPairingTarget {
	b.Helper()
	at := harness.DefaultStart
	remote, remoteCert := peer.certificate(b)
	initiator := transactionBenchmarkParty(b, key.Certificate(), key.NetworkSigner().Public())
	target := transactionBenchmarkParty(b, remoteCert, peer.network.Public())
	t := &transactionBenchmarkPairingTarget{
		conn:   pairing.Conn{Local: target, Remote: initiator, Protocol: transactionBenchmarkPairingProtocol},
		frames: make(chan []byte, 8), dialed: make(chan pairing.SessionID, 1),
		peer: engine.PairingPeer{
			Conn: pairing.Conn{Local: initiator, Remote: target, Protocol: transactionBenchmarkPairingProtocol},
			Cert: remoteCert, Record: remote, Seen: record.SerialSeenOf(remoteCert),
		},
	}
	t.world = pairing.World{
		Params: pairing.DefaultParams(), Identity: peer.identity, Revocations: revocation.NewSet(),
		Trust: trust.View{
			Local: peer.node, Settings: trust.DefaultSettings(), LocallyRevoked: trust.NodeSet{},
			RevokedCredentials: trust.DigestSet{}, DeclaredAcceptsInheritance: map[ident.NodeID]bool{},
		},
	}
	state, err := pairing.Restore(pairing.Snapshot{})
	if err != nil {
		b.Fatal(err)
	}
	if t.state, _, err = state.TargetOpen(t.world, 1, at); err != nil {
		b.Fatal(err)
	}
	return t
}

// answer gives the target's end a frame the engine sent and returns its answer.
func (t *transactionBenchmarkPairingTarget) answer(frame []byte) ([]byte, error) {
	next, acts, err := t.state.TargetReceive(t.world, t.conn, 1, frame, rand.Reader, harness.DefaultStart)
	if err != nil {
		return nil, err
	}
	t.state = next
	for _, a := range acts {
		if send, ok := a.(pairing.Send); ok {
			return send.Frame, nil
		}
	}
	return nil, errors.New("the target answered nothing")
}

// request asks the target for a pairing and plays it through the two exchanges
// (the commitment answered by the reveal, the opening answered by the
// acceptance) until the engine answers the owner.
func (t *transactionBenchmarkPairingTarget) request(n *transactionBenchmarkTrustNode, cmd engine.ManagementCommand) (engine.StoredResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	type outcome struct {
		res engine.StoredResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := n.engine.Execute(ctx, cmd)
		done <- outcome{res, err}
	}()
	var sid pairing.SessionID
	select {
	case sid = <-t.dialed:
	case o := <-done:
		return o.res, o.err
	case <-ctx.Done():
		return engine.StoredResult{}, ctx.Err()
	}
	for range 2 {
		select {
		case frame := <-t.frames:
			reply, err := t.answer(frame)
			if err != nil {
				return engine.StoredResult{}, err
			}
			if err := n.engine.PairingFrame(ctx, sid, reply); err != nil {
				return engine.StoredResult{}, err
			}
		case <-ctx.Done():
			return engine.StoredResult{}, ctx.Err()
		}
	}
	select {
	case o := <-done:
		return o.res, o.err
	case <-ctx.Done():
		return engine.StoredResult{}, ctx.Err()
	}
}

func prepareTransactionTrustRequest(b *testing.B, observer txn.Observer) transactionBenchmarkFixture {
	peer := newTransactionBenchmarkPeer(b, 3)
	n := newTransactionBenchmarkTrustNode(b, observer, nil)
	target := newTransactionBenchmarkPairingTarget(b, n.key, peer)
	n.net.dial = func(sid pairing.SessionID) (engine.PairingPeer, engine.PairingStream, error) {
		target.dialed <- sid
		return target.peer, target, nil
	}
	cmd := transactionBenchmarkTrustCommand(1, "trust.request", engine.RequestPairingOp(peer.node, nil, func(v engine.PairingRequested) ([]byte, error) {
		if v.Saved == nil {
			return nil, fmt.Errorf("the target refused the request: %s", v.Refusal)
		}
		return json.Marshal(map[string]string{"status": v.Saved.Status.String()})
	}))
	side := n.side()
	side.execute = func(cmd engine.ManagementCommand) (engine.StoredResult, error) { return target.request(n, cmd) }
	return transactionBenchmarkOwnerOperation(side, cmd, n.effect("the request", func(sc *store.Scan) bool {
		return len(sc.PairingInitiators) == 1 && sc.PairingInitiators[0].Transcript.Terms().Target.Node() == peer.node
	}))
}
