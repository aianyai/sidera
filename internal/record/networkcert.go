package record

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/timing"
)

// NetworkKeyCertificateType is the type string of a network-key certificate.
const NetworkKeyCertificateType = "sidera/network-key-cert/1"

// NetworkKeyCertificate says which network key a node currently uses. The
// node's long-term identity key signs it, so the network key can be replaced
// without changing the node's identifier. It records the network public key, not
// a libp2p peer identifier: the peer identifier is a function of that key, and
// the contract packages do not depend on libp2p.
type NetworkKeyCertificate struct {
	// Node is the node the certificate is for. The signer must be the identity
	// key that derives this identifier.
	Node ident.NodeID
	// NetworkKey is the key the node's connections and network-key records use.
	NetworkKey ident.PublicKey
	// Serial starts at 1 and rises with each certificate the node issues; peers
	// accept only the highest serial they have seen.
	Serial uint64
	// NotBefore and NotAfter bound the validity: valid from NotBefore, expired
	// at NotAfter, compared without tolerance.
	NotBefore, NotAfter time.Time
}

// IssueNetworkKeyCertificate builds the certificate a node issues at issuedAt
// for its network key. NotBefore is issuedAt minus the clock tolerance, not
// issuedAt itself: a higher serial replaces the old certificate at once, and
// validity is compared without tolerance, so a peer whose clock runs up to the
// tolerance behind would otherwise find the new certificate not yet valid and
// the node unverifiable after every renewal. The result is sealed with Seal,
// which refuses invalid values.
func IssueNetworkKeyCertificate(node ident.NodeID, networkKey ident.PublicKey, serial uint64, issuedAt, notAfter time.Time) NetworkKeyCertificate {
	return NetworkKeyCertificate{
		Node:       node,
		NetworkKey: networkKey,
		Serial:     serial,
		NotBefore:  wholeSeconds(issuedAt).Add(-timing.ClockTolerance),
		NotAfter:   wholeSeconds(notAfter),
	}
}

type networkKeyCertificateWire struct {
	Node       []byte  `cbor:"1,keyasint"`
	NetworkKey KeyWire `cbor:"2,keyasint"`
	Serial     uint64  `cbor:"3,keyasint"`
	NotBefore  int64   `cbor:"4,keyasint"`
	NotAfter   int64   `cbor:"5,keyasint"`
}

// NetworkKeyCertificateDescriptor describes the network-key certificate. It is
// signed by exactly one key: the node's identity key.
var NetworkKeyCertificateDescriptor = NewDescriptor(NetworkKeyCertificateType,
	func(c NetworkKeyCertificate) (networkKeyCertificateWire, error) {
		nb, err := TimeToWire(c.NotBefore)
		if err != nil {
			return networkKeyCertificateWire{}, fmt.Errorf("not_before: %w", err)
		}
		na, err := TimeToWire(c.NotAfter)
		if err != nil {
			return networkKeyCertificateWire{}, fmt.Errorf("not_after: %w", err)
		}
		return networkKeyCertificateWire{Node: c.Node.Bytes(), NetworkKey: KeyToWire(c.NetworkKey), Serial: c.Serial, NotBefore: nb, NotAfter: na}, nil
	},
	func(w networkKeyCertificateWire) (NetworkKeyCertificate, error) {
		node, err := ident.NodeIDFromBytes(w.Node)
		if err != nil {
			return NetworkKeyCertificate{}, fmt.Errorf("node: %w", err)
		}
		key, err := KeyFromWire(w.NetworkKey)
		if err != nil {
			return NetworkKeyCertificate{}, fmt.Errorf("network key: %w", err)
		}
		if err := checkNetworkKeyCertificate(node, key, w.Serial, w.NotBefore, w.NotAfter); err != nil {
			return NetworkKeyCertificate{}, err
		}
		nb, err := TimeFromWire(w.NotBefore)
		if err != nil {
			return NetworkKeyCertificate{}, fmt.Errorf("not_before: %w", err)
		}
		na, err := TimeFromWire(w.NotAfter)
		if err != nil {
			return NetworkKeyCertificate{}, fmt.Errorf("not_after: %w", err)
		}
		return NetworkKeyCertificate{Node: node, NetworkKey: key, Serial: w.Serial, NotBefore: nb, NotAfter: na}, nil
	},
	func(c NetworkKeyCertificate) []SignerSlot { return []SignerSlot{NodeIdentityKey(c.Node)} },
)

// checkNetworkKeyCertificate holds the rules of the certificate: the fields are
// set, the window is not empty, and the network key is not the node's identity
// key (a key in both roles would let a compromised connection key sign
// certificates for the node). Only the decoding side calls it; encoding
// reaches it through the descriptor, which decodes what it wrote.
func checkNetworkKeyCertificate(node ident.NodeID, key ident.PublicKey, serial uint64, notBefore, notAfter int64) error {
	if node.IsZero() {
		return fmt.Errorf("node: %w", ident.ErrZero)
	}
	if key.IsZero() {
		return fmt.Errorf("network key: %w", ErrKey)
	}
	if err := RequireCounter(serial); err != nil {
		return fmt.Errorf("serial: %w", err)
	}
	if notAfter <= notBefore {
		return fmt.Errorf("%w: not_after must be after not_before", ErrTime)
	}
	if derived, err := ident.NodeIDFromKey(key); err == nil && derived == node {
		return fmt.Errorf("%w: the network key is the identity key of the node", ErrRoleSeparation)
	}
	return nil
}

// Validity says where a point in time lies relative to a validity window.
type Validity int

const (
	// ValidityUnknown is the zero value; it is never returned for a real window.
	ValidityUnknown Validity = iota
	// NotYetValid: now is before NotBefore.
	NotYetValid
	// Valid: NotBefore <= now < NotAfter.
	Valid
	// Expired: now is at or after NotAfter.
	Expired
)

func (v Validity) String() string {
	switch v {
	case NotYetValid:
		return "not-yet-valid"
	case Valid:
		return "valid"
	case Expired:
		return "expired"
	}
	return "unknown"
}

// ValidityAt judges the certificate's window at now, without tolerance: a
// certificate is valid from the NotBefore instant and expired at the NotAfter
// instant.
func (c NetworkKeyCertificate) ValidityAt(now time.Time) Validity {
	switch {
	case c.NotBefore.IsZero() || c.NotAfter.IsZero():
		return ValidityUnknown
	case now.Before(c.NotBefore):
		return NotYetValid
	case !now.Before(c.NotAfter):
		return Expired
	}
	return Valid
}

// SerialSeen is the highest network-key certificate serial seen for one node,
// with the digest of the certificate seen at that serial. It is not the
// accepted certificate: a certificate may be seen and never accepted (expired,
// revoked, refused for a conflict), and the highest serial seen must still
// stand, or an older certificate and its key could come back. The caller keeps
// it per node and advances it whenever DecideSerial answers SerialAccept.
// Revoking a certificate never lowers it.
type SerialSeen struct {
	Node   ident.NodeID
	Serial uint64
	// Digest is the record digest of the certificate seen at Serial. It
	// identifies the certificate as a whole, window included.
	Digest Digest
}

// SerialSeenOf returns the state that records v as the highest serial seen for
// its node.
func SerialSeenOf(v Verified[NetworkKeyCertificate]) SerialSeen {
	return SerialSeen{Node: v.payload.Node, Serial: v.payload.Serial, Digest: v.digest}
}

// SerialVerdict is the outcome of comparing a new certificate with the highest
// serial already seen for the same node.
type SerialVerdict int

const (
	// SerialInvalid is the zero value: the comparison could not be made (the
	// incoming certificate has no serial, no node or no digest).
	SerialInvalid SerialVerdict = iota
	// SerialAccept: the incoming serial is higher than any seen (or none has
	// been seen). Record it as the highest seen with SerialSeenOf.
	SerialAccept
	// SerialStale: the incoming serial is lower than the highest seen, or it is
	// the very certificate seen at that serial. Nothing new.
	SerialStale
	// SerialConflict: the same serial carries a different certificate (another
	// network key, or the same key with another window). The identity key signed
	// two certificates under one serial, which is a sign of a copied or
	// compromised identity key. Keep what was seen and tell the owner.
	SerialConflict
	// SerialWrongNode: the incoming certificate is for another node than the
	// state; the caller looked up the wrong state. Nothing is accepted.
	SerialWrongNode
)

// DecideSerial applies the rule "accept only the highest serial seen". seen is
// the state for the incoming certificate's node, or nil if none has been seen.
// incoming must have passed Verify, which is what the type says. The decision
// ignores validity in time and revocation: an expired certificate with a higher
// serial still advances the serial seen, and a revoked one keeps it where it
// was, so an older key cannot come back either way.
func DecideSerial(seen *SerialSeen, incoming Verified[NetworkKeyCertificate]) SerialVerdict {
	c := incoming.payload
	if c.Serial < 1 || c.Node.IsZero() || incoming.digest.IsZero() {
		return SerialInvalid
	}
	if seen == nil {
		return SerialAccept
	}
	switch {
	case seen.Node != c.Node:
		return SerialWrongNode
	case c.Serial > seen.Serial:
		return SerialAccept
	case c.Serial < seen.Serial:
		return SerialStale
	case incoming.digest != seen.Digest:
		return SerialConflict
	}
	return SerialStale
}

// KeyRevocationStatus is the outcome of looking an accepted network-key
// certificate up in the revocation records. Its zero value means nobody looked:
// a caller that forgot the lookup must get a refusal, never a certificate that
// merely looks unrevoked.
type KeyRevocationStatus int

const (
	// KeyRevocationUnchecked is the zero value: the revocation records were not
	// consulted. NetworkKeyBelongs refuses it.
	KeyRevocationUnchecked KeyRevocationStatus = iota
	// KeyRevocationClear means the certificate was looked up and is not revoked.
	KeyRevocationClear
	// KeyRevocationRevoked means the certificate has been revoked.
	KeyRevocationRevoked
)

// AcceptedNetworkKey is what a node currently accepts as another node's
// network key: the accepted certificate and the result of its revocation
// lookup. The accepted certificate is the highest-serial certificate that was
// verified and taken in; it is kept apart from SerialSeen.
type AcceptedNetworkKey struct {
	// Cert is the accepted certificate; nil if none is known.
	Cert *NetworkKeyCertificate
	// Revocation is the result of looking Cert up in the revocation records.
	// This package does not read revocations; the caller fills it. The zero
	// value KeyRevocationUnchecked is refused, and so is any value outside the
	// known outcomes.
	Revocation KeyRevocationStatus
}

// Binding is the answer to "does this network key belong to this node".
type Binding int

const (
	// BindingInvalid is the zero value; it is never returned as an answer.
	BindingInvalid Binding = iota
	// BindingOK: the accepted certificate names this key for this node, was
	// checked as not revoked and is valid now.
	BindingOK
	// BindingNoCertificate: no accepted certificate for the node is known.
	BindingNoCertificate
	// BindingNodeMismatch: the certificate is for another node.
	BindingNodeMismatch
	// BindingKeyMismatch: the certificate names a different network key.
	BindingKeyMismatch
	// BindingNotYetValid: the certificate's window has not started.
	BindingNotYetValid
	// BindingExpired: the certificate's window has ended.
	BindingExpired
	// BindingRevoked: the certificate names this key for this node but has been
	// revoked.
	BindingRevoked
	// BindingRevocationUnchecked: the certificate names this key for this node
	// but nobody looked it up in the revocation records. It is a caller bug and
	// is refused, never read as "not revoked".
	BindingRevocationUnchecked
)

func (b Binding) String() string {
	switch b {
	case BindingOK:
		return "ok"
	case BindingNoCertificate:
		return "no-certificate"
	case BindingNodeMismatch:
		return "node-mismatch"
	case BindingKeyMismatch:
		return "key-mismatch"
	case BindingNotYetValid:
		return "not-yet-valid"
	case BindingExpired:
		return "expired"
	case BindingRevoked:
		return "revoked"
	case BindingRevocationUnchecked:
		return "revocation-unchecked"
	}
	return "invalid"
}

// NetworkKeyBelongs decides whether key is the network key of node, given what
// is accepted for that node. It is the only place that answers this question:
// every record signed by a network key (declarations, vouchers, reachability
// records, delivery reports) relies on it. A key that is not the certificate's
// is a mismatch whatever the certificate's state; a certificate that names the
// key is judged next for revocation, where a certificate that was never looked
// up is refused like a revoked one, then for its window.
func NetworkKeyBelongs(accepted AcceptedNetworkKey, node ident.NodeID, key ident.PublicKey, now time.Time) Binding {
	cert := accepted.Cert
	switch {
	case cert == nil:
		return BindingNoCertificate
	case node.IsZero() || cert.Node != node:
		return BindingNodeMismatch
	case key.IsZero() || cert.NetworkKey != key:
		return BindingKeyMismatch
	case accepted.Revocation == KeyRevocationRevoked:
		return BindingRevoked
	case accepted.Revocation != KeyRevocationClear:
		return BindingRevocationUnchecked
	}
	switch cert.ValidityAt(now) {
	case NotYetValid:
		return BindingNotYetValid
	case Expired:
		return BindingExpired
	case Valid:
		return BindingOK
	}
	return BindingNoCertificate
}
