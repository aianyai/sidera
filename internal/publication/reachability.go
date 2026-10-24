package publication

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// ReachabilityType is the type string of a reachability record.
const ReachabilityType = "sidera/reachability-record/1"

// Limits of a reachability record.
const (
	// MaxAddresses is the most addresses one record lists.
	MaxAddresses = 16
	// MaxAddressLen is the longest address, in bytes.
	MaxAddressLen = 256
	// MaxRelays is the most relays one record lists.
	MaxRelays = 8
)

// ReachabilityRecord is the payload of a reachability record: how to reach a
// node. The node is the one the embedded network-key certificate names.
//
// Addresses are opaque to this package: the transport layer writes and reads
// them (for example as multiaddrs), and a contract package never parses them.
type ReachabilityRecord struct {
	// Counter is the node's reachability counter, from 1, raised with every new
	// record. Peers keep only the highest (DecideReachability).
	Counter uint64
	// ExpiresAt is the instant the record stops being usable. This package
	// judges it (ExpiredAt), unlike other time fields, because no other package
	// owns this window. It is required: a record cannot last for ever.
	ExpiresAt time.Time
	// Addresses are the node's addresses, most preferred first. Each is a
	// non-empty byte string of at most MaxAddressLen bytes.
	Addresses [][]byte
	// Relays are the relay nodes the node has made a reservation with.
	Relays []ident.NodeID
	// CertificateRecord is the network-key certificate as a signed record, as it
	// is embedded in the payload. It is the one input: encoding reads only this
	// field.
	CertificateRecord record.Record
	// certificate is the verified content of CertificateRecord, derived: set by
	// NewReachabilityRecord and by decoding, and unexported so that it cannot
	// disagree with CertificateRecord. Its node is the node the record is about
	// and the network key it names is the key that must sign the record. Read it
	// with Certificate.
	certificate record.Verified[record.NetworkKeyCertificate]
}

// Certificate returns the verified content of the embedded network-key
// certificate. It is the zero value on a ReachabilityRecord that was neither
// built by NewReachabilityRecord nor produced by verification.
func (r ReachabilityRecord) Certificate() record.Verified[record.NetworkKeyCertificate] {
	return r.certificate
}

// NewReachabilityRecord builds the payload of a reachability record. certificate
// is the node's network-key certificate as a signed record; it must verify as
// one. The record is signed with record.Seal and the descriptor of
// ReachabilityDescriptor.
func NewReachabilityRecord(counter uint64, expiresAt time.Time, addresses [][]byte, relays []ident.NodeID, certificate record.Record) (ReachabilityRecord, error) {
	cert, err := record.Verify(record.NetworkKeyCertificateDescriptor, certificate)
	if err != nil {
		return ReachabilityRecord{}, fmt.Errorf("%w: %w", ErrCertificate, err)
	}
	return ReachabilityRecord{
		Counter: counter, ExpiresAt: expiresAt, Addresses: addresses, Relays: relays,
		CertificateRecord: certificate, certificate: cert,
	}, nil
}

// Node returns the node the record is about: the node of the embedded
// certificate.
func (r ReachabilityRecord) Node() ident.NodeID { return r.certificate.Payload.Node }

// ExpiredAt reports whether the record can no longer be used at now. The
// instant ExpiresAt is already expired and no tolerance is added (like the
// network-key certificate's window). A record without an expiry time, whose
// zero ExpiresAt lies before any real time, counts as expired.
func (r ReachabilityRecord) ExpiredAt(now time.Time) bool {
	return !now.Before(r.ExpiresAt)
}

type reachabilityWire struct {
	Counter     uint64   `cbor:"1,keyasint"`
	ExpiresAt   int64    `cbor:"2,keyasint"`
	Certificate []byte   `cbor:"3,keyasint"`
	Addresses   [][]byte `cbor:"4,keyasint"`
	Relays      [][]byte `cbor:"5,keyasint"`
}

func reachabilityToWire(r ReachabilityRecord) (reachabilityWire, error) {
	expires, err := record.TimeToWire(r.ExpiresAt)
	if err != nil {
		return reachabilityWire{}, fmt.Errorf("expires_at: %w", err)
	}
	cert, err := r.CertificateRecord.Encode()
	if err != nil {
		return reachabilityWire{}, fmt.Errorf("%w: %w", ErrCertificate, err)
	}
	relays := make([][]byte, len(r.Relays))
	for i, n := range r.Relays {
		relays[i] = n.Bytes()
	}
	return reachabilityWire{Counter: r.Counter, ExpiresAt: expires, Certificate: cert, Addresses: r.Addresses, Relays: relays}, nil
}

func reachabilityFromWire(w reachabilityWire) (ReachabilityRecord, error) {
	if err := record.RequireCounter(w.Counter); err != nil {
		return ReachabilityRecord{}, fmt.Errorf("counter: %w", err)
	}
	expires, err := record.TimeFromWire(w.ExpiresAt)
	if err != nil {
		return ReachabilityRecord{}, fmt.Errorf("expires_at: %w", err)
	}
	certRecord, err := record.Decode(w.Certificate, record.DefaultMaxInput)
	if err != nil {
		return ReachabilityRecord{}, fmt.Errorf("%w: %w", ErrCertificate, err)
	}
	cert, err := record.Verify(record.NetworkKeyCertificateDescriptor, certRecord)
	if err != nil {
		return ReachabilityRecord{}, fmt.Errorf("%w: %w", ErrCertificate, err)
	}
	if len(w.Addresses) > MaxAddresses {
		return ReachabilityRecord{}, fmt.Errorf("%w: %d addresses, at most %d", ErrAddress, len(w.Addresses), MaxAddresses)
	}
	for i, a := range w.Addresses {
		if len(a) == 0 || len(a) > MaxAddressLen {
			return ReachabilityRecord{}, fmt.Errorf("%w: address %d is %d bytes, want 1..%d", ErrAddress, i, len(a), MaxAddressLen)
		}
	}
	if len(w.Relays) > MaxRelays {
		return ReachabilityRecord{}, fmt.Errorf("%w: %d relays, at most %d", ErrRelay, len(w.Relays), MaxRelays)
	}
	r := ReachabilityRecord{
		Counter: w.Counter, ExpiresAt: expires, Addresses: w.Addresses,
		CertificateRecord: certRecord, certificate: cert,
	}
	seen := make(map[ident.NodeID]struct{}, len(w.Relays))
	for i, b := range w.Relays {
		n, err := ident.NodeIDFromBytes(b)
		if err != nil {
			return ReachabilityRecord{}, fmt.Errorf("%w: relay %d: %w", ErrRelay, i, err)
		}
		if _, dup := seen[n]; dup {
			return ReachabilityRecord{}, fmt.Errorf("%w: relay %d is listed twice", ErrRelay, i)
		}
		seen[n] = struct{}{}
		r.Relays = append(r.Relays, n)
	}
	return r, nil
}

// errNotTheEmbeddedKey is what the reachability record's signer slot answers
// for any key but the network key of the certificate the record embeds.
var errNotTheEmbeddedKey = errors.New("not the network key of the embedded certificate")

// ReachabilityDescriptor describes the reachability record for a verification at
// now. It is signed by one key, which must be both the network key of the
// certificate the record embeds and the key that accepted names for the record's
// node (record.NetworkKeyOf, with the binding reason on refusal).
//
// The first condition makes the verdict the same for every verifier whatever
// certificate it holds: a verifier that already holds a newer certificate than
// the embedded one would otherwise accept a record that a verifier reading the
// embedded certificate refuses. Every verifier therefore reads the embedded
// certificate first (EmbeddedNetworkKeyCertificate), runs it through
// record.DecideSerial and the revocation lookup, and only then builds the
// accepted key this descriptor is created from. Expiry of the record is not part
// of verification; DecideReachability judges it.
func ReachabilityDescriptor(accepted record.AcceptedNetworkKey, now time.Time) record.Descriptor[ReachabilityRecord] {
	return record.NewDescriptor(ReachabilityType,
		reachabilityToWire,
		reachabilityFromWire,
		func(r ReachabilityRecord) []record.SignerSlot {
			embedded := r.certificate.Payload.NetworkKey
			belongs := record.NetworkKeyOf(r.Node(), accepted, now)
			return []record.SignerSlot{func(k ident.PublicKey) error {
				if k != embedded {
					return errNotTheEmbeddedKey
				}
				return belongs(k)
			}}
		},
	)
}

// EmbeddedNetworkKeyCertificate returns the network-key certificate embedded in
// a reachability record, read through record.Peek and so checked as a
// certificate (well formed, signed by the identity key of the node it names) and
// nothing more. The record's own signature has not been checked, so the result
// is untrusted: it is only the claim from which a verifier decides which
// certificate to accept (record.DecideSerial, the revocation lookup) and builds
// the accepted key for record.Verify. Nothing else in the record may be used
// until record.Verify has accepted it.
func EmbeddedNetworkKeyCertificate(r record.Record) (record.Verified[record.NetworkKeyCertificate], error) {
	u, err := record.Peek(ReachabilityDescriptor(record.AcceptedNetworkKey{}, time.Time{}), r)
	if err != nil {
		return record.Verified[record.NetworkKeyCertificate]{}, err
	}
	return u.Payload.certificate, nil
}
