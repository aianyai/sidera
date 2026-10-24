package preauth

import (
	"bytes"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
	"github.com/aianyai/sidera/internal/trust"
)

// world is a local node with a remote node and a remote person to decide on. It
// builds real signed records with the record package's issuing functions; only
// the expected answers are written independently, in each table row.
type world struct {
	t    testing.TB
	now  time.Time
	peer record.Signer // the remote node's network key, the key the handshake authenticates

	identity, network record.Signer
	root, device      record.Signer

	local ident.NodeID
	rev   *revocation.Set
	view  trust.View
	seen  map[ident.NodeID]record.SerialSeen
	list  admin.List
}

func newWorld(t testing.TB) *world {
	t.Helper()
	w := &world{
		t:        t,
		now:      epoch.Add(time.Hour),
		identity: seedSigner(t, 0x11),
		network:  seedSigner(t, 0x12),
		root:     seedSigner(t, 0x21),
		device:   seedSigner(t, 0x22),
		local:    nodeOf(t, seedSigner(t, 0x01)),
		rev:      revocation.NewSet(),
		seen:     map[ident.NodeID]record.SerialSeen{},
	}
	w.peer = w.network
	w.view = trust.View{
		Local:                      w.local,
		Settings:                   trust.DefaultSettings(),
		LocallyRevoked:             trust.NodeSet{},
		Retired:                    trust.NodeSet{},
		RevokedCredentials:         trust.DigestSet{},
		Vouchers:                   trust.Vouchers{},
		DeclaredAcceptsInheritance: map[ident.NodeID]bool{},
	}
	return w
}

func (w *world) remoteNode() ident.NodeID { return nodeOf(w.t, w.identity) }

// trusts gives the local node a direct credential with the remote node.
func (w *world) trusts() *world {
	w.view.Credentials = append(w.view.Credentials, trust.Credential{Digest: trust.CredentialDigest{1}, A: w.local, B: w.remoteNode()})
	return w
}

// trustsThrough gives the local node trust in the remote node by inheritance
// through a middle node, with fresh vouchers unless stale is set.
func (w *world) trustsThrough(stale bool) *world {
	mid := nodeOf(w.t, seedSigner(w.t, 0x02))
	e1, e2 := trust.CredentialDigest{2}, trust.CredentialDigest{3}
	w.view.Credentials = append(w.view.Credentials,
		trust.Credential{Digest: e1, A: w.local, B: mid},
		trust.Credential{Digest: e2, A: mid, B: w.remoteNode()})
	w.view.DeclaredAcceptsInheritance[w.remoteNode()] = true
	if !stale {
		w.view.Vouchers[trust.VoucherKey{Issuer: mid, Edge: e1}] = trust.Voucher{Issuer: mid, Edge: e1, IssuedAt: w.now, Counter: 1}
		w.view.Vouchers[trust.VoucherKey{Issuer: mid, Edge: e2}] = trust.Voucher{Issuer: mid, Edge: e2, IssuedAt: w.now, Counter: 1}
	}
	return w
}

// cert issues a network-key certificate of the remote node at the given serial,
// valid from epoch (less the clock tolerance) to endTime.
func (w *world) cert(serial uint64) (record.Record, record.Verified[record.NetworkKeyCertificate]) {
	return w.certWindow(serial, epoch, endTime)
}

func (w *world) certWindow(serial uint64, from, to time.Time) (record.Record, record.Verified[record.NetworkKeyCertificate]) {
	w.t.Helper()
	c := record.IssueNetworkKeyCertificate(w.remoteNode(), w.network.Public(), serial, from, to)
	rec, err := record.Seal(record.NetworkKeyCertificateDescriptor, c, w.identity)
	if err != nil {
		w.t.Fatal(err)
	}
	v, err := record.Verify(record.NetworkKeyCertificateDescriptor, rec)
	if err != nil {
		w.t.Fatal(err)
	}
	return rec, v
}

func (w *world) deviceCert(level admin.Level, from, to time.Time) (record.Record, record.Verified[record.DeviceCertificate]) {
	w.t.Helper()
	person := personOf(w.t, w.root)
	c := record.IssueDeviceCertificate(person, w.device.Public(), level, from, to)
	rec, err := record.Seal(record.DeviceCertificateDescriptor, c, w.root)
	if err != nil {
		w.t.Fatal(err)
	}
	v, err := record.Verify(record.DeviceCertificateDescriptor, rec)
	if err != nil {
		w.t.Fatal(err)
	}
	return rec, v
}

func (w *world) listPerson(level admin.Level, expires time.Time) *world {
	l, problems := admin.NewList([]admin.Entry{{Person: personOf(w.t, w.root), Level: level, ExpiresAt: expires}})
	if len(problems) > 0 {
		w.t.Fatal(problems)
	}
	w.list = l
	return w
}

func (w *world) revoke(r record.Record, err error) *world {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
	rv, err := revocation.Verify(r)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.rev.Add(rv); err != nil {
		w.t.Fatal(err)
	}
	return w
}

func (w *world) facts() Facts {
	return Facts{Now: w.now, Revocations: w.rev, Trust: w.view, SerialSeen: w.seen, Authorized: w.list}
}

func TestDecideNode(t *testing.T) {
	type want struct {
		state   State
		reason  Reason
		outcome trust.Outcome // checked when the member or non-member answer carries a verdict
		trustR  trust.Reason  // checked when nonzero
	}
	rows := []struct {
		rule string
		// setup returns the facts, the peer key and the message
		setup func(w *world) (Facts, ident.PublicKey, Exchange)
		want  want
	}{
		{"a directly trusted node is a member", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			rec, _ := w.cert(1)
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{StateMember, ReasonMemberDirect, trust.AllowedDirect, trust.ReasonDirect}},
		{"a node trusted by inheritance is a member", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trustsThrough(false)
			rec, _ := w.cert(1)
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{StateMember, ReasonMemberInherited, trust.AllowedInherited, trust.ReasonInherited}},
		{"a node whose trust is suspended is a member, with the suspended outcome", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trustsThrough(true)
			rec, _ := w.cert(1)
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{StateMember, ReasonMemberSuspended, trust.Suspended, trust.ReasonVoucherMissing}},
		{"a stranger with a valid certificate is a non-member", func(w *world) (Facts, ident.PublicKey, Exchange) {
			rec, _ := w.cert(1)
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{StateNonMember, ReasonNotTrusted, trust.Denied, trust.ReasonNoPath}},
		{"a node revoked locally is a non-member, whatever its credentials say", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			w.view.LocallyRevoked[w.remoteNode()] = struct{}{}
			rec, _ := w.cert(1)
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{StateNonMember, ReasonNotTrusted, trust.Denied, trust.ReasonLocallyRevoked}},
		{"a trusted node presenting another network key is refused", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			rec, _ := w.cert(1)
			return w.facts(), seedSigner(t, 0x99).Public(), NewNodeExchange(rec)
		}, want{state: StateUnauthenticated, reason: ReasonKeyMismatch}},
		{"a trusted node whose identity key is retired is refused", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			w.revoke(revocation.IssueNodeSelfRevocation(w.identity))
			rec, _ := w.cert(1)
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{state: StateUnauthenticated, reason: ReasonNodeRetired}},
		{"a trusted node whose certificate is revoked is refused", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			rec, v := w.cert(1)
			w.revoke(revocation.IssueNetworkKeyCertificateRevocation(v, w.identity))
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{state: StateUnauthenticated, reason: ReasonCertificateRevoked}},
		{"a trusted node with an expired certificate is refused", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			rec, _ := w.certWindow(1, epoch, epoch.Add(30*time.Minute))
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{state: StateUnauthenticated, reason: ReasonCertificateExpired}},
		{"a trusted node with a certificate that has not begun is refused", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			rec, _ := w.certWindow(1, epoch.Add(2*time.Hour), endTime)
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{state: StateUnauthenticated, reason: ReasonCertificateNotYetValid}},
		{"a certificate older than the highest serial seen is refused", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			_, newer := w.cert(2)
			w.seen[w.remoteNode()] = record.SerialSeenOf(newer)
			rec, _ := w.cert(1)
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{state: StateUnauthenticated, reason: ReasonCertificateStale}},
		{"the certificate already seen at the highest serial is accepted again", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			rec, v := w.cert(2)
			w.seen[w.remoteNode()] = record.SerialSeenOf(v)
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{StateMember, ReasonMemberDirect, trust.AllowedDirect, 0}},
		{"two certificates under one serial are a conflict", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			_, other := w.certWindow(2, epoch, epoch.Add(48*time.Hour))
			w.seen[w.remoteNode()] = record.SerialSeenOf(other)
			rec, _ := w.cert(2)
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{state: StateUnauthenticated, reason: ReasonSerialConflict}},
		{"a higher serial than the one seen is accepted", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			_, older := w.cert(1)
			w.seen[w.remoteNode()] = record.SerialSeenOf(older)
			rec, _ := w.cert(2)
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{StateMember, ReasonMemberDirect, trust.AllowedDirect, 0}},
		{"a serial state filed under another node cannot be judged", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			other := record.SerialSeen{Node: nodeOf(t, seedSigner(t, 0x77)), Serial: 1, Digest: record.Digest{1}}
			w.seen[w.remoteNode()] = other
			rec, _ := w.cert(1)
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{state: StateUnauthenticated, reason: ReasonInvalidInput}},
		{"a person's certificate in a node message does not verify", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			rec, _ := w.deviceCert(admin.LevelManage, epoch, endTime)
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{state: StateUnauthenticated, reason: ReasonCertificateInvalid}},
		{"a certificate with a damaged signature does not verify", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			rec, _ := w.cert(1)
			rec.Signatures = append([]record.Signature(nil), rec.Signatures...)
			sig := bytes.Clone(rec.Signatures[0].Sig)
			sig[0] ^= 1
			rec.Signatures[0].Sig = sig
			return w.facts(), w.peer.Public(), NewNodeExchange(rec)
		}, want{state: StateUnauthenticated, reason: ReasonCertificateInvalid}},
		{"a certificate signed by the network key and not the identity key does not verify", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			rec, _ := w.cert(1)
			forged, err := record.Sign(rec.Type, rec.Payload, w.network)
			if err != nil {
				t.Fatal(err)
			}
			return w.facts(), w.peer.Public(), NewNodeExchange(forged)
		}, want{state: StateUnauthenticated, reason: ReasonCertificateInvalid}},
		{"no time cannot be judged", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			rec, _ := w.cert(1)
			f := w.facts()
			f.Now = time.Time{}
			return f, w.peer.Public(), NewNodeExchange(rec)
		}, want{state: StateUnauthenticated, reason: ReasonInvalidInput}},
		{"no authenticated peer key cannot be judged", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			rec, _ := w.cert(1)
			return w.facts(), ident.PublicKey{}, NewNodeExchange(rec)
		}, want{state: StateUnauthenticated, reason: ReasonInvalidInput}},
		{"no revocation set is never read as nothing revoked", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			rec, _ := w.cert(1)
			f := w.facts()
			f.Revocations = nil
			return f, w.peer.Public(), NewNodeExchange(rec)
		}, want{state: StateUnauthenticated, reason: ReasonRevocationsUnavailable}},
		{"an unset message kind cannot be judged", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.trusts()
			rec, _ := w.cert(1)
			return w.facts(), w.peer.Public(), Exchange{Record: rec}
		}, want{state: StateUnauthenticated, reason: ReasonInvalidInput}},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			f, peer, ex := row.setup(newWorld(t))
			d := Decide(f, peer, ex)
			if d.State != row.want.state || d.Reason != row.want.reason {
				t.Fatalf("got %v/%v, want %v/%v", d.State, d.Reason, row.want.state, row.want.reason)
			}
			if d.Completed() != (row.want.state != StateUnauthenticated) {
				t.Errorf("Completed() = %v", d.Completed())
			}
			if row.want.state == StateUnauthenticated {
				if !d.Node.IsZero() || !d.Person.IsZero() || d.Trust.Outcome != trust.Denied || !d.NodeCertificate.Digest().IsZero() {
					t.Errorf("a refused exchange carries identity or trust: %+v", d)
				}
				return
			}
			if d.Trust.Outcome != row.want.outcome {
				t.Errorf("trust outcome %v, want %v", d.Trust.Outcome, row.want.outcome)
			}
			if row.want.trustR != 0 && d.Trust.Reason != row.want.trustR {
				t.Errorf("trust reason %v, want %v", d.Trust.Reason, row.want.trustR)
			}
			if d.Node != nodeOf(t, seedSigner(t, 0x11)) || d.NodeCertificate.Digest().IsZero() {
				t.Errorf("an accepted node exchange must name the node and keep the verified certificate: %+v", d)
			}
		})
	}
}

// The serial seen is advanced whenever a verified certificate carries a higher
// serial, and left alone otherwise, also when the exchange is refused: the node
// did issue the serial, and an older certificate must not come back.
func TestDecideAdvancesTheSerialSeen(t *testing.T) {
	rows := []struct {
		rule     string
		seenAt   uint64 // 0: nothing seen
		present  uint64
		retired  bool
		wrongKey bool
		advance  bool
	}{
		{"the first certificate seen advances the serial", 0, 1, false, false, true},
		{"a higher serial advances it", 1, 3, false, false, true},
		{"the same certificate again leaves it", 3, 3, false, false, false},
		{"an older certificate leaves it", 3, 2, false, false, false},
		{"a refused certificate with a higher serial still advances it (key mismatch)", 1, 2, false, true, true},
		{"a refused certificate with a higher serial still advances it (retired)", 1, 2, true, false, true},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			w := newWorld(t).trusts()
			if row.seenAt > 0 {
				_, v := w.cert(row.seenAt)
				w.seen[w.remoteNode()] = record.SerialSeenOf(v)
			}
			if row.retired {
				w.revoke(revocation.IssueNodeSelfRevocation(w.identity))
			}
			rec, v := w.cert(row.present)
			peer := w.peer.Public()
			if row.wrongKey {
				peer = seedSigner(t, 0x98).Public()
			}
			d := Decide(w.facts(), peer, NewNodeExchange(rec))
			if row.advance {
				if d.SerialSeen != record.SerialSeenOf(v) {
					t.Errorf("SerialSeen = %+v, want the state of the presented certificate", d.SerialSeen)
				}
			} else if d.SerialSeen != (record.SerialSeen{}) {
				t.Errorf("SerialSeen = %+v, want none", d.SerialSeen)
			}
		})
	}
}

func TestDecidePerson(t *testing.T) {
	rows := []struct {
		rule   string
		setup  func(w *world) (Facts, ident.PublicKey, Exchange)
		state  State
		reason Reason
		level  admin.Level
	}{
		{"a listed person with a valid certificate is an authorized person at the lower of list level and cap", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.listPerson(admin.LevelFullControl, time.Time{})
			rec, _ := w.deviceCert(admin.LevelManage, epoch, endTime)
			return w.facts(), w.device.Public(), NewPersonExchange(rec)
		}, StateAuthorizedPerson, ReasonAuthorizedPerson, admin.LevelManage},
		{"the list level caps a device that allows more", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.listPerson(admin.LevelView, time.Time{})
			rec, _ := w.deviceCert(admin.LevelFullControl, epoch, endTime)
			return w.facts(), w.device.Public(), NewPersonExchange(rec)
		}, StateAuthorizedPerson, ReasonAuthorizedPerson, admin.LevelView},
		{"a person who is not listed is a non-member", func(w *world) (Facts, ident.PublicKey, Exchange) {
			rec, _ := w.deviceCert(admin.LevelManage, epoch, endTime)
			return w.facts(), w.device.Public(), NewPersonExchange(rec)
		}, StateNonMember, ReasonPersonNotAuthorized, admin.LevelNone},
		{"a person whose list entry expired is a non-member", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.listPerson(admin.LevelManage, epoch.Add(30*time.Minute))
			rec, _ := w.deviceCert(admin.LevelManage, epoch, endTime)
			return w.facts(), w.device.Public(), NewPersonExchange(rec)
		}, StateNonMember, ReasonPersonNotAuthorized, admin.LevelNone},
		{"a listed person presenting another key than the device key is refused", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.listPerson(admin.LevelManage, time.Time{})
			rec, _ := w.deviceCert(admin.LevelManage, epoch, endTime)
			return w.facts(), seedSigner(t, 0x97).Public(), NewPersonExchange(rec)
		}, StateUnauthenticated, ReasonKeyMismatch, admin.LevelNone},
		{"a listed person whose device key is revoked is refused", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.listPerson(admin.LevelManage, time.Time{})
			w.revoke(revocation.IssueDeviceKeyRevocation(w.root, w.device.Public()))
			rec, _ := w.deviceCert(admin.LevelManage, epoch, endTime)
			return w.facts(), w.device.Public(), NewPersonExchange(rec)
		}, StateUnauthenticated, ReasonCertificateRevoked, admin.LevelNone},
		{"a listed person whose root key is revoked is refused", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.listPerson(admin.LevelFullControl, time.Time{})
			w.revoke(revocation.IssuePersonSelfRevocation(w.root))
			rec, _ := w.deviceCert(admin.LevelFullControl, epoch, endTime)
			return w.facts(), w.device.Public(), NewPersonExchange(rec)
		}, StateUnauthenticated, ReasonPersonRevoked, admin.LevelNone},
		{"a listed person with an expired device certificate is refused", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.listPerson(admin.LevelManage, time.Time{})
			rec, _ := w.deviceCert(admin.LevelManage, epoch, epoch.Add(30*time.Minute))
			return w.facts(), w.device.Public(), NewPersonExchange(rec)
		}, StateUnauthenticated, ReasonCertificateExpired, admin.LevelNone},
		{"a listed person with a device certificate that has not begun is refused", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.listPerson(admin.LevelManage, time.Time{})
			rec, _ := w.deviceCert(admin.LevelManage, epoch.Add(2*time.Hour), endTime)
			return w.facts(), w.device.Public(), NewPersonExchange(rec)
		}, StateUnauthenticated, ReasonCertificateNotYetValid, admin.LevelNone},
		{"a node's certificate in a person message does not verify", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.listPerson(admin.LevelManage, time.Time{})
			rec, _ := w.cert(1)
			return w.facts(), w.device.Public(), NewPersonExchange(rec)
		}, StateUnauthenticated, ReasonCertificateInvalid, admin.LevelNone},
		{"no revocation set is never read as nothing revoked", func(w *world) (Facts, ident.PublicKey, Exchange) {
			w.listPerson(admin.LevelManage, time.Time{})
			rec, _ := w.deviceCert(admin.LevelManage, epoch, endTime)
			f := w.facts()
			f.Revocations = nil
			return f, w.device.Public(), NewPersonExchange(rec)
		}, StateUnauthenticated, ReasonRevocationsUnavailable, admin.LevelNone},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			f, peer, ex := row.setup(newWorld(t))
			d := Decide(f, peer, ex)
			if d.State != row.state || d.Reason != row.reason {
				t.Fatalf("got %v/%v, want %v/%v", d.State, d.Reason, row.state, row.reason)
			}
			if d.Standing.Level != row.level {
				t.Errorf("level %v, want %v", d.Standing.Level, row.level)
			}
			if row.state == StateUnauthenticated {
				if !d.Person.IsZero() || !d.DeviceCertificate.Digest().IsZero() {
					t.Errorf("a refused exchange carries identity: %+v", d)
				}
				return
			}
			if d.Person != personOf(t, seedSigner(t, 0x21)) || d.DeviceCertificate.Digest().IsZero() {
				t.Errorf("an accepted person exchange must name the person and keep the verified certificate: %+v", d)
			}
		})
	}
}

// A person is never a member and a node is never an authorized person, whatever
// the lists say: the state follows the kind of certificate.
func TestKindDecidesWhichStatesArePossible(t *testing.T) {
	w := newWorld(t).trusts().listPerson(admin.LevelFullControl, time.Time{})
	nodeRec, _ := w.cert(1)
	personRec, _ := w.deviceCert(admin.LevelFullControl, epoch, endTime)
	if d := Decide(w.facts(), w.peer.Public(), NewNodeExchange(nodeRec)); d.State != StateMember {
		t.Errorf("node: %v", d.State)
	}
	if d := Decide(w.facts(), w.device.Public(), NewPersonExchange(personRec)); d.State != StateAuthorizedPerson {
		t.Errorf("person: %v", d.State)
	}
}

// The golden certificates decide the same way as certificates built here: the
// bytes on the wire and the decision agree.
func TestDecideOnTheGoldenMessages(t *testing.T) {
	node, err := DecodeExchange(goldenNodeExchange)
	if err != nil {
		t.Fatal(err)
	}
	person, err := DecodeExchange(goldenPersonExchange)
	if err != nil {
		t.Fatal(err)
	}
	w := newWorld(t)
	w.now = epoch.Add(time.Hour)
	goldenNode := nodeOf(t, rfcSigner(t, 1))
	w.view.Credentials = []trust.Credential{{Digest: trust.CredentialDigest{9}, A: w.local, B: goldenNode}}
	l, _ := admin.NewList([]admin.Entry{{Person: personOf(t, rfcSigner(t, 3)), Level: admin.LevelFullControl}})
	w.list = l

	d := Decide(w.facts(), rfcSigner(t, 2).Public(), node)
	if d.State != StateMember || d.Node != goldenNode {
		t.Errorf("node: %v/%v %v", d.State, d.Reason, d.Node)
	}
	d = Decide(w.facts(), rfcSigner(t, 2).Public(), person)
	if d.State != StateAuthorizedPerson || d.Standing.Level != admin.LevelManage || d.Person != personOf(t, rfcSigner(t, 3)) {
		t.Errorf("person: %v/%v level %v", d.State, d.Reason, d.Standing.Level)
	}
}

func TestHeldMembers(t *testing.T) {
	rows := []struct {
		rule  string
		setup func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate])
		want  int // number of keys; they are all the remote node's network key
	}{
		{"the valid certificate of a directly trusted node", func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate]) {
			w.trusts()
			_, v := w.cert(1)
			return w.facts(), []record.Verified[record.NetworkKeyCertificate]{v}
		}, 1},
		{"the valid certificate of a node trusted by inheritance", func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate]) {
			w.trustsThrough(false)
			_, v := w.cert(1)
			return w.facts(), []record.Verified[record.NetworkKeyCertificate]{v}
		}, 1},
		{"the valid certificate of a node whose trust is suspended", func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate]) {
			w.trustsThrough(true)
			_, v := w.cert(1)
			return w.facts(), []record.Verified[record.NetworkKeyCertificate]{v}
		}, 1},
		{"a stranger's certificate is not a member's", func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate]) {
			_, v := w.cert(1)
			return w.facts(), []record.Verified[record.NetworkKeyCertificate]{v}
		}, 0},
		{"an expired certificate of a member", func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate]) {
			w.trusts()
			_, v := w.certWindow(1, epoch, epoch.Add(30*time.Minute))
			return w.facts(), []record.Verified[record.NetworkKeyCertificate]{v}
		}, 0},
		{"a certificate of a member that has not begun", func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate]) {
			w.trusts()
			_, v := w.certWindow(1, epoch.Add(2*time.Hour), endTime)
			return w.facts(), []record.Verified[record.NetworkKeyCertificate]{v}
		}, 0},
		{"a revoked certificate of a member", func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate]) {
			w.trusts()
			_, v := w.cert(1)
			w.revoke(revocation.IssueNetworkKeyCertificateRevocation(v, w.identity))
			return w.facts(), []record.Verified[record.NetworkKeyCertificate]{v}
		}, 0},
		{"a certificate of a retired node", func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate]) {
			w.trusts()
			_, v := w.cert(1)
			w.revoke(revocation.IssueNodeSelfRevocation(w.identity))
			return w.facts(), []record.Verified[record.NetworkKeyCertificate]{v}
		}, 0},
		{"a certificate older than the highest serial seen", func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate]) {
			w.trusts()
			_, newer := w.cert(2)
			w.seen[w.remoteNode()] = record.SerialSeenOf(newer)
			_, v := w.cert(1)
			return w.facts(), []record.Verified[record.NetworkKeyCertificate]{v}
		}, 0},
		{"the certificate seen at the highest serial", func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate]) {
			w.trusts()
			_, v := w.cert(2)
			w.seen[w.remoteNode()] = record.SerialSeenOf(v)
			return w.facts(), []record.Verified[record.NetworkKeyCertificate]{v}
		}, 1},
		{"the same key twice is listed once", func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate]) {
			w.trusts()
			_, v := w.cert(1)
			return w.facts(), []record.Verified[record.NetworkKeyCertificate]{v, v}
		}, 1},
		{"no revocation set gives none", func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate]) {
			w.trusts()
			_, v := w.cert(1)
			f := w.facts()
			f.Revocations = nil
			return f, []record.Verified[record.NetworkKeyCertificate]{v}
		}, 0},
		{"no time gives none", func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate]) {
			w.trusts()
			_, v := w.cert(1)
			f := w.facts()
			f.Now = time.Time{}
			return f, []record.Verified[record.NetworkKeyCertificate]{v}
		}, 0},
		{"a zero certificate gives none", func(w *world) (Facts, []record.Verified[record.NetworkKeyCertificate]) {
			w.trusts()
			return w.facts(), []record.Verified[record.NetworkKeyCertificate]{{}}
		}, 0},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			w := newWorld(t)
			f, certs := row.setup(w)
			got := HeldMembers(f, certs)
			if len(got) != row.want {
				t.Fatalf("got %d keys, want %d", len(got), row.want)
			}
			for _, k := range got {
				if k != w.network.Public() {
					t.Errorf("returned a key that is not the member's network key")
				}
			}
		})
	}
}
