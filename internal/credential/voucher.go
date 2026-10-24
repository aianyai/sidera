package credential

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// VoucherType is the type string of an online voucher.
const VoucherType = "sidera/online-voucher/1"

// Voucher is the payload of an online voucher: a node says it is online and
// still stands behind one of its trust edges. It carries no validity period; the
// verifier computes validity from IssuedAt, its own clock and its own grace
// period (package trust).
type Voucher struct {
	// Issuer is the node vouching. It signs with its network key, and it must be
	// an end of the credential that Edge names (CheckVoucher).
	Issuer ident.NodeID
	// Edge is the digest of the credential the voucher is for.
	Edge trust.CredentialDigest
	// IssuedAt is passed on as a fact. A voucher stamped in the future is judged
	// by trust.Vouchers.Accept, not here.
	IssuedAt time.Time
	// Counter rises with every voucher the issuer signs for the edge, from 1.
	// Package trust rejects a counter that does not advance.
	Counter uint64
}

type voucherWire struct {
	Issuer   []byte `cbor:"1,keyasint"`
	Edge     []byte `cbor:"2,keyasint"`
	IssuedAt int64  `cbor:"3,keyasint"`
	Counter  uint64 `cbor:"4,keyasint"`
}

// VoucherDescriptor describes the voucher record for a verification at now. It
// is signed by one key: the network key that accepted names for the issuer
// (record.NetworkKeyOf), so an identity key, an expired or revoked certificate's
// key or another node's key is refused with the specific binding reason.
func VoucherDescriptor(accepted record.AcceptedNetworkKey, now time.Time) record.Descriptor[Voucher] {
	return record.NewDescriptor(VoucherType,
		voucherToWire,
		voucherFromWire,
		func(v Voucher) []record.SignerSlot {
			return []record.SignerSlot{record.NetworkKeyOf(v.Issuer, accepted, now)}
		},
	)
}

// VoucherIssuer returns the node a voucher claims to be from, read before the
// signature is checked, only so that the verifier can look up the network-key
// certificate it has accepted for that node and build the VoucherDescriptor.
// The result is untrusted: anyone can write any issuer into a record. Nothing
// may be done with it until record.Verify accepts the record under the
// descriptor built from it.
func VoucherIssuer(r record.Record) (ident.NodeID, error) {
	u, err := record.Peek(VoucherDescriptor(record.AcceptedNetworkKey{}, time.Time{}), r)
	if err != nil {
		return ident.NodeID{}, err
	}
	return u.Payload().Issuer, nil
}

func voucherToWire(v Voucher) (voucherWire, error) {
	issued, err := record.TimeToWire(v.IssuedAt)
	if err != nil {
		return voucherWire{}, fmt.Errorf("issued_at: %w", err)
	}
	return voucherWire{Issuer: v.Issuer.Bytes(), Edge: v.Edge[:], IssuedAt: issued, Counter: v.Counter}, nil
}

func voucherFromWire(w voucherWire) (Voucher, error) {
	issuer, err := ident.NodeIDFromBytes(w.Issuer)
	if err != nil {
		return Voucher{}, fmt.Errorf("issuer: %w", err)
	}
	edge, err := digestFromWire(w.Edge)
	if err != nil {
		return Voucher{}, fmt.Errorf("edge: %w", err)
	}
	issued, err := record.TimeFromWire(w.IssuedAt)
	if err != nil {
		return Voucher{}, fmt.Errorf("issued_at: %w", err)
	}
	if err := record.RequireCounter(w.Counter); err != nil {
		return Voucher{}, fmt.Errorf("counter: %w", err)
	}
	return Voucher{Issuer: issuer, Edge: edge, IssuedAt: issued, Counter: w.Counter}, nil
}

// digestFromWire reads a credential digest: 32 bytes, not all zero.
func digestFromWire(b []byte) (trust.CredentialDigest, error) {
	var d trust.CredentialDigest
	if len(b) != len(d) {
		return d, fmt.Errorf("%w: %d bytes, want %d", ErrIdentifier, len(b), len(d))
	}
	copy(d[:], b)
	if d == (trust.CredentialDigest{}) {
		return trust.CredentialDigest{}, fmt.Errorf("%w: all zero digest", ErrIdentifier)
	}
	return d, nil
}

// VoucherReason says why a voucher was or was not taken for a credential.
type VoucherReason int

const (
	// VoucherInvalid is the zero value; it is never returned as an answer.
	VoucherInvalid VoucherReason = iota
	// VoucherOK: the voucher is for this credential and its issuer is an end.
	VoucherOK
	// VoucherWrongEdge: the credential given is not the one the voucher names,
	// by digest. The caller looked up the wrong credential.
	VoucherWrongEdge
	// VoucherIssuerNotAnEnd: the issuer is neither end of the credential. A
	// node that is not a party to the edge cannot vouch for it, whatever its
	// signature.
	VoucherIssuerNotAnEnd
)

func (r VoucherReason) String() string {
	switch r {
	case VoucherOK:
		return "ok"
	case VoucherWrongEdge:
		return "wrong-edge"
	case VoucherIssuerNotAnEnd:
		return "issuer-not-an-end"
	}
	return "invalid"
}

// CheckVoucher applies the one rule package trust leaves to its caller: the
// issuer must be an end of the credential the voucher names. The caller
// provides that credential (found by v.Payload().Edge) as trust.Credential, the
// form package trust holds it in. On VoucherOK the result is the voucher to
// give to trust.Vouchers.Accept; otherwise it is the zero Voucher.
//
// The signature has been checked, which the type of v says: only
// record.Verify returns it. A zero Verified or a zero credential is not an
// answer (VoucherInvalid): an unset issuer or digest equals an unset end and
// would otherwise match.
func CheckVoucher(v record.Verified[Voucher], cred trust.Credential) (trust.Voucher, VoucherReason) {
	p := v.Payload()
	switch {
	case p.Issuer.IsZero() || p.Edge == (trust.CredentialDigest{}) || cred.Digest == (trust.CredentialDigest{}):
		return trust.Voucher{}, VoucherInvalid
	case cred.Digest != p.Edge:
		return trust.Voucher{}, VoucherWrongEdge
	case p.Issuer != cred.A && p.Issuer != cred.B:
		return trust.Voucher{}, VoucherIssuerNotAnEnd
	}
	return trust.Voucher{Issuer: p.Issuer, Edge: p.Edge, IssuedAt: p.IssuedAt, Counter: p.Counter}, VoucherOK
}
