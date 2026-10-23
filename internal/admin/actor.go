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
	// ActorLocalOwner is the OS user of this machine acting on the local
	// interface.
	ActorLocalOwner
	// ActorRemotePerson is a person who presented a device certificate.
	ActorRemotePerson
)

// DeviceCert holds the facts about a person's device certificate that
// administration rules read. The caller has already verified the certificate
// chain up to the person's root key and checked the revocation sets; this
// package never sees keys or signatures.
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
	// CertRevoked reports that the device certificate has been revoked.
	CertRevoked bool
	// RootKeyRevoked reports that the person's root key has been revoked.
	RootKeyRevoked bool
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
// certificate must be valid and unrevoked, the person must be on the list with
// an unexpired entry, and the level is the lower of the list level and the
// certificate's cap. Whatever the person may do on other nodes is irrelevant.
//
// The caller re-evaluates this for requests on a live connection, so removing
// or downgrading an entry takes effect on the next request.
func EffectiveLevel(list List, person ident.PersonID, cert DeviceCert, now time.Time) Standing {
	return standing(list, person, cert, now, now)
}

// standing evaluates the list at listAt and the certificate's validity window
// at certAt. Revocations apply regardless of time.
func standing(list List, person ident.PersonID, cert DeviceCert, listAt, certAt time.Time) Standing {
	deny := func(r Reason) Standing { return Standing{Reason: r} }
	switch {
	case cert.RootKeyRevoked:
		return deny(ReasonRootKeyRevoked)
	case cert.CertRevoked:
		return deny(ReasonCertRevoked)
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
