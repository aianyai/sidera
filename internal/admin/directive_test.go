package admin

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/timing"
)

func TestAcceptDirective(t *testing.T) {
	self, other := node(t, 1), node(t, 3)
	mgr, viewer, fc, unlisted := person(t, 10), person(t, 11), person(t, 12), person(t, 13)
	list := mustList(t,
		Entry{Person: mgr, Level: LevelManage},
		Entry{Person: viewer, Level: LevelView},
		Entry{Person: fc, Level: LevelFullControl},
	)
	signedAt := t0.Add(-time.Hour)
	// The node lost contact before the directive was signed.
	offlineSince := signedAt.Add(-time.Hour)

	// good builds a directive that is accepted; each row alters it.
	good := func(mut func(*Directive)) Directive {
		d := Directive{
			Kind: DirectiveRevokeTrustEdge, Node: self,
			SignedAt: signedAt, Signer: mgr,
			Cert: DeviceCert{MaxLevel: LevelFullControl, NotBefore: signedAt.Add(-time.Hour), NotAfter: signedAt.Add(time.Hour)},
		}
		if mut != nil {
			mut(&d)
		}
		return d
	}
	nearNow := func(d *Directive) {
		d.SignedAt = t0.Add(timing.ClockTolerance)
		d.Cert.NotBefore = t0.Add(-time.Hour)
		d.Cert.NotAfter = t0.Add(time.Hour)
	}

	tests := []struct {
		name string
		list List
		d    Directive
		now  time.Time
		want DirectiveVerdict
	}{
		{"a manage-level signer's revoke of an own edge is executed", list, good(nil), t0,
			DirectiveVerdict{DispositionExecute, ReasonOK}},
		{"a full-control signer's revoke of an own edge is executed", list, good(func(d *Directive) { d.Signer = fc }), t0,
			DirectiveVerdict{DispositionExecute, ReasonOK}},
		{"a certificate is valid at the instant of its NotBefore", list,
			good(func(d *Directive) { d.Cert.NotBefore = signedAt }), t0,
			DirectiveVerdict{DispositionExecute, ReasonOK}},

		{"a view-level signer is only a warning", list, good(func(d *Directive) { d.Signer = viewer }), t0,
			DirectiveVerdict{DispositionWarn, ReasonListLevelTooLow}},
		{"a signer absent from the list is only a warning", list, good(func(d *Directive) { d.Signer = unlisted }), t0,
			DirectiveVerdict{DispositionWarn, ReasonNotOnList}},
		{"a signer on an empty list is only a warning", List{}, good(nil), t0,
			DirectiveVerdict{DispositionWarn, ReasonNotOnList}},
		{"a signer whose device cert caps below manage is only a warning",
			list, good(func(d *Directive) { d.Cert.MaxLevel = LevelView }), t0,
			DirectiveVerdict{DispositionWarn, ReasonDeviceCapTooLow}},
		{"a signer removed from the list after signing is only a warning (authority is judged at execution)",
			mustList(t, Entry{Person: fc, Level: LevelFullControl}), good(nil), t0,
			DirectiveVerdict{DispositionWarn, ReasonNotOnList}},
		{"a signer downgraded after signing is only a warning (authority is judged at execution)",
			mustList(t, Entry{Person: mgr, Level: LevelView}), good(nil), t0,
			DirectiveVerdict{DispositionWarn, ReasonListLevelTooLow}},
		{"a list entry that expired before execution is only a warning",
			mustList(t, Entry{Person: mgr, Level: LevelManage, ExpiresAt: t0.Add(-time.Minute)}), good(nil), t0,
			DirectiveVerdict{DispositionWarn, ReasonEntryExpired}},
		{"a list entry that is still valid at execution is accepted",
			mustList(t, Entry{Person: mgr, Level: LevelManage, ExpiresAt: t0.Add(time.Minute)}), good(nil), t0,
			DirectiveVerdict{DispositionExecute, ReasonOK}},
		{"a non-revoke directive is rejected whoever signed it",
			list, good(func(d *Directive) { d.Signer = unlisted; d.Kind = DirectiveUnknown }), t0,
			DirectiveVerdict{DispositionReject, ReasonDirectiveKindNotAllowed}},
		{"a directive for another node is rejected even from an unlisted signer",
			list, good(func(d *Directive) { d.Signer = unlisted; d.Node = other }), t0,
			DirectiveVerdict{DispositionReject, ReasonDirectiveNotForThisNode}},

		{"a revoked device certificate is rejected", list,
			good(func(d *Directive) { d.Cert.CertRevoked = true }), t0,
			DirectiveVerdict{DispositionReject, ReasonCertRevoked}},
		{"a revoked root key is rejected", list,
			good(func(d *Directive) { d.Cert.RootKeyRevoked = true }), t0,
			DirectiveVerdict{DispositionReject, ReasonRootKeyRevoked}},
		{"a certificate revoked after signing is still rejected: a compromised key can claim any signing time", list,
			good(func(d *Directive) { d.Cert.CertRevoked = true; d.SignedAt = signedAt.Add(-30 * time.Minute) }), t0,
			DirectiveVerdict{DispositionReject, ReasonCertRevoked}},
		{"a certificate already expired at signing time is rejected", list,
			good(func(d *Directive) { d.Cert.NotAfter = signedAt }), t0,
			DirectiveVerdict{DispositionReject, ReasonCertExpired}},
		{"a certificate not yet valid at signing time is rejected", list,
			good(func(d *Directive) { d.Cert.NotBefore = signedAt.Add(time.Second) }), t0,
			DirectiveVerdict{DispositionReject, ReasonCertNotYetValid}},
		{"a short-lived certificate that expired after signing still counts (the node may have been offline for long)", list,
			good(func(d *Directive) { d.Cert.NotAfter = signedAt.Add(time.Minute) }), t0.Add(48 * time.Hour),
			DirectiveVerdict{DispositionExecute, ReasonOK}},
		{"a directive signed later than now plus the clock tolerance is rejected", list,
			good(func(d *Directive) { nearNow(d); d.SignedAt = d.SignedAt.Add(time.Nanosecond) }), t0,
			DirectiveVerdict{DispositionReject, ReasonDirectiveSignedInFuture}},
		{"a signing time within the clock tolerance is accepted", list,
			good(nearNow), t0,
			DirectiveVerdict{DispositionExecute, ReasonOK}},

		{"a directive addressed to another node is rejected", list,
			good(func(d *Directive) { d.Node = other }), t0,
			DirectiveVerdict{DispositionReject, ReasonDirectiveNotForThisNode}},
		{"a directive without an addressee is rejected", list,
			good(func(d *Directive) { d.Node = ident.NodeID{} }), t0,
			DirectiveVerdict{DispositionReject, ReasonDirectiveNotForThisNode}},
		{"an unknown directive kind is rejected", list,
			good(func(d *Directive) { d.Kind = DirectiveUnknown }), t0,
			DirectiveVerdict{DispositionReject, ReasonDirectiveKindNotAllowed}},
		{"a directive kind from the future is rejected", list,
			good(func(d *Directive) { d.Kind = DirectiveRevokeTrustEdge + 1 }), t0,
			DirectiveVerdict{DispositionReject, ReasonDirectiveKindNotAllowed}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AcceptDirective(self, tc.list, tc.d, offlineSince, tc.now)
			if got != tc.want {
				t.Fatalf("verdict = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAcceptDirectiveWithUnsetLocalNodeRejects(t *testing.T) {
	mgr := person(t, 10)
	list := mustList(t, Entry{Person: mgr, Level: LevelManage})
	d := Directive{Kind: DirectiveRevokeTrustEdge, SignedAt: t0, Signer: mgr, Cert: validCert(LevelManage)}
	got := AcceptDirective(ident.NodeID{}, list, d, t0.Add(-time.Hour), t0)
	if got.Disposition != DispositionReject {
		t.Fatalf("verdict = %+v, want reject", got)
	}
}

func TestAcceptDirectiveWithoutOfflineSinceRejects(t *testing.T) {
	self, mgr := node(t, 1), person(t, 10)
	list := mustList(t, Entry{Person: mgr, Level: LevelManage})
	d := Directive{Kind: DirectiveRevokeTrustEdge, Node: self, SignedAt: t0, Signer: mgr, Cert: validCert(LevelManage)}
	got := AcceptDirective(self, list, d, time.Time{}, t0)
	want := DirectiveVerdict{DispositionReject, ReasonOfflineSinceMissing}
	if got != want {
		t.Fatalf("verdict = %+v, want %+v", got, want)
	}
}

// TestAcceptDirectiveSigningTimeAgainstOfflineWindow covers claimed signing
// times that fall outside the window in which the node could not be reached.
func TestAcceptDirectiveSigningTimeAgainstOfflineWindow(t *testing.T) {
	self := node(t, 1)
	mgr := person(t, 10)
	list := mustList(t, Entry{Person: mgr, Level: LevelManage})
	tol := timing.ClockTolerance
	offlineSince := t0.Add(-30 * time.Minute)

	// signed returns a directive claiming to be signed at the given time with a
	// certificate that is valid for one hour around it.
	signed := func(at time.Time) Directive {
		return Directive{
			Kind: DirectiveRevokeTrustEdge, Node: self, SignedAt: at, Signer: mgr,
			Cert: DeviceCert{MaxLevel: LevelFullControl, NotBefore: at.Add(-time.Hour), NotAfter: at.Add(time.Hour)},
		}
	}
	// An expired temporary subkey: valid until t0-1h, so it was not valid when
	// the node lost contact.
	expiredKey := Directive{
		Kind: DirectiveRevokeTrustEdge, Node: self, Signer: mgr,
		SignedAt: t0.Add(-90 * time.Minute),
		Cert:     DeviceCert{MaxLevel: LevelFullControl, NotBefore: t0.Add(-2 * time.Hour), NotAfter: t0.Add(-time.Hour)},
	}

	tests := []struct {
		name string
		d    Directive
		want DirectiveVerdict
	}{
		{"a directive signed after the node lost contact is accepted",
			signed(offlineSince.Add(time.Minute)), DirectiveVerdict{DispositionExecute, ReasonOK}},
		{"a signing time exactly the clock tolerance before the offline start is accepted",
			signed(offlineSince.Add(-tol)), DirectiveVerdict{DispositionExecute, ReasonOK}},
		{"a signing time one instant beyond the clock tolerance before the offline start is rejected",
			signed(offlineSince.Add(-tol - time.Nanosecond)), DirectiveVerdict{DispositionReject, ReasonDirectiveSignedBeforeOffline}},
		{"a directive with no signing time is rejected",
			signed(time.Time{}), DirectiveVerdict{DispositionReject, ReasonDirectiveSignedAtMissing}},
		{"an expired temporary subkey cannot backdate a directive into its validity window",
			expiredKey, DirectiveVerdict{DispositionReject, ReasonDirectiveSignedBeforeOffline}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AcceptDirective(self, list, tc.d, offlineSince, t0)
			if got != tc.want {
				t.Fatalf("verdict = %+v, want %+v", got, tc.want)
			}
		})
	}
}
