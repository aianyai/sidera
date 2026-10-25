package fakeagent

import (
	"errors"
	"fmt"
)

// Category says what kind of failure an agent call came to. The categories are
// those of the agent interface; the last, OutcomeUnknown, is the one a client
// decides for itself: the request was written and no answer was read.
type Category string

// The categories.
const (
	CategoryInvalidArgument Category = "invalid_argument"
	CategoryUnauthenticated Category = "unauthenticated"
	CategoryNotFound        Category = "not_found"
	CategoryConflict        Category = "conflict"
	CategoryRefused         Category = "refused"
	CategoryBackpressure    Category = "backpressure"
	CategoryUnavailable     Category = "unavailable"
	CategoryInternal        Category = "internal"
	CategoryOutcomeUnknown  Category = "outcome_unknown"
)

// Categories lists every category in the order above.
func Categories() []Category {
	return []Category{
		CategoryInvalidArgument, CategoryUnauthenticated, CategoryNotFound, CategoryConflict,
		CategoryRefused, CategoryBackpressure, CategoryUnavailable, CategoryInternal, CategoryOutcomeUnknown,
	}
}

// Valid reports whether c is a category.
func (c Category) Valid() bool {
	for _, k := range Categories() {
		if c == k {
			return true
		}
	}
	return false
}

// Error is the error of an agent call: a category, a code within it (a stable
// lower-case word: a rejection reason such as backlog_full, or an error code such
// as lease_unknown) and a message for people.
type Error struct {
	Category Category
	Code     string
	Message  string
}

func (e *Error) Error() string { return fmt.Sprintf("%s/%s: %s", e.Category, e.Code, e.Message) }

// CategoryOf returns the category of the error, internal for an error that is not
// an *Error, and empty for nil.
func CategoryOf(err error) Category {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Category
	}
	return CategoryInternal
}

// CodeOf returns the code of the error, "unclassified" for an error that is not an
// *Error, and empty for nil.
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "unclassified"
}
