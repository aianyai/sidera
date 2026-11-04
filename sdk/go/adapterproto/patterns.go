package adapterproto

import (
	"regexp"

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
	// DisplayNamePattern is the text of a display name: no character that a
	// terminal acts on or that reorders text. It refuses the control characters
	// (U+0000 to U+001F, U+007F to U+009F), the line and paragraph separators
	// (U+2028, U+2029) and the bidirectional formatting controls (U+061C,
	// U+200E, U+200F, U+202A to U+202E, U+2066 to U+2069). The pattern holds the
	// characters themselves, not regular-expression escapes, which engines do not
	// all read alike. The length is MaxDisplayName; ValidateDisplayName
	// applies both.
	DisplayNamePattern = "^[^\u0000-\u001f\u007f-\u009f\u061c\u200e\u200f\u2028-\u202e\u2066-\u2069]*$"
	// reasonWordPattern is a short snake_case word.
	reasonWordPattern = `^[a-z][a-z0-9_]*$`
)

// MaxPresetNameLen bounds the length of a preset name.
const MaxPresetNameLen = 64

var (
	adapterIDRE   = regexp.MustCompile(AdapterIDPattern)
	displayNameRE = regexp.MustCompile(DisplayNamePattern)
	presetNameRE  = regexp.MustCompile(PresetNamePattern)
	configKeyRE   = regexp.MustCompile(ConfigKeyPattern)
	envNameRE     = regexp.MustCompile(EnvNamePattern)
	reasonWordRE  = regexp.MustCompile(reasonWordPattern)
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
		"DisplayName": DisplayNamePattern,
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

// ValidateDisplayName checks a display name: MaxDisplayName characters at most,
// at least one, none of them refused by DisplayNamePattern. The one rule of every
// display name the node reads, stores, forwards or prints. A refusal never
// repeats the name.
func ValidateDisplayName(s string) error {
	if err := strLen(s, 1, MaxDisplayName); err != nil {
		return err
	}
	if !displayNameRE.MatchString(s) {
		return bad("contains a control or bidirectional formatting character")
	}
	return nil
}

// ValidateOptionalDisplayName checks a display name that may be absent.
func ValidateOptionalDisplayName(s string) error {
	if s == "" {
		return nil
	}
	return ValidateDisplayName(s)
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
