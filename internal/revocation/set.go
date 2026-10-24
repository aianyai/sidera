package revocation

import (
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
// A nil *Set is a set that was never loaded, not an empty one: every query and
// Add panics on it with a message naming the cause. Answering "nothing is
// revoked" for a set nobody loaded would be the fail-open path the three-valued
// revocation status exists to close, and a caller that forgot to initialise its
// set would never notice. The set of a node that has really checked and holds no
// revocation is NewSet(), which answers "clear".
//
// A Set is not safe for concurrent use: Add writes and the queries read the same
// maps, so a caller that shares one between goroutines serializes the calls.
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

// notLoaded is the panic value of every method called on a nil *Set.
const notLoaded = "revocation: revocation set not loaded"

// mustBeLoaded panics on a nil Set. Every exported method calls it first, so no
// answer is ever given on behalf of a set that was never loaded.
func (s *Set) mustBeLoaded() {
	if s == nil {
		panic(notLoaded)
	}
}

// NewSet returns an empty set that has been checked: it revokes nothing and its
// queries say so.
func NewSet() *Set {
	return &Set{
		nodes:   map[ident.NodeID]struct{}{},
		persons: map[ident.PersonID]struct{}{},
		certs:   map[certificateKey]struct{}{},
		devices: map[deviceKey]struct{}{},
	}
}

// Add keeps r. The caller decides what to keep (see Classify); a revocation
// that is not added has no effect on any answer. It fails on an invalid
// Revocation and panics on a nil Set.
func (s *Set) Add(r Revocation) error {
	s.mustBeLoaded()
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
	s.mustBeLoaded()
	_, ok := s.nodes[n]
	return ok
}

// PersonRevoked reports whether p has a verified self-revocation: the root key
// is void, and so is every certificate it signed.
func (s *Set) PersonRevoked(p ident.PersonID) bool {
	s.mustBeLoaded()
	_, ok := s.persons[p]
	return ok
}

// Retired returns the nodes with a verified self-revocation, in the form
// trust.View.Retired takes. The result is a copy; an empty set gives an empty
// map, which retires nobody.
func (s *Set) Retired() trust.NodeSet {
	s.mustBeLoaded()
	out := trust.NodeSet{}
	for n := range s.nodes {
		out[n] = struct{}{}
	}
	return out
}

// DeviceCertificate returns the facts administration reads about cert: the
// signature-derived part (cap and window, as record.DeviceCertificate.AdminCert
// gives them) with the revocation status filled in. The status is always a
// checked one, never admin.RevocationUnchecked, so administration never refuses
// the certificate for a lookup nobody made:
//
//   - admin.RevocationRootKeyRevoked: the person the certificate names has a
//     verified self-revocation. It is the wider fact and wins over a revoked
//     device key.
//   - admin.RevocationCertRevoked: the person has a verified revocation of this
//     device key. The revocation is of the key, so it covers every certificate
//     of that person for it, whichever window the certificate has. A revocation
//     by another person never matches: the lookup is by the certificate's
//     person, and a statement can only name the person its signing root key
//     derives.
//   - admin.RevocationClear: neither.
//
// Revocations apply whatever the time: no clock is read. cert must have been
// verified against record.DeviceCertificateDescriptor; a zero Verified yields a
// certificate that grants nothing.
func (s *Set) DeviceCertificate(cert record.Verified[record.DeviceCertificate]) admin.DeviceCert {
	s.mustBeLoaded()
	c := cert.Payload()
	out := c.AdminCert()
	switch {
	case s.PersonRevoked(c.Person):
		out.Revocation = admin.RevocationRootKeyRevoked
	case s.deviceRevoked(c.Person, c.DeviceKey):
		out.Revocation = admin.RevocationCertRevoked
	default:
		out.Revocation = admin.RevocationClear
	}
	return out
}

// RemotePerson returns the actor for a remote person who presented cert. The
// person is the one the certificate names and the certificate facts are
// DeviceCertificate's for that same certificate, so a caller cannot pair a person
// with another person's certificate facts or with a revocation status nobody
// looked up. It is the way to build a remote admin.Actor from a verified
// certificate; cert must have been verified against
// record.DeviceCertificateDescriptor.
func (s *Set) RemotePerson(cert record.Verified[record.DeviceCertificate]) admin.Actor {
	s.mustBeLoaded()
	return admin.RemotePerson(cert.Payload().Person, s.DeviceCertificate(cert))
}

func (s *Set) deviceRevoked(p ident.PersonID, k ident.PublicKey) bool {
	_, ok := s.devices[deviceKey{p, k}]
	return ok
}

// NetworkKey returns the accepted network key for cert with its revocation
// status filled in, ready for record.NetworkKeyBelongs. The status is always a
// checked one, never record.KeyRevocationUnchecked: it is
// record.KeyRevocationRevoked when
//
//   - its node has a verified revocation of this very certificate (node, serial
//     and digest all match), or
//   - its node has a verified self-revocation: the identity key that signed the
//     certificate is void, and so is everything it signed. Otherwise an attacker
//     holding a leaked identity key would still have certificates the node's
//     peers accept.
//
// and record.KeyRevocationClear otherwise.
//
// It does not touch the highest serial seen; a revocation never lowers it (see
// record.SerialSeen). cert must have been verified against
// record.NetworkKeyCertificateDescriptor.
func (s *Set) NetworkKey(cert record.Verified[record.NetworkKeyCertificate]) record.AcceptedNetworkKey {
	s.mustBeLoaded()
	c := cert.Payload()
	status := record.KeyRevocationClear
	if s.certificateRevoked(c.Node, c.Serial, cert.Digest()) {
		status = record.KeyRevocationRevoked
	}
	return record.AcceptedNetworkKey{Cert: &c, Revocation: status}
}

func (s *Set) certificateRevoked(node ident.NodeID, serial uint64, digest record.Digest) bool {
	if _, ok := s.nodes[node]; ok {
		return true
	}
	_, ok := s.certs[certificateKey{node, serial, digest}]
	return ok
}
