package revocation

import (
	"fmt"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// Set is the revocations a node has verified and chose to keep. It answers the
// questions the judging packages ask about keys and certificates. Revocation is
// permanent: nothing is ever removed, and adding the same fact twice changes
// nothing, so the answers do not depend on the order revocations arrived in.
//
// Every query is on *Set; a nil *Set is the empty set, which revokes nothing
// (a node with no revocation source reports everything as unrevoked, as record
// documents for its own flags). Add on a nil Set fails.
type Set struct {
	nodes   map[ident.NodeID]struct{}
	persons map[ident.PersonID]struct{}
	certs   map[certificateKey]struct{}
	devices map[deviceKey]struct{}
}

// certificateKey names one certificate: the serial alone is not enough, as the
// identity key could sign two certificates under one serial.
type certificateKey struct {
	node   ident.NodeID
	serial uint64
	digest record.Digest
}

// deviceKey names a device key within one person: the same key under another
// person is another device.
type deviceKey struct {
	person ident.PersonID
	key    ident.PublicKey
}

// NewSet returns an empty set.
func NewSet() *Set {
	return &Set{
		nodes:   map[ident.NodeID]struct{}{},
		persons: map[ident.PersonID]struct{}{},
		certs:   map[certificateKey]struct{}{},
		devices: map[deviceKey]struct{}{},
	}
}

// Add keeps r. The caller decides what to keep (see Classify); a revocation
// that is not added has no effect on any answer. It fails on a nil Set and on an
// invalid Revocation.
func (s *Set) Add(r Revocation) error {
	if s == nil {
		return fmt.Errorf("%w: nil set", ErrInvalidRevocation)
	}
	if err := r.check(); err != nil {
		return err
	}
	switch r.kind {
	case KindNodeSelf:
		s.nodes[r.node] = struct{}{}
	case KindPersonSelf:
		s.persons[r.person] = struct{}{}
	case KindNetworkKeyCertificate:
		s.certs[certificateKey{r.node, r.serial, r.cert}] = struct{}{}
	case KindDeviceKey:
		s.devices[deviceKey{r.person, r.device}] = struct{}{}
	}
	return nil
}

// NodeRetired reports whether n has a verified self-revocation.
func (s *Set) NodeRetired(n ident.NodeID) bool {
	if s == nil {
		return false
	}
	_, ok := s.nodes[n]
	return ok
}

// PersonRevoked reports whether p has a verified self-revocation: the root key
// is void, and so is every certificate it signed.
func (s *Set) PersonRevoked(p ident.PersonID) bool {
	if s == nil {
		return false
	}
	_, ok := s.persons[p]
	return ok
}

// Retired returns the nodes with a verified self-revocation, in the form
// trust.View.Retired takes. The result is a copy; an empty set gives an empty
// map, which retires nobody.
func (s *Set) Retired() trust.NodeSet {
	out := trust.NodeSet{}
	if s == nil {
		return out
	}
	for n := range s.nodes {
		out[n] = struct{}{}
	}
	return out
}

// DeviceCertificate returns the facts administration reads about cert: the
// signature-derived part (cap and window, as record.DeviceCertificate.AdminCert
// gives them) with both revocation flags filled in.
//
//   - RootKeyRevoked: the person the certificate names has a verified
//     self-revocation.
//   - CertRevoked: the person has a verified revocation of this device key. The
//     revocation is of the key, so it covers every certificate of that person for
//     it, whichever window the certificate has. A revocation by another person
//     never matches: the lookup is by the certificate's person, and a statement
//     can only name the person its signing root key derives.
//
// Revocations apply whatever the time: no clock is read. cert must have been
// verified against record.DeviceCertificateDescriptor; a zero Verified yields a
// certificate that grants nothing.
func (s *Set) DeviceCertificate(cert record.Verified[record.DeviceCertificate]) admin.DeviceCert {
	c := cert.Payload
	out := c.AdminCert()
	out.RootKeyRevoked = s.PersonRevoked(c.Person)
	out.CertRevoked = s.deviceRevoked(c.Person, c.DeviceKey)
	return out
}

func (s *Set) deviceRevoked(p ident.PersonID, k ident.PublicKey) bool {
	if s == nil {
		return false
	}
	_, ok := s.devices[deviceKey{p, k}]
	return ok
}

// NetworkKey returns the accepted network key for cert with its Revoked flag
// filled in, ready for record.NetworkKeyBelongs. The certificate is revoked
// when
//
//   - its node has a verified revocation of this very certificate (node, serial
//     and digest all match), or
//   - its node has a verified self-revocation: the identity key that signed the
//     certificate is void, and so is everything it signed. Otherwise an attacker
//     holding a leaked identity key would still have certificates the node's
//     peers accept.
//
// It does not touch the highest serial seen; a revocation never lowers it (see
// record.SerialSeen). cert must have been verified against
// record.NetworkKeyCertificateDescriptor.
func (s *Set) NetworkKey(cert record.Verified[record.NetworkKeyCertificate]) record.AcceptedNetworkKey {
	c := cert.Payload
	return record.AcceptedNetworkKey{Cert: &c, Revoked: s.certificateRevoked(c.Node, c.Serial, cert.Digest)}
}

func (s *Set) certificateRevoked(node ident.NodeID, serial uint64, digest record.Digest) bool {
	if s == nil {
		return false
	}
	if _, ok := s.nodes[node]; ok {
		return true
	}
	_, ok := s.certs[certificateKey{node, serial, digest}]
	return ok
}
