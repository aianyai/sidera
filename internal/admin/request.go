package admin

import (
	"fmt"
	"io"

	"github.com/aianyai/sidera/internal/ident"
)

// OperationID identifies one management request. The sender picks it and keeps
// it when it resends the same request after an unknown outcome. The zero value
// means "none".
type OperationID [16]byte

// IsZero reports whether the ID is unset.
func (id OperationID) IsZero() bool { return id == OperationID{} }

// NewOperationID reads a new random OperationID from r. Callers pass
// crypto/rand.Reader; the package itself performs no hidden I/O. It fails if r
// fails or yields all zero bytes, which would mean "none".
func NewOperationID(r io.Reader) (OperationID, error) {
	var id OperationID
	if _, err := io.ReadFull(r, id[:]); err != nil {
		return OperationID{}, fmt.Errorf("operation id: read randomness: %w", err)
	}
	if id.IsZero() {
		return OperationID{}, fmt.Errorf("operation id: %w", ident.ErrZero)
	}
	return id, nil
}

// Fingerprint is a digest of a request's complete content (operation and
// parameters), computed by the caller with a collision-resistant hash. Two
// requests with the same fingerprint are the same request.
type Fingerprint [32]byte

// Version is the version of a configuration setting. The first version is 1;
// the zero value means "not supplied".
type Version uint64

// RequestKey is what a stored result is filed under. The operation ID is
// scoped to the person who sent it, so one person can neither read nor collide
// with another's results. The local owner is the zero Person.
type RequestKey struct {
	Person ident.PersonID
	ID     OperationID
}

// KeyFor returns the key for a request sent by actor. Call it only for an actor
// that Authorize allowed: for a denied or unknown actor the key would file a
// result, or let a lookup probe results, under a caller who has no standing.
// The same key scopes OpQueryOperationResult to the requester.
func KeyFor(actor Actor, id OperationID) RequestKey {
	if actor.Kind == ActorRemotePerson {
		return RequestKey{Person: actor.Person, ID: id}
	}
	return RequestKey{ID: id}
}

// Request is the part of a management request the pure checks read.
type Request struct {
	ID OperationID
	Op Operation
	// ExpectedVersion is the setting version the sender last saw. Required for
	// configuration changes.
	ExpectedVersion Version
}

// ValidateRequest checks the request's shape: a side-effecting request has an
// operation ID and a configuration change has an expected version. It returns
// ReasonOK for a well-formed request.
//
// The order of handling a request is fixed: Authorize, ValidateRequest; then,
// for a side-effecting request only, CheckReplay under KeyFor(actor, id), and
// only when CheckReplay says execute, CheckVersion, the execution and the
// storing of the result with its fingerprint. A stored result is returned as it
// is, without a version check: the request already ran, and a later version
// change must not turn its answer into a conflict. A request that is not
// side-effecting is executed after validation; it is never checked for replay
// and its result is never stored.
func ValidateRequest(r Request) Reason {
	switch {
	case r.Op.SideEffecting() && r.ID.IsZero():
		return ReasonMissingOperationID
	case r.Op.IsConfigChange() && r.ExpectedVersion == 0:
		return ReasonMissingExpectedVersion
	default:
		return ReasonOK
	}
}

// VersionVerdict is the result of CheckVersion.
type VersionVerdict struct {
	// Apply is true when the change may be applied.
	Apply  bool
	Reason Reason
}

// CheckVersion compares the version a change was based on with the current
// one. Any difference is a conflict: the change is rejected and the sender
// refreshes and decides again. Another person's change is never overwritten.
func CheckVersion(expected, current Version) VersionVerdict {
	if expected != current {
		return VersionVerdict{Reason: ReasonVersionConflict}
	}
	return VersionVerdict{Apply: true, Reason: ReasonOK}
}

// ReplayAction says what to do with a request whose operation ID may have been
// seen before.
type ReplayAction int

const (
	// ReplayExecute means the ID is new: execute the request and store its
	// result.
	ReplayExecute ReplayAction = iota
	// ReplayReturnStored means the same request was already executed: return the
	// stored result and do not execute again.
	ReplayReturnStored
	// ReplayConflict means the ID was used for a different request: execute
	// nothing and report the conflict.
	ReplayConflict
)

// ReplayVerdict is the result of CheckReplay.
type ReplayVerdict struct {
	Action ReplayAction
	Reason Reason
}

// StoredRequest is the fingerprint of a request that was executed earlier,
// stored with its result under the request's RequestKey.
type StoredRequest struct {
	Fingerprint Fingerprint
}

// CheckReplay applies the rule that one operation ID has one result. stored is
// the record filed under the request's key, or nil if there is none. A resend
// of the same request returns the stored result; reusing the ID for different
// content is refused, because returning the result of another operation would
// confirm something the sender never asked for.
func CheckReplay(stored *StoredRequest, incoming Fingerprint) ReplayVerdict {
	switch {
	case stored == nil:
		return ReplayVerdict{Action: ReplayExecute, Reason: ReasonFirstSeen}
	case stored.Fingerprint == incoming:
		return ReplayVerdict{Action: ReplayReturnStored, Reason: ReasonSameRequest}
	default:
		return ReplayVerdict{Action: ReplayConflict, Reason: ReasonOperationIDReused}
	}
}
