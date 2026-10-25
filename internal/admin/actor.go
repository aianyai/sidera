package admin

import (
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// ActorKind says how a caller reached the node.
type ActorKind int

const (
	// ActorUnknown is the zero value. It has no rights.
	ActorUnknown ActorKind = iota
	// ActorLocalOwner is the OS user of this machine acting on the management
	// interface.
	ActorLocalOwner
	// ActorRemotePerson is a person who presented a device certificate.
	ActorRemotePerson
)

// RevocationStatus is the outcome of looking a device certificate and its
// person's root key up in the revocation sets. Its zero value means nobody
// looked: a caller that forgot the lookup must get a refusal, never a
// certificate that merely looks unrevoked.
type RevocationStatus int

const (
	// RevocationUnchecked is the zero value: the revocation sets were not
	// consulted. It grants nothing.
	RevocationUnchecked RevocationStatus = iota
	// RevocationClear means both the certificate and the person's root key were
	// looked up and neither is revoked.
	RevocationClear
	// RevocationCertRevoked means the device certificate is revoked and the
	// root key is not.
	RevocationCertRevoked
	// RevocationRootKeyRevoked means the person's root key is revoked, whether
	// or not the device certificate is too: the root key's revocation is the
	// wider fact and the one reported.
	RevocationRootKeyRevoked
)

// DeviceCert holds the facts about a person's device certificate that
// administration rules read. The caller has already verified the certificate
// chain up to the person's root key; this package never sees keys or
// signatures. The caller states the result of the revocation lookup in
// Revocation, and a certificate whose lookup was skipped is refused.
type DeviceCert struct {
	// MaxLevel is the highest level the certificate allows, for example View for
	// a key issued on a machine the person does not fully trust. A certificate
	// that states no cap must be passed as LevelFullControl, so that only the
	// list level limits the person; the zero value LevelNone means the
	// certificate grants nothing.
	MaxLevel Level
	// NotBefore is when the certificate becomes valid.
	NotBefore time.Time
	// NotAfter is when the certificate stops being valid, at that instant. A
	// zero NotAfter counts as already expired.
	NotAfter time.Time
	// Revocation is the result of the revocation lookup for the certificate and
	// the person's root key. The zero value RevocationUnchecked grants nothing.
	Revocation RevocationStatus
}

// Actor is whoever asks to administer the node.
type Actor struct {
	Kind ActorKind
	// Person and Cert are read only for ActorRemotePerson.
	Person ident.PersonID
	Cert   DeviceCert
}

// LocalOwner returns the actor for the local owner.
func LocalOwner() Actor { return Actor{Kind: ActorLocalOwner} }

// RemotePerson returns the actor for a remote person who presented a device
// certificate whose chain the caller has verified.
func RemotePerson(p ident.PersonID, cert DeviceCert) Actor {
	return Actor{Kind: ActorRemotePerson, Person: p, Cert: cert}
}

// Standing is a remote person's rights on a node at some moment.
type Standing struct {
	// Level is the effective level: the lower of ListLevel and the device
	// certificate's cap. It is LevelNone if the person has no rights.
	Level Level
	// ListLevel is the level on this node's list, or LevelNone if the person has
	// no usable entry.
	ListLevel Level
	// CapLevel is the device certificate's maximum level.
	CapLevel Level
	// Reason is ReasonOK when nothing disqualifies the person, and otherwise the
	// first disqualifying fact. A person who passes every check but whose
	// certificate cap is LevelNone has Reason ReasonDeviceCapTooLow.
	Reason Reason
}

// EffectiveLevel returns a remote person's rights on this node at now. The
// certificate must be valid and checked as unrevoked, the person must be on the
// list with an unexpired entry, and the level is the lower of the list level and
// the certificate's cap. Whatever the person may do on other nodes is irrelevant.
//
// The caller re-evaluates this for requests on a live connection, so removing
// or downgrading an entry takes effect on the next request.
func EffectiveLevel(list List, person ident.PersonID, cert DeviceCert, now time.Time) Standing {
	return standing(list, person, cert, now, now)
}

// standing evaluates the list at listAt and the certificate's validity window
// at certAt. Revocations apply regardless of time, and a revocation status
// that is not a known checked outcome counts as unchecked.
func standing(list List, person ident.PersonID, cert DeviceCert, listAt, certAt time.Time) Standing {
	deny := func(r Reason) Standing { return Standing{Reason: r} }
	switch {
	case cert.Revocation == RevocationRootKeyRevoked:
		return deny(ReasonRootKeyRevoked)
	case cert.Revocation == RevocationCertRevoked:
		return deny(ReasonCertRevoked)
	case cert.Revocation != RevocationClear:
		return deny(ReasonRevocationUnchecked)
	case certAt.Before(cert.NotBefore):
		return deny(ReasonCertNotYetValid)
	case !certAt.Before(cert.NotAfter):
		return deny(ReasonCertExpired)
	}
	entry, ok := list.Lookup(person)
	switch {
	case !ok:
		return deny(ReasonNotOnList)
	case entry.expiredAt(listAt):
		return deny(ReasonEntryExpired)
	}
	s := Standing{
		ListLevel: entry.Level.usable(),
		CapLevel:  cert.MaxLevel.usable(),
		Reason:    ReasonOK,
	}
	s.Level = minLevel(s.ListLevel, s.CapLevel)
	if s.Level == LevelNone {
		s.Reason = ReasonDeviceCapTooLow
	}
	return s
}
