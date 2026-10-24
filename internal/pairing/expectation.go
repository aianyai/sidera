package pairing

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
)

// ErrExpectation reports an expectation that cannot be entered.
var ErrExpectation = errors.New("invalid expectation")

// Author is who entered an expectation: the local owner or a remote person.
// An expectation stands only while its author still has the right to confirm
// trust, so the author is kept and asked again each time the expectation is
// used. The zero Author is unset.
type Author struct {
	kind   admin.ActorKind
	person ident.PersonID
}

// LocalAuthor is the local owner.
func LocalAuthor() Author { return Author{kind: admin.ActorLocalOwner} }

// PersonAuthor is the remote person with the given identifier.
func PersonAuthor(p ident.PersonID) (Author, error) {
	if p.IsZero() {
		return Author{}, fmt.Errorf("%w: person is unset", ErrExpectation)
	}
	return Author{kind: admin.ActorRemotePerson, person: p}, nil
}

// AuthorOf returns the author an actor acts as.
func AuthorOf(a admin.Actor) (Author, error) {
	switch a.Kind {
	case admin.ActorLocalOwner:
		return LocalAuthor(), nil
	case admin.ActorRemotePerson:
		return PersonAuthor(a.Person)
	}
	return Author{}, fmt.Errorf("%w: unknown actor", ErrExpectation)
}

// IsZero reports whether a is the unset author.
func (a Author) IsZero() bool { return a.kind == admin.ActorUnknown }

// IsLocalOwner reports whether the author is the local owner.
func (a Author) IsLocalOwner() bool { return a.kind == admin.ActorLocalOwner }

// Person returns the author's person; the zero identifier for the local owner.
func (a Author) Person() ident.PersonID { return a.person }

func (a Author) valid() bool {
	switch a.kind {
	case admin.ActorLocalOwner:
		return a.person.IsZero()
	case admin.ActorRemotePerson:
		return !a.person.IsZero()
	}
	return false
}

// Expectation is a node identifier an owner entered ahead of time: this node
// is the one I mean to pair with. It stands for a confirmation made in
// advance, by someone with the right to confirm trust, bound to one node
// identifier. It is single use, expires after ExpectationTTL and is dropped
// when its author loses the right.
//
// Nothing else counts as an expectation: not an address, not a node found on
// the local network, not anything a peer says about itself.
type Expectation struct {
	// Node is the node expected.
	Node ident.NodeID
	// Author is who entered it.
	Author Author
	// Operation is the identifier of the management request that entered it.
	Operation admin.OperationID
	// CreatedAt is when it was entered, whole seconds.
	CreatedAt time.Time
}

func (e Expectation) validate() error {
	switch {
	case e.Node.IsZero():
		return fmt.Errorf("%w: node is unset", ErrExpectation)
	case !e.Author.valid():
		return fmt.Errorf("%w: author is unset", ErrExpectation)
	case e.Operation.IsZero():
		return fmt.Errorf("%w: operation is unset", ErrExpectation)
	case e.CreatedAt.IsZero() || !e.CreatedAt.Equal(time.Unix(e.CreatedAt.Unix(), 0)):
		return fmt.Errorf("%w: created_at must be whole seconds", ErrExpectation)
	}
	return nil
}

// ExpiresAt is the instant the expectation stops standing.
func (e Expectation) ExpiresAt(p Params) time.Time { return e.CreatedAt.Add(p.ExpectationTTL) }

// ExpiredAt reports whether the expectation has run out at now.
func (e Expectation) ExpiredAt(p Params, now time.Time) bool { return !now.Before(e.ExpiresAt(p)) }
