package store_test

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/recfile"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/record"
)

// The four writes of the trust and peer kinds, and the cross-owner writes of the
// local removal and the revocation, put their records where a scan finds them,
// as they were given.
func TestTrustAndPeerRecordsComeBackFromTheDisk(t *testing.T) {
	r := newRig(t)
	cred, rev, peer := credentialOf(t, 1, 2), revocationOf(t, 3, 4), peerOf(t, 2, 1, 3)
	mustDo(t, r.st.TrustCredential(cred, &peer))
	mustDo(t, r.st.TrustRevocation(rev))
	newer := peerOf(t, 2, 2, 3) // the serial seen is above the serial of the certificate
	mustDo(t, r.st.PeerCert(newer))

	local := localRevokedOf(t, 5)
	revoked := revocationOf(t, 5, 6)
	pl, err := store.PrepareLocalRevoked(local)
	mustDo(t, err)
	pr, err := store.PrepareCredRevocation(revoked)
	mustDo(t, err)
	res, err := store.PrepareAdminResult(adminResult(1))
	mustDo(t, err)
	tx := r.w.Begin(&declXRevoke)
	mustDo(t, store.PutLocalRevoked(tx, store.RoleLocalRevoked, pl))
	mustDo(t, store.PutCredRevocation(tx, store.RoleCredRevocation, pr))
	mustDo(t, store.PutAdminResult(tx, store.RoleResult, res))
	mustDo(t, tx.Commit())

	sc := r.scan()
	if len(sc.Damaged) != 0 || len(sc.Unknown) != 0 {
		t.Fatalf("damaged %v, unknown %v", sc.Damaged, sc.Unknown)
	}
	if len(sc.Credentials) != 1 || !equalRecords(sc.Credentials[0], cred) {
		t.Errorf("credentials: %#v", sc.Credentials)
	}
	if len(sc.CredRevocations) != 2 {
		t.Fatalf("revocations: %#v", sc.CredRevocations)
	}
	for _, want := range []store.CredRevocationRecord{rev, revoked} {
		found := false
		for _, got := range sc.CredRevocations {
			found = found || equalRecords(got, want)
		}
		if !found {
			t.Errorf("the revocation of %s is not in the scan", want.Revoked)
		}
	}
	if len(sc.Peers) != 1 || !equalRecords(sc.Peers[0], newer) {
		t.Errorf("peers: %#v, want the replacement %#v", sc.Peers, newer)
	}
	if len(sc.LocalRevoked) != 1 || !equalRecords(sc.LocalRevoked[0], local) {
		t.Errorf("local-revoked: %#v", sc.LocalRevoked)
	}
}

// The owner takes one node back into trust and the other stays; a node with no
// record is not an error and writes nothing.
func TestLocalRevokedClearRemovesOnlyThatNode(t *testing.T) {
	r := newRig(t)
	seed(t, r.mem, localRevokedOf(t, 1), localRevokedOf(t, 2))
	mustDo(t, r.st.LocalRevokedClear(trustNode(t, 1)))
	if got := r.scan().LocalRevoked; len(got) != 1 || got[0].Node != trustNode(t, 2) {
		t.Errorf("after the clear: %#v", got)
	}
	before := r.rec.count()
	mustDo(t, r.st.LocalRevokedClear(trustNode(t, 1)))
	if r.rec.count() != before {
		t.Error("clearing a node with no record wrote something")
	}
	if err := r.st.LocalRevokedClear(store.LocalRevokedRecord{}.Node); err == nil {
		t.Error("the zero node was accepted")
	}
}

// Trust and the keys of peers are kept for good: no retention ends and no sweep
// takes one, however late it is.
func TestTrustAndPeerRecordsAreNeverSwept(t *testing.T) {
	recs := []store.Record{credentialOf(t, 1, 2), revocationOf(t, 1, 2), localRevokedOf(t, 1), peerOf(t, 2, 1, 1)}
	for _, rec := range recs {
		if until, finished := store.RetainUntil(rec); finished || !until.IsZero() {
			t.Errorf("%T: RetainUntil = %v, %v, want no end", rec, until, finished)
		}
	}
	r := newRig(t)
	seed(t, r.mem, recs...)
	plan := store.PlanSweep(r.scan(), base.Add(100*365*24*time.Hour))
	if n := len(plan.Records) + len(plan.Ctl) + len(plan.Outs) + len(plan.OutLosts) + len(plan.Endpoints); n != 0 {
		t.Errorf("a sweep far in the future takes %d of them: %+v", n, plan)
	}
}

// A file of a kind of state/trust/ or state/peers/ is read as its kind says and
// is refused, as a damaged body, when its body is not what the key and the kind
// make it: the key names another record, the bytes are another type, the encoding
// is not the canonical one, or a field breaks its rule. The writer refuses the
// same values, so nothing it writes can be damaged.
func TestTrustAndPeerRecordsAreHeldToTheirRules(t *testing.T) {
	cred, rev := credentialOf(t, 1, 2), revocationOf(t, 1, 2)
	credWire, revWire := mustWire(t, cred.Record), mustWire(t, rev.Record)
	peer := peerOf(t, 2, 2, 3)
	certWire := mustWire(t, peer.Certificate)
	otherNode := peerOf(t, 3, 1, 1)
	localGood := string(bodyOf(t, localRevokedOf(t, 1)))
	peerGood := string(bodyOf(t, peer))

	keyOf := func(rec store.Record) string { _, key, _ := splitFile(t, rec); return key }
	credKey, otherCredKey := keyOf(cred), keyOf(credentialOf(t, 1, 3))
	node1, node2 := keyOf(localRevokedOf(t, 1)), keyOf(localRevokedOf(t, 2))
	peerKey := keyOf(peer)
	peerBody := func(cert []byte, seen string, digest string) string {
		return `{"cert":"` + base64.RawURLEncoding.EncodeToString(cert) + `","serial_seen":` + seen + `,"digest":"` + digest + `"}`
	}
	certDigest := peer.Certificate.Digest().String()
	seenDigest := peer.SeenDigest.String() // peer is at serial 2 with serial 3 seen

	for _, c := range []struct {
		name       string
		kind       store.Kind
		key, body  string
		wantDamage bool
	}{
		{"a credential", store.KindCredential, credKey, string(credWire), false},
		{"a revocation", store.KindCredRevocation, keyOf(rev), string(revWire), false},
		{"a local removal", store.KindLocalRevoked, node1, localGood, false},
		{"a peer", store.KindPeer, peerKey, peerGood, false},

		{"a credential under the key of another", store.KindCredential, otherCredKey, string(credWire), true},
		{"a revocation in the place of a credential", store.KindCredential, credKey, string(revWire), true},
		{"a credential in the place of a revocation", store.KindCredRevocation, credKey, string(credWire), true},
		{"a revocation under the key of another credential", store.KindCredRevocation, otherCredKey, string(revWire), true},
		{"a credential with trailing bytes", store.KindCredential, credKey, string(credWire) + "\x00", true},
		{"a credential cut short", store.KindCredential, credKey, string(credWire[:len(credWire)-1]), true},
		{"an empty credential", store.KindCredential, credKey, "", true},

		{"a local removal of another node than the key's", store.KindLocalRevoked, node2, localGood, true},
		{"a local removal with a spaced body", store.KindLocalRevoked, node1, strings.Replace(localGood, `","at"`, `", "at"`, 1), true},
		{"a local removal with an extra field", store.KindLocalRevoked, node1, strings.Replace(localGood, `{`, `{"x":1,`, 1), true},
		{"a local removal with no operation", store.KindLocalRevoked, node1, strings.Replace(localGood, `"trust.revoke-node"`, `""`, 1), true},
		{"a local removal with an operation of capitals", store.KindLocalRevoked, node1, strings.Replace(localGood, `trust.revoke-node`, `Trust`, 1), true},
		{"a local removal at the zero time", store.KindLocalRevoked, node1, strings.Replace(localGood, `2026-10-24T09:00:00Z`, `0001-01-01T00:00:00Z`, 1), true},

		{"a peer whose serial seen is above its certificate", store.KindPeer, peerKey, peerBody(certWire, "9", seenDigest), false},
		{"a peer whose serial seen is its certificate's, with that digest", store.KindPeer, peerKey, peerBody(certWire, "2", certDigest), false},
		{"a peer whose serial seen is its certificate's, with another digest", store.KindPeer, peerKey, peerBody(certWire, "2", seenDigest), true},
		{"a peer whose serial seen is below its certificate", store.KindPeer, peerKey, peerBody(certWire, "1", certDigest), true},
		{"a peer with no serial seen", store.KindPeer, peerKey, peerBody(certWire, "0", certDigest), true},
		{"a peer with a zero digest seen", store.KindPeer, peerKey, peerBody(certWire, "3", strings.Repeat("0", 64)), true},
		{"a peer with a digest in capitals", store.KindPeer, peerKey, peerBody(certWire, "3", strings.ToUpper(seenDigest)), true},
		{"a peer with a digest that is not hex", store.KindPeer, peerKey, peerBody(certWire, "3", "zz"), true},
		{"a peer with the certificate of another node", store.KindPeer, peerKey, peerBody(mustWire(t, otherNode.Certificate), "3", otherNode.Certificate.Digest().String()), true},
		{"a peer whose certificate is a credential", store.KindPeer, peerKey, peerBody(credWire, "3", cred.Digest.String()), true},
		{"a peer with a certificate that is not a record", store.KindPeer, peerKey, peerBody([]byte("not a record"), "3", certDigest), true},
		{"a peer with padded base64", store.KindPeer, peerKey, strings.Replace(peerGood, `","serial_seen"`, `=","serial_seen"`, 1), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			file, err := recfile.EncodeFrame(string(c.kind), []byte(c.key), []byte(c.body))
			if err != nil {
				t.Fatal(err)
			}
			rec, dmg := store.DecodeRecord(c.kind.Dir(), nameOf(string(c.kind), c.key), file)
			if (dmg != nil) != c.wantDamage {
				t.Fatalf("damage = %+v, want damage %v", dmg, c.wantDamage)
			}
			if dmg != nil {
				if dmg.Class != store.BodyDamaged || dmg.Reason != store.ReasonBodyInvalid {
					t.Errorf("damage = class %d reason %d (%s), want a body damage of an invalid body", dmg.Class, dmg.Reason, dmg.Detail)
				}
				return
			}
			again, err := store.EncodeRecord(rec)
			if err != nil || !bytes.Equal(again, file) {
				t.Errorf("decode then encode: %v\n in  %q\n out %q", err, file, again)
			}
		})
	}

	// What the decoder refuses the writer refuses, for the shapes only a caller can
	// make: a key that does not match the record held with it.
	for name, rec := range map[string]store.Record{
		"a credential under another digest":          store.CredentialRecord{Digest: record.Digest{1}, Record: cred.Record},
		"a revocation for another credential":        store.CredRevocationRecord{Revoked: record.Digest{1}, Record: rev.Record},
		"a credential that is a revocation":          store.CredentialRecord{Digest: rev.Record.Digest(), Record: rev.Record},
		"a peer under another node":                  store.PeerRecord{Node: trustNode(t, 3), Certificate: peer.Certificate, SerialSeen: 3, SeenDigest: peer.SeenDigest},
		"a peer whose serial seen is too low":        store.PeerRecord{Node: peer.Node, Certificate: peer.Certificate, SerialSeen: 1, SeenDigest: peer.SeenDigest},
		"a peer with no digest seen":                 store.PeerRecord{Node: peer.Node, Certificate: peer.Certificate, SerialSeen: 3},
		"a peer with a digest foreign to its serial": store.PeerRecord{Node: peer.Node, Certificate: peer.Certificate, SerialSeen: 2, SeenDigest: peer.SeenDigest},
		"a local removal without a node":             store.LocalRevokedRecord{At: base, Operation: "trust.revoke-node"},
		"a local removal without a time":             store.LocalRevokedRecord{Node: trustNode(t, 1), Operation: "trust.revoke-node"},
		"a local removal with a blank operation":     store.LocalRevokedRecord{Node: trustNode(t, 1), At: base, Operation: " "},
	} {
		if _, err := store.EncodeRecord(rec); err == nil {
			t.Errorf("%s: EncodeRecord accepted it", name)
		}
	}
}

func mustWire(t *testing.T, rec record.Record) []byte {
	t.Helper()
	b, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// splitFile is the kind, the key and the body of the file the store writes for rec.
func splitFile(t *testing.T, rec store.Record) (kind, key string, body []byte) {
	t.Helper()
	fr, dmg := recfile.DecodeFrame(mustEncode(t, rec))
	if dmg != nil {
		t.Fatalf("%+v", dmg)
	}
	return fr.Kind, string(fr.Key), fr.Body
}

func bodyOf(t *testing.T, rec store.Record) []byte {
	t.Helper()
	_, _, body := splitFile(t, rec)
	return body
}
