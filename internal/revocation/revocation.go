package revocation

import (
	"errors"
	"fmt"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// ErrInvalidRevocation reports a Revocation that is the zero value or lacks a
// field its kind needs. Only Of and Verify build a Revocation, so this means the
// value came from a zero record.Verified.
var ErrInvalidRevocation = errors.New("invalid revocation")

// Kind says which of the four statements a Revocation comes from.
type Kind int

const (
	// KindInvalid is the zero value; it is never the kind of a real revocation.
	KindInvalid Kind = iota
	// KindNodeSelf: a node's identity key is void.
	KindNodeSelf
	// KindPersonSelf: a person's root key is void.
	KindPersonSelf
	// KindNetworkKeyCertificate: one network-key certificate is revoked.
	KindNetworkKeyCertificate
	// KindDeviceKey: one device key of a person is revoked.
	KindDeviceKey
)

func (k Kind) String() string {
	switch k {
	case KindNodeSelf:
		return "node-self"
	case KindPersonSelf:
		return "person-self"
	case KindNetworkKeyCertificate:
		return "network-key-certificate"
	case KindDeviceKey:
		return "device-key"
	}
	return "invalid"
}

// NodeLevel reports whether the kind concerns a node (its identity or one of
// its certificates). The other kinds concern a person.
func (k Kind) NodeLevel() bool { return k == KindNodeSelf || k == KindNetworkKeyCertificate }

// Revocation is a verified revocation of any of the four kinds, reduced to what
// the queries and the retention rule read. Its fields are private: a value comes
// from Of or Verify, from a statement that passed record.Verify against its own
// descriptor, so the signer rule of the kind has been applied. The zero value is
// invalid.
type Revocation struct {
	kind   Kind
	digest record.Digest
	node   ident.NodeID
	person ident.PersonID
	serial uint64
	cert   record.Digest
	device ident.PublicKey
}

// Kind returns the kind of the statement.
func (r Revocation) Kind() Kind { return r.kind }

// Digest returns the record digest of the statement, which identifies it.
func (r Revocation) Digest() record.Digest { return r.digest }

// Node returns the node of a node-level revocation, and the zero NodeID for the
// other kinds.
func (r Revocation) Node() ident.NodeID { return r.node }

// Person returns the person of a person-level revocation, and the zero PersonID
// for the other kinds.
func (r Revocation) Person() ident.PersonID { return r.person }

// payload is the closed set of statement payloads Of accepts.
type payload interface {
	NodeSelfRevocation | PersonSelfRevocation | NetworkKeyCertificateRevocation | DeviceKeyRevocation
}

// Of returns the Revocation for a verified statement of one of the four types.
// The statement must have been verified against its own descriptor
// (NodeSelfRevocationDescriptor and so on); Of does not check signatures.
func Of[P payload](v record.Verified[P]) Revocation {
	switch p := any(v.Payload()).(type) {
	case NodeSelfRevocation:
		return Revocation{kind: KindNodeSelf, digest: v.Digest(), node: p.Node}
	case PersonSelfRevocation:
		return Revocation{kind: KindPersonSelf, digest: v.Digest(), person: p.Person}
	case NetworkKeyCertificateRevocation:
		return Revocation{kind: KindNetworkKeyCertificate, digest: v.Digest(), node: p.Node, serial: p.Serial, cert: p.Certificate}
	case DeviceKeyRevocation:
		return Revocation{kind: KindDeviceKey, digest: v.Digest(), person: p.Person, device: p.DeviceKey}
	}
	return Revocation{}
}

// Verify checks a record of any of the four types and returns its Revocation.
// It is the entry point for a record received from a peer or read from storage:
// the type string selects the descriptor, and a record of another type is
// refused with record.ErrType. The signer rule, including that a statement is
// signed by the key it names, is the descriptor's.
func Verify(r record.Record) (Revocation, error) {
	switch r.Type {
	case NodeSelfRevocationType:
		return verified(NodeSelfRevocationDescriptor, r)
	case PersonSelfRevocationType:
		return verified(PersonSelfRevocationDescriptor, r)
	case NetworkKeyCertificateRevocationType:
		return verified(NetworkKeyCertificateRevocationDescriptor, r)
	case DeviceKeyRevocationType:
		return verified(DeviceKeyRevocationDescriptor, r)
	}
	return Revocation{}, fmt.Errorf("%w: %q is not a revocation statement", record.ErrType, r.Type)
}

func verified[P payload](d record.Descriptor[P], r record.Record) (Revocation, error) {
	v, err := record.Verify(d, r)
	if err != nil {
		return Revocation{}, err
	}
	return Of(v), nil
}

// check refuses a Revocation that is zero or lacks a field its kind needs.
func (r Revocation) check() error {
	if r.digest.IsZero() {
		return fmt.Errorf("%w: no digest", ErrInvalidRevocation)
	}
	switch r.kind {
	case KindNodeSelf:
		if r.node.IsZero() {
			return fmt.Errorf("%w: no node", ErrInvalidRevocation)
		}
	case KindPersonSelf:
		if r.person.IsZero() {
			return fmt.Errorf("%w: no person", ErrInvalidRevocation)
		}
	case KindNetworkKeyCertificate:
		if r.node.IsZero() || r.serial < 1 || r.cert.IsZero() {
			return fmt.Errorf("%w: certificate revocation lacks node, serial or digest", ErrInvalidRevocation)
		}
	case KindDeviceKey:
		if r.person.IsZero() || r.device.IsZero() {
			return fmt.Errorf("%w: device revocation lacks person or device key", ErrInvalidRevocation)
		}
	default:
		return fmt.Errorf("%w: no kind", ErrInvalidRevocation)
	}
	return nil
}
