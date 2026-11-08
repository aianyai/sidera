package engine_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/record"
)

func trustRig(t *testing.T, opts ...rigOption) *rig {
	t.Helper()
	return newRig(t, 10, append(opts, withKeys())...)
}

func inLoop(t testing.TB, e *engine.Engine, fn func(engine.LoopSide)) {
	t.Helper()
	if err := engine.InLoop(context.Background(), e, fn); err != nil {
		t.Fatalf("InLoop: %v", err)
	}
}

// loopErr runs a step of the loop and returns the error it gave.
func loopErr(t testing.TB, e *engine.Engine, fn func(engine.LoopSide) error) error {
	t.Helper()
	var err error
	inLoop(t, e, func(l engine.LoopSide) { err = fn(l) })
	return err
}

// inboundFrom takes in a message for the endpoint as if the node from sent it, and
// returns the target record it left.
func (r *rig) inboundFrom(t testing.TB, e *engine.Engine, from ident.NodeID, to ident.EndpointID, id string) store.InRecord {
	t.Helper()
	now := r.clk.Now().Truncate(time.Second)
	accept := now.Add(delivery.DefaultAcceptBefore)
	env := envelope.Envelope{
		ID: delivery.MessageID(id), To: []ident.Address{{Node: r.node, Endpoint: to}}, Kind: envelope.KindMessage,
		Seq: map[ident.EndpointID]uint64{to: 1}, SentAt: now, AcceptBefore: accept, ReportBy: delivery.ReportBy(accept),
		ContentType: "text/plain", Body: []byte("hello"),
	}
	data, err := env.Encode(envelope.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Inbound(context.Background(), transport.RemoteOrigin(from), mustEndpoint(t, 2), data, 0); err != nil {
		t.Fatalf("Inbound %s: %v", id, err)
	}
	for _, in := range r.scan().In {
		if in.Key.Message == delivery.MessageID(id) {
			return in
		}
	}
	t.Fatalf("message %s left no record", id)
	return store.InRecord{}
}

func hasResult(sc *store.Scan, key admin.RequestKey) bool {
	for _, a := range sc.Admin {
		if a.Key == key {
			return true
		}
	}
	return false
}

func wantTrusted(t testing.TB, in store.InRecord, trusted bool, why string) {
	t.Helper()
	switch {
	case trusted && in.Record.State != delivery.StateAccepted:
		t.Errorf("%s: the message is %s (%s), want accepted", why, in.Record.State, in.Record.Reject)
	case !trusted && (in.Record.State != delivery.StateRejected || in.Record.Reject != delivery.RejectUnauthorized):
		t.Errorf("%s: the message is %s (%s), want rejected as unauthorized", why, in.Record.State, in.Record.Reject)
	}
}

// What a node signs in its own name is judged by the certificate this node holds
// for it: without one its reports do not verify; with one they do, until a
// certificate of a higher serial replaces it and the key of the lower one is void.
// The serial only moves forward: a lower serial is refused, the same certificate
// again is the peer's reconnect and writes nothing, and the same serial with other
// content, or a record that is not the certificate's, is refused. Whatever is kept
// is what the next start reads.
func TestAPeerCertificateIsKeptAndItsSerialOnlyMovesForward(t *testing.T) {
	r := trustRig(t)
	e := r.start()
	peer := newPeerNode(t, 1)
	other := newPeerNode(t, 2)
	ctx := context.Background()
	verifies := func(serial uint64) bool {
		rec := peer.report(r.node, serial)
		return loopErr(t, e, func(l engine.LoopSide) error { return l.VerifyReport(rec, peer.node) }) == nil
	}
	note := func(p engine.TestPeer) error { return e.NotePeerCertificate(ctx, p.Cert, p.Rec, p.Seen) }
	if verifies(1) {
		t.Fatal("a report of a node whose certificate is not held verified")
	}

	own, err := record.Decode(r.keys.CertificateWire(), record.DefaultMaxInput)
	if err != nil {
		t.Fatal(err)
	}
	ownCert, err := record.Verify(record.NetworkKeyCertificateDescriptor, own)
	if err != nil {
		t.Fatal(err)
	}
	mismatched := peer.certificate(3)
	mismatched.Rec = peer.certificate(4).Rec

	for _, step := range []struct {
		name       string
		peer       engine.TestPeer
		refused    bool
		commits    int // the writes of peers' certificates so far
		heldSerial uint64
	}{
		{"the first certificate is kept", peer.certificate(1), false, 1, 1},
		{"the same certificate again writes nothing", peer.certificate(1), false, 1, 1},
		{"a higher serial replaces it", peer.certificate(2), false, 2, 2},
		{"a lower serial is refused", peer.certificate(1), true, 2, 2},
		{"the same serial with other content is refused", peer.certificateFor(2, 20*24*time.Hour), true, 2, 2},
		{"a record that is not the certificate's is refused", mismatched, true, 2, 2},
		{"this node's own certificate is refused", engine.TestPeer{Cert: ownCert, Rec: own, Seen: record.SerialSeenOf(ownCert)}, true, 2, 2},
		{"the serial seen advances with the certificate", peer.certificate(3), false, 3, 3},
		{"a certificate of another node is its own record", other.certificate(1), false, 4, 1},
	} {
		err := note(step.peer)
		if (err != nil) != step.refused {
			t.Errorf("%s: %v, refused %v", step.name, err, step.refused)
		}
		if step.refused {
			if _, ok := engine.ReasonOf(err); !ok {
				t.Errorf("%s: %v is no refusal of the engine", step.name, err)
			}
		}
		if got := r.commits("peer-cert"); got != step.commits {
			t.Errorf("%s: %d writes of a peer certificate, want %d", step.name, got, step.commits)
		}
		for _, p := range r.scan().Peers {
			if p.Node == peer.node && p.SerialSeen != step.heldSerial && step.heldSerial != 1 {
				t.Errorf("%s: the serial seen is %d, want %d", step.name, p.SerialSeen, step.heldSerial)
			}
		}
	}
	if !verifies(3) || verifies(2) || verifies(1) {
		t.Error("only the key of the highest serial should stand")
	}

	r.restart()
	e = r.start()
	if !verifies(3) || verifies(2) {
		t.Error("after a restart the peer's key is not the one that was kept")
	}
}

// A node this node holds a credential with is trusted, and so is let in by the
// rules of the node; it stops being trusted when the credential is revoked, and
// when the owner removes it, which also revokes the credential, and clearing the
// removal does not bring a revoked credential back.
func TestATrustedNodeIsLetInAndStopsWhenItsCredentialIsRevokedOrTheNodeRemoved(t *testing.T) {
	r := trustRig(t)
	e := r.start()
	peer := newPeerNode(t, 1)
	to := register(t, e, "notes")
	cred := peer.credentialWith(r.keys.IdentitySigner(), false)
	wantTrusted(t, r.inboundFrom(t, e, peer.node, to, "m1"), false, "no credential")

	if err := loopErr(t, e, func(l engine.LoopSide) error { return l.InstallCredential(cred.Record, nil) }); err != nil {
		t.Fatal(err)
	}
	wantTrusted(t, r.inboundFrom(t, e, peer.node, to, "m2"), true, "a credential")

	if err := loopErr(t, e, func(l engine.LoopSide) error { return l.RevokeCredential(cred.Digest) }); err != nil {
		t.Fatal(err)
	}
	wantTrusted(t, r.inboundFrom(t, e, peer.node, to, "m3"), false, "the credential revoked")

	// The owner removes a node that holds a credential: the credential is revoked
	// with it, and the entry is kept until the owner clears it.
	r = trustRig(t)
	e = r.start()
	to = register(t, e, "notes")
	if err := loopErr(t, e, func(l engine.LoopSide) error { return l.InstallCredential(cred.Record, nil) }); err != nil {
		t.Fatal(err)
	}
	var revoked []record.Digest
	if err := loopErr(t, e, func(l engine.LoopSide) (err error) {
		revoked, err = l.RevokeNodeLocally(requestKey(1), fingerprintOf("remove"), "trust.revoke_node", peer.node)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 1 || revoked[0] != cred.Digest {
		t.Errorf("the removal revoked %v, want the credential %s", revoked, cred.Digest)
	}
	sc := r.scan()
	if len(sc.LocalRevoked) != 1 || sc.LocalRevoked[0].Operation != "trust.revoke_node" || len(sc.CredRevocations) != 1 || !hasResult(sc, requestKey(1)) {
		t.Errorf("the removal left %d entries, %d revocations and result %v, want one of each", len(sc.LocalRevoked), len(sc.CredRevocations), hasResult(sc, requestKey(1)))
	}
	wantTrusted(t, r.inboundFrom(t, e, peer.node, to, "m4"), false, "the node removed")
	if err := loopErr(t, e, func(l engine.LoopSide) error { return l.ClearLocallyRevoked(peer.node) }); err != nil {
		t.Fatal(err)
	}
	if got := r.scan().LocalRevoked; len(got) != 0 {
		t.Errorf("%d entries after the clear", len(got))
	}
	wantTrusted(t, r.inboundFrom(t, e, peer.node, to, "m5"), false, "the removal cleared, the credential still revoked")
}

// The trust view is built from what the disk holds, and every record counts only
// after it verifies: an entry of the owner beats a credential that is whole, and
// clearing it brings the credential's trust back; a credential that carries one
// signature only is no credential.
func TestTheTrustViewIsBuiltFromVerifiedRecordsOfTheDisk(t *testing.T) {
	for _, c := range []struct {
		name      string
		onlyPeer  bool
		removed   bool
		wantFirst bool
		wantClear bool
	}{
		{"a credential and the owner's removal of the node", false, true, false, true},
		{"a credential and no removal", false, false, true, true},
		{"a credential with one signature", true, false, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := trustRig(t)
			peer := newPeerNode(t, 1)
			r.seedRecord(peer.credentialWith(r.keys.IdentitySigner(), c.onlyPeer))
			if c.removed {
				r.seedRecord(store.LocalRevokedRecord{Node: peer.node, At: t0, Operation: "trust.revoke_node"})
			}
			e := r.start()
			to := register(t, e, "notes")
			wantTrusted(t, r.inboundFrom(t, e, peer.node, to, "m1"), c.wantFirst, "as found")
			if err := loopErr(t, e, func(l engine.LoopSide) error { return l.ClearLocallyRevoked(peer.node) }); err != nil {
				t.Fatal(err)
			}
			wantTrusted(t, r.inboundFrom(t, e, peer.node, to, "m2"), c.wantClear, "after the clear")
			if c.onlyPeer && !slices.ContainsFunc(r.log.Messages(), func(m string) bool { return strings.Contains(m, "a stored credential does not count") }) {
				t.Error("the credential that does not verify was not logged")
			}
		})
	}
}

// The network is given the facts it judges peers by when the engine starts and
// after every change of them, and after none else; a snapshot is not changed by
// the changes that follow it.
func TestTheNetworkIsGivenAFreshSnapshotWhenTheFactsChange(t *testing.T) {
	net := &fakeNetwork{}
	r := trustRig(t, withNetwork(net))
	e := r.start()
	peer := newPeerNode(t, 1)
	cert := peer.certificate(1)
	cred := peer.credentialWith(r.keys.IdentitySigner(), false)

	steps := []struct {
		name string
		do   func() error
		push bool
	}{
		{"a peer's certificate", func() error { return e.NotePeerCertificate(context.Background(), cert.Cert, cert.Rec, cert.Seen) }, true},
		{"the same certificate again", func() error { return e.NotePeerCertificate(context.Background(), cert.Cert, cert.Rec, cert.Seen) }, false},
		{"a credential", func() error {
			return loopErr(t, e, func(l engine.LoopSide) error { return l.InstallCredential(cred.Record, nil) })
		}, true},
		{"the credential's revocation", func() error {
			return loopErr(t, e, func(l engine.LoopSide) error { return l.RevokeCredential(cred.Digest) })
		}, true},
		{"the removal of the node", func() error {
			return loopErr(t, e, func(l engine.LoopSide) (err error) {
				_, err = l.RevokeNodeLocally(requestKey(1), fingerprintOf("remove"), "trust.revoke_node", peer.node)
				return err
			})
		}, true},
		{"the clear of the removal", func() error {
			return loopErr(t, e, func(l engine.LoopSide) error { return l.ClearLocallyRevoked(peer.node) })
		}, true},
		{"the clear of what is not removed", func() error {
			return loopErr(t, e, func(l engine.LoopSide) error { return l.ClearLocallyRevoked(peer.node) })
		}, false},
	}
	snaps := net.snapshots()
	if len(snaps) != 1 {
		t.Fatalf("%d snapshots at the start, want 1", len(snaps))
	}
	first := snaps[0]
	if first.Own.Digest() != r.keys.Certificate().Digest() || string(first.OwnWire) != string(r.keys.CertificateWire()) {
		t.Error("the first snapshot does not hold the node's own certificate and its record")
	}
	if first.Facts.Revocations == nil || first.Facts.SerialSeen == nil || len(first.Facts.SerialSeen) != 0 || len(first.Held) != 0 || len(first.Facts.Trust.Credentials) != 0 {
		t.Errorf("the first snapshot is not the facts of a node that knows nothing: %+v", first.Facts)
	}
	for _, s := range steps {
		before := len(net.snapshots())
		if err := s.do(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if got := len(net.snapshots()) - before; (got == 1) != s.push || got > 1 {
			t.Errorf("%s: %d snapshots pushed, want pushed %v", s.name, got, s.push)
		}
	}

	snaps = net.snapshots()
	byStep := func(i int) engine.Membership { return snaps[i] } // 0 is the start
	if len(byStep(1).Held) != 1 || byStep(1).Facts.SerialSeen[peer.node].Serial != 1 {
		t.Error("the snapshot of the certificate does not hold it and its serial")
	}
	if len(byStep(2).Facts.Trust.Credentials) != 1 || len(byStep(2).Facts.Trust.RevokedCredentials) != 0 {
		t.Error("the snapshot of the credential does not hold it, or holds it revoked")
	}
	if len(byStep(3).Facts.Trust.RevokedCredentials) != 1 {
		t.Error("the snapshot of the revocation does not hold it")
	}
	if len(byStep(4).Facts.Trust.LocallyRevoked) != 1 || len(byStep(5).Facts.Trust.LocallyRevoked) != 0 {
		t.Error("the removal and its clear are not in the snapshots")
	}
	// A snapshot is as it was made: the revocation that followed it is not in it.
	if len(byStep(2).Facts.Trust.RevokedCredentials) != 0 || len(byStep(4).Facts.Trust.LocallyRevoked) != 1 {
		t.Error("a snapshot was changed after it was pushed")
	}
}

// A renewal of the node's own certificate, which the composition root's task makes,
// is seen by the engine after the task ran, and the network is given the new one.
func TestTheNetworkIsGivenTheRenewedCertificate(t *testing.T) {
	net := &fakeNetwork{}
	r := trustRig(t, withNetwork(net))
	e, _, err := r.open()
	if err != nil {
		t.Fatal(err)
	}
	renewed := make(chan struct{}, 1)
	if err := e.AddPeriodic("renew", time.Hour, func(in engine.TaskInput) error {
		defer func() { renewed <- struct{}{} }()
		return r.keys.Renew(in.Now)
	}); err != nil {
		t.Fatal(err)
	}
	e.Start()
	e.Serve()
	t.Cleanup(func() { r.stop(e) })
	before := r.keys.SerialSeen().Serial
	r.clk.Advance(time.Hour)
	select {
	case <-renewed:
	case <-time.After(10 * time.Second):
		t.Fatal("the task did not run")
	}
	settle(t, e)
	snaps := net.snapshots()
	if len(snaps) != 2 || snaps[1].Own.Payload().Serial != before+1 {
		t.Fatalf("%d snapshots, the last of serial %d, want 2, the last of serial %d", len(snaps), snaps[len(snaps)-1].Own.Payload().Serial, before+1)
	}
}
