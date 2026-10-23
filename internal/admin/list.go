package admin

import (
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// Entry is one authorized person on a node's list.
type Entry struct {
	// Person is the digest of the person's root key.
	Person ident.PersonID
	// Level is the most the person may do on this node. It must be a valid
	// level: LevelView, LevelManage or LevelFullControl.
	Level Level
	// ExpiresAt is when the entry stops granting rights. The zero time means
	// the entry never expires.
	ExpiresAt time.Time
}

// expiredAt reports whether the entry no longer grants rights at t. An entry
// expires at the instant ExpiresAt, not after it.
func (e Entry) expiredAt(t time.Time) bool {
	return !e.ExpiresAt.IsZero() && !t.Before(e.ExpiresAt)
}

// ProblemReason says what is wrong with a list entry.
type ProblemReason string

const (
	// ProblemZeroPerson means the entry has no person ID.
	ProblemZeroPerson ProblemReason = "zero_person"
	// ProblemUnknownLevel means the entry's level is not view, manage or
	// full-control.
	ProblemUnknownLevel ProblemReason = "unknown_level"
	// ProblemDuplicatePerson means the person already appears in an earlier
	// entry. Two entries for one person would make the effective level depend on
	// which one a lookup finds first.
	ProblemDuplicatePerson ProblemReason = "duplicate_person"
)

// ListProblem is one reason a set of entries is not a valid list.
type ListProblem struct {
	// Index is the position of the offending entry in the input. For a
	// duplicate it is the later entry.
	Index  int
	Reason ProblemReason
}

// List is a node's authorized-persons list. The zero value is the empty list,
// which authorizes nobody. A List can only be built from valid entries, so
// every lookup is unambiguous.
//
// Only the local owner changes a list; this package never does so on behalf of
// a remote person (see OpModifyAuthorizedList).
type List struct {
	entries []Entry
}

// NewList validates entries and returns the list. If any entry is invalid it
// returns the empty list and every problem found, in input order, so the owner
// sees all of them at once.
func NewList(entries []Entry) (List, []ListProblem) {
	var problems []ListProblem
	seen := make(map[ident.PersonID]bool, len(entries))
	for i, e := range entries {
		if e.Person.IsZero() {
			problems = append(problems, ListProblem{Index: i, Reason: ProblemZeroPerson})
		} else if seen[e.Person] {
			problems = append(problems, ListProblem{Index: i, Reason: ProblemDuplicatePerson})
		}
		seen[e.Person] = true
		if !e.Level.Valid() {
			problems = append(problems, ListProblem{Index: i, Reason: ProblemUnknownLevel})
		}
	}
	if len(problems) > 0 {
		return List{}, problems
	}
	return List{entries: append([]Entry(nil), entries...)}, nil
}

// Entries returns a copy of the list's entries in their original order.
func (l List) Entries() []Entry { return append([]Entry(nil), l.entries...) }

// Lookup returns the person's entry, whether or not it has expired.
func (l List) Lookup(p ident.PersonID) (Entry, bool) {
	for _, e := range l.entries {
		if e.Person == p {
			return e, true
		}
	}
	return Entry{}, false
}
