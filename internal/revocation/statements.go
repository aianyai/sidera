package revocation

import (
	"fmt"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// Type strings of the four statements. The version is part of the signed bytes;
// a changed or extended statement is a new type string.
const (
	NodeSelfRevocationType              = "sidera/node-self-revocation/1"
	PersonSelfRevocationType            = "sidera/person-self-revocation/1"
	NetworkKeyCertificateRevocationType = "sidera/network-key-cert-revocation/1"
	DeviceKeyRevocationType             = "sidera/device-key-revocation/1"
)

// None of the statements carries a time. A revocation applies whenever it is
// learned, however old it is, and admin already says so; a time would be
// informational only, a field no rule may read, and it would give one fact
// several digests (the same statement signed at two moments), which the digest
// based synchronisation of revocations cannot tell apart from two facts.

// NodeSelfRevocation declares that a node's identity key is void. The identity
// key signs it; it is made together with the key and kept offline, and
// published when the key leaks. Nobody else can sign it, so whoever forwards it
// cannot forge it.
type NodeSelfRevocation struct {
	// Node is the node whose identity is revoked. The signer must be the
	// identity key that derives this identifier.
	Node ident.NodeID
}

// PersonSelfRevocation declares that a person's root key is void. The root key
// signs it; it is made together with the key and kept offline.
type PersonSelfRevocation struct {
	// Person is the person whose root key is revoked. The signer must be the
	// root key that derives this identifier.
	Person ident.PersonID
}

// NetworkKeyCertificateRevocation retires one network-key certificate of a node
// that has replaced its network key. The node's identity key signs it.
type NetworkKeyCertificateRevocation struct {
	// Node is the node the certificate is for. The signer must be the identity
	// key that derives this identifier.
	Node ident.NodeID
	// Serial is the serial of the revoked certificate.
	Serial uint64
	// Certificate is the digest of the revoked certificate. Serial alone would
	// name every certificate the identity key ever signed under that serial; the
	// digest names exactly one.
	Certificate record.Digest
}

// DeviceKeyRevocation retires a device key of a person, for example a lost
// laptop. The person's root key signs it, and it applies only to certificates of
// that person: the person is the identifier the signing root key derives, so a
// person can revoke their own devices and nobody else's.
type DeviceKeyRevocation struct {
	// Person is the person whose device is revoked. The signer must be the root
	// key that derives this identifier.
	Person ident.PersonID
	// DeviceKey is the revoked device key. It is revoked for every certificate
	// of this person, present and future.
	DeviceKey ident.PublicKey
}

type nodeSelfRevocationWire struct {
	Node []byte `cbor:"1,keyasint"`
}

type personSelfRevocationWire struct {
	Person []byte `cbor:"1,keyasint"`
}

type networkKeyCertificateRevocationWire struct {
	Node        []byte `cbor:"1,keyasint"`
	Serial      uint64 `cbor:"2,keyasint"`
	Certificate []byte `cbor:"3,keyasint"`
}

type deviceKeyRevocationWire struct {
	Person    []byte         `cbor:"1,keyasint"`
	DeviceKey record.KeyWire `cbor:"2,keyasint"`
}

// NodeSelfRevocationDescriptor describes the node self-revocation. It is signed
// by exactly one key: the identity key of the node it names.
var NodeSelfRevocationDescriptor = record.NewDescriptor(NodeSelfRevocationType,
	func(s NodeSelfRevocation) (nodeSelfRevocationWire, error) {
		return nodeSelfRevocationWire{Node: s.Node.Bytes()}, nil
	},
	func(w nodeSelfRevocationWire) (NodeSelfRevocation, error) {
		node, err := nodeFromWire(w.Node)
		return NodeSelfRevocation{Node: node}, err
	},
	func(s NodeSelfRevocation) []record.SignerSlot {
		return []record.SignerSlot{record.NodeIdentityKey(s.Node)}
	},
)

// PersonSelfRevocationDescriptor describes the person self-revocation. It is
// signed by exactly one key: the root key of the person it names.
var PersonSelfRevocationDescriptor = record.NewDescriptor(PersonSelfRevocationType,
	func(s PersonSelfRevocation) (personSelfRevocationWire, error) {
		return personSelfRevocationWire{Person: s.Person.Bytes()}, nil
	},
	func(w personSelfRevocationWire) (PersonSelfRevocation, error) {
		person, err := personFromWire(w.Person)
		return PersonSelfRevocation{Person: person}, err
	},
	func(s PersonSelfRevocation) []record.SignerSlot {
		return []record.SignerSlot{record.PersonRootKey(s.Person)}
	},
)

// NetworkKeyCertificateRevocationDescriptor describes the revocation of a
// network-key certificate. It is signed by exactly one key: the identity key of
// the node it names. The network key itself cannot revoke its own certificate: a
// leaked network key must not be able to retire the certificate that limits it
// (or, by revoking a later one, cut its node off).
var NetworkKeyCertificateRevocationDescriptor = record.NewDescriptor(NetworkKeyCertificateRevocationType,
	func(s NetworkKeyCertificateRevocation) (networkKeyCertificateRevocationWire, error) {
		return networkKeyCertificateRevocationWire{Node: s.Node.Bytes(), Serial: s.Serial, Certificate: s.Certificate[:]}, nil
	},
	func(w networkKeyCertificateRevocationWire) (NetworkKeyCertificateRevocation, error) {
		node, err := nodeFromWire(w.Node)
		if err != nil {
			return NetworkKeyCertificateRevocation{}, err
		}
		if err := record.RequireCounter(w.Serial); err != nil {
			return NetworkKeyCertificateRevocation{}, fmt.Errorf("serial: %w", err)
		}
		if len(w.Certificate) != len(record.Digest{}) {
			return NetworkKeyCertificateRevocation{}, fmt.Errorf("certificate digest: %d bytes, want %d", len(w.Certificate), len(record.Digest{}))
		}
		var digest record.Digest
		copy(digest[:], w.Certificate)
		if digest.IsZero() {
			return NetworkKeyCertificateRevocation{}, fmt.Errorf("certificate digest: zero")
		}
		return NetworkKeyCertificateRevocation{Node: node, Serial: w.Serial, Certificate: digest}, nil
	},
	func(s NetworkKeyCertificateRevocation) []record.SignerSlot {
		return []record.SignerSlot{record.NodeIdentityKey(s.Node)}
	},
)

// DeviceKeyRevocationDescriptor describes the revocation of a device key. It is
// signed by exactly one key: the root key of the person it names. A statement
// whose person is not the signer's cannot be sealed or verified, so a person's
// revocation can never reach another person's devices.
var DeviceKeyRevocationDescriptor = record.NewDescriptor(DeviceKeyRevocationType,
	func(s DeviceKeyRevocation) (deviceKeyRevocationWire, error) {
		return deviceKeyRevocationWire{Person: s.Person.Bytes(), DeviceKey: record.KeyToWire(s.DeviceKey)}, nil
	},
	func(w deviceKeyRevocationWire) (DeviceKeyRevocation, error) {
		person, err := personFromWire(w.Person)
		if err != nil {
			return DeviceKeyRevocation{}, err
		}
		key, err := record.KeyFromWire(w.DeviceKey)
		if err != nil {
			return DeviceKeyRevocation{}, fmt.Errorf("device key: %w", err)
		}
		// No certificate can name the root key as a device key, so a statement
		// that does revokes nothing. Whoever signs it believes they have revoked
		// the root key: refuse it loudly; the root key is revoked by its own
		// self-revocation.
		if derived, err := ident.PersonIDFromKey(key); err == nil && derived == person {
			return DeviceKeyRevocation{}, fmt.Errorf("%w: the device key is the root key of the person", record.ErrRoleSeparation)
		}
		return DeviceKeyRevocation{Person: person, DeviceKey: key}, nil
	},
	func(s DeviceKeyRevocation) []record.SignerSlot {
		return []record.SignerSlot{record.PersonRootKey(s.Person)}
	},
)

// nodeFromWire and personFromWire convert an identifier; ident refuses a wrong
// length and the all-zero value.
func nodeFromWire(b []byte) (ident.NodeID, error) {
	node, err := ident.NodeIDFromBytes(b)
	if err != nil {
		return ident.NodeID{}, fmt.Errorf("node: %w", err)
	}
	return node, nil
}

func personFromWire(b []byte) (ident.PersonID, error) {
	person, err := ident.PersonIDFromBytes(b)
	if err != nil {
		return ident.PersonID{}, fmt.Errorf("person: %w", err)
	}
	return person, nil
}

// IssueNodeSelfRevocation builds and signs the self-revocation of the node whose
// identity key is identity. It names the node the key derives, so it cannot name
// another. It is made when the key is made and kept offline by the caller; this
// function stores nothing.
func IssueNodeSelfRevocation(identity record.Signer) (record.Record, error) {
	node, err := ident.NodeIDFromKey(identity.Public())
	if err != nil {
		return record.Record{}, fmt.Errorf("identity key: %w", err)
	}
	return record.Seal(NodeSelfRevocationDescriptor, NodeSelfRevocation{Node: node}, identity)
}

// IssuePersonSelfRevocation builds and signs the self-revocation of the person
// whose root key is root. It is made when the key is made and kept offline by
// the caller; this function stores nothing.
func IssuePersonSelfRevocation(root record.Signer) (record.Record, error) {
	person, err := ident.PersonIDFromKey(root.Public())
	if err != nil {
		return record.Record{}, fmt.Errorf("root key: %w", err)
	}
	return record.Seal(PersonSelfRevocationDescriptor, PersonSelfRevocation{Person: person}, root)
}

// IssueNetworkKeyCertificateRevocation builds and signs the revocation of cert,
// a verified certificate of the node whose identity key is identity. Sealing
// refuses any other signer, the network key included.
func IssueNetworkKeyCertificateRevocation(cert record.Verified[record.NetworkKeyCertificate], identity record.Signer) (record.Record, error) {
	return record.Seal(NetworkKeyCertificateRevocationDescriptor, NetworkKeyCertificateRevocation{
		Node:        cert.Payload().Node,
		Serial:      cert.Payload().Serial,
		Certificate: cert.Digest(),
	}, identity)
}

// IssueDeviceKeyRevocation builds and signs the revocation of deviceKey for the
// person whose root key is root. It names the person the key derives, so it
// cannot name another.
func IssueDeviceKeyRevocation(root record.Signer, deviceKey ident.PublicKey) (record.Record, error) {
	person, err := ident.PersonIDFromKey(root.Public())
	if err != nil {
		return record.Record{}, fmt.Errorf("root key: %w", err)
	}
	return record.Seal(DeviceKeyRevocationDescriptor, DeviceKeyRevocation{Person: person, DeviceKey: deviceKey}, root)
}
