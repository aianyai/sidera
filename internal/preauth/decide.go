package preauth

import (
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
	"github.com/aianyai/sidera/internal/trust"
)

// Reason says why Decide put a connection in the state it did. Reasons are
// stable machine-readable values; callers and tests branch on them.
type Reason uint8

const (
	// ReasonUnset is the zero value; no decision carries it.
	ReasonUnset Reason = iota

	// Reasons of a member.

	// ReasonMemberDirect: trust.Evaluate allowed the node directly.
	ReasonMemberDirect
	// ReasonMemberInherited: trust.Evaluate allowed the node by inheritance.
	ReasonMemberInherited
	// ReasonMemberSuspended: trust.Evaluate suspended the node, which means a
	// path to it exists and lacks only a fresh voucher. The connection is a
	// member's: the policy layer receives the suspended outcome and neither hands
	// out nor rejects what it holds, and the vouchers that end the suspension
	// travel on member protocols.
	ReasonMemberSuspended

	// Reason of an authorized person.

	// ReasonAuthorizedPerson: the device certificate is valid and the person has
	// a standing on the local list.
	ReasonAuthorizedPerson

	// Reasons of a non-member: the certificate is valid and the peer is neither a
	// member nor an authorized person.

	// ReasonNotTrusted: a node whose certificate is valid and whom trust.Evaluate
	// denied (never paired, revoked locally, retired as an intermediate, and so
	// on; Decision.Trust says which).
	ReasonNotTrusted
	// ReasonPersonNotAuthorized: a person whose device certificate is valid and
	// who has no standing on the list (not listed, entry expired, device cap too
	// low; Decision.Standing says which).
	ReasonPersonNotAuthorized

	// Reasons the exchange is refused. The state stays unauthenticated and the
	// network layer closes the connection.

	// ReasonInvalidInput: the facts or the message cannot be judged: no time, no
	// peer key, an unset kind, a serial state filed under the wrong node.
	ReasonInvalidInput
	// ReasonRevocationsUnavailable: no revocation set was given. Without one
	// nothing can be known to be unrevoked.
	ReasonRevocationsUnavailable
	// ReasonCertificateInvalid: the record does not verify against its type, or
	// names a state the verification cannot make sense of.
	ReasonCertificateInvalid
	// ReasonKeyMismatch: the key the secure handshake authenticated is not the
	// key the certificate names (the network key of a node, the device key of a
	// person). The peer presents somebody else's certificate.
	ReasonKeyMismatch
	// ReasonCertificateStale: the node already presented, or another route
	// delivered, a certificate with a higher serial. The old network key does not
	// come back.
	ReasonCertificateStale
	// ReasonSerialConflict: the identity key signed two certificates under one
	// serial. Neither is trusted until the owner sorts it out.
	ReasonSerialConflict
	// ReasonNodeRetired: the node's identity key has a verified self-revocation.
	ReasonNodeRetired
	// ReasonCertificateRevoked: the certificate, or for a person the device key,
	// is revoked.
	ReasonCertificateRevoked
	// ReasonPersonRevoked: the person's root key has a verified self-revocation.
	ReasonPersonRevoked
	// ReasonRevocationUnchecked: a revocation lookup nobody made. It cannot come
	// from the revocation package, which always checks; it is refused, never read
	// as "not revoked".
	ReasonRevocationUnchecked
	// ReasonCertificateExpired: the certificate's validity has ended.
	ReasonCertificateExpired
	// ReasonCertificateNotYetValid: the certificate's validity has not begun.
	ReasonCertificateNotYetValid
)

func (r Reason) String() string {
	switch r {
	case ReasonMemberDirect:
		return "member-direct"
	case ReasonMemberInherited:
		return "member-inherited"
	case ReasonMemberSuspended:
		return "member-suspended"
	case ReasonAuthorizedPerson:
		return "authorized-person"
	case ReasonNotTrusted:
		return "not-trusted"
	case ReasonPersonNotAuthorized:
		return "person-not-authorized"
	case ReasonInvalidInput:
		return "invalid-input"
	case ReasonRevocationsUnavailable:
		return "revocations-unavailable"
	case ReasonCertificateInvalid:
		return "certificate-invalid"
	case ReasonKeyMismatch:
		return "key-mismatch"
	case ReasonCertificateStale:
		return "certificate-stale"
	case ReasonSerialConflict:
		return "serial-conflict"
	case ReasonNodeRetired:
		return "node-retired"
	case ReasonCertificateRevoked:
		return "certificate-revoked"
	case ReasonPersonRevoked:
		return "person-revoked"
	case ReasonRevocationUnchecked:
		return "revocation-unchecked"
	case ReasonCertificateExpired:
		return "certificate-expired"
	case ReasonCertificateNotYetValid:
		return "certificate-not-yet-valid"
	}
	return "unset"
}

// Facts are the local facts a decision reads. The caller loads them before it
// decides and passes them as they are now; this package reads no clock and no
// storage.
type Facts struct {
	// Now is the time of the decision, UTC. The zero value is refused.
	Now time.Time
	// Revocations is every identity-level revocation the node keeps. It must be
	// loaded: nil is refused (ReasonRevocationsUnavailable), unlike the
	// revocation package, where a nil set means "nothing revoked". An empty set
	// from revocation.NewSet says "looked, found nothing".
	Revocations *revocation.Set
	// Trust is the view trust.Evaluate decides on. Its Retired set should be
	// Revocations.Retired(); a retired node is refused before trust is asked.
	Trust trust.View
	// SerialSeen is the highest network-key certificate serial seen, per node. A
	// node missing from the map has had none seen.
	SerialSeen map[ident.NodeID]record.SerialSeen
	// Authorized is the local authorized-persons list. The zero List is empty.
	Authorized admin.List
}

// Decision is the answer of Decide.
type Decision struct {
	// State is the state the connection moves to. StateUnauthenticated after an
	// exchange means the exchange was refused: the connection is closed, and
	// Reason says why. It is never StateUnset.
	State  State
	Reason Reason

	// Node is the node whose certificate was accepted, for State member or
	// non-member after a node exchange. Zero otherwise.
	Node ident.NodeID
	// Person is the person whose device certificate was accepted, for State
	// authorized person or non-member after a person exchange. Zero otherwise.
	Person ident.PersonID

	// Trust is trust.Evaluate's verdict for the node, set when it was asked
	// (member and non-member after a node exchange). Its Outcome is what the
	// policy layer takes as the sender's trust.
	Trust trust.Verdict
	// Standing is admin.EffectiveLevel's answer, set when it was asked (a person
	// whose device certificate is valid). Standing.Level is the person's level.
	Standing admin.Standing

	// NodeCertificate is the verified network-key certificate, set when a node
	// exchange completed (member or non-member). The caller keeps it as the
	// accepted certificate of the node.
	NodeCertificate record.Verified[record.NetworkKeyCertificate]
	// DeviceCertificate is the verified device certificate, set when a person
	// exchange completed.
	DeviceCertificate record.Verified[record.DeviceCertificate]

	// SerialSeen, when it is not the zero value, is the new highest serial seen
	// for its node: the caller stores it. It is set whenever a verified
	// certificate advanced the serial, also when the exchange is refused for
	// another reason, because the node did issue that serial and an older
	// certificate must not come back.
	SerialSeen record.SerialSeen
}

// Completed reports whether the exchange was accepted: the connection is a
// non-member, a member or an authorized person. Otherwise it was refused.
func (d Decision) Completed() bool { return d.State.Valid() && d.State != StateUnauthenticated }

// Decide turns a received exchange message into a connection state. peer is the
// public key the secure handshake authenticated for the remote side, which is
// the only identity the peer proved; the message is what it claims.
//
// A node exchange yields:
//
//   - refused, state unauthenticated: the record does not verify, the serial is
//     older than the highest seen or conflicts with it, the node is retired, the
//     certificate is revoked, expired or not yet valid, or it names a network key
//     other than peer.
//   - non-member: the certificate is valid and names peer, and trust.Evaluate
//     denies the node.
//   - member: trust.Evaluate allows the node directly or by inheritance, or
//     suspends it. A suspended node is a member because its trust is not gone,
//     only waiting for a voucher; the policy layer is handed the suspended
//     outcome and holds what it must, and a connection refusing the node would
//     only hide the vouchers' route.
//
// A person exchange yields:
//
//   - refused: the device certificate does not verify, names a device key other
//     than peer, is revoked (the key or the person's root key), expired or not
//     yet valid.
//   - non-member: the certificate is valid and the person has no standing on the
//     list (not listed, entry expired).
//   - authorized person: admin.EffectiveLevel gives a level.
//
// Decide fails closed: whatever it cannot judge is a refusal, and a node or
// person becomes a member or an authorized person only through a positive answer
// of the package that owns the rule.
func Decide(f Facts, peer ident.PublicKey, ex Exchange) Decision {
	switch {
	case f.Now.IsZero(), peer.IsZero():
		return refused(ReasonInvalidInput)
	case f.Revocations == nil:
		return refused(ReasonRevocationsUnavailable)
	}
	switch ex.Kind {
	case KindNode:
		return decideNode(f, peer, ex.Record)
	case KindPerson:
		return decidePerson(f, peer, ex.Record)
	}
	return refused(ReasonInvalidInput)
}

func refused(r Reason) Decision { return Decision{State: StateUnauthenticated, Reason: r} }

func decideNode(f Facts, peer ident.PublicKey, rec record.Record) Decision {
	cert, err := record.Verify(record.NetworkKeyCertificateDescriptor, rec)
	if err != nil {
		return refused(ReasonCertificateInvalid)
	}
	node := cert.Payload().Node

	// The serial comes first and is kept whatever else is found: the node did
	// issue this serial, so an older certificate must not come back.
	advance, refusal := checkSerial(f.SerialSeen, cert)
	if refusal != ReasonUnset {
		return withSerial(refused(refusal), advance)
	}

	switch record.NetworkKeyBelongs(f.Revocations.NetworkKey(cert), node, peer, f.Now) {
	case record.BindingOK:
	case record.BindingKeyMismatch:
		return withSerial(refused(ReasonKeyMismatch), advance)
	case record.BindingRevoked:
		// A node's self-revocation also revokes the certificates it signed; the
		// reason says which one it is.
		if f.Revocations.NodeRetired(node) {
			return withSerial(refused(ReasonNodeRetired), advance)
		}
		return withSerial(refused(ReasonCertificateRevoked), advance)
	case record.BindingRevocationUnchecked:
		return withSerial(refused(ReasonRevocationUnchecked), advance)
	case record.BindingExpired:
		return withSerial(refused(ReasonCertificateExpired), advance)
	case record.BindingNotYetValid:
		return withSerial(refused(ReasonCertificateNotYetValid), advance)
	default:
		return withSerial(refused(ReasonCertificateInvalid), advance)
	}

	verdict := trust.Evaluate(f.Trust, node, f.Now)
	d := Decision{Node: node, Trust: verdict, NodeCertificate: cert, SerialSeen: advance}
	d.State, d.Reason = nodeState(verdict.Outcome)
	return d
}

// nodeState maps trust's outcome for a node with a valid certificate to a state.
// Allowed directly, allowed by inheritance and suspended are members; denied,
// and any outcome this package does not know, is a non-member and never a
// member.
func nodeState(o trust.Outcome) (State, Reason) {
	switch o {
	case trust.AllowedDirect:
		return StateMember, ReasonMemberDirect
	case trust.AllowedInherited:
		return StateMember, ReasonMemberInherited
	case trust.Suspended:
		return StateMember, ReasonMemberSuspended
	}
	return StateNonMember, ReasonNotTrusted
}

// checkSerial applies the rule "only the highest serial seen counts" to a
// verified certificate. It returns the state to store when the serial advanced
// (the zero value otherwise) and ReasonUnset when the certificate may go on, or
// the reason it is refused. The very certificate seen at the highest serial is
// stale for DecideSerial, which answers "nothing new", and is the normal case of
// a peer that connects again; only a lower serial is an old certificate.
func checkSerial(seen map[ident.NodeID]record.SerialSeen, cert record.Verified[record.NetworkKeyCertificate]) (record.SerialSeen, Reason) {
	var prior *record.SerialSeen
	if s, ok := seen[cert.Payload().Node]; ok {
		prior = &s
	}
	switch record.DecideSerial(prior, cert) {
	case record.SerialAccept:
		return record.SerialSeenOf(cert), ReasonUnset
	case record.SerialStale:
		if prior == nil || prior.Serial != cert.Payload().Serial {
			return record.SerialSeen{}, ReasonCertificateStale
		}
		return record.SerialSeen{}, ReasonUnset
	case record.SerialConflict:
		return record.SerialSeen{}, ReasonSerialConflict
	}
	return record.SerialSeen{}, ReasonInvalidInput
}

// HeldMembers returns, in the order given and without repeats, the network keys
// of the held certificates that belong to members: the certificate is the
// current one for its node, valid now and not revoked, and the node is a member
// as Decide would decide (trusted directly, by inheritance, or suspended). The
// accounting exempts these peers from the aggregate cap before they have
// completed an exchange on this run. Facts without a time or a revocation set
// give none.
func HeldMembers(f Facts, certs []record.Verified[record.NetworkKeyCertificate]) []ident.PublicKey {
	if f.Now.IsZero() || f.Revocations == nil {
		return nil
	}
	var out []ident.PublicKey
	seen := map[ident.PublicKey]bool{}
	for _, cert := range certs {
		c := cert.Payload()
		if _, refusal := checkSerial(f.SerialSeen, cert); refusal != ReasonUnset {
			continue
		}
		if record.NetworkKeyBelongs(f.Revocations.NetworkKey(cert), c.Node, c.NetworkKey, f.Now) != record.BindingOK {
			continue
		}
		if state, _ := nodeState(trust.Evaluate(f.Trust, c.Node, f.Now).Outcome); state != StateMember || seen[c.NetworkKey] {
			continue
		}
		seen[c.NetworkKey] = true
		out = append(out, c.NetworkKey)
	}
	return out
}

func withSerial(d Decision, s record.SerialSeen) Decision {
	d.SerialSeen = s
	return d
}

func decidePerson(f Facts, peer ident.PublicKey, rec record.Record) Decision {
	cert, err := record.Verify(record.DeviceCertificateDescriptor, rec)
	if err != nil {
		return refused(ReasonCertificateInvalid)
	}
	if cert.Payload().DeviceKey != peer {
		return refused(ReasonKeyMismatch)
	}
	person := cert.Payload().Person
	standing := admin.EffectiveLevel(f.Authorized, person, f.Revocations.DeviceCertificate(cert), f.Now)
	d := Decision{Person: person, Standing: standing, DeviceCertificate: cert}
	switch standing.Reason {
	case admin.ReasonOK:
		if standing.Level.Valid() {
			d.State, d.Reason = StateAuthorizedPerson, ReasonAuthorizedPerson
			return d
		}
	case admin.ReasonRootKeyRevoked:
		return refused(ReasonPersonRevoked)
	case admin.ReasonCertRevoked:
		return refused(ReasonCertificateRevoked)
	case admin.ReasonRevocationUnchecked:
		return refused(ReasonRevocationUnchecked)
	case admin.ReasonCertExpired:
		return refused(ReasonCertificateExpired)
	case admin.ReasonCertNotYetValid:
		return refused(ReasonCertificateNotYetValid)
	}
	// Not on the list, entry expired, cap too low, or a reason this package does
	// not know: a valid certificate without a standing, never an authorized
	// person.
	d.State, d.Reason = StateNonMember, ReasonPersonNotAuthorized
	return d
}
