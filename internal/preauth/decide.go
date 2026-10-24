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
	// member nor an authorized person. A node is a non-member node, a person a
	// non-member person.

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
	// peer key, an unset kind, a serial state that was not loaded (a nil map).
	ReasonInvalidInput
	// ReasonRevocationsUnavailable: no revocation set was given (a nil set).
	// Without one nothing can be known to be unrevoked, and the revocation
	// package panics on a nil set, so this package refuses first: a missing set is
	// a decision, not a crash.
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
	// serial, which points to a copied or compromised identity key. The first
	// certificate seen stays the trusted one and this one is refused; nothing is
	// recorded for the conflict, so the stored serial state is unchanged.
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
	// loaded. The revocation package panics on a nil set so that a set nobody
	// loaded cannot read as "nothing revoked"; this package refuses a nil set
	// first (ReasonRevocationsUnavailable), so a missing set is a decision and
	// not a crash. An empty set from revocation.NewSet says "looked, found
	// nothing".
	Revocations *revocation.Set
	// Trust is the view trust.Evaluate decides on. Its Retired set is replaced by
	// Revocations.TrustView before every evaluation, so retirement has one
	// source, the revocation set, and the caller fills nothing in; a Retired set
	// the view brings is not read.
	Trust trust.View
	// SerialSeen is the highest network-key certificate serial seen, per node. A
	// node missing from the map has had none seen. The map must be loaded: a nil
	// map is a state nobody loaded and is refused (ReasonInvalidInput), because
	// reading it as "nothing seen" would let an old network key back in. A node
	// that has seen nothing at all passes an empty map.
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
	// non-member node after a node exchange. Zero otherwise.
	Node ident.NodeID
	// Person is the person whose device certificate was accepted, for State
	// authorized person or non-member person after a person exchange. Zero
	// otherwise.
	Person ident.PersonID

	// Trust is trust.Evaluate's verdict for the node at the time of the exchange,
	// set when it was asked (member and non-member node after a node exchange).
	// It says why the state is what it is; it is not the sender's trust for a
	// request made later on the connection. The policy layer takes the current
	// verdict of trust.Evaluate for every request, so a trust that ended after
	// the exchange is seen there at once, and Keeps closes the connection that
	// the change demoted.
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
	//
	// Decisions on several connections may run at once, each against the facts it
	// loaded, so the caller stores the value only after re-checking it against the
	// state stored now (record.DecideSerial accepts it), or only when its serial
	// is higher than the stored one. A lower serial stored over a higher one would
	// bring the old network key back.
	SerialSeen record.SerialSeen
}

// Completed reports whether the exchange was accepted: the connection is a
// non-member node or person, a member or an authorized person. Otherwise it was
// refused.
func (d Decision) Completed() bool { return d.State.Valid() && d.State != StateUnauthenticated }

// Keeps reports whether a connection now in state current stays open when d is
// the decision made on it again with the current facts (see the caller
// guarantees in the README). A refused decision closes it. So does a demotion:
// a member or authorized person that is now a non-member. The accounting
// released the connection when it became a member, so it holds no non-member
// place and no NonMemberLifetime would end it; the peer reconnects and is
// accounted as the non-member it now is. A promotion (pairing made a non-member
// a member) or an unchanged state keeps the connection, and Completed is called
// again.
func (d Decision) Keeps(current State) bool {
	return d.Completed() && !(current.HasStanding() && d.State.NonMember())
}

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
//   - non-member node: the certificate is valid and names peer, and
//     trust.Evaluate denies the node.
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
//   - non-member person: the certificate is valid and the person has no standing
//     on the list (not listed, entry expired).
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
	case f.SerialSeen == nil:
		return refused(ReasonInvalidInput)
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

	switch record.NetworkKeyBelongs(f.Revocations.NetworkKey(cert, highestSeen(f.SerialSeen, advance, node).Serial), node, peer, f.Now) {
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

	verdict := trust.Evaluate(f.trustView(), node, f.Now)
	d := Decision{Node: node, Trust: verdict, NodeCertificate: cert, SerialSeen: advance}
	d.State, d.Reason = nodeState(verdict.Outcome)
	return d
}

// nodeState maps trust's outcome for a node with a valid certificate to a state.
// Allowed directly, allowed by inheritance and suspended are members; denied,
// and any outcome this package does not know, is a non-member node and never a
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
	return StateNonMemberNode, ReasonNotTrusted
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

// highestSeen is the highest serial seen for node after the certificate just
// decided on: the advance when it raised the serial, otherwise what is stored.
// It is what record.NetworkKeyBelongs judges the certificate against, so the
// network key of a certificate that is not the highest is refused there as well
// as by checkSerial.
func highestSeen(seen map[ident.NodeID]record.SerialSeen, advance record.SerialSeen, node ident.NodeID) record.SerialSeen {
	if !advance.Node.IsZero() {
		return advance
	}
	return seen[node]
}

// HeldMembers returns, in the order given and without repeats, the network keys
// of the held certificates that belong to members: the certificate is the
// current one for its node, valid now and not revoked, and the node is a member
// as Decide would decide (trusted directly, by inheritance, or suspended). The
// accounting exempts these peers from the aggregate cap before they have
// completed an exchange on this run. Facts without a time, a revocation set or a
// loaded serial state give none.
func HeldMembers(f Facts, certs []record.Verified[record.NetworkKeyCertificate]) []ident.PublicKey {
	if f.Now.IsZero() || f.Revocations == nil || f.SerialSeen == nil {
		return nil
	}
	var out []ident.PublicKey
	view := f.trustView()
	seen := map[ident.PublicKey]bool{}
	for _, cert := range certs {
		c := cert.Payload()
		advance, refusal := checkSerial(f.SerialSeen, cert)
		if refusal != ReasonUnset {
			continue
		}
		if record.NetworkKeyBelongs(f.Revocations.NetworkKey(cert, highestSeen(f.SerialSeen, advance, c.Node).Serial), c.Node, c.NetworkKey, f.Now) != record.BindingOK {
			continue
		}
		if state, _ := nodeState(trust.Evaluate(view, c.Node, f.Now).Outcome); state != StateMember || seen[c.NetworkKey] {
			continue
		}
		seen[c.NetworkKey] = true
		out = append(out, c.NetworkKey)
	}
	return out
}

// NextHeldExpiry returns the earliest instant after f.Now at which the answer of
// HeldMembers changes with the clock alone: the earliest end of a held
// certificate or of a credential in the trust view that has not ended yet. The
// zero time means nothing held or credited ends. HeldMembers is a function of
// the facts and the time, so a caller that refreshes SetHeld only when the facts
// change would keep a member exempt after its certificate or credential ran out;
// it also refreshes when this instant passes. Facts without a time give the
// zero time.
func NextHeldExpiry(f Facts, certs []record.Verified[record.NetworkKeyCertificate]) time.Time {
	var next time.Time
	consider := func(t time.Time) {
		if !t.IsZero() && t.After(f.Now) && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	if f.Now.IsZero() {
		return time.Time{}
	}
	for _, c := range certs {
		consider(c.Payload().NotAfter)
	}
	for _, c := range f.Trust.Credentials {
		consider(c.NotAfter)
	}
	return next
}

// trustView is the view trust.Evaluate decides on: Facts.Trust with its Retired
// set taken from the revocation set (Revocations.TrustView). A retired
// intermediate carries no trust, so the set must reach trust.Evaluate, not only
// the check of the node itself; deriving it here from the one source keeps the
// caller from having to remember it. Facts.Revocations must not be nil.
// Facts.Trust itself is not changed.
func (f Facts) trustView() trust.View { return f.Revocations.TrustView(f.Trust) }

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
	d.State, d.Reason = StateNonMemberPerson, ReasonPersonNotAuthorized
	return d
}
