package adapterproto

import (
	"cmp"
	"regexp"
	"slices"
	"strings"

	"github.com/aianyai/sidera/sdk/go/semver"
)

// Patterns of names with a fixed syntax, also used by the manifest. The schema
// states the same expressions, which a test compares.
const (
	// AdapterIDPattern is the identifier of an adapter, such as "example".
	AdapterIDPattern = `^[a-z0-9][a-z0-9-]{0,31}$`
	// PresetNamePattern is the name of a runtime preset; its length is at most
	// MaxPresetNameLen. This package is the one home of the syntax:
	// ValidatePresetName applies it wherever a message names a preset. The
	// authorization rules only require a preset name to be non-empty and do not
	// check the syntax.
	PresetNamePattern = `^[a-z0-9][a-z0-9._-]*$`
	// ConfigKeyPattern is the key of a configuration field.
	ConfigKeyPattern = `^[a-z][a-z0-9_]{0,63}$`
	// ConfigMapKeyPattern is the key of one entry of a map value. It holds
	// visible ASCII only (U+0021 to U+007E): an owner writes it and no other rule
	// checks it, and without blanks or control characters it can be shown as it
	// is. 128 characters hold any model name.
	ConfigMapKeyPattern = `^[!-~]{1,128}$`
	// EnvNamePattern is the name of an environment variable the adapter injects.
	EnvNamePattern = `^[A-Za-z_][A-Za-z0-9_]*$`
	// ModelNamePattern is the name of a model as a runtime spells it: an alias, a
	// full id, a cloud provider's id or an ARN. Its length is at most
	// MaxModelNameLen. It holds no blank, control or non-ASCII character and no
	// shell metacharacter, so a name reaches an argv, a log and a terminal as it
	// is. It is read alike by RE2 and by ECMAScript with the u flag.
	ModelNamePattern = `^[A-Za-z0-9][A-Za-z0-9._:/@+\[\]-]*$`
	// reasonWordPattern is a short snake_case word.
	reasonWordPattern = `^[a-z][a-z0-9_]*$`
)

// MaxPresetNameLen bounds the length of a preset name.
const MaxPresetNameLen = 64

// MaxModelNameLen bounds the length of a model name.
const MaxModelNameLen = 128

var (
	adapterIDRE    = regexp.MustCompile(AdapterIDPattern)
	presetNameRE   = regexp.MustCompile(PresetNamePattern)
	modelNameRE    = regexp.MustCompile(ModelNamePattern)
	configKeyRE    = regexp.MustCompile(ConfigKeyPattern)
	configMapKeyRE = regexp.MustCompile(ConfigMapKeyPattern)
	envNameRE      = regexp.MustCompile(EnvNamePattern)
	reasonWordRE   = regexp.MustCompile(reasonWordPattern)
)

// Patterns returns every pattern of the protocol keyed by the name of its
// definition in the schema.
func Patterns() map[string]string {
	return map[string]string{
		"AttemptID":    attemptIDPattern,
		"LeaseRef":     leaseRefPattern,
		"Token":        tokenPattern,
		"AdapterID":    AdapterIDPattern,
		"PresetName":   PresetNamePattern,
		"DisplayName":  PersonTextPattern(),
		"Description":  DescriptionPattern(),
		"ModelName":    ModelNamePattern,
		"ConfigKey":    ConfigKeyPattern,
		"ConfigMapKey": ConfigMapKeyPattern,
		"EnvName":      EnvNamePattern,
		"ReasonWord":   reasonWordPattern,
		"SemVer":       semver.Pattern,
	}
}

// ValidatePresetName checks the syntax of a preset name.
func ValidatePresetName(s string) error {
	if err := strLen(s, 1, MaxPresetNameLen); err != nil {
		return err
	}
	return matches(presetNameRE, s)
}

// ValidateModelName checks the syntax of a model name. This package is the one
// home of the syntax: every message that names a model applies it.
func ValidateModelName(s string) error {
	if err := strLen(s, 1, MaxModelNameLen); err != nil {
		return err
	}
	return matches(modelNameRE, s)
}

// personTextRefused are the code points that text shown to a person never
// holds, as inclusive ranges, in ascending order:
//   - the control characters (U+0000 to U+001F, U+007F to U+009F), which a
//     terminal acts on;
//   - the line and paragraph separators (U+2028, U+2029) and the bidirectional
//     formatting controls (U+061C, U+200E, U+200F, U+202A to U+202E, U+2066 to
//     U+2069), which break or reorder lines of text;
//   - the invisible format characters that draw nothing: the zero width space
//     (U+200B), the whole block from the word joiner to the deprecated format
//     characters (U+2060 to U+206F: the word joiner, the invisible operators,
//     the isolates, the deprecated format characters and U+2065, which is
//     unassigned and which Unicode lists as default-ignorable), the byte order
//     mark used as a zero width no-break space (U+FEFF) and the tag characters
//     (U+E0000 to U+E007F, an invisible copy of ASCII);
//   - the other characters that draw nothing or a blank in place of a letter: the
//     combining grapheme joiner (U+034F), the Hangul fillers (U+115F, U+1160,
//     U+3164, U+FFA0), the Khmer vowels that were withdrawn (U+17B4, U+17B5), the
//     shorthand format controls (U+1BCA0 to U+1BCA3), the musical format controls
//     (U+1D173 to U+1D17A) and the ranges that Unicode reserves for more
//     invisible characters (U+FFF0 to U+FFF8, U+E0080 to U+E00FF, U+E01F0 to
//     U+E0FFF).
//
// None of them moves anything on a terminal, but they make two names that look the
// same differ and hide text from a person, which a model reading the same text
// still reads.
//
// The zero width joiner and non-joiner (U+200D, U+200C) are not in the class:
// they join emoji and spell words of Persian and Indic scripts. This table is the
// one definition of the class of one character: every function below follows it,
// for a display name and for a description alike. What only a whole text shows
// (variation selectors, a name with nothing to see) is checked by checkText and
// ValidatePersonText.
var personTextRefused = [...][2]rune{
	{0x0000, 0x001F}, {0x007F, 0x009F}, {0x034F, 0x034F}, {0x061C, 0x061C},
	{0x115F, 0x1160}, {0x17B4, 0x17B5}, {0x200B, 0x200B}, {0x200E, 0x200F},
	{0x2028, 0x202E}, {0x2060, 0x206F}, {0x3164, 0x3164},
	{0xFEFF, 0xFEFF}, {0xFFA0, 0xFFA0}, {0xFFF0, 0xFFF8}, {0x1BCA0, 0x1BCA3},
	{0x1D173, 0x1D17A}, {0xE0000, 0xE00FF}, {0xE01F0, 0xE0FFF},
}

// variationSelectors are the characters that choose a variant of the character
// before them: the Mongolian free variation selectors (U+180B to U+180D, U+180F),
// the variation selectors (U+FE00 to U+FE0F, which make an emoji of a symbol) and
// the ideographic variation selectors (U+E0100 to U+E01EF). Each is legal after a
// character, which is how emoji and Chinese, Japanese and Korean variants are
// written. A selector at the start of a text or after another selector has no
// character to vary and draws nothing, and a run of them can carry any bytes
// behind one visible character, so a text never has either.
var variationSelectors = [...][2]rune{
	{0x180B, 0x180D}, {0x180F, 0x180F}, {0xFE00, 0xFE0F}, {0xE0100, 0xE01EF},
}

// personTextBlank are the characters of the class that a name may hold and that
// show nothing, or only a blank: the spaces (Unicode White_Space characters that
// are not controls or separators of lines, which the class refuses), the braille
// blank, and the invisible characters that the class lets through (the soft
// hyphen, the Mongolian vowel separator and the zero width non-joiner and
// joiner). A character that is neither refused, a variation selector nor in this
// table is visible: see isVisible.
var personTextBlank = [...][2]rune{
	{0x0020, 0x0020}, {0x00A0, 0x00A0}, {0x00AD, 0x00AD}, {0x1680, 0x1680},
	{0x180E, 0x180E}, {0x2000, 0x200A}, {0x200C, 0x200D}, {0x202F, 0x202F},
	{0x205F, 0x205F}, {0x2800, 0x2800}, {0x3000, 0x3000},
}

// descriptionKept are the characters of that class that a description keeps, in
// ascending order: a description may span lines and be indented, and a line feed
// and a tab only lay text out. A description is the person-text class with these
// two allowed and nothing else changed.
var descriptionKept = [...]rune{'\t', '\n'}

// inRanges reports whether r is in one of the inclusive ranges.
func inRanges(r rune, ranges [][2]rune) bool {
	for _, rg := range ranges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// refusedIn reports whether r is in the person-text class once the characters in
// kept are taken out of it.
func refusedIn(r rune, kept []rune) bool {
	for _, k := range kept {
		if r == k {
			return false
		}
	}
	return inRanges(r, personTextRefused[:])
}

// isVariationSelector reports whether r is one of variationSelectors.
func isVariationSelector(r rune) bool { return inRanges(r, variationSelectors[:]) }

// isVisible reports whether r is a character that shows something when it stands
// alone in a name: it is not refused, not a variation selector and not blank.
func isVisible(r rune) bool {
	return !refusedIn(r, nil) && !isVariationSelector(r) && !inRanges(r, personTextBlank[:])
}

// RefusedInPersonText reports whether r may not appear in a text that is shown
// to a person: a display name, which a terminal or a page would otherwise act on,
// reorder or read differently from a person. It is the rule that every place that reads, stores, forwards or
// prints such text applies; a text with such a character is refused, never
// cleaned. A description is held to the same class with line feed and tab
// allowed: see RefusedInDescription. The rule is of one character; ValidatePersonText
// adds what only the whole text shows, which is a rule of validation and not of
// escaping.
func RefusedInPersonText(r rune) bool { return refusedIn(r, nil) }

// RefusedInDescription reports whether r may not appear in the description of an
// endpoint: the characters of RefusedInPersonText except line feed and tab.
func RefusedInDescription(r rune) bool { return refusedIn(r, descriptionKept[:]) }

// checkText checks the characters of a text and the order of its variation
// selectors: none is refused (leaving kept out of the class), and none is first
// or follows another. charsMsg is the refusal for a character. A refusal never
// repeats the text.
func checkText(s string, kept []rune, charsMsg string) error {
	afterSelector := true // the start of the text has no character to vary either
	for _, r := range s {
		if refusedIn(r, kept) {
			return bad("%s", charsMsg)
		}
		selector := isVariationSelector(r)
		if selector && afterSelector {
			return bad("has a variation selector at its start or right after another")
		}
		afterSelector = selector
	}
	return nil
}

// ValidatePersonText checks a name shown to a person (not a description, which
// ValidateDescription checks): no character refused by RefusedInPersonText, no
// variation selector at the start or right after another, and at least one
// visible character, so a name made of blanks and invisible characters, which a
// person cannot tell from another, is refused. It does not check the length,
// which each field bounds for itself. A refusal never repeats the text.
func ValidatePersonText(s string) error {
	if err := checkText(s, nil, "contains a control, bidirectional formatting or invisible format character"); err != nil {
		return err
	}
	if strings.IndexFunc(s, isVisible) < 0 {
		return bad("has no visible character")
	}
	return nil
}

// PersonTextPattern is the regular expression of the rules of ValidatePersonText,
// for the schema, which states it for the display name. The pattern holds the
// characters themselves, not regular-expression escapes, which engines do not all
// read alike.
func PersonTextPattern() string { return textPattern(nil, true) }

// DescriptionPattern is the regular expression of the rules of
// ValidateDescription, built from the same tables as PersonTextPattern so the two
// cannot drift.
func DescriptionPattern() string { return textPattern(descriptionKept[:], false) }

// textPattern writes the rules of a text, less the characters in kept (ascending)
// from the refused class, as an anchored regular expression. With R the refused
// characters, S the variation selectors, B the blank ones and V what remains:
//
//	every text:  ^(?:[^RS][S]?)*$       a character, each with at most one selector
//	a name:      ^(?:[B][S]?)*[V][S]?(?:[^RS][S]?)*$
//	                                    the same, with a visible character first
//
// A selector is only matched after a character that is none, so none comes first
// or after another. Blank characters before the first visible one are consumed
// by the first group, which cannot take a visible one, so a text is matched in
// one way and the pattern cannot backtrack badly. The expression is in the
// subset that RE2 and ECMAScript with the u flag read alike: no lookaround.
func textPattern(kept []rune, mustSeeSomething bool) string {
	refused := refusedRanges(kept)
	selector := "[" + classOf(variationSelectors[:]) + "]?"
	char := "[^" + classOf(refused, variationSelectors[:]) + "]" + selector
	if !mustSeeSomething {
		return "^(?:" + char + ")*$"
	}
	blank := "[" + classOf(personTextBlank[:]) + "]" + selector
	visible := "[^" + classOf(refused, variationSelectors[:], personTextBlank[:]) + "]" + selector
	return "^(?:" + blank + ")*" + visible + "(?:" + char + ")*$"
}

// refusedRanges is personTextRefused with the characters in kept (ascending) taken
// out, as ranges.
func refusedRanges(kept []rune) [][2]rune {
	var out [][2]rune
	for _, rg := range personTextRefused {
		first := rg[0]
		for _, k := range kept {
			if k < first || k > rg[1] {
				continue
			}
			if k > first {
				out = append(out, [2]rune{first, k - 1})
			}
			first = k + 1
		}
		if first <= rg[1] {
			out = append(out, [2]rune{first, rg[1]})
		}
	}
	return out
}

// classOf writes the union of the range lists as the body of a regular-expression
// character class: ascending, adjacent ranges joined, a range of two characters
// as the two.
func classOf(lists ...[][2]rune) string {
	var all [][2]rune
	for _, l := range lists {
		all = append(all, l...)
	}
	slices.SortFunc(all, func(a, b [2]rune) int { return cmp.Compare(a[0], b[0]) })
	var b strings.Builder
	for i := 0; i < len(all); i++ {
		first, last := all[i][0], all[i][1]
		for i+1 < len(all) && all[i+1][0] <= last+1 {
			i++
			last = max(last, all[i][1])
		}
		b.WriteRune(first)
		switch last - first {
		case 0:
		case 1:
			b.WriteRune(last)
		default:
			b.WriteRune('-')
			b.WriteRune(last)
		}
	}
	return b.String()
}

// ValidateDisplayName checks a display name: MaxDisplayName characters at most,
// at least one, and the rules of ValidatePersonText. The one rule of every display
// name the node reads, stores, forwards or prints.
func ValidateDisplayName(s string) error { return personTextLen(s, MaxDisplayName) }

// personTextLen checks a name shown to a person of 1 to most characters: the
// length bound of a display name, or of another short field that follows its
// rules.
func personTextLen(s string, most int) error {
	if err := strLen(s, 1, most); err != nil {
		return err
	}
	return ValidatePersonText(s)
}

// ValidateOptionalDisplayName checks a display name that may be absent.
func ValidateOptionalDisplayName(s string) error {
	if s == "" {
		return nil
	}
	return ValidateDisplayName(s)
}

// ValidateDescription checks the description of an endpoint: MaxDescription
// characters at most, at least one, none of them refused by RefusedInDescription,
// no variation selector at the start or right after another. A description is
// prose, so it may be blank. The one rule of every description the node reads,
// stores, forwards or prints.
func ValidateDescription(s string) error {
	if err := strLen(s, 1, MaxDescription); err != nil {
		return err
	}
	return checkText(s, descriptionKept[:], "contains a control character other than line feed and tab, or a bidirectional formatting or invisible format character")
}

// ValidateOptionalDescription checks a description that may be absent.
func ValidateOptionalDescription(s string) error {
	if s == "" {
		return nil
	}
	return ValidateDescription(s)
}

// ValidateAdapterID checks the syntax of an adapter identifier.
func ValidateAdapterID(s string) error { return matches(adapterIDRE, s) }

// ValidateConfigKey checks the syntax of a configuration key.
func ValidateConfigKey(s string) error { return matches(configKeyRE, s) }

// ValidateConfigMapKey checks the syntax of the key of one entry of a map value.
func ValidateConfigMapKey(s string) error {
	if err := strLen(s, 1, 128); err != nil {
		return err
	}
	return matches(configMapKeyRE, s)
}

// ValidateSemVer checks that s is a Semantic Versioning 2.0.0 version.
func ValidateSemVer(s string) error {
	if _, err := semver.Parse(s); err != nil {
		return bad("%q is not a SemVer 2.0.0 version of at most %d characters", s, semver.MaxLen)
	}
	return nil
}
