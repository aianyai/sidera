package manageapi

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/pairing"
	"github.com/aianyai/sidera/internal/record"
)

// pairingParties makes the two ends of a pairing from fixed keys, through real
// certificates, and opens the transcript they bind, so that the identifier and the
// code of a record built on it are the same on every run.
func pairingParties(t *testing.T) (initiator, target pairing.Party, transcript pairing.Transcript) {
	t.Helper()
	party := func(seed byte) pairing.Party {
		identity, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
		if err != nil {
			t.Fatal(err)
		}
		network, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed + 100}, ed25519.SeedSize)))
		if err != nil {
			t.Fatal(err)
		}
		node, err := ident.NodeIDFromKey(identity.Public())
		if err != nil {
			t.Fatal(err)
		}
		cert := record.IssueNetworkKeyCertificate(node, network.Public(), 1, t0.Add(-time.Hour), t0.Add(24*time.Hour))
		sealed, err := record.Seal(record.NetworkKeyCertificateDescriptor, cert, identity)
		if err != nil {
			t.Fatal(err)
		}
		verified, err := record.Verify(record.NetworkKeyCertificateDescriptor, sealed)
		if err != nil {
			t.Fatal(err)
		}
		p, err := pairing.NewParty(verified, record.KeyRevocationClear, 1, network.Public(), t0)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	initiator, target = party(1), party(2)
	terms := pairing.Terms{Protocol: "/sidera/pairing/test", Initiator: initiator, Target: target, CreatedAt: t0}
	nA, nB := pairing.Nonce{1}, pairing.Nonce{2}
	commitment, err := terms.Commit(nA)
	if err != nil {
		t.Fatal(err)
	}
	transcript, err = pairing.Open(terms, commitment, nB, nA)
	if err != nil {
		t.Fatal(err)
	}
	return initiator, target, transcript
}

// The six methods of trust ask the engine for the operation of the catalogue each is
// mapped to, with the node written one way however the request spells it. A request
// for a pairing is one request with its addresses left out and with an empty list
// of them, and another when the addresses differ.
func TestTheCommandsOfTrust(t *testing.T) {
	r := newRig(t)
	peer := ownNode.String()
	send := func(id byte, method, params string) engine.ManagementCommand {
		t.Helper()
		before := len(r.svc.calls())
		if status, out := r.post(method, `{"operation_id":"`+opID(id)+`","params":`+params+`}`); status != 200 {
			t.Fatalf("%s answered %d %s", method, status, out)
		}
		calls := r.svc.calls()
		if len(calls) != before+1 {
			t.Fatalf("%d commands, want %d", len(calls), before+1)
		}
		return calls[before]
	}
	for i, tc := range []struct {
		method string
		kind   admin.OpKind
	}{
		{"trust.expect", admin.OpConfirmTrust},
		{"trust.forget", admin.OpRejectTrustRequest},
		{"trust.request", admin.OpConfirmTrust},
		{"trust.remove", admin.OpLocallyRevokeNode},
		{"trust.clear", admin.OpClearLocallyRevoked},
	} {
		c := send(byte(i+1), tc.method, `{"node":"`+peer+`"}`)
		want := sha256.Sum256([]byte("sidera-manage-v1\x00" + tc.method + "\x00" + `{"node":"` + peer + `"}` + "\x00" + "0"))
		if c.Name != tc.method || c.Expected != 0 || c.Operation.Admin().Kind != tc.kind || c.Fingerprint != want {
			t.Errorf("%s: command %+v, fingerprint %x want %x", tc.method, c, c.Fingerprint, want)
		}
	}
	rejected := send(13, "trust.reject", `{"pairing_id":"`+strings.Repeat("ab", 32)+`"}`)
	if rejected.Name != "trust.reject" || rejected.Operation.Admin().Kind != admin.OpRejectTrustRequest {
		t.Errorf("trust.reject: command %+v, want the operation that rejects a trust request", rejected)
	}
	const hint = "/ip4/10.0.0.1/tcp/7744"
	omitted := send(10, "trust.request", `{"node":"`+peer+`"}`)
	empty := send(11, "trust.request", `{"node":"`+peer+`","addresses":[]}`)
	hinted := send(12, "trust.request", `{"addresses":["`+hint+`"],"node":"`+peer+`"}`)
	if omitted.Fingerprint != empty.Fingerprint || omitted.Fingerprint == hinted.Fingerprint {
		t.Error("a request with no addresses differs from one with an empty list, or one with an address does not differ")
	}
	want := sha256.Sum256([]byte("sidera-manage-v1\x00trust.request\x00" + `{"node":"` + peer + `","addresses":["` + hint + `"]}` + "\x00" + "0"))
	if hinted.Fingerprint != want {
		t.Errorf("fingerprint %x, want %x", hinted.Fingerprint, want)
	}
}

// The words of each result, written out by hand: the empty list of credentials is a
// list, and a request that was refused has no identifier and no code but says why.
func TestTheResultsOfTrust(t *testing.T) {
	initiator, target, transcript := pairingParties(t)
	peer := target.Node()
	saved := pairing.InitiatorRecord{Transcript: transcript, ReceivedAt: t0, ExpiresAt: t0.Add(5 * time.Minute), Status: pairing.InitiatorActive}
	confirmed := saved
	confirmed.Confirmation, confirmed.Signature = &pairing.Confirmation{}, &record.Record{}
	complete := saved
	complete.Status = pairing.InitiatorComplete
	withdrawing := saved
	withdrawing.Status = pairing.InitiatorWithdrawing
	rejectedIncoming := pairing.TargetRecord{Transcript: transcript, ReceivedAt: t0, ExpiresAt: t0.Add(5 * time.Minute), Status: pairing.TargetRejected, Rejection: pairing.WithdrawDeclined}
	id, code := pairingIDText(saved.ID()), saved.Display().Code.String()
	if len(id) != 64 || len(code) != 7 || code[3] != ' ' {
		t.Fatalf("the identifier %q and the code %q are not in the forms of the interface", id, code)
	}
	digest := record.Digest{0xab, 31: 0xcd}
	digestText := "ab" + strings.Repeat("00", 30) + "cd"

	for _, tc := range []struct {
		name string
		got  func() ([]byte, error)
		want string
	}{
		{"an expectation", func() ([]byte, error) {
			return expectedResult(engine.NodeExpected{Node: peer, ExpiresAt: t0.Add(time.Hour)})
		}, `{"node":"` + peer.String() + `","expires_at":"2026-10-24T10:00:00Z"}`},
		{"a forgotten expectation", func() ([]byte, error) { return forgottenResult(peer) }, `{"node":"` + peer.String() + `","forgotten":true}`},
		{"a request that waits for this owner", func() ([]byte, error) {
			return requestedResult(engine.PairingRequested{Node: peer, Saved: &saved})
		}, `{"pairing_id":"` + id + `","node":"` + peer.String() + `","status":"awaiting_initiator_signature","code":"` + code + `"}`},
		{"a request that waits for the target", func() ([]byte, error) {
			return requestedResult(engine.PairingRequested{Node: peer, Saved: &confirmed})
		}, `{"pairing_id":"` + id + `","node":"` + peer.String() + `","status":"awaiting_target","code":"` + code + `"}`},
		{"a request that is complete", func() ([]byte, error) {
			return requestedResult(engine.PairingRequested{Node: peer, Saved: &complete})
		}, `{"pairing_id":"` + id + `","node":"` + peer.String() + `","status":"complete","code":"` + code + `"}`},
		{"a request the target refused", func() ([]byte, error) {
			return requestedResult(engine.PairingRequested{Node: peer, Refusal: pairing.RefusalBusy})
		}, `{"pairing_id":"","node":"` + peer.String() + `","status":"refused","code":"","refusal":"busy"}`},
		{"a removal that revoked a credential", func() ([]byte, error) {
			return removedResult(engine.NodeRemoved{Node: peer, Credentials: []record.Digest{digest}})
		}, `{"node":"` + peer.String() + `","revoked_credentials":["` + digestText + `"]}`},
		{"a removal that revoked nothing", func() ([]byte, error) { return removedResult(engine.NodeRemoved{Node: peer, Already: true}) },
			`{"node":"` + peer.String() + `","revoked_credentials":[]}`},
		{"a request this node held, rejected", func() ([]byte, error) {
			return rejectedResult(engine.PairingRejected{Incoming: &rejectedIncoming})
		}, `{"pairing_id":"` + id + `","node":"` + initiator.Node().String() + `","status":"rejected"}`},
		{"a request this node started, withdrawn", func() ([]byte, error) {
			return rejectedResult(engine.PairingRejected{Outgoing: &withdrawing})
		}, `{"pairing_id":"` + id + `","node":"` + peer.String() + `","status":"withdrawing"}`},
		{"taking a node back", func() ([]byte, error) {
			return clearedResult(engine.NodeCleared{Node: peer, NewPairingRequired: true})
		}, `{"node":"` + peer.String() + `","new_pairing_required":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.got()
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// trust.list answers every list, empty ones as [], in the words of the interface;
// and each record is named by the state it is in, the same way wherever it is
// shown.
func TestTrustListWordsTheViewAndNamesEachRecordByItsState(t *testing.T) {
	r := newRig(t)
	status, out := r.post("trust.list", `{}`)
	if want := `{"result":{"trusted":[],"expectations":[],"incoming":[],"outgoing":[],"locally_revoked":[]}}`; status != 200 || strings.TrimSpace(string(out)) != want {
		t.Errorf("an empty view: %d %s", status, out)
	}

	initiator, target, transcript := pairingParties(t)
	signature := &record.Record{}
	confirmation := &pairing.Confirmation{}
	for _, tc := range []struct {
		name string
		rec  pairing.InitiatorRecord
		want string
	}{
		{"active, not confirmed", pairing.InitiatorRecord{Status: pairing.InitiatorActive}, "awaiting_initiator_signature"},
		{"active, confirmed without a signature", pairing.InitiatorRecord{Status: pairing.InitiatorActive, Confirmation: confirmation}, "awaiting_initiator_signature"},
		{"active, confirmed and signed", pairing.InitiatorRecord{Status: pairing.InitiatorActive, Confirmation: confirmation, Signature: signature}, "awaiting_target"},
		{"complete", pairing.InitiatorRecord{Status: pairing.InitiatorComplete}, "complete"},
		{"withdrawing", pairing.InitiatorRecord{Status: pairing.InitiatorWithdrawing}, "withdrawing"},
		{"rejected", pairing.InitiatorRecord{Status: pairing.InitiatorRejected}, "rejected"},
		{"gave up", pairing.InitiatorRecord{Status: pairing.InitiatorGaveUp}, "gave_up"},
	} {
		if got := initiatorStatusName(tc.rec); got != tc.want {
			t.Errorf("an initiator record that is %s is named %q, want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		rec  pairing.TargetRecord
		want string
	}{
		{"pending without the signature", pairing.TargetRecord{Status: pairing.TargetPending}, "awaiting_initiator_signature"},
		{"pending with the signature", pairing.TargetRecord{Status: pairing.TargetPending, InitiatorSignature: signature}, "awaiting_target"},
		{"complete", pairing.TargetRecord{Status: pairing.TargetComplete}, "complete"},
		{"rejected", pairing.TargetRecord{Status: pairing.TargetRejected}, "rejected"},
		{"expired", pairing.TargetRecord{Status: pairing.TargetExpired}, "expired"},
		{"cancelled", pairing.TargetRecord{Status: pairing.TargetCancelled}, "cancelled"},
	} {
		if got := targetStatusName(tc.rec); got != tc.want {
			t.Errorf("a target record that is %s is named %q, want %q", tc.name, got, tc.want)
		}
	}

	// A view with an entry in every list. The records are those of one transcript,
	// seen from each end: the initiator's node is the remote of the target's record
	// and the target's node is the remote of the initiator's.
	out1 := pairing.InitiatorRecord{Transcript: transcript, ReceivedAt: t0, ExpiresAt: t0.Add(time.Minute), Status: pairing.InitiatorWithdrawn, Reason: pairing.WithdrawDeclined}
	in1 := pairing.TargetRecord{Transcript: transcript, ReceivedAt: t0, ExpiresAt: t0.Add(5 * time.Minute), Status: pairing.TargetPending, Expected: pairing.LocalAuthor()}
	revoked, err := ident.NodeIDFromBytes([]byte(strings.Repeat("\x09", ident.NodeIDLen)))
	if err != nil {
		t.Fatal(err)
	}
	digest := record.Digest{1}
	r.svc.trust = engine.TrustView{
		Trusted: []engine.TrustedNode{
			{Node: target.Node(), Credential: digest, CreatedAt: t0, NotAfter: t0.Add(48 * time.Hour), Connected: true},
			{Node: initiator.Node(), Credential: digest, CreatedAt: t0},
		},
		Expectations:   []engine.ExpectedNode{{Node: target.Node(), ExpiresAt: t0.Add(time.Hour)}},
		Incoming:       []pairing.TargetRecord{in1},
		Outgoing:       []pairing.InitiatorRecord{out1},
		LocallyRevoked: []ident.NodeID{revoked},
	}
	status, body := r.post("trust.list", `{}`)
	if status != 200 {
		t.Fatalf("answered %d %s", status, body)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(got["result"], &result); err != nil {
		t.Fatal(err)
	}
	digestText := "01" + strings.Repeat("00", 31)
	for name, want := range map[string]string{
		"trusted": `[{"node":"` + target.Node().String() + `","credential":"` + digestText + `","created_at":"2026-10-24T09:00:00Z","not_after":"2026-10-26T09:00:00Z","connected":true},` +
			`{"node":"` + initiator.Node().String() + `","credential":"` + digestText + `","created_at":"2026-10-24T09:00:00Z","connected":false}]`,
		"expectations": `[{"node":"` + target.Node().String() + `","expires_at":"2026-10-24T10:00:00Z"}]`,
		"incoming": `[{"pairing_id":"` + pairingIDText(in1.ID()) + `","node":"` + initiator.Node().String() + `","status":"awaiting_initiator_signature","code":"` +
			in1.Display().Code.String() + `","expected":true,"expires_at":"2026-10-24T09:05:00Z"}]`,
		"outgoing": `[{"pairing_id":"` + pairingIDText(out1.ID()) + `","node":"` + target.Node().String() + `","status":"withdrawn","code":"` +
			out1.Display().Code.String() + `","reason":"declined"}]`,
		"locally_revoked": `["` + revoked.String() + `"]`,
	} {
		if string(result[name]) != want {
			t.Errorf("%s\n%s\nwant\n%s", name, result[name], want)
		}
	}
}

// A node's profile is a member of its row in trust.list and network.show, with the
// members the node gave and the time this node got it; a node with none has no
// member, and a member the node did not give is left out of the profile.
func TestTheProfileOfANodeIsInItsRowOfTrustListAndNetworkShow(t *testing.T) {
	r := newRig(t)
	nodeOf := func(b byte) ident.NodeID {
		node, err := ident.NodeIDFromBytes([]byte(strings.Repeat(string([]byte{b}), ident.NodeIDLen)))
		if err != nil {
			t.Fatal(err)
		}
		return node
	}
	known, unknown := nodeOf(5), nodeOf(6)
	full := &engine.ProfileView{
		Name: "build box", HostName: "inuc", Platform: "linux/amd64", System: "Ubuntu 24.04.1 LTS",
		SystemBuild: "6.8.0-45-generic", SideraVersion: "0.17.0", IssuedAt: t0, AsOf: t0.Add(time.Minute),
	}
	partial := &engine.ProfileView{Name: "nameless", AsOf: t0.Add(time.Minute)}
	asOf := strconv.FormatInt(full.AsOf.Unix(), 10)
	fullJSON := `{"name":"build box","host_name":"inuc","platform":"linux/amd64","system":"Ubuntu 24.04.1 LTS","system_build":"6.8.0-45-generic","sidera_version":"0.17.0","as_of":` + asOf + `}`
	partialJSON := `{"name":"nameless","as_of":` + asOf + `}`
	for _, tc := range []struct {
		name    string
		profile *engine.ProfileView
		want    string // the member of the row, or "" when the row has none
	}{
		{"every member given", full, `,"profile":` + fullJSON},
		{"some members given", partial, `,"profile":` + partialJSON},
		{"none fetched", nil, ``},
	} {
		node := known
		if tc.profile == nil {
			node = unknown
		}
		r.svc.trust = engine.TrustView{Trusted: []engine.TrustedNode{{Node: node, Credential: record.Digest{1}, CreatedAt: t0, Profile: tc.profile}}}
		r.svc.network = engine.NetworkView{Peers: []engine.NetworkPeer{{Node: node, Profile: tc.profile}}}
		wantTrusted := `[{"node":"` + node.String() + `","credential":"01` + strings.Repeat("00", 31) + `","created_at":"2026-10-24T09:00:00Z","connected":false` + tc.want + `}]`
		wantPeers := `[{"node":"` + node.String() + `","addresses":[],"certificate_held":false,"connected":false` + tc.want + `}]`
		for method, c := range map[string]struct{ member, want string }{
			"trust.list": {"trusted", wantTrusted}, "network.show": {"peers", wantPeers},
		} {
			status, body := r.post(method, `{}`)
			var got struct {
				Result map[string]json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(body, &got); err != nil || status != 200 {
				t.Fatalf("%s: %d %s %v", method, status, body, err)
			}
			if string(got.Result[c.member]) != c.want {
				t.Errorf("%s, %s:\n%s\nwant\n%s", method, tc.name, got.Result[c.member], c.want)
			}
		}
	}
}
