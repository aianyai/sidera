package credential

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// deviceCertRecord returns a device certificate record: the root key of
// rootSeed certifies the key of deviceSeed up to level for [notBefore, notAfter).
func deviceCertRecord(t testing.TB, rootSeed, deviceSeed int, level admin.Level, notBefore, notAfter time.Time) record.Record {
	t.Helper()
	c := record.DeviceCertificate{
		Person:    personOf(t, rootSeed),
		DeviceKey: signerOf(t, deviceSeed).Public(),
		MaxLevel:  level,
		NotBefore: notBefore,
		NotAfter:  notAfter,
	}
	r, err := record.Seal(record.DeviceCertificateDescriptor, c, signerOf(t, rootSeed))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// goldenCert is the certificate of goldenDirective: manage, two hours from t0.
func goldenCert(t testing.TB) record.Record {
	t.Helper()
	return deviceCertRecord(t, keyRoot, keyDevice, admin.LevelManage, t0, t0.Add(2*time.Hour))
}

var directiveSignedAt = t0.Add(1000 * time.Second)

func sampleDirective(t testing.TB) OfflineDirective {
	t.Helper()
	d, err := NewOfflineDirective(nodeOf(t, keyA), trust.CredentialDigest(mustHex(t, goldenCredentialDigest)[0:32:32]),
		nodeOf(t, keyB), nodeOf(t, keyA), directiveSignedAt, goldenCert(t))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestGoldenDirective(t *testing.T) {
	r := mustDecode(t, goldenDirective)
	v, err := record.Verify(DirectiveDescriptor, r)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	d := v.Payload
	want := sampleDirective(t)
	if d.Node != want.Node || d.Credential != want.Credential || d.A != want.A || d.B != want.B || !d.SignedAt.Equal(directiveSignedAt) {
		t.Errorf("payload = %+v", d)
	}
	if d.A != nodeOf(t, keyB) || d.B != nodeOf(t, keyA) {
		t.Error("claimed ends are not in ascending order")
	}
	if v.Digest.String() != goldenDirectiveDigest {
		t.Errorf("digest = %s", v.Digest)
	}
	if !bytes.Equal(mustEncode(t, d.CertificateRecord), goldenDeviceCertificate) {
		t.Error("embedded certificate differs from the vector")
	}
	if d.Certificate().Digest.String() != goldenDeviceCertificateDigest {
		t.Errorf("certificate digest = %s", d.Certificate().Digest)
	}
	if v.Signers[0] != signerOf(t, keyDevice).Public() {
		t.Error("signed by an unexpected key")
	}
}

func TestGoldenDirectiveIsWhatTheCodeWrites(t *testing.T) {
	r, err := record.Seal(DirectiveDescriptor, sampleDirective(t), signerOf(t, keyDevice))
	if err != nil {
		t.Fatal(err)
	}
	if got := mustEncode(t, r); !bytes.Equal(got, goldenDirective) {
		t.Errorf("directive differs from the vector:\n got %x\nwant %x", got, goldenDirective)
	}
	if got := mustEncode(t, goldenCert(t)); !bytes.Equal(got, goldenDeviceCertificate) {
		t.Errorf("certificate differs from the vector")
	}
}

func TestDirectiveFacts(t *testing.T) {
	v, err := record.Verify(DirectiveDescriptor, mustDecode(t, goldenDirective))
	if err != nil {
		t.Fatal(err)
	}
	d := v.Payload
	got := d.AdminDirective(clearFacts)
	want := admin.Directive{
		Kind:     admin.DirectiveRevokeTrustEdge,
		Node:     nodeOf(t, keyA),
		SignedAt: directiveSignedAt,
		Signer:   personOf(t, keyRoot),
		Cert:     admin.DeviceCert{MaxLevel: admin.LevelManage, NotBefore: t0, NotAfter: t0.Add(2 * time.Hour), Revocation: admin.RevocationClear},
	}
	if got != want {
		t.Errorf("admin directive = %+v\nwant           %+v", got, want)
	}
	// The certificate facts are exactly what the revocation source returns, each
	// status as it is: the directive adds nothing and drops nothing.
	for _, status := range []admin.RevocationStatus{
		admin.RevocationUnchecked, admin.RevocationClear, admin.RevocationCertRevoked, admin.RevocationRootKeyRevoked,
	} {
		if cert := d.AdminDirective(certFacts{status}).Cert; cert.Revocation != status || cert.MaxLevel != admin.LevelManage {
			t.Errorf("status %v: cert = %+v", status, cert)
		}
	}
	td := d.TrustDirective()
	if td.Credential != d.Credential || td.A != nodeOf(t, keyB) || td.B != nodeOf(t, keyA) {
		t.Errorf("trust directive = %+v", td)
	}
}

// A certificate that states no cap is written as full control and reaches admin
// as such; the level of the directive is never raised by this package.
func TestDirectiveCarriesTheCertificateCap(t *testing.T) {
	for _, level := range []admin.Level{admin.LevelView, admin.LevelManage, admin.LevelFullControl} {
		cert := deviceCertRecord(t, keyRoot, keyDevice, level, t0, t0.Add(time.Hour))
		d, err := NewOfflineDirective(nodeOf(t, keyA), trust.CredentialDigest{1}, nodeOf(t, keyA), nodeOf(t, keyB), directiveSignedAt, cert)
		if err != nil {
			t.Fatal(err)
		}
		r, err := record.Seal(DirectiveDescriptor, d, signerOf(t, keyDevice))
		if err != nil {
			t.Fatal(err)
		}
		v, err := record.Verify(DirectiveDescriptor, r)
		if err != nil {
			t.Fatal(err)
		}
		if got := v.Payload.AdminDirective(clearFacts).Cert.MaxLevel; got != level {
			t.Errorf("cap = %v, want %v", got, level)
		}
	}
}

func TestDirectiveSigners(t *testing.T) {
	d := sampleDirective(t)
	payload := directivePayload(t, d)
	tests := []struct {
		name    string
		signers []int
		want    error
	}{
		{"signing device key does not match the attached certificate", []int{keyOther}, record.ErrSignerNotAllowed},
		{"signed by the root key instead of the device key", []int{keyRoot}, record.ErrSignerNotAllowed},
		{"signed by the addressed node's identity key", []int{keyA}, record.ErrSignerNotAllowed},
		{"extra signature: root key next to the device key", []int{keyDevice, keyRoot}, record.ErrSignerNotAllowed},
		{"extra signature: another device key", []int{keyDevice, keyOther}, record.ErrSignerNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ss []record.Signer
			for _, n := range tc.signers {
				ss = append(ss, signerOf(t, n))
			}
			forged, err := record.Sign(DirectiveType, payload, ss...)
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(DirectiveDescriptor, forged)
			requireErr(t, err, tc.want)
			_, err = record.Seal(DirectiveDescriptor, d, ss...)
			requireErr(t, err, tc.want)
		})
	}
	t.Run("wrong type string", func(t *testing.T) {
		r, err := record.Sign(RevocationType, payload, signerOf(t, keyDevice))
		if err != nil {
			t.Fatal(err)
		}
		_, err = record.Verify(DirectiveDescriptor, r)
		requireErr(t, err, record.ErrType)
	})
	t.Run("no signature", func(t *testing.T) {
		r := mustDecode(t, goldenDirective)
		r.Signatures = nil
		if _, err := record.Verify(DirectiveDescriptor, r); err == nil {
			t.Error("a directive without a signature verified")
		}
	})
}

// directivePayload returns the wire payload of d.
func directivePayload(t testing.TB, d OfflineDirective) []byte {
	t.Helper()
	r, err := record.Seal(DirectiveDescriptor, d, signerOf(t, keyDevice))
	if err != nil {
		t.Fatal(err)
	}
	return r.Payload
}

func TestDirectivePayloadRules(t *testing.T) {
	a, b := nodeOf(t, keyA), nodeOf(t, keyB)
	cert := mustEncode(t, goldenCert(t))
	good := directiveWire{
		Node: a.Bytes(), Credential: bytes.Repeat([]byte{7}, 32), A: b.Bytes(), B: a.Bytes(),
		SignedAt: unixT0 + 1000, Certificate: cert,
	}
	// A device certificate record signed by a key that is not the root key of the
	// person it names, and one of another type.
	certPayload := goldenCertPayload(t)
	forgedCert, err := record.Sign(record.DeviceCertificateType, certPayload, signerOf(t, keyOther))
	if err != nil {
		t.Fatal(err)
	}
	networkCert, err := record.Seal(record.NetworkKeyCertificateDescriptor,
		record.NetworkKeyCertificate{Node: a, NetworkKey: signerOf(t, keyNetA).Public(), Serial: 1, NotBefore: t0, NotAfter: t0.Add(time.Hour)},
		signerOf(t, keyA))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(w *directiveWire)
		want   error
	}{
		{"valid", func(w *directiveWire) {}, nil},
		{"addressed node unset", func(w *directiveWire) { w.Node = make([]byte, 32) }, ident.ErrZero},
		{"credential digest unset", func(w *directiveWire) { w.Credential = make([]byte, 32) }, ErrIdentifier},
		{"credential digest short", func(w *directiveWire) { w.Credential = w.Credential[:8] }, ErrIdentifier},
		{"both ends equal", func(w *directiveWire) { w.B = w.A }, ErrEnds},
		{"ends in descending order", func(w *directiveWire) { w.A, w.B = w.B, w.A }, ErrEnds},
		{"end unset", func(w *directiveWire) { w.A = make([]byte, 32) }, ident.ErrZero},
		{"signed_at zero", func(w *directiveWire) { w.SignedAt = 0 }, record.ErrTime},
		{"signed_at negative", func(w *directiveWire) { w.SignedAt = -1 }, record.ErrTime},
		{"certificate missing", func(w *directiveWire) { w.Certificate = nil }, ErrCertificate},
		{"certificate is garbage", func(w *directiveWire) { w.Certificate = []byte{1, 2, 3} }, ErrCertificate},
		{"certificate with a trailing byte", func(w *directiveWire) { w.Certificate = append(append([]byte(nil), cert...), 0) }, ErrCertificate},
		{"certificate not signed by the root key of its person", func(w *directiveWire) { w.Certificate = mustEncode(t, forgedCert) }, ErrCertificate},
		{"embedded record is not a device certificate", func(w *directiveWire) { w.Certificate = mustEncode(t, networkCert) }, ErrCertificate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := good
			tc.mutate(&w)
			payload, err := record.MarshalCanonical(w)
			if err != nil {
				t.Fatal(err)
			}
			r, err := record.Sign(DirectiveType, payload, signerOf(t, keyDevice))
			if err != nil {
				t.Fatal(err)
			}
			_, err = record.Verify(DirectiveDescriptor, r)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("rejected: %v", err)
				}
				return
			}
			requireErr(t, err, record.ErrPayload, tc.want)
		})
	}
}

func goldenCertPayload(t testing.TB) []byte {
	t.Helper()
	return goldenCert(t).Payload
}

func TestNewOfflineDirectiveRefusesBadCertificates(t *testing.T) {
	forged, err := record.Sign(record.DeviceCertificateType, goldenCertPayload(t), signerOf(t, keyOther))
	if err != nil {
		t.Fatal(err)
	}
	partialPair, err := record.Sign("sidera/test/other/1", []byte{0xa0}, signerOf(t, keyRoot))
	if err != nil {
		t.Fatal(err)
	}
	for name, cert := range map[string]record.Record{
		"zero record":              {},
		"signed by another key":    forged,
		"another record type":      partialPair,
		"certificate without sigs": {Type: record.DeviceCertificateType, Payload: goldenCertPayload(t)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewOfflineDirective(nodeOf(t, keyA), trust.CredentialDigest{1}, nodeOf(t, keyA), nodeOf(t, keyB), directiveSignedAt, cert)
			requireErr(t, err, ErrCertificate)
		})
	}
}

func TestSealRefusesInvalidDirectives(t *testing.T) {
	good := sampleDirective(t)
	tests := []struct {
		name   string
		mutate func(d *OfflineDirective)
		want   error
	}{
		{"both ends the same node", func(d *OfflineDirective) { d.B = d.A }, ErrEnds},
		{"no signing time", func(d *OfflineDirective) { d.SignedAt = time.Time{} }, record.ErrTime},
		{"no credential", func(d *OfflineDirective) { d.Credential = trust.CredentialDigest{} }, ErrIdentifier},
		{"no certificate", func(d *OfflineDirective) {
			d.CertificateRecord = record.Record{}
			d.certificate = record.Verified[record.DeviceCertificate]{}
		}, ErrCertificate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := good
			tc.mutate(&d)
			_, err := record.Seal(DirectiveDescriptor, d, signerOf(t, keyDevice))
			requireErr(t, err, tc.want)
		})
	}
}

func TestNewOfflineDirectiveOrdersTheEnds(t *testing.T) {
	cert := goldenCert(t)
	a, b := nodeOf(t, keyA), nodeOf(t, keyB)
	d1, err := NewOfflineDirective(a, trust.CredentialDigest{1}, a, b, directiveSignedAt, cert)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := NewOfflineDirective(a, trust.CredentialDigest{1}, b, a, directiveSignedAt, cert)
	if err != nil {
		t.Fatal(err)
	}
	if d1.A != d2.A || d1.B != d2.B || bytes.Compare(d1.A.Bytes(), d1.B.Bytes()) >= 0 {
		t.Errorf("ends not ordered: %v %v", d1, d2)
	}
}

// recordingFacts remembers the certificate it was asked about.
type recordingFacts struct {
	asked  record.Verified[record.DeviceCertificate]
	status admin.RevocationStatus
}

func (f *recordingFacts) DeviceCertificate(v record.Verified[record.DeviceCertificate]) admin.DeviceCert {
	f.asked = v
	return certFacts{f.status}.DeviceCertificate(v)
}

// The revocation source is asked about the certificate the directive carries, and
// nothing but its answer becomes the signer's certificate facts.
func TestAdminDirectiveAsksTheSourceAboutTheAttachedCertificate(t *testing.T) {
	d := sampleDirective(t)
	src := &recordingFacts{status: admin.RevocationCertRevoked}
	got := d.AdminDirective(src)
	if src.asked.Digest != d.Certificate().Digest || src.asked.Payload != d.Certificate().Payload || src.asked.Digest.IsZero() {
		t.Errorf("asked about %+v, want the attached certificate %+v", src.asked, d.Certificate())
	}
	if got.Cert.Revocation != admin.RevocationCertRevoked {
		t.Errorf("cert = %+v", got.Cert)
	}
}

// A directive cannot be turned into admin facts without a revocation source.
func TestAdminDirectivePanicsWithoutARevocationSource(t *testing.T) {
	defer func() {
		got := recover()
		msg, _ := got.(string)
		if !strings.Contains(msg, "revocation source") {
			t.Errorf("AdminDirective(nil) panicked with %v, want the revocation-source message", got)
		}
	}()
	sampleDirective(t).AdminDirective(nil)
}

// A source that has not looked yields facts that admin refuses.
func TestUncheckedRevocationIsRefusedByAdmin(t *testing.T) {
	d := sampleDirective(t)
	got := admin.AcceptDirective(nodeOf(t, keyA), admin.List{}, d.AdminDirective(certFacts{admin.RevocationUnchecked}), t0, directiveSignedAt.Add(time.Minute))
	if got.Disposition != admin.DispositionReject || got.Reason != admin.ReasonRevocationUnchecked {
		t.Errorf("got %v %v", got.Disposition, got.Reason)
	}
}

// The derived certificate cannot disagree with the embedded record.
func TestDirectiveCertificateIsDerivedFromTheEmbeddedRecord(t *testing.T) {
	d := sampleDirective(t)
	want, err := record.Verify(record.DeviceCertificateDescriptor, d.CertificateRecord)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Certificate(); got.Digest != want.Digest || got.Payload != want.Payload {
		t.Errorf("Certificate() = %+v, want %+v", got, want)
	}
}
