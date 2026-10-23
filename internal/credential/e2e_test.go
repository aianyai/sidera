package credential

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// The tests in this file run records through the packages that judge them:
// everything a decision function reads here was produced by signing and
// verifying real records, and the verdicts are those of packages admin and
// trust.

func makeDirective(t testing.TB, node ident.NodeID, digest trust.CredentialDigest, x, y ident.NodeID, signedAt time.Time, cert record.Record) record.Verified[OfflineDirective] {
	t.Helper()
	d, err := NewOfflineDirective(node, digest, x, y, signedAt, cert)
	if err != nil {
		t.Fatal(err)
	}
	r, err := record.Seal(DirectiveDescriptor, d, signerOf(t, keyDevice))
	if err != nil {
		t.Fatal(err)
	}
	v, err := record.Verify(DirectiveDescriptor, mustDecode(t, mustEncode(t, r)))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func listWith(t testing.TB, level admin.Level) admin.List {
	t.Helper()
	l, problems := admin.NewList([]admin.Entry{{Person: personOf(t, keyRoot), Level: level}})
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	return l
}

// TestDirectiveThroughAdminAndTrust: record -> admin.AcceptDirective ->
// trust.ApplyDirective, over the cases that decide the outcome.
func TestDirectiveThroughAdminAndTrust(t *testing.T) {
	a, b, c := nodeOf(t, keyA), nodeOf(t, keyB), nodeOf(t, keyOther)
	cred := verifiedCredential(t, sampleCredential(t))
	digest := trust.CredentialDigest(cred.Digest)
	view := func() trust.View {
		return trust.View{Local: a, Settings: trust.DefaultSettings(), Credentials: []trust.Credential{Facts(cred)}}
	}
	validCert := func(level admin.Level) record.Record {
		return deviceCertRecord(t, keyRoot, keyDevice, level, t0, t0.Add(2*time.Hour))
	}
	offlineSince := t0
	signed := t0.Add(1000 * time.Second)
	now := t0.Add(2000 * time.Second)

	tests := []struct {
		name        string
		list        admin.List
		cert        record.Record
		node        ident.NodeID
		ends        [2]ident.NodeID
		signedAt    time.Time
		now         time.Time
		revoked     CertRevocation
		wantAdmin   admin.Disposition
		wantReason  admin.Reason
		wantTrust   trust.DirectiveDisposition // checked only when admin executes
		wantRevokes bool
	}{
		{name: "manage on the list and in the certificate", list: listWith(t, admin.LevelManage), cert: validCert(admin.LevelManage),
			wantAdmin: admin.DispositionExecute, wantReason: admin.ReasonOK, wantTrust: trust.DirectiveApply, wantRevokes: true},
		{name: "certificate without a cap is full control, the list level decides", list: listWith(t, admin.LevelManage), cert: validCert(admin.LevelFullControl),
			wantAdmin: admin.DispositionExecute, wantReason: admin.ReasonOK, wantTrust: trust.DirectiveApply, wantRevokes: true},
		{name: "certificate cap view with full control on the list", list: listWith(t, admin.LevelFullControl), cert: validCert(admin.LevelView),
			wantAdmin: admin.DispositionWarn, wantReason: admin.ReasonDeviceCapTooLow},
		{name: "list level view with a manage certificate", list: listWith(t, admin.LevelView), cert: validCert(admin.LevelManage),
			wantAdmin: admin.DispositionWarn, wantReason: admin.ReasonListLevelTooLow},
		{name: "certificate expired now but valid when the directive was signed", list: listWith(t, admin.LevelManage), cert: validCert(admin.LevelManage),
			now: t0.Add(5 * time.Hour), wantAdmin: admin.DispositionExecute, wantReason: admin.ReasonOK, wantTrust: trust.DirectiveApply, wantRevokes: true},
		{name: "temporary sub-key: ten minutes of life, used after it ended", list: listWith(t, admin.LevelManage),
			cert:     deviceCertRecord(t, keyRoot, keyDevice, admin.LevelManage, t0, t0.Add(10*time.Minute)),
			signedAt: t0.Add(5 * time.Minute), now: t0.Add(3 * time.Hour),
			wantAdmin: admin.DispositionExecute, wantReason: admin.ReasonOK, wantTrust: trust.DirectiveApply, wantRevokes: true},
		{name: "temporary sub-key: signed after it ended", list: listWith(t, admin.LevelManage),
			cert:     deviceCertRecord(t, keyRoot, keyDevice, admin.LevelManage, t0, t0.Add(10*time.Minute)),
			signedAt: t0.Add(11 * time.Minute), now: t0.Add(3 * time.Hour),
			wantAdmin: admin.DispositionReject, wantReason: admin.ReasonCertExpired},
		{name: "signed before the certificate starts", list: listWith(t, admin.LevelManage), cert: validCert(admin.LevelManage),
			signedAt: t0.Add(-30 * time.Second), wantAdmin: admin.DispositionReject, wantReason: admin.ReasonCertNotYetValid},
		{name: "certificate revoked", list: listWith(t, admin.LevelManage), cert: validCert(admin.LevelManage),
			revoked: CertRevocation{CertRevoked: true}, wantAdmin: admin.DispositionReject, wantReason: admin.ReasonCertRevoked},
		{name: "root key revoked", list: listWith(t, admin.LevelManage), cert: validCert(admin.LevelManage),
			revoked: CertRevocation{RootKeyRevoked: true}, wantAdmin: admin.DispositionReject, wantReason: admin.ReasonRootKeyRevoked},
		{name: "signer not on the list is only a warning", list: admin.List{}, cert: validCert(admin.LevelManage),
			wantAdmin: admin.DispositionWarn, wantReason: admin.ReasonNotOnList},
		{name: "addressed to another node", list: listWith(t, admin.LevelManage), cert: validCert(admin.LevelManage), node: b,
			wantAdmin: admin.DispositionReject, wantReason: admin.ReasonDirectiveNotForThisNode},
		{name: "claimed ends differ from the credential's", list: listWith(t, admin.LevelManage), cert: validCert(admin.LevelManage),
			ends: [2]ident.NodeID{a, c}, wantAdmin: admin.DispositionExecute, wantReason: admin.ReasonOK, wantTrust: trust.DirectiveMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			node, ends, at, n := tc.node, tc.ends, tc.signedAt, tc.now
			if node.IsZero() {
				node = a
			}
			if ends[0].IsZero() {
				ends = [2]ident.NodeID{a, b}
			}
			if at.IsZero() {
				at = signed
			}
			if n.IsZero() {
				n = now
			}
			v := makeDirective(t, node, digest, ends[0], ends[1], at, tc.cert)
			got := admin.AcceptDirective(a, tc.list, v.Payload.AdminDirective(tc.revoked), offlineSince, n)
			if got.Disposition != tc.wantAdmin || got.Reason != tc.wantReason {
				t.Fatalf("admin: %v %v, want %v %v", got.Disposition, got.Reason, tc.wantAdmin, tc.wantReason)
			}
			if tc.wantAdmin != admin.DispositionExecute {
				return
			}
			tv := trust.ApplyDirective(view(), v.Payload.TrustDirective())
			if tv.Disposition != tc.wantTrust {
				t.Fatalf("trust: %v, want %v", tv.Disposition, tc.wantTrust)
			}
			if tc.wantRevokes {
				if len(tv.Effects.RevokeCredentials) != 1 || tv.Effects.RevokeCredentials[0] != digest ||
					len(tv.Effects.StopVouching) != 1 || tv.Effects.StopVouching[0] != digest {
					t.Fatalf("effects = %+v", tv.Effects)
				}
				// Carrying the effect out makes the direct trust go away.
				w := view()
				if out := trust.Evaluate(w, b, n); out.Outcome != trust.AllowedDirect {
					t.Fatalf("before: %v %v", out.Outcome, out.Reason)
				}
				w.RevokedCredentials = trust.DigestSet{tv.Effects.RevokeCredentials[0]: {}}
				if out := trust.Evaluate(w, b, n); out.Outcome != trust.Denied || out.Reason != trust.ReasonCredentialRevoked {
					t.Fatalf("after: %v %v", out.Outcome, out.Reason)
				}
			} else if len(tv.Effects.RevokeCredentials) != 0 {
				t.Fatalf("effects without apply: %+v", tv.Effects)
			}
		})
	}
}

func TestDirectiveForAnEdgeOfOthers(t *testing.T) {
	a, b, c := nodeOf(t, keyA), nodeOf(t, keyB), nodeOf(t, keyOther)
	bc := verifiedCredential2(t, New(b, c, t0, time.Time{}, Nonce{5}), keyB, keyOther)
	// Node A holds the credential B-C in its two-hop view, and a directive for it
	// reaches A (the administrator may address any node on whose list it is).
	v := makeDirective(t, a, trust.CredentialDigest(bc.Digest), b, c, t0.Add(time.Second), goldenCert(t))
	view := trust.View{Local: a, Settings: trust.DefaultSettings(), Credentials: []trust.Credential{Facts(bc)}}
	if got := trust.ApplyDirective(view, v.Payload.TrustDirective()); got.Disposition != trust.DirectiveNotForThisNode {
		t.Errorf("disposition = %v, want not-for-this-node", got.Disposition)
	}
	// Once revoked, a repeated directive is already effective.
	own := verifiedCredential(t, sampleCredential(t))
	view2 := trust.View{Local: a, Settings: trust.DefaultSettings(), Credentials: []trust.Credential{Facts(own)},
		RevokedCredentials: trust.DigestSet{trust.CredentialDigest(own.Digest): {}}}
	v2 := makeDirective(t, a, trust.CredentialDigest(own.Digest), a, b, t0.Add(time.Second), goldenCert(t))
	if got := trust.ApplyDirective(view2, v2.Payload.TrustDirective()); got.Disposition != trust.DirectiveAlreadyEffective {
		t.Errorf("disposition = %v, want already-effective", got.Disposition)
	}
}

// TestVoucherThroughTrust: voucher -> CheckVoucher -> trust.Vouchers.Accept ->
// trust.Evaluate, for the chain local - m - target.
func TestVoucherThroughTrust(t *testing.T) {
	const (
		kLocal  = keyOther
		kM      = keyA
		kTarget = keyB
	)
	local, m, target := nodeOf(t, kLocal), nodeOf(t, kM), nodeOf(t, kTarget)
	lm := verifiedCredential2(t, New(local, m, t0, time.Time{}, Nonce{1}), kLocal, kM)
	mt := verifiedCredential2(t, New(m, target, t0, time.Time{}, Nonce{2}), kM, kTarget)
	creds := map[trust.CredentialDigest]trust.Credential{
		trust.CredentialDigest(lm.Digest): Facts(lm), trust.CredentialDigest(mt.Digest): Facts(mt),
	}
	now := t0.Add(time.Hour)
	voucherOf := func(issuer int, issuerNode ident.NodeID, edge record.Digest, issuedAt time.Time, counter uint64) record.Verified[Voucher] {
		net := keyNetA
		if issuer == kTarget {
			net = keyNetB
		}
		d := VoucherDescriptor(acceptedNetworkKey(t, issuer, net), now)
		r, err := record.Seal(d, Voucher{Issuer: issuerNode, Edge: trust.CredentialDigest(edge), IssuedAt: issuedAt, Counter: counter}, signerOf(t, net))
		if err != nil {
			t.Fatal(err)
		}
		v, err := record.Verify(d, r)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	newView := func(vs trust.Vouchers) trust.View {
		return trust.View{
			Local: local, Settings: trust.DefaultSettings(),
			Credentials:                []trust.Credential{Facts(lm), Facts(mt)},
			Vouchers:                   vs,
			DeclaredAcceptsInheritance: map[ident.NodeID]bool{target: true},
		}
	}
	receive := func(vs trust.Vouchers, v record.Verified[Voucher]) (VoucherReason, trust.VoucherDecision) {
		got, reason := CheckVoucher(v, creds[v.Payload.Edge])
		if reason != VoucherOK {
			return reason, trust.VoucherDecision{}
		}
		return reason, vs.Accept(got, now)
	}

	t.Run("fresh vouchers from the intermediate make the inherited trust", func(t *testing.T) {
		vs := trust.Vouchers{}
		for _, e := range []record.Verified[Credential]{lm, mt} {
			reason, dec := receive(vs, voucherOf(kM, m, e.Digest, now.Add(-time.Minute), 1))
			if reason != VoucherOK || !dec.Accepted {
				t.Fatalf("voucher refused: %v %+v", reason, dec)
			}
		}
		if got := trust.Evaluate(newView(vs), target, now); got.Outcome != trust.AllowedInherited {
			t.Fatalf("outcome = %v %v", got.Outcome, got.Reason)
		}
	})

	t.Run("a voucher from a node that is not an end never reaches trust", func(t *testing.T) {
		vs := trust.Vouchers{}
		// The target is an end of m-target but not of local-m.
		forged := voucherOf(kTarget, target, lm.Digest, now.Add(-time.Minute), 1)
		reason, _ := receive(vs, forged)
		if reason != VoucherIssuerNotAnEnd {
			t.Fatalf("reason = %v, want issuer-not-an-end", reason)
		}
		if len(vs) != 0 {
			t.Fatal("a refused voucher was stored")
		}
		// The only voucher present is for the other edge, so the path is suspended.
		if _, dec := receive(vs, voucherOf(kM, m, mt.Digest, now.Add(-time.Minute), 1)); !dec.Accepted {
			t.Fatal("honest voucher refused")
		}
		got := trust.Evaluate(newView(vs), target, now)
		if got.Outcome != trust.Suspended || got.Reason != trust.ReasonVoucherMissing {
			t.Fatalf("outcome = %v %v, want suspended/voucher-missing", got.Outcome, got.Reason)
		}
	})

	t.Run("a voucher stamped in the future is a fact trust judges", func(t *testing.T) {
		vs := trust.Vouchers{}
		v := voucherOf(kM, m, lm.Digest, now.Add(time.Hour), 1)
		reason, dec := receive(vs, v)
		if reason != VoucherOK {
			t.Fatalf("this package refused a future voucher: %v", reason)
		}
		if dec.Accepted || dec.Reason != trust.VoucherFromFuture {
			t.Fatalf("trust decision = %+v, want from-future", dec)
		}
	})

	t.Run("a counter that does not advance is refused by trust", func(t *testing.T) {
		vs := trust.Vouchers{}
		if _, dec := receive(vs, voucherOf(kM, m, lm.Digest, now.Add(-time.Minute), 5)); !dec.Accepted {
			t.Fatal("first voucher refused")
		}
		for _, counter := range []uint64{5, 4, 1} {
			if _, dec := receive(vs, voucherOf(kM, m, lm.Digest, now.Add(-time.Second), counter)); dec.Accepted || dec.Reason != trust.VoucherCounterNotAdvancing {
				t.Errorf("counter %d: %+v", counter, dec)
			}
		}
		if _, dec := receive(vs, voucherOf(kM, m, lm.Digest, now.Add(-time.Second), 6)); !dec.Accepted {
			t.Error("counter 6 refused")
		}
	})

	t.Run("a voucher older than the grace period suspends the path", func(t *testing.T) {
		vs := trust.Vouchers{}
		old := now.Add(-trust.DefaultGracePeriod - time.Minute)
		for _, e := range []record.Verified[Credential]{lm, mt} {
			if _, dec := receive(vs, voucherOf(kM, m, e.Digest, old, 1)); !dec.Accepted {
				t.Fatal("refused")
			}
		}
		got := trust.Evaluate(newView(vs), target, now)
		if got.Outcome != trust.Suspended || got.Reason != trust.ReasonVoucherStale {
			t.Fatalf("outcome = %v %v", got.Outcome, got.Reason)
		}
	})
}

func verifiedCredential2(t testing.TB, c Credential, first, second int) record.Verified[Credential] {
	t.Helper()
	v, err := record.Verify(Descriptor, sealCredential(t, c, first, second))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestExpiredCredentialStillReachesTrust: record verification does not judge a
// credential's end, so an expired credential arrives in the view, where trust
// refuses it and ClearLocallyRevoked knows that clearing restores nothing.
func TestExpiredCredentialStillReachesTrust(t *testing.T) {
	local, x := nodeOf(t, keyA), nodeOf(t, keyB)
	cred := verifiedCredential(t, New(local, x, t0, t0.Add(time.Hour), Nonce{8}))
	now := t0.Add(2 * time.Hour)
	f := Facts(cred)
	if f.NotAfter.IsZero() || !f.NotAfter.Equal(t0.Add(time.Hour)) {
		t.Fatalf("NotAfter = %v", f.NotAfter)
	}
	view := trust.View{
		Local: local, Settings: trust.DefaultSettings(), Credentials: []trust.Credential{f},
		LocallyRevoked: trust.NodeSet{x: {}},
	}
	if got := trust.ClearLocallyRevoked(view, x, now); !got.WasRevoked || !got.NewPairingRequired {
		t.Errorf("clear effect = %+v, want a new pairing required", got)
	}
	if got := trust.ClearLocallyRevoked(view, x, t0.Add(30*time.Minute)); got.NewPairingRequired {
		t.Errorf("a credential that has not expired needs no new pairing: %+v", got)
	}
	view.LocallyRevoked = nil
	if got := trust.Evaluate(view, x, now); got.Outcome != trust.Denied || got.Reason != trust.ReasonCredentialExpired {
		t.Errorf("evaluate = %v %v", got.Outcome, got.Reason)
	}
	if got := trust.Evaluate(view, x, t0.Add(30*time.Minute)); got.Outcome != trust.AllowedDirect {
		t.Errorf("evaluate before the end = %v %v", got.Outcome, got.Reason)
	}
	// A credential without an end never expires.
	noEnd := verifiedCredential(t, New(local, x, t0, time.Time{}, Nonce{9}))
	view.Credentials = []trust.Credential{Facts(noEnd)}
	if got := trust.Evaluate(view, x, t0.Add(1000*time.Hour)); got.Outcome != trust.AllowedDirect {
		t.Errorf("credential without an end: %v %v", got.Outcome, got.Reason)
	}
}

// TestRevocationThroughTrust: a verified revocation fills
// View.RevokedCredentials with exactly the digest of the credential it embeds.
func TestRevocationThroughTrust(t *testing.T) {
	local, x := nodeOf(t, keyA), nodeOf(t, keyB)
	cred := verifiedCredential(t, New(local, x, t0, time.Time{}, Nonce{10}))
	again := verifiedCredential(t, New(local, x, t0, time.Time{}, Nonce{11})) // the same pair, paired anew
	view := trust.View{Local: local, Settings: trust.DefaultSettings(), Credentials: []trust.Credential{Facts(cred)}}
	if got := trust.Evaluate(view, x, t0); got.Outcome != trust.AllowedDirect {
		t.Fatalf("before: %v %v", got.Outcome, got.Reason)
	}
	rev, err := NewRevocation(cred.Payload)
	if err != nil {
		t.Fatal(err)
	}
	r, err := record.Seal(RevocationDescriptor, rev, signerOf(t, keyB))
	if err != nil {
		t.Fatal(err)
	}
	v, err := record.Verify(RevocationDescriptor, mustDecode(t, mustEncode(t, r)))
	if err != nil {
		t.Fatal(err)
	}
	if v.Payload.Digest != trust.CredentialDigest(cred.Digest) || v.Payload.Digest == trust.CredentialDigest(again.Digest) {
		t.Fatalf("revoked digest %s is not the credential's %s alone", v.Payload.Digest, trust.CredentialDigest(cred.Digest))
	}
	view.RevokedCredentials = trust.DigestSet{v.Payload.Digest: {}}
	if got := trust.Evaluate(view, x, t0); got.Outcome != trust.Denied || got.Reason != trust.ReasonCredentialRevoked {
		t.Fatalf("after: %v %v", got.Outcome, got.Reason)
	}
	// Pairing anew makes a different credential, which the old revocation does not touch.
	view.Credentials = append(view.Credentials, Facts(again))
	if got := trust.Evaluate(view, x, t0); got.Outcome != trust.AllowedDirect {
		t.Fatalf("after pairing anew: %v %v", got.Outcome, got.Reason)
	}
}

// TestCredentialClusterRows covers the pure-function rows of cluster formation,
// cascade and eviction, and online vouchers, with credentials and vouchers made
// of real records. Nodes a, b, c, d are chained a-b, b-c, c-d.
func TestCredentialClusterRows(t *testing.T) {
	seeds := map[string]int{"a": 0x61, "b": 0x62, "c": 0x63, "d": 0x64}
	nets := map[string]int{"a": 0x71, "b": 0x72, "c": 0x73, "d": 0x74}
	id := func(n string) ident.NodeID { return nodeOf(t, seeds[n]) }
	now := t0.Add(time.Hour)
	creds := map[string]record.Verified[Credential]{}
	edge := func(x, y string) record.Verified[Credential] {
		k := x + y
		if c, ok := creds[k]; ok {
			return c
		}
		c := verifiedCredential2(t, New(id(x), id(y), t0, time.Time{}, Nonce{byte(len(creds) + 1)}), seeds[x], seeds[y])
		creds[k] = c
		return c
	}
	voucher := func(issuer string, e record.Verified[Credential], counter uint64) trust.Voucher {
		d := VoucherDescriptor(acceptedNetworkKey(t, seeds[issuer], nets[issuer]), now)
		r, err := record.Seal(d, Voucher{Issuer: id(issuer), Edge: trust.CredentialDigest(e.Digest), IssuedAt: now.Add(-time.Minute), Counter: counter}, signerOf(t, nets[issuer]))
		if err != nil {
			t.Fatal(err)
		}
		v, err := record.Verify(d, r)
		if err != nil {
			t.Fatal(err)
		}
		got, reason := CheckVoucher(v, Facts(e))
		if reason != VoucherOK {
			t.Fatalf("voucher of %s refused: %v", issuer, reason)
		}
		return got
	}
	type opts struct {
		edges     [][2]string
		accepting map[string]bool // inheritance switch per node; absent means on
		revoked   []record.Verified[Credential]
		vouchers  [][2]string // issuer, edge key
	}
	run := func(local, target string, o opts) trust.Verdict {
		v := trust.View{Local: id(local), Settings: trust.DefaultSettings(), Vouchers: trust.Vouchers{}, DeclaredAcceptsInheritance: map[ident.NodeID]bool{}, RevokedCredentials: trust.DigestSet{}}
		for n := range seeds {
			on := true
			if set, ok := o.accepting[n]; ok {
				on = set
			}
			v.DeclaredAcceptsInheritance[id(n)] = on
		}
		v.Settings.AcceptsInheritance = v.DeclaredAcceptsInheritance[id(local)]
		for _, e := range o.edges {
			v.Credentials = append(v.Credentials, Facts(edge(e[0], e[1])))
		}
		for _, c := range o.revoked {
			v.RevokedCredentials[trust.CredentialDigest(c.Digest)] = struct{}{}
		}
		for i, vo := range o.vouchers {
			e := creds[vo[1]]
			v.Vouchers.Accept(voucher(vo[0], e, uint64(i+1)), now)
		}
		return trust.Evaluate(v, id(target), now)
	}
	chain := [][2]string{{"a", "b"}, {"b", "c"}, {"c", "d"}}
	bothVouchers := [][2]string{{"b", "ab"}, {"b", "bc"}}

	t.Run("a-b and b-c with both switches on: a reaches c", func(t *testing.T) {
		if got := run("a", "c", opts{edges: chain, vouchers: bothVouchers}); got.Outcome != trust.AllowedInherited {
			t.Fatalf("%v %v", got.Outcome, got.Reason)
		}
	})
	t.Run("a refuses inheritance: not c, and c's view of a is the same", func(t *testing.T) {
		o := opts{edges: chain, vouchers: bothVouchers, accepting: map[string]bool{"a": false}}
		if got := run("a", "c", o); got.Outcome != trust.Denied || got.Reason != trust.ReasonInheritanceDisabled {
			t.Fatalf("a to c: %v %v", got.Outcome, got.Reason)
		}
		if got := run("c", "a", o); got.Outcome != trust.Denied || got.Reason != trust.ReasonInheritanceDisabled {
			t.Fatalf("c to a: %v %v", got.Outcome, got.Reason)
		}
	})
	t.Run("c refuses inheritance: the same from both sides", func(t *testing.T) {
		o := opts{edges: chain, vouchers: bothVouchers, accepting: map[string]bool{"c": false}}
		for _, p := range [][2]string{{"a", "c"}, {"c", "a"}} {
			if got := run(p[0], p[1], o); got.Outcome != trust.Denied {
				t.Fatalf("%s to %s: %v %v", p[0], p[1], got.Outcome, got.Reason)
			}
		}
	})
	t.Run("inheritance extends one level only: a-b-c-d", func(t *testing.T) {
		vs := [][2]string{{"b", "ab"}, {"b", "bc"}, {"c", "bc"}, {"c", "cd"}}
		if got := run("a", "d", opts{edges: chain, vouchers: vs}); got.Outcome != trust.Denied || got.Reason != trust.ReasonNoPath {
			t.Fatalf("a to d: %v %v", got.Outcome, got.Reason)
		}
		if got := run("a", "c", opts{edges: chain, vouchers: vs}); got.Outcome != trust.AllowedInherited {
			t.Fatalf("a to c: %v %v", got.Outcome, got.Reason)
		}
	})
	t.Run("revoking b-c ends the inherited trust and only that", func(t *testing.T) {
		o := opts{edges: chain, vouchers: bothVouchers, revoked: []record.Verified[Credential]{edge("b", "c")}}
		if got := run("a", "c", o); got.Outcome != trust.Denied || got.Reason != trust.ReasonCredentialRevoked {
			t.Fatalf("a to c: %v %v", got.Outcome, got.Reason)
		}
		if got := run("a", "b", o); got.Outcome != trust.AllowedDirect {
			t.Fatalf("a to b: %v %v", got.Outcome, got.Reason)
		}
	})
	t.Run("two paths: revoking one keeps the trust", func(t *testing.T) {
		edges := [][2]string{{"a", "b"}, {"b", "c"}, {"a", "d"}, {"d", "c"}}
		vs := [][2]string{{"b", "ab"}, {"b", "bc"}, {"d", "ad"}, {"d", "dc"}}
		for _, e := range edges {
			edge(e[0], e[1])
		}
		if got := run("a", "c", opts{edges: edges, vouchers: vs}); got.Outcome != trust.AllowedInherited {
			t.Fatalf("both paths: %v %v", got.Outcome, got.Reason)
		}
		if got := run("a", "c", opts{edges: edges, vouchers: vs, revoked: []record.Verified[Credential]{edge("b", "c")}}); got.Outcome != trust.AllowedInherited {
			t.Fatalf("one path revoked: %v %v", got.Outcome, got.Reason)
		}
		both := []record.Verified[Credential]{edge("b", "c"), edge("d", "c")}
		if got := run("a", "c", opts{edges: edges, vouchers: vs, revoked: both}); got.Outcome != trust.Denied {
			t.Fatalf("both paths revoked: %v %v", got.Outcome, got.Reason)
		}
	})
	t.Run("without the intermediate's voucher the inherited trust is suspended", func(t *testing.T) {
		if got := run("a", "c", opts{edges: chain, vouchers: [][2]string{{"b", "ab"}}}); got.Outcome != trust.Suspended {
			t.Fatalf("%v %v", got.Outcome, got.Reason)
		}
	})
}
