package revocation

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
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
// device key, and its person's root key by the person's self-revocation, the
// root key being the wider fact; nothing else changes the status, the status is
// always a checked one, and everything else of the certificate is as AdminCert
// gives it.
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
		rule string
		set  *Set
		cert record.Verified[record.DeviceCertificate]
		want admin.RevocationStatus
	}{
		{"an empty set revokes nothing and has looked", NewSet(), certP, admin.RevocationClear},
		{"the person's revocation of the device key revokes the certificate", addAll(t, pRevokesD), certP, admin.RevocationCertRevoked},
		{"the person's self-revocation revokes the root key", addAll(t, pSelf), certP, admin.RevocationRootKeyRevoked},
		{"both revocations: the root key is the wider fact and is the one reported", addAll(t, pRevokesD, pSelf), certP, admin.RevocationRootKeyRevoked},
		{"another device key of the person leaves the certificate alone", addAll(t, pRevokesD2), certP, admin.RevocationClear},
		{"another person's revocation of the same device key leaves the certificate alone", addAll(t, qRevokesD), certP, admin.RevocationClear},
		{"another person's self-revocation leaves the certificate alone", addAll(t, qSelf), certP, admin.RevocationClear},
		{"a node-level revocation leaves the certificate alone", addAll(t, nodeSelf), certP, admin.RevocationClear},
		{"the revocation of the device key by P does not revoke Q's certificate for the same key", addAll(t, pRevokesD), certQ, admin.RevocationClear},
		{"Q's own revocation of the key revokes Q's certificate", addAll(t, pRevokesD, qRevokesD), certQ, admin.RevocationCertRevoked},
		{"P's self-revocation does not void Q's root key", addAll(t, pSelf), certQ, admin.RevocationClear},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			got := row.set.DeviceCertificate(row.cert)
			want := row.cert.Payload().AdminCert()
			want.Revocation = row.want
			if got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}
	t.Run("a zero certificate grants nothing and revokes nothing", func(t *testing.T) {
		got := addAll(t, pRevokesD).DeviceCertificate(record.Verified[record.DeviceCertificate]{})
		if got.MaxLevel != admin.LevelNone || !got.NotAfter.IsZero() || got.Revocation != admin.RevocationClear {
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
	person := cert.Payload().Person
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
		if got := set.DeviceCertificate(v).Revocation; got != admin.RevocationCertRevoked {
			t.Fatalf("the renewed certificate: status %v, want cert-revoked", got)
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
	t.Run("an empty set gives an empty non-nil set that retires nobody", func(t *testing.T) {
		if got := NewSet().Retired(); got == nil || len(got) != 0 {
			t.Fatalf("got %#v", got)
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
		want record.KeyRevocationStatus
	}{
		{"an empty set revokes nothing and has looked", NewSet(), cert1, record.KeyRevocationClear},
		{"the revocation names this certificate", addAll(t, rev1), cert1, record.KeyRevocationRevoked},
		{"the revocation of serial 1 does not revoke serial 2", addAll(t, rev1), cert2, record.KeyRevocationClear},
		{"the same serial with another digest is another certificate", addAll(t, rev1), cert1b, record.KeyRevocationClear},
		{"another node's identical-looking certificate is not revoked", addAll(t, rev1), otherCert, record.KeyRevocationClear},
		{"a revocation of the other certificate under the same serial leaves this one", addAll(t, must(IssueNetworkKeyCertificateRevocation(cert1b, identity))), cert1, record.KeyRevocationClear},
		{"a self-revocation voids every certificate the identity key signed", addAll(t, must(IssueNodeSelfRevocation(identity))), cert2, record.KeyRevocationRevoked},
		{"a self-revocation of another node does not", addAll(t, must(IssueNodeSelfRevocation(other))), cert2, record.KeyRevocationClear},
		{"a device or person revocation does not", addAll(t, must(IssuePersonSelfRevocation(rfcSigner(t, 1))), must(IssueDeviceKeyRevocation(rfcSigner(t, 1), netA.Public()))), cert1, record.KeyRevocationClear},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			got := row.set.NetworkKey(row.cert, row.cert.Payload().Serial)
			if got.Revocation != row.want {
				t.Fatalf("Revocation = %v, want %v", got.Revocation, row.want)
			}
			if got.Cert == nil || *got.Cert != row.cert.Payload() {
				t.Fatalf("Cert = %+v, want the verified payload", got.Cert)
			}
			if got.HighestSerial != row.cert.Payload().Serial {
				t.Fatalf("HighestSerial = %d, want the serial given", got.HighestSerial)
			}
		})
	}
}

// Rule: Add refuses what it cannot keep, and keeps the same fact once.
func TestSetAdd(t *testing.T) {
	k1 := rfcSigner(t, 1)
	rev := mustRevocation(t, must(IssueNodeSelfRevocation(k1)), nil)
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
			got.NetworkKey(cert, cert.Payload().Serial).Revocation != want.NetworkKey(cert, cert.Payload().Serial).Revocation ||
			len(got.Retired()) != len(want.Retired()) {
			t.Fatalf("order %d changed an answer", i)
		}
	}
}

// Rule: Set.RemotePerson builds the remote actor from the certificate alone: the
// person is the certificate's, the facts are DeviceCertificate's for the same
// certificate, and administration judges the result as it judges any actor.
func TestRemotePerson(t *testing.T) {
	p, q := rfcSigner(t, 3), seedSigner(t, 7)
	d := rfcSigner(t, 2)
	certP := deviceCertificate(t, p, d, admin.LevelFullControl)
	certQ := deviceCertificate(t, q, d, admin.LevelFullControl)
	list, problems := admin.NewList([]admin.Entry{
		{Person: personOf(t, p), Level: admin.LevelFullControl},
		{Person: personOf(t, q), Level: admin.LevelFullControl},
	})
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	now := epoch.Add(time.Hour)

	rows := []struct {
		rule       string
		set        *Set
		cert       record.Verified[record.DeviceCertificate]
		wantPerson ident.PersonID
		wantStatus admin.RevocationStatus
		wantLevel  admin.Level
	}{
		{"an empty set gives a checked, clear actor", NewSet(), certP, personOf(t, p), admin.RevocationClear, admin.LevelFullControl},
		{"the person is the certificate's person", NewSet(), certQ, personOf(t, q), admin.RevocationClear, admin.LevelFullControl},
		{"the device revocation of the certificate's person reaches the actor", addAll(t, must(IssueDeviceKeyRevocation(p, d.Public()))), certP, personOf(t, p), admin.RevocationCertRevoked, admin.LevelNone},
		{"the root revocation of the certificate's person reaches the actor", addAll(t, must(IssuePersonSelfRevocation(p))), certP, personOf(t, p), admin.RevocationRootKeyRevoked, admin.LevelNone},
		{"another person's revocations do not reach the actor", addAll(t, must(IssueDeviceKeyRevocation(q, d.Public())), must(IssuePersonSelfRevocation(q))), certP, personOf(t, p), admin.RevocationClear, admin.LevelFullControl},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			actor := row.set.RemotePerson(row.cert)
			if actor.Kind != admin.ActorRemotePerson || actor.Person != row.wantPerson {
				t.Fatalf("got kind %v person %v", actor.Kind, actor.Person)
			}
			if actor.Cert != row.set.DeviceCertificate(row.cert) || actor.Cert.Revocation != row.wantStatus {
				t.Fatalf("cert facts %+v, want status %v and DeviceCertificate's facts", actor.Cert, row.wantStatus)
			}
			if got := admin.EffectiveLevel(list, actor.Person, actor.Cert, now).Level; got != row.wantLevel {
				t.Fatalf("effective level %v, want %v", got, row.wantLevel)
			}
		})
	}
}

// Rule: a nil *Set is a set that was never loaded, and every method panics on it
// rather than answer "nothing is revoked" for a set nobody checked.
func TestNilSetPanics(t *testing.T) {
	p, d := rfcSigner(t, 1), rfcSigner(t, 2)
	node := rfcSigner(t, 3)
	cert := deviceCertificate(t, p, d, admin.LevelManage)
	netKey := seedSigner(t, 5)
	_, netCert := networkCertificate(t, node, netKey, 1, epoch, endTime)
	rev := mustRevocation(t, must(IssueNodeSelfRevocation(node)), nil)

	var nilSet *Set
	rows := []struct {
		rule string
		call func()
	}{
		{"Add on a nil set panics", func() { _ = nilSet.Add(rev) }},
		{"NodeRetired on a nil set panics", func() { nilSet.NodeRetired(rev.node) }},
		{"PersonRevoked on a nil set panics", func() { nilSet.PersonRevoked(personOf(t, p)) }},
		{"Retired on a nil set panics", func() { nilSet.Retired() }},
		{"DeviceCertificate on a nil set panics", func() { nilSet.DeviceCertificate(cert) }},
		{"RemotePerson on a nil set panics", func() { nilSet.RemotePerson(cert) }},
		{"NetworkKey on a nil set panics", func() { nilSet.NetworkKey(netCert, netCert.Payload().Serial) }},
	}
	for _, row := range rows {
		t.Run(row.rule, func(t *testing.T) {
			defer func() {
				got, _ := recover().(string)
				if !strings.Contains(got, "revocation set not loaded") {
					t.Fatalf("panic = %q, want the not-loaded message", got)
				}
			}()
			row.call()
			t.Fatal("returned an answer for a set that was never loaded")
		})
	}
}
