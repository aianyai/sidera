package credential

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// DirectiveType is the type string of an offline administrative directive. The
// type fixes what the directive asks for: to revoke one trust edge, the only
// kind a node ever executes (admin.DirectiveRevokeTrustEdge). A directive
// that asks for anything else is a different type.
const DirectiveType = "sidera/offline-directive/1"

// OfflineDirective is the payload of an offline directive: an administrator's
// instruction, signed while the addressed node could not be reached, to revoke
// one of that node's trust edges. It carries the administrator's device
// certificate, so the node can check the whole chain when it comes back, and is
// signed by that certificate's device key.
//
// The directive claims the two ends of the edge; package trust compares the
// claim with the credential it holds and refuses a directive whose ends differ
// (trust.ApplyDirective). This package does not look at the credential.
type OfflineDirective struct {
	// Node is the node the directive is addressed to.
	Node ident.NodeID
	// Credential is the digest of the credential to revoke.
	Credential trust.CredentialDigest
	// A and B are the claimed ends of the credential, in ascending order of their
	// identifier bytes (the order of Credential.A and Credential.B), and different.
	A, B ident.NodeID
	// SignedAt is the signing time the signer claims. It is passed on as a fact:
	// admin.AcceptDirective judges it, and judges the certificate's window at
	// this time.
	SignedAt time.Time
	// CertificateRecord is the device certificate as a signed record, as it is
	// embedded in the payload. It is the one input: NewOfflineDirective takes it,
	// and encoding reads only this field.
	CertificateRecord record.Record
	// certificate is the verified content of CertificateRecord. It is derived:
	// NewOfflineDirective and decoding set it, and it names the one key allowed
	// to sign the directive. It is unexported so that it cannot disagree with
	// CertificateRecord; read it with Certificate.
	certificate record.Verified[record.DeviceCertificate]
}

// Certificate returns the verified content of the embedded device certificate.
// It is the zero value on an OfflineDirective that was neither built by
// NewOfflineDirective nor produced by verification.
func (d OfflineDirective) Certificate() record.Verified[record.DeviceCertificate] {
	return d.certificate
}

// NewOfflineDirective builds a directive for the edge x-y (either order) of the
// credential with the given digest, addressed to node, signed at signedAt by the
// holder of the certificate in certificate, a signed device-certificate record.
// It refuses a certificate that does not verify as a device certificate.
func NewOfflineDirective(node ident.NodeID, credential trust.CredentialDigest, x, y ident.NodeID, signedAt time.Time, certificate record.Record) (OfflineDirective, error) {
	cert, err := record.Verify(record.DeviceCertificateDescriptor, certificate)
	if err != nil {
		return OfflineDirective{}, fmt.Errorf("%w: %w", ErrCertificate, err)
	}
	a, b := x, y
	if bytes.Compare(a.Bytes(), b.Bytes()) > 0 {
		a, b = b, a
	}
	return OfflineDirective{
		Node: node, Credential: credential, A: a, B: b, SignedAt: signedAt,
		CertificateRecord: certificate, certificate: cert,
	}, nil
}

type directiveWire struct {
	Node        []byte `cbor:"1,keyasint"`
	Credential  []byte `cbor:"2,keyasint"`
	A           []byte `cbor:"3,keyasint"`
	B           []byte `cbor:"4,keyasint"`
	SignedAt    int64  `cbor:"5,keyasint"`
	Certificate []byte `cbor:"6,keyasint"`
}

// errNotTheDeviceKey is what the directive's signer slot answers for any key
// but the attached certificate's device key.
var errNotTheDeviceKey = errors.New("not the device key of the attached certificate")

// DirectiveDescriptor describes the directive record. It is signed by exactly one
// key: the device key of the certificate the directive carries. The certificate
// is itself checked on decoding: it must be a well-formed device-certificate
// record signed by the root key of the person it names. That the person is on
// the node's list, the level and the certificate's window are for package admin.
var DirectiveDescriptor = record.NewDescriptor(DirectiveType,
	directiveToWire,
	directiveFromWire,
	func(d OfflineDirective) []record.SignerSlot {
		key := d.certificate.Payload.DeviceKey
		return []record.SignerSlot{func(k ident.PublicKey) error {
			if k != key {
				return errNotTheDeviceKey
			}
			return nil
		}}
	},
)

func directiveToWire(d OfflineDirective) (directiveWire, error) {
	cert, err := d.CertificateRecord.Encode()
	if err != nil {
		return directiveWire{}, fmt.Errorf("%w: %w", ErrCertificate, err)
	}
	signedAt, err := record.TimeToWire(d.SignedAt)
	if err != nil {
		return directiveWire{}, fmt.Errorf("signed_at: %w", err)
	}
	return directiveWire{
		Node: d.Node.Bytes(), Credential: d.Credential[:],
		A: d.A.Bytes(), B: d.B.Bytes(), SignedAt: signedAt, Certificate: cert,
	}, nil
}

func directiveFromWire(w directiveWire) (OfflineDirective, error) {
	node, err := ident.NodeIDFromBytes(w.Node)
	if err != nil {
		return OfflineDirective{}, fmt.Errorf("node: %w", err)
	}
	credential, err := digestFromWire(w.Credential)
	if err != nil {
		return OfflineDirective{}, fmt.Errorf("credential: %w", err)
	}
	a, err := ident.NodeIDFromBytes(w.A)
	if err != nil {
		return OfflineDirective{}, fmt.Errorf("end a: %w", err)
	}
	b, err := ident.NodeIDFromBytes(w.B)
	if err != nil {
		return OfflineDirective{}, fmt.Errorf("end b: %w", err)
	}
	if bytes.Compare(w.A, w.B) >= 0 {
		return OfflineDirective{}, ErrEnds
	}
	signedAt, err := record.TimeFromWire(w.SignedAt)
	if err != nil {
		return OfflineDirective{}, fmt.Errorf("signed_at: %w", err)
	}
	certRecord, err := record.Decode(w.Certificate, record.DefaultMaxInput)
	if err != nil {
		return OfflineDirective{}, fmt.Errorf("%w: %w", ErrCertificate, err)
	}
	cert, err := record.Verify(record.DeviceCertificateDescriptor, certRecord)
	if err != nil {
		return OfflineDirective{}, fmt.Errorf("%w: %w", ErrCertificate, err)
	}
	return OfflineDirective{
		Node: node, Credential: credential, A: a, B: b, SignedAt: signedAt,
		CertificateRecord: certRecord, certificate: cert,
	}, nil
}

// DeviceCertificateFacts is the source of the revocation facts about a device
// certificate: the revocation records, which this package never reads. It
// returns the facts package admin reads, with Revocation set to the outcome of
// looking up both the certificate and the person's root key. An implementation
// that has not looked returns the zero status (admin.RevocationUnchecked), which
// admin refuses.
type DeviceCertificateFacts interface {
	DeviceCertificate(record.Verified[record.DeviceCertificate]) admin.DeviceCert
}

// AdminDirective returns the facts package admin reads: the kind (fixed by the
// record type), the addressed node, the claimed signing time, and the signer
// with the facts of the attached certificate, which facts supplies, so the
// revocation status of the certificate cannot be left out. facts must not be
// nil: AdminDirective panics at once on a nil source rather than report a
// certificate nobody looked up. Whether the path through the real revocation set
// ends in the right admin verdict is tested where the revocation set is
// available, not in this package.
func (d OfflineDirective) AdminDirective(facts DeviceCertificateFacts) admin.Directive {
	if facts == nil {
		panic("credential: AdminDirective needs a revocation source")
	}
	return admin.Directive{
		Kind:     admin.DirectiveRevokeTrustEdge,
		Node:     d.Node,
		SignedAt: d.SignedAt,
		Signer:   d.certificate.Payload.Person,
		Cert:     facts.DeviceCertificate(d.certificate),
	}
}

// TrustDirective returns the instruction package trust applies after admin has
// accepted the directive: the credential to revoke and the claimed ends.
func (d OfflineDirective) TrustDirective() trust.EdgeRevocationDirective {
	return trust.EdgeRevocationDirective{Credential: d.Credential, A: d.A, B: d.B}
}
