package handoff

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Marker is one piece of text by which a runtime shows that it replaced or cut
// a tool result. Exactly one field is set: Literal is matched as a substring,
// Regexp as an RE2 regular expression (Go regexp, linear time), which must not
// match the empty text.
type Marker struct {
	Literal string
	Regexp  string
}

// AckMarkers is what an adapter declares about its runtime for
// CheckBeforeAck. The core knows no runtime: each adapter says how its
// runtime shows the two things that make a tool result differ from what recv
// printed. Both lists may be empty, which declares nothing.
type AckMarkers struct {
	// Replaced marks a notice that stands in for the result, such as one that
	// says the output was saved to a file and gives a path and a preview.
	Replaced []Marker
	// Truncated marks the place where the runtime cut the result.
	Truncated []Marker
}

// ErrInvalidMarker is wrapped by the errors of a marker set that cannot be
// used.
var ErrInvalidMarker = errors.New("handoff: invalid marker")

// MarkerSet is an AckMarkers that has been validated and compiled. The zero
// value declares no markers.
type MarkerSet struct {
	replaced  []matcher
	truncated []matcher
}

// matcher counts the occurrences of one marker.
type matcher struct {
	literal string
	re      *regexp.Regexp
}

func (m matcher) count(s string) int {
	if m.re != nil {
		return len(m.re.FindAllStringIndex(s, -1))
	}
	return strings.Count(s, m.literal)
}

// Compile validates the markers and prepares them for CheckBeforeAck. A marker
// that sets both fields or neither, a regular expression that does not
// compile, and one that matches the empty text (it would be found everywhere)
// are rejected with an error that wraps ErrInvalidMarker.
func (m AckMarkers) Compile() (MarkerSet, error) {
	replaced, err := compileMarkers("replaced", m.Replaced)
	if err != nil {
		return MarkerSet{}, err
	}
	truncated, err := compileMarkers("truncated", m.Truncated)
	if err != nil {
		return MarkerSet{}, err
	}
	return MarkerSet{replaced: replaced, truncated: truncated}, nil
}

func compileMarkers(list string, markers []Marker) ([]matcher, error) {
	out := make([]matcher, 0, len(markers))
	for i, m := range markers {
		switch {
		case m.Literal != "" && m.Regexp != "":
			return nil, fmt.Errorf("%w: %s[%d] sets both a literal and a regular expression", ErrInvalidMarker, list, i)
		case m.Literal != "":
			out = append(out, matcher{literal: m.Literal})
		case m.Regexp != "":
			re, err := regexp.Compile(m.Regexp)
			if err != nil {
				return nil, fmt.Errorf("%w: %s[%d]: %v", ErrInvalidMarker, list, i, err)
			}
			if re.MatchString("") {
				return nil, fmt.Errorf("%w: %s[%d]: %q matches the empty text", ErrInvalidMarker, list, i, m.Regexp)
			}
			out = append(out, matcher{re: re})
		default:
			return nil, fmt.Errorf("%w: %s[%d] is empty", ErrInvalidMarker, list, i)
		}
	}
	return out, nil
}

// exceeds reports whether observed has more occurrences than expected of any
// of the markers.
func exceeds(markers []matcher, observed, expected string) bool {
	for _, m := range markers {
		if m.count(observed) > m.count(expected) {
			return true
		}
	}
	return false
}

// AckVerdict says whether an adapter may acknowledge a recv on the agent's
// behalf.
type AckVerdict int

const (
	// AckAllowed: the model saw what recv handed out.
	AckAllowed AckVerdict = iota + 1
	// AckRefused: it may not have; the lease must run its course so that the
	// message is handed out again.
	AckRefused
)

// AckReason says why CheckBeforeAck reached its verdict.
type AckReason int

const (
	// AckSeenInFull: the observed result contains the expected output.
	AckSeenInFull AckReason = iota + 1
	// AckReplaced: the runtime replaced the result with a notice, for example
	// that the output was saved to a file; the model saw the notice, not the
	// messages.
	AckReplaced
	// AckTruncated: the runtime cut the result and marked the cut.
	AckTruncated
	// AckContentMismatch: the observed result does not contain the expected
	// output, and no declared marker says why.
	AckContentMismatch
)

// CheckBeforeAck compares what the model saw in the tool result of a recv with
// what recv handed out, before an adapter acknowledges on the agent's behalf.
//
// Acknowledging a message the model did not read loses it: the sender is told
// "delivered" and nobody reads the message. So the adapter acknowledges only
// when the expected output appears in the observed result in full. A marker of
// a replaced or cut result gets its own reason, because it says what to fix
// (a smaller budget); a marker counts only if the observed result has more of
// it than the expected output, since a message may itself discuss truncation.
// Whitespace around the expected output is ignored: runtimes trim and wrap
// results.
//
// The markers refine a refusal and also refuse a result that still contains
// the whole output next to a notice; the test that decides is containment, so
// a runtime that declares no markers is still held to it: different content is
// refused, and the output seen in full is accepted.
//
// observed is the text of the tool result as the model received it, and
// expected is Page.Output.
func CheckBeforeAck(markers MarkerSet, observed, expected string) (AckVerdict, AckReason) {
	if exceeds(markers.replaced, observed, expected) {
		return AckRefused, AckReplaced
	}
	if exceeds(markers.truncated, observed, expected) {
		return AckRefused, AckTruncated
	}
	if !strings.Contains(observed, strings.TrimSpace(expected)) {
		return AckRefused, AckContentMismatch
	}
	return AckAllowed, AckSeenInFull
}
