package handoff

import (
	"regexp"
	"strings"
)

// Runtime is a native agent runtime whose tool results the model reads.
type Runtime int

const (
	RuntimeClaude Runtime = iota + 1
	RuntimeCodex
)

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
	// AckSavedToFile: Claude replaced the result with a notice that the output
	// was saved to a file; the model saw a path and a preview, not the messages.
	AckSavedToFile
	// AckTruncated: Codex cut the result and marked the cut.
	AckTruncated
	// AckContentMismatch: the observed result does not contain the expected
	// output, and no known marker says why.
	AckContentMismatch
	// AckUnknownRuntime: the runtime is not one whose results are understood.
	AckUnknownRuntime
)

// claudeSavedMarkers appear in the notice Claude puts in place of an output it
// saved to a file.
var claudeSavedMarkers = []string{"<persisted-output>", "Full output saved to:"}

// codexTruncationMarkers appear where Codex cut a result: the warning line,
// and the "N tokens truncated" (or chars) text it inserts at the cut, written
// with the single ellipsis character or with three dots.
var codexTruncationMarkers = []*regexp.Regexp{
	regexp.MustCompile(`Warning: truncated output`),
	regexp.MustCompile(`(?:…|\.\.\.)\s*\d*\s*(?:tokens|chars|bytes) truncated\s*(?:…|\.\.\.)`),
}

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
// observed is the text of the tool result as the model received it, and
// expected is Page.Output.
func CheckBeforeAck(rt Runtime, observed, expected string) (AckVerdict, AckReason) {
	switch rt {
	case RuntimeClaude:
		for _, marker := range claudeSavedMarkers {
			if strings.Count(observed, marker) > strings.Count(expected, marker) {
				return AckRefused, AckSavedToFile
			}
		}
	case RuntimeCodex:
		for _, marker := range codexTruncationMarkers {
			if len(marker.FindAllStringIndex(observed, -1)) > len(marker.FindAllStringIndex(expected, -1)) {
				return AckRefused, AckTruncated
			}
		}
	default:
		return AckRefused, AckUnknownRuntime
	}
	if !strings.Contains(observed, strings.TrimSpace(expected)) {
		return AckRefused, AckContentMismatch
	}
	return AckAllowed, AckSeenInFull
}
