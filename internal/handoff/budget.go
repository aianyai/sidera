package handoff

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// Unit is the unit in which a runtime path limits tool output.
type Unit int

const (
	// UnitChars counts UTF-16 code units, which is how a JavaScript string
	// (the host of the Claude tools) measures its length. A rune outside the
	// basic plane costs 2.
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

// Path names one way a tool result reaches a model.
type Path string

const (
	PathClaudeBashSuccess Path = "claude-bash-success"
	PathClaudeBashFailure Path = "claude-bash-failure"
	PathClaudeMCP         Path = "claude-mcp"
	PathCodex             Path = "codex"
)

// PathBudget is the limit on what the model sees of a result on one path.
type PathBudget struct {
	Path   Path
	Budget Budget
}

// PathBudgets returns the visible limit of every known path, in the unit that
// path counts in. They are data about the runtimes, to be revised as the
// runtimes change; the figures are those measured on Claude Code 2.1.308 and
// Codex 0.163.0.
//
//   - Claude Bash success: about 30,000 characters inline; more is saved to a
//     file and the model sees a path and a short preview.
//   - Claude Bash failure (non-zero exit code): about 10,000 characters,
//     keeping head and tail only.
//   - Claude MCP tool: 25,000 tokens.
//   - Codex: 10,000 tokens, cut in the middle.
func PathBudgets() []PathBudget {
	return []PathBudget{
		{PathClaudeBashSuccess, Budget{UnitChars, 30_000}},
		{PathClaudeBashFailure, Budget{UnitChars, 10_000}},
		{PathClaudeMCP, Budget{UnitTokens, 25_000}},
		{PathCodex, Budget{UnitTokens, 10_000}},
	}
}

// SafetyMarginPercent is the share of the smallest path limit that
// DefaultBudget gives up. The runtimes wrap a result in text of their own
// (status lines, tags, a preview header) that counts against the limit but is
// not in the output, and their limits are measured on one version and may
// move; a tenth leaves room for both, which is far more than the wrappers
// seen so far take.
const SafetyMarginPercent = 10

// DefaultBudget returns the budget that is safe on every path of PathBudgets
// at once, in tokens.
//
// Limits in characters and in tokens can be compared because EstimateTokens
// never returns less than UTF16Len: a text whose estimate fits a limit L in
// either unit has at most L characters and at most L real tokens. The result
// is therefore the smallest of the limits, less the safety margin.
func DefaultBudget() Budget {
	smallest := 0
	for _, pb := range PathBudgets() {
		if smallest == 0 || pb.Budget.Limit < smallest {
			smallest = pb.Budget.Limit
		}
	}
	return Budget{UnitTokens, smallest * (100 - SafetyMarginPercent) / 100}
}

// runeStarts returns the byte offset of every rune of s, followed by len(s).
func runeStarts(s string) []int {
	offsets := make([]int, 0, utf8.RuneCountInString(s)+1)
	for i := range s {
		offsets = append(offsets, i)
	}
	return append(offsets, len(s))
}
