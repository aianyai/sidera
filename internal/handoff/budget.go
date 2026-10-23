package handoff

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// Unit is the unit in which a runtime path limits tool output.
type Unit int

const (
	// UnitChars counts UTF-16 code units, which is how a runtime hosted in
	// JavaScript measures the length of a string. A rune outside the basic
	// plane costs 2.
	UnitChars Unit = iota + 1
	// UnitTokens counts the conservative token estimate of EstimateTokens.
	UnitTokens
)

// Budget is a limit on one tool result, in one unit.
type Budget struct {
	Unit  Unit
	Limit int
}

// ErrInvalidBudget is wrapped by errors about a budget with an unknown unit
// or a limit that is not positive.
var ErrInvalidBudget = errors.New("handoff: invalid budget")

func (b Budget) validate() error {
	if b.Unit != UnitChars && b.Unit != UnitTokens {
		return fmt.Errorf("%w: unknown unit %d", ErrInvalidBudget, b.Unit)
	}
	if b.Limit <= 0 {
		return fmt.Errorf("%w: limit must be positive, got %d", ErrInvalidBudget, b.Limit)
	}
	return nil
}

// Measure returns the size of s in the budget's unit.
func (b Budget) Measure(s string) int {
	if b.Unit == UnitChars {
		return UTF16Len(s)
	}
	return EstimateTokens(s)
}

// UTF16Len returns the number of UTF-16 code units of s.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}

// EstimateTokens returns an upper bound on the number of tokens any model
// tokenizer spends on s: the number of UTF-8 bytes.
//
// A token covers at least one byte of its input, and a tokenizer that falls
// back to bytes for rare characters spends at most one token per byte, so no
// text costs more tokens than it has bytes. The bound therefore holds for
// ASCII text, however dense in digits or punctuation, and for every non-ASCII
// rune: a Chinese or Japanese character counts 3, an emoji 4, always at least
// the one token it needs. It is also never smaller than UTF16Len, which is
// what lets DefaultBudget compare limits given in different units.
//
// The price is that ordinary English text, which really costs about a quarter
// of a token per byte, is counted four times too high, so long messages are
// split into more parts than strictly needed. Under-counting would show only
// as a truncated tool result, which the pre-acknowledgement check turns into
// a redelivery; the estimate prefers the extra parts to the redelivery.
func EstimateTokens(s string) int { return len(s) }

// SmallestVisibleLimit is the smallest limit, in the units of Budget, on what
// a model is shown of one tool result among the output paths of the runtimes
// Sidera has been measured against: 10,000 tokens. Limits in characters and in
// tokens compare because EstimateTokens never returns less than UTF16Len: a
// text whose estimate fits a limit L in either unit has at most L characters
// and at most L real tokens, so the smallest limit is safe on every such path
// at once.
const SmallestVisibleLimit = 10_000

// SafetyMarginPercent is the share of SmallestVisibleLimit that the default
// budget gives up. The runtimes wrap a result in text of their own (status
// lines, tags, a preview header) that counts against the limit but is not in
// the output, and their limits are measured on one version and may move; a
// tenth leaves room for both, which is far more than the wrappers seen so far
// take.
const SafetyMarginPercent = 10

// DefaultBudgetLimit is the limit of DefaultBudget: SmallestVisibleLimit less
// SafetyMarginPercent, 9,000.
const DefaultBudgetLimit = SmallestVisibleLimit * (100 - SafetyMarginPercent) / 100

// DefaultBudget returns the conservative budget for a front end whose adapter
// declares none, in tokens. It is safe on every path measured (see
// SmallestVisibleLimit). A runtime whose visible limit is smaller than that
// must declare its own budget; the default does not adapt to it.
func DefaultBudget() Budget {
	return Budget{UnitTokens, DefaultBudgetLimit}
}

// runeStarts returns the byte offset of every rune of s, followed by len(s).
func runeStarts(s string) []int {
	offsets := make([]int, 0, utf8.RuneCountInString(s)+1)
	for i := range s {
		offsets = append(offsets, i)
	}
	return append(offsets, len(s))
}
