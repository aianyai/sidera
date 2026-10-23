package revocation

import (
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// addAll verifies and adds every record; a set built here has only what a
// verified statement can put in it.
func addAll(t testing.TB, recs ...record.Record) *Set {
	t.Helper()
	s := NewSet()
	for _, r := range recs {
		rev, err := Verify(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Add(rev); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// Rule: a device certificate is revoked by its person's revocation of its
// device key, and its person's root key by the person's self-revocation; nothing
// else changes the flags, and everything else of the certificate is as
// AdminCert gives it.
func TestDeviceCertificateStanding(t *testing.T) {
	p, q := rfcSigner(t, 3), seedSigner(t, 7)
	d, d2 := rfcSigner(t, 2), seedSigner(t, 8)
	node := rfcSigner(t, 1)
	certP := deviceCertificate(t, p, d, admin.LevelManage)
	certQ := deviceCertificate(t, q, d, admin.LevelView) // Q uses the same device key

	pRevokesD := must(IssueDeviceKeyRevocation(p, d.Public()))
	pRevokesD2 := must(IssueDeviceKeyRevocation(p, d2.Public()))
	qRevokesD := must(IssueDeviceKeyRevocation(q, d.Public()))
	pSelf := must(IssuePersonSelfRevocation(p))
	qSelf := must(IssuePersonSelfRevocation(q))
	nodeSelf := must(IssueNodeSelfRevocation(node))

	rows := []struct {
		rule        string
		set         *Set
		cert        record.Verified[record.DeviceCertificate]
		certRevoked bool
		rootRevoked bool
	}{
		{"a nil set revokes nothing", nil, certP, false, false},
		{"an empty set revokes nothing", NewSet(), certP, false, false},
		{"the person's revocation of the device key revokes the certificate", addAll(t, pRevokesD), certP, true, false},
		{"the person's self-revocation revokes the root key", addAll(t, pSelf), certP, false, true},
		{"both revocations set both flags", addAll(t, pRevokesD, pSelf), certP, true, true},
		{"another device key of the person leaves the certificate alone", addAll(t, pRevokesD2), certP, false, false},
		{"another person's revocation of the same device key leaves the certificate alone", addAll(t, qRevokesD), certP, false, false},
		{"another person's self-revocation leaves the certificate alone", addAll(t, qSelf), certP, false, false},
		{"a node-level revocation leaves the certificate alone", addAll(t, nodeSelf), certP, false, false},
		{"the revocation of the device key by P does not revoke Q's certificate for the same key", addAll(t, pRevokesD), certQ, false, false},
		{"Q's own revocation of the key revokes Q's certificate", addAll(t, pRevokesD, qRevokesD), certQ, true, false},
		{"P's self-revocation does not void Q's root key", addAll(t, pSelf), certQ, false, false},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			got := row.set.DeviceCertificate(row.cert)
			want := row.cert.Payload.AdminCert()
			want.CertRevoked, want.RootKeyRevoked = row.certRevoked, row.rootRevoked
			if got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}
	t.Run("a zero certificate grants nothing and revokes nothing", func(t *testing.T) {
		got := addAll(t, pRevokesD).DeviceCertificate(record.Verified[record.DeviceCertificate]{})
		if got.MaxLevel != admin.LevelNone || !got.NotAfter.IsZero() || got.CertRevoked || got.RootKeyRevoked {
			t.Fatalf("got %+v", got)
		}
	})
}

// Rule: a revocation applies whatever the time, so the standing it gives holds
// in administration at any instant: before the certificate's window, inside it,
// and after. Re-issuing the certificate with another window does not lift it.
func TestRevokedDeviceCertificateIsRefusedAtAnyTime(t *testing.T) {
	p, d := rfcSigner(t, 3), rfcSigner(t, 2)
	list, problems := admin.NewList([]admin.Entry{{Person: personOf(t, p), Level: admin.LevelFullControl}})
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	cert := deviceCertificate(t, p, d, admin.LevelFullControl)
	person := cert.Payload.Person
	instants := []time.Time{epoch.Add(-24 * time.Hour), epoch, epoch.Add(time.Hour), endTime.Add(-time.Second), endTime, endTime.Add(24 * time.Hour)}
	for _, tc := range []struct {
		rule string
		set  *Set
		want admin.Reason
	}{
		{"the device revocation denies the certificate", addAll(t, must(IssueDeviceKeyRevocation(p, d.Public()))), admin.ReasonCertRevoked},
		{"the root revocation denies the certificate and wins over the device revocation", addAll(t, must(IssuePersonSelfRevocation(p)), must(IssueDeviceKeyRevocation(p, d.Public()))), admin.ReasonRootKeyRevoked},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			for _, now := range instants {
				got := admin.EffectiveLevel(list, person, tc.set.DeviceCertificate(cert), now)
				if got.Level != admin.LevelNone || got.Reason != tc.want {
					t.Fatalf("at %v: got %+v, want denied with %v", now, got, tc.want)
				}
			}
		})
	}
	t.Run("without a revocation the same certificate grants its level inside its window", func(t *testing.T) {
		got := admin.EffectiveLevel(list, person, NewSet().DeviceCertificate(cert), epoch.Add(time.Hour))
		if got.Level != admin.LevelFullControl || got.Reason != admin.ReasonOK {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("a new certificate for the revoked key stays revoked", func(t *testing.T) {
		set := addAll(t, must(IssueDeviceKeyRevocation(p, d.Public())))
		renewed := record.DeviceCertificate{Person: person, DeviceKey: d.Public(), MaxLevel: admin.LevelFullControl, NotBefore: endTime, NotAfter: endTime.Add(time.Hour * 24 * 30)}
		r, err := record.Seal(record.DeviceCertificateDescriptor, renewed, p)
		if err != nil {
			t.Fatal(err)
		}
		v, err := record.Verify(record.DeviceCertificateDescriptor, r)
		if err != nil {
			t.Fatal(err)
		}
		if !set.DeviceCertificate(v).CertRevoked {
			t.Fatal("the renewed certificate is not revoked")
		}
	})
}

// Rule: the retired set is the nodes with a verified self-revocation, in the
// form trust.View.Retired takes.
func TestRetired(t *testing.T) {
	k1, k2 := rfcSigner(t, 1), rfcSigner(t, 2)
	n1, n2 := nodeOf(t, k1), nodeOf(t, k2)
	_, cert := goldenCertificate(t)
	certRev := must(IssueNetworkKeyCertificateRevocation(cert, k1))
	devRev := must(IssueDeviceKeyRevocation(rfcSigner(t, 3), k2.Public()))
	personSelf := must(IssuePersonSelfRevocation(rfcSigner(t, 3)))

	t.Run("a self-revocation retires its node", func(t *testing.T) {
		s := addAll(t, must(IssueNodeSelfRevocation(k1)))
		got := s.Retired()
		if len(got) != 1 {
			t.Fatalf("got %v", got)
		}
		if _, ok := got[n1]; !ok || !s.NodeRetired(n1) || s.NodeRetired(n2) {
			t.Fatal("wrong node retired")
		}
	})
	t.Run("two self-revocations retire two nodes", func(t *testing.T) {
		got := addAll(t, must(IssueNodeSelfRevocation(k1)), must(IssueNodeSelfRevocation(k2))).Retired()
		if len(got) != 2 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("no other kind retires a node", func(t *testing.T) {
		s := addAll(t, certRev, devRev, personSelf)
		if len(s.Retired()) != 0 || s.NodeRetired(n1) {
			t.Fatalf("got %v", s.Retired())
		}
	})
	t.Run("an empty or nil set gives an empty non-nil set that retires nobody", func(t *testing.T) {
		for _, s := range []*Set{nil, NewSet()} {
			if got := s.Retired(); got == nil || len(got) != 0 {
				t.Fatalf("got %#v", got)
			}
		}
	})
	t.Run("the result is a copy", func(t *testing.T) {
		s := addAll(t, must(IssueNodeSelfRevocation(k1)))
		got := s.Retired()
		delete(got, n1)
		got[n2] = struct{}{}
		if !s.NodeRetired(n1) || s.NodeRetired(n2) {
			t.Fatal("the set changed through the copy")
		}
	})
	t.Run("a person is revoked only by a person self-revocation", func(t *testing.T) {
		p3 := personOf(t, rfcSigner(t, 3))
		if addAll(t, certRev, devRev).PersonRevoked(p3) || !addAll(t, personSelf).PersonRevoked(p3) {
			t.Fatal("wrong person standing")
		}
		var nilSet *Set
		if nilSet.PersonRevoked(p3) {
			t.Fatal("nil set revokes a person")
		}
	})
	t.Run("the retired set is the form trust takes", func(t *testing.T) {
		var _ trust.NodeSet = NewSet().Retired()
	})
}

// Rule: a network-key certificate is revoked by the revocation that names its
// node, its serial and its digest, and by nothing less.
func TestNetworkKeyRevoked(t *testing.T) {
	identity, other := rfcSigner(t, 1), seedSigner(t, 5)
	netA, netB := rfcSigner(t, 2), seedSigner(t, 6)
	_, cert1 := networkCertificate(t, identity, netA, 1, epoch, endTime)
	_, cert2 := networkCertificate(t, identity, netB, 2, epoch, endTime)
	// The identity key signs a second certificate under serial 1: another window.
	_, cert1b := networkCertificate(t, identity, netA, 1, epoch, endTime.Add(time.Hour))
	_, otherCert := networkCertificate(t, other, netA, 1, epoch, endTime)

	rev1 := must(IssueNetworkKeyCertificateRevocation(cert1, identity))
	rows := []struct {
		rule string
		set  *Set
		cert record.Verified[record.NetworkKeyCertificate]
		want bool
	}{
		{"a nil set revokes nothing", nil, cert1, false},
		{"the revocation names this certificate", addAll(t, rev1), cert1, true},
		{"the revocation of serial 1 does not revoke serial 2", addAll(t, rev1), cert2, false},
		{"the same serial with another digest is another certificate", addAll(t, rev1), cert1b, false},
		{"another node's identical-looking certificate is not revoked", addAll(t, rev1), otherCert, false},
		{"a revocation of the other certificate under the same serial leaves this one", addAll(t, must(IssueNetworkKeyCertificateRevocation(cert1b, identity))), cert1, false},
		{"a self-revocation voids every certificate the identity key signed", addAll(t, must(IssueNodeSelfRevocation(identity))), cert2, true},
		{"a self-revocation of another node does not", addAll(t, must(IssueNodeSelfRevocation(other))), cert2, false},
		{"a device or person revocation does not", addAll(t, must(IssuePersonSelfRevocation(rfcSigner(t, 1))), must(IssueDeviceKeyRevocation(rfcSigner(t, 1), netA.Public()))), cert1, false},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			got := row.set.NetworkKey(row.cert)
			if got.Revoked != row.want {
				t.Fatalf("Revoked = %v, want %v", got.Revoked, row.want)
			}
			if got.Cert == nil || *got.Cert != row.cert.Payload {
				t.Fatalf("Cert = %+v, want the verified payload", got.Cert)
			}
		})
	}
}

// Rule: Add refuses what it cannot keep, and keeps the same fact once.
func TestSetAdd(t *testing.T) {
	k1 := rfcSigner(t, 1)
	rev := mustRevocation(t, must(IssueNodeSelfRevocation(k1)), nil)
	var nilSet *Set
	if err := nilSet.Add(rev); !errors.Is(err, ErrInvalidRevocation) {
		t.Fatalf("nil set: %v", err)
	}
	s := NewSet()
	for i := 0; i < 3; i++ {
		if err := s.Add(rev); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.Retired()) != 1 {
		t.Fatal("the same fact was kept more than once")
	}
	if err := s.Add(Revocation{}); !errors.Is(err, ErrInvalidRevocation) {
		t.Fatalf("zero revocation: %v", err)
	}
}

// Rule: the answers do not depend on the order revocations were added in.
func TestAnswersDoNotDependOnOrder(t *testing.T) {
	p, d := rfcSigner(t, 3), rfcSigner(t, 2)
	identity := rfcSigner(t, 1)
	_, cert := goldenCertificate(t)
	recs := []record.Record{
		must(IssueNodeSelfRevocation(identity)),
		must(IssuePersonSelfRevocation(p)),
		must(IssueDeviceKeyRevocation(p, d.Public())),
		must(IssueNetworkKeyCertificateRevocation(cert, identity)),
	}
	dcert := deviceCertificate(t, p, d, admin.LevelView)
	want := addAll(t, recs...)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20; i++ {
		rng.Shuffle(len(recs), func(a, b int) { recs[a], recs[b] = recs[b], recs[a] })
		got := addAll(t, recs...)
		if got.DeviceCertificate(dcert) != want.DeviceCertificate(dcert) ||
			got.NetworkKey(cert).Revoked != want.NetworkKey(cert).Revoked ||
			len(got.Retired()) != len(want.Retired()) {
			t.Fatalf("order %d changed an answer", i)
		}
	}
}
