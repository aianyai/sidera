package adapterproto

import (
	"regexp"
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
	// EnvNamePattern is the name of an environment variable the adapter injects.
	EnvNamePattern = `^[A-Za-z_][A-Za-z0-9_]*$`
	// reasonWordPattern is a short snake_case word.
	reasonWordPattern = `^[a-z][a-z0-9_]*$`
)

// MaxPresetNameLen bounds the length of a preset name.
const MaxPresetNameLen = 64

var (
	adapterIDRE  = regexp.MustCompile(AdapterIDPattern)
	presetNameRE = regexp.MustCompile(PresetNamePattern)
	configKeyRE  = regexp.MustCompile(ConfigKeyPattern)
	envNameRE    = regexp.MustCompile(EnvNamePattern)
	reasonWordRE = regexp.MustCompile(reasonWordPattern)
)

// Patterns returns every pattern of the protocol keyed by the name of its
// definition in the schema.
func Patterns() map[string]string {
	return map[string]string{
		"AttemptID":   attemptIDPattern,
		"LeaseRef":    leaseRefPattern,
		"Token":       tokenPattern,
		"AdapterID":   AdapterIDPattern,
		"PresetName":  PresetNamePattern,
		"DisplayName": PersonTextPattern(),
		"Description": DescriptionPattern(),
		"ConfigKey":   ConfigKeyPattern,
		"EnvName":     EnvNamePattern,
		"ReasonWord":  reasonWordPattern,
		"SemVer":      semver.Pattern,
	}
}

// ValidatePresetName checks the syntax of a preset name.
func ValidatePresetName(s string) error {
	if err := strLen(s, 1, MaxPresetNameLen); err != nil {
		return err
	}
	return matches(presetNameRE, s)
}

// personTextRefused are the code points that text shown to a person never
// holds, as inclusive ranges: the control characters (U+0000 to U+001F, U+007F to
// U+009F), the line and paragraph separators (U+2028, U+2029) and the
// bidirectional formatting controls (U+061C, U+200E, U+200F, U+202A to U+202E,
// U+2066 to U+2069). A terminal acts on the first group, and the others break or
// reorder lines of text. This table is the one definition of the class: every
// function below follows it, for a display name and for a description alike.
var personTextRefused = [...][2]rune{
	{0x0000, 0x001F}, {0x007F, 0x009F}, {0x061C, 0x061C}, {0x200E, 0x200F},
	{0x2028, 0x202E}, {0x2066, 0x2069},
}

// descriptionKept are the characters of that class that a description keeps, in
// ascending order: a description may span lines and be indented, and a line feed
// and a tab only lay text out. A description is the person-text class with these
// two allowed and nothing else changed.
var descriptionKept = [...]rune{'\t', '\n'}

// refusedIn reports whether r is in the person-text class once the characters in
// kept are taken out of it.
func refusedIn(r rune, kept []rune) bool {
	for _, k := range kept {
		if r == k {
			return false
		}
	}
	for _, rg := range personTextRefused {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// RefusedInPersonText reports whether r may not appear in a text that is shown
// to a person: a display name, which a terminal or a page would otherwise act on,
// or reorder. It is the rule that every place that reads, stores, forwards or
// prints such text applies; a text with such a character is refused, never
// cleaned. A description is held to the same class with line feed and tab
// allowed: see RefusedInDescription.
func RefusedInPersonText(r rune) bool { return refusedIn(r, nil) }

// RefusedInDescription reports whether r may not appear in the description of an
// endpoint: the characters of RefusedInPersonText except line feed and tab.
func RefusedInDescription(r rune) bool { return refusedIn(r, descriptionKept[:]) }

// ValidatePersonText checks the characters of a text shown to a person. It does
// not check the length, which each field bounds for itself. A refusal never
// repeats the text.
func ValidatePersonText(s string) error {
	if strings.IndexFunc(s, RefusedInPersonText) >= 0 {
		return bad("contains a control or bidirectional formatting character")
	}
	return nil
}

// PersonTextPattern is the regular expression of the rule of
// RefusedInPersonText, for the schema, which states it for the display name. The
// pattern holds the characters themselves, not regular-expression escapes, which
// engines do not all read alike.
func PersonTextPattern() string { return textPattern(nil) }

// DescriptionPattern is the regular expression of the rule of
// RefusedInDescription, built from the same table as PersonTextPattern so the
// two cannot drift.
func DescriptionPattern() string { return textPattern(descriptionKept[:]) }

// textPattern writes the class of personTextRefused, less the characters in kept
// (ascending), as the negated character class of an anchored regular expression.
func textPattern(kept []rune) string {
	var b strings.Builder
	b.WriteString("^[^")
	span := func(first, last rune) {
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
	for _, rg := range personTextRefused {
		first := rg[0]
		for _, k := range kept {
			if k < first || k > rg[1] {
				continue
			}
			if k > first {
				span(first, k-1)
			}
			first = k + 1
		}
		if first <= rg[1] {
			span(first, rg[1])
		}
	}
	b.WriteString("]*$")
	return b.String()
}

// ValidateDisplayName checks a display name: MaxDisplayName characters at most,
// at least one, none of them refused by RefusedInPersonText. The one rule of every
// display name the node reads, stores, forwards or prints.
func ValidateDisplayName(s string) error {
	if err := strLen(s, 1, MaxDisplayName); err != nil {
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
// characters at most, at least one, none of them refused by RefusedInDescription.
// The one rule of every description the node reads, stores, forwards or prints.
func ValidateDescription(s string) error {
	if err := strLen(s, 1, MaxDescription); err != nil {
		return err
	}
	if strings.IndexFunc(s, RefusedInDescription) >= 0 {
		return bad("contains a control character other than line feed and tab, or a bidirectional formatting character")
	}
	return nil
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

// ValidateSemVer checks that s is a Semantic Versioning 2.0.0 version.
func ValidateSemVer(s string) error {
	if _, err := semver.Parse(s); err != nil {
		return bad("%q is not a SemVer 2.0.0 version of at most %d characters", s, semver.MaxLen)
	}
	return nil
}
