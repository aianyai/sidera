package record

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/ident"
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
// set and the window is not empty. Only the decoding side calls it; encoding
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

// SerialVerdict is the outcome of comparing a new certificate with the highest
// one already accepted for the same node.
type SerialVerdict int

const (
	// SerialInvalid is the zero value: the comparison could not be made (the
	// incoming certificate has no serial or no node).
	SerialInvalid SerialVerdict = iota
	// SerialAccept: the incoming serial is higher than any seen (or none has
	// been seen). Store it.
	SerialAccept
	// SerialStale: the incoming serial is lower than the highest seen, or equal
	// with the same key. Nothing new; keep the stored certificate.
	SerialStale
	// SerialConflict: the same serial carries a different network key. The
	// identity key signed two different certificates under one serial, which is a
	// sign of a copied or compromised identity key. Keep the stored certificate
	// and tell the owner.
	SerialConflict
	// SerialWrongNode: the two certificates are for different nodes; the caller
	// looked up the wrong stored certificate. Nothing is accepted.
	SerialWrongNode
)

// DecideSerial applies the rule "accept only the highest serial seen". known is
// the highest-serial certificate already accepted for the node, or nil if none.
// Both certificates must already have passed Verify; validity in time is not
// part of this decision, because an expired certificate with a higher serial
// still advances the highest serial seen (it must not let an older key back).
func DecideSerial(known *NetworkKeyCertificate, incoming NetworkKeyCertificate) SerialVerdict {
	if incoming.Serial < 1 || incoming.Node.IsZero() {
		return SerialInvalid
	}
	if known == nil {
		return SerialAccept
	}
	switch {
	case known.Node != incoming.Node:
		return SerialWrongNode
	case incoming.Serial > known.Serial:
		return SerialAccept
	case incoming.Serial < known.Serial:
		return SerialStale
	case incoming.NetworkKey != known.NetworkKey:
		return SerialConflict
	}
	return SerialStale
}

// Binding is the answer to "does this network key belong to this node".
type Binding int

const (
	// BindingInvalid is the zero value; it is never returned as an answer.
	BindingInvalid Binding = iota
	// BindingOK: the accepted certificate names this key for this node and is
	// valid now.
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
)

// NetworkKeyBelongs decides whether key is the network key of node, given the
// certificate accepted for that node (nil if none). It is the only place that
// answers this question: every record signed by a network key (declarations,
// vouchers, reachability records, delivery reports) relies on it.
func NetworkKeyBelongs(accepted *NetworkKeyCertificate, node ident.NodeID, key ident.PublicKey, now time.Time) Binding {
	switch {
	case accepted == nil:
		return BindingNoCertificate
	case node.IsZero() || accepted.Node != node:
		return BindingNodeMismatch
	case key.IsZero() || accepted.NetworkKey != key:
		return BindingKeyMismatch
	}
	switch accepted.ValidityAt(now) {
	case NotYetValid:
		return BindingNotYetValid
	case Expired:
		return BindingExpired
	case Valid:
		return BindingOK
	}
	return BindingNoCertificate
}
