// Package manifest parses and validates the adapter package manifest,
// sidera-adapter.json, the file at the root of an adapter package that tells the
// daemon what the adapter is, how to start it and what it needs.
//
// Parse is strict: unknown fields, fields whose name differs in case, null
// values, missing required fields and explicit empty values for optional fields
// are errors, and a manifest Parse accepts is one manifest.schema.json accepts.
// Validate states the structural rules once; the text of an acknowledgement
// marker is not judged here (see Marker).
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/internal/semver"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// FileName is the name of the manifest in a package's root directory.
const FileName = "sidera-adapter.json"

// Version is the only manifest format version.
const Version = 1

// Limits of the manifest, also stated in the schema.
const (
	// MaxBudgetLimit is the largest output budget a manifest may declare.
	MaxBudgetLimit = 131072
	// MaxRecvWaitSeconds is the longest an agent's recv (CLI or MCP) may wait for
	// a message, whatever a manifest declares: a manifest may declare less for a
	// front end, never more. A direct endpoint's endpoint.recv has its own cap,
	// adapterproto.MaxWaitSeconds, which this does not govern.
	MaxRecvWaitSeconds = 120
	maxFields          = 64
	maxMarkers         = 32
	maxMarkerText      = 1024
	maxString          = 4096
	maxIntValue        = 1<<53 - 1
)

// ErrBeyondSchema is matched by errors.Is for a failed rule that JSON Schema
// cannot state, such as "min is not above max".
var ErrBeyondSchema = adapterproto.ErrBeyondSchema

// Error says which part of the manifest breaks which rule.
type Error struct {
	// Field is the path of the field, for example "config.instance[2].default".
	Field string
	Msg   string
	// Beyond is true when the rule cannot be stated in JSON Schema.
	Beyond bool
}

func (e *Error) Error() string {
	if e.Field == "" {
		return "manifest: " + e.Msg
	}
	return "manifest: " + e.Field + ": " + e.Msg
}

// Is reports a match with ErrBeyondSchema for rules the schema cannot state.
func (e *Error) Is(target error) bool { return target == ErrBeyondSchema && e.Beyond }

func fail(field, format string, args ...any) error {
	return &Error{Field: field, Msg: fmt.Sprintf(format, args...)}
}

func failBeyond(field, format string, args ...any) error {
	return &Error{Field: field, Msg: fmt.Sprintf(format, args...), Beyond: true}
}

// under places an error from a nested check under a field name.
func under(field string, err error) error {
	if err == nil {
		return nil
	}
	var me *Error
	if errors.As(err, &me) {
		f := field
		if me.Field != "" {
			sep := "."
			if me.Field[0] == '[' {
				sep = ""
			}
			f = field + sep + me.Field
		}
		return &Error{Field: f, Msg: me.Msg, Beyond: me.Beyond}
	}
	var ve *adapterproto.ValidationError
	if errors.As(err, &ve) {
		f := field
		if ve.Field != "" {
			sep := "."
			if ve.Field[0] == '[' {
				sep = ""
			}
			f = field + sep + ve.Field
		}
		return &Error{Field: f, Msg: ve.Msg, Beyond: ve.Beyond}
	}
	return &Error{Field: field, Msg: err.Error()}
}

func idx(i int) string { return fmt.Sprintf("[%d]", i) }

// Manifest is the content of sidera-adapter.json.
type Manifest struct {
	// ManifestVersion is the format version, always Version.
	ManifestVersion int `json:"manifest_version"`
	// ID names the adapter. By convention its program is sidera-adapter-<id>.
	ID string `json:"id"`
	// Version is the adapter's own SemVer version; the adapter reports the same
	// one in initialize.
	Version string `json:"version"`
	// Protocol is the range of protocol versions the adapter implements.
	Protocol adapterproto.Range `json:"protocol"`
	// Entry says how to start the adapter.
	Entry Entry `json:"entry"`
	// Platforms lists where the package runs.
	Platforms []Platform `json:"platforms"`
	// Requires lists programs the adapter needs, for display and the install
	// check; the probe is what proves they work.
	Requires []Requirement `json:"requires,omitempty"`
	// Tested lists the closed ranges of runtime versions the adapter was tested
	// against.
	Tested []TestedRange `json:"tested,omitempty"`
	// Config describes the configuration the adapter takes.
	Config *Config `json:"config,omitempty"`
	// Budgets gives the output budget of recv per front end.
	Budgets []Budget `json:"budgets,omitempty"`
	// RecvWaitLimits gives, per agent front end (CLI or MCP), the longest a recv
	// through that front end may wait for a message. A front end without a limit
	// gets the daemon's default.
	RecvWaitLimits []RecvWaitLimit `json:"recv_wait_limits,omitempty"`
	// AckMarkers declares how the runtime shows a replaced or cut tool result.
	AckMarkers *AckMarkers `json:"ack_markers,omitempty"`
}

// Entry says how to start the adapter.
type Entry struct {
	// Program is a path inside the package: not absolute, with no ".." segment.
	Program string   `json:"program"`
	Args    []string `json:"args,omitempty"`
}

// Platform is an operating system and architecture, in Go's GOOS and GOARCH
// vocabulary.
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// Requirement is a program the adapter needs.
type Requirement struct {
	Program string `json:"program"`
	// OS limits the requirement to these operating systems; absent means all.
	OS []string `json:"os,omitempty"`
}

// TestedRange is a closed range of versions of one runtime, compared as SemVer.
type TestedRange struct {
	Runtime string `json:"runtime"`
	Min     string `json:"min"`
	Max     string `json:"max"`
}

// Contains reports whether version is a SemVer version inside the range. A
// version that is not SemVer is never inside.
func (r TestedRange) Contains(version string) bool {
	v, err := semver.Parse(version)
	if err != nil {
		return false
	}
	lo, err1 := semver.Parse(r.Min)
	hi, err2 := semver.Parse(r.Max)
	if err1 != nil || err2 != nil {
		return false
	}
	return semver.Compare(lo, v) <= 0 && semver.Compare(v, hi) <= 0
}

// Tests reports whether any of the manifest's tested ranges for runtime
// contains version.
func (m Manifest) Tests(runtime, version string) bool {
	for _, r := range m.Tested {
		if r.Runtime == runtime && r.Contains(version) {
			return true
		}
	}
	return false
}

// Config describes configuration values: those of the adapter instance and those
// of each preset.
type Config struct {
	Instance []Field `json:"instance,omitempty"`
	Preset   []Field `json:"preset,omitempty"`
}

// FieldType is the type of a configuration field.
type FieldType string

// The configuration vocabulary. It is deliberately small: the daemon validates
// values at run time and builds forms from it without a schema engine.
const (
	FieldString     FieldType = "string"
	FieldInteger    FieldType = "integer"
	FieldBoolean    FieldType = "boolean"
	FieldEnum       FieldType = "enum"
	FieldDirectory  FieldType = "directory"
	FieldStringList FieldType = "string_list"
)

var fieldTypes = []FieldType{FieldString, FieldInteger, FieldBoolean, FieldEnum, FieldDirectory, FieldStringList}

// FieldTypes lists the field types.
func FieldTypes() []FieldType { return slices.Clone(fieldTypes) }

// Field describes one configuration field. A directory value must be an
// absolute path that exists, which the daemon checks; a preset's project
// directory is one, because it is fixed per preset.
type Field struct {
	// Key is the name of the value in the configuration.
	Key         string    `json:"key"`
	Type        FieldType `json:"type"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Required    bool      `json:"required,omitempty"`
	// Default is a JSON value of the field's type.
	Default json.RawMessage `json:"default,omitempty"`
	// Min and Max bound an integer field.
	Min *int64 `json:"min,omitempty"`
	Max *int64 `json:"max,omitempty"`
	// Options are the values of an enum field.
	Options []string `json:"options,omitempty"`
}

// Frontend is where a budget applies.
type Frontend string

// The front ends a budget can name: the two agent tools and the direct
// endpoints that adapters read themselves.
const (
	FrontendCLI    Frontend = "cli"
	FrontendMCP    Frontend = "mcp"
	FrontendDirect Frontend = "direct"
)

var budgetFrontends = []Frontend{FrontendCLI, FrontendMCP, FrontendDirect}

// BudgetFrontends lists the front ends a budget can name.
func BudgetFrontends() []Frontend { return slices.Clone(budgetFrontends) }

// Unit is how a budget is measured.
type Unit string

// The units of a budget. Chars are UTF-16 code units; tokens are the estimate the
// handoff package defines.
const (
	UnitChars  Unit = "chars"
	UnitTokens Unit = "tokens"
)

var units = []Unit{UnitChars, UnitTokens}

// Units lists the budget units.
func Units() []Unit { return slices.Clone(units) }

// Budget is the output budget of one front end. The author subtracts any safety
// margin; the limit is used as given.
type Budget struct {
	Frontend Frontend `json:"frontend"`
	Unit     Unit     `json:"unit"`
	Limit    int      `json:"limit"`
}

// RecvWaitLimit is the longest, in whole seconds, that a recv through one agent
// front end (CLI or MCP) may wait for a message. The client of a front end ends a
// wait that runs past its own timeout, so only the adapter knows how long its
// front end can wait. Direct endpoints have no entry: the adapter chooses the
// wait of its own endpoint.recv.
type RecvWaitLimit struct {
	Frontend adapterproto.Frontend `json:"frontend"`
	MaxS     int                   `json:"max_s"`
}

// AckMarkers declares how the runtime shows a tool result it replaced by a notice
// or cut short, so that a refused acknowledgement can say which.
type AckMarkers struct {
	Replaced  []Marker `json:"replaced,omitempty"`
	Truncated []Marker `json:"truncated,omitempty"`
}

// Marker is a literal substring or an RE2 regular expression: exactly one of the
// two is set. Parse checks only that shape. Whether the text is usable (not
// empty, a regular expression that compiles and does not match the empty text)
// is decided in one place, where the daemon compiles the markers when it admits
// the adapter.
type Marker struct {
	Literal *string `json:"literal,omitempty"`
	Regexp  *string `json:"regexp,omitempty"`
}

// Validate checks that exactly one of the two is set and that the text is not
// absurdly long.
func (m Marker) Validate() error {
	if (m.Literal == nil) == (m.Regexp == nil) {
		return fail("", "a marker sets exactly one of literal and regexp")
	}
	text := m.Literal
	if text == nil {
		text = m.Regexp
	}
	if n := len([]rune(*text)); n > maxMarkerText {
		return fail("", "marker text has %d characters, at most %d", n, maxMarkerText)
	}
	return nil
}

// Parse decodes and validates a manifest.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	if err := strictjson.Decode(data, &m); err != nil {
		return Manifest{}, &Error{Msg: strings.TrimPrefix(err.Error(), "strictjson: ")}
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Validate checks every structural rule of the manifest.
func (m Manifest) Validate() error {
	if m.ManifestVersion != Version {
		return fail("manifest_version", "must be %d", Version)
	}
	if err := adapterproto.ValidateAdapterID(m.ID); err != nil {
		return under("id", err)
	}
	if err := adapterproto.ValidateSemVer(m.Version); err != nil {
		return under("version", err)
	}
	if err := m.Protocol.Validate(); err != nil {
		return under("protocol", err)
	}
	if err := m.Entry.validate(); err != nil {
		return under("entry", err)
	}
	if n := len(m.Platforms); n < 1 || n > 64 {
		return fail("platforms", "%d entries, need 1..64", n)
	}
	for i, p := range m.Platforms {
		if err := p.validate(); err != nil {
			return under("platforms"+idx(i), err)
		}
	}
	if err := unique("platforms", m.Platforms); err != nil {
		return err
	}
	if m.Requires != nil && (len(m.Requires) < 1 || len(m.Requires) > 64) {
		return fail("requires", "%d entries, need 1..64", len(m.Requires))
	}
	for i, r := range m.Requires {
		if err := r.validate(); err != nil {
			return under("requires"+idx(i), err)
		}
	}
	if m.Tested != nil && (len(m.Tested) < 1 || len(m.Tested) > 64) {
		return fail("tested", "%d entries, need 1..64", len(m.Tested))
	}
	for i, r := range m.Tested {
		if err := r.validate(); err != nil {
			return under("tested"+idx(i), err)
		}
	}
	if m.Config != nil {
		if err := m.Config.validate(); err != nil {
			return under("config", err)
		}
	}
	if err := validateBudgets(m.Budgets); err != nil {
		return under("budgets", err)
	}
	if err := validateRecvWaitLimits(m.RecvWaitLimits); err != nil {
		return under("recv_wait_limits", err)
	}
	if m.AckMarkers != nil {
		if err := m.AckMarkers.validate(); err != nil {
			return under("ack_markers", err)
		}
	}
	return nil
}

func unique[T comparable](field string, items []T) error {
	seen := make(map[T]bool, len(items))
	for i, it := range items {
		if seen[it] {
			return fail(field+idx(i), "duplicate entry")
		}
		seen[it] = true
	}
	return nil
}

func length(s string, min, max int) error {
	if n := len([]rune(s)); n < min || n > max {
		return fail("", "length %d outside %d..%d", n, min, max)
	}
	return nil
}

// Path rules of the entry program, in the form the schema states them: not
// absolute (a leading slash or backslash, or a drive letter) and no ".."
// segment, with either slash or backslash as the separator.
var (
	absolutePathRE = regexp.MustCompile(`^[/\\]`)
	driveRE        = regexp.MustCompile(`^[A-Za-z]:`)
	dotDotRE       = regexp.MustCompile(`(^|[/\\])\.\.($|[/\\])`)
)

func (e Entry) validate() error {
	if err := length(e.Program, 1, 256); err != nil {
		return under("program", err)
	}
	if absolutePathRE.MatchString(e.Program) || driveRE.MatchString(e.Program) {
		return fail("program", "%q is not relative to the package", e.Program)
	}
	if dotDotRE.MatchString(e.Program) {
		return fail("program", "%q leaves the package with ..", e.Program)
	}
	if e.Args != nil && (len(e.Args) < 1 || len(e.Args) > 64) {
		return fail("args", "%d entries, need 1..64", len(e.Args))
	}
	for i, a := range e.Args {
		if err := length(a, 0, 1024); err != nil {
			return under("args"+idx(i), err)
		}
	}
	return nil
}

func (p Platform) validate() error {
	if !slices.Contains(knownOS, p.OS) {
		return fail("os", "unknown operating system %q", p.OS)
	}
	if !slices.Contains(knownArch, p.Arch) {
		return fail("arch", "unknown architecture %q", p.Arch)
	}
	return nil
}

func (r Requirement) validate() error {
	if err := length(r.Program, 1, 128); err != nil {
		return under("program", err)
	}
	if r.OS != nil && (len(r.OS) < 1 || len(r.OS) > 32) {
		return fail("os", "%d entries, need 1..32", len(r.OS))
	}
	for i, o := range r.OS {
		if !slices.Contains(knownOS, o) {
			return fail("os"+idx(i), "unknown operating system %q", o)
		}
	}
	return unique("os", r.OS)
}

func (r TestedRange) validate() error {
	if err := length(r.Runtime, 1, 64); err != nil {
		return under("runtime", err)
	}
	if err := adapterproto.ValidateSemVer(r.Min); err != nil {
		return under("min", err)
	}
	if err := adapterproto.ValidateSemVer(r.Max); err != nil {
		return under("max", err)
	}
	lo, _ := semver.Parse(r.Min)
	hi, _ := semver.Parse(r.Max)
	if semver.Compare(lo, hi) > 0 {
		return failBeyond("", "min %s is above max %s", r.Min, r.Max)
	}
	return nil
}

func (c Config) validate() error {
	for _, g := range []struct {
		name   string
		fields []Field
	}{{"instance", c.Instance}, {"preset", c.Preset}} {
		if g.fields != nil && (len(g.fields) < 1 || len(g.fields) > maxFields) {
			return fail(g.name, "%d fields, need 1..%d", len(g.fields), maxFields)
		}
		keys := make(map[string]bool, len(g.fields))
		for i, f := range g.fields {
			if err := f.validate(); err != nil {
				return under(g.name+idx(i), err)
			}
			if keys[f.Key] {
				return failBeyond(g.name+idx(i)+".key", "duplicate key %q", f.Key)
			}
			keys[f.Key] = true
		}
	}
	return nil
}

func (f Field) validate() error {
	if err := adapterproto.ValidateConfigKey(f.Key); err != nil {
		return under("key", err)
	}
	if !slices.Contains(fieldTypes, f.Type) {
		return fail("type", "unknown field type %q", string(f.Type))
	}
	if err := length(f.Title, 1, 128); err != nil {
		return under("title", err)
	}
	if f.Description != "" {
		if err := length(f.Description, 1, 1024); err != nil {
			return under("description", err)
		}
	}
	if f.Type != FieldInteger && (f.Min != nil || f.Max != nil) {
		return fail("min", "min and max belong to integer fields")
	}
	if f.Type == FieldEnum {
		if n := len(f.Options); n < 1 || n > 64 {
			return fail("options", "an enum field has 1..64 options, got %d", n)
		}
		for i, o := range f.Options {
			if err := length(o, 1, 128); err != nil {
				return under("options"+idx(i), err)
			}
		}
		if err := unique("options", f.Options); err != nil {
			return err
		}
	} else if f.Options != nil {
		return fail("options", "only an enum field has options")
	}
	if f.Type == FieldInteger {
		for _, b := range []*int64{f.Min, f.Max} {
			if b != nil && (*b < -maxIntValue || *b > maxIntValue) {
				return fail("min", "bound %d outside the exact integer range", *b)
			}
		}
		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			return failBeyond("min", "min %d is above max %d", *f.Min, *f.Max)
		}
	}
	if f.Default != nil {
		if err := f.validateDefault(); err != nil {
			return under("default", err)
		}
	}
	return nil
}

func (f Field) validateDefault() error {
	switch f.Type {
	case FieldString, FieldDirectory, FieldEnum:
		var s string
		if err := decodeValue(f.Default, &s); err != nil {
			return fail("", "a %s default is a string", f.Type)
		}
		if err := length(s, 0, maxString); err != nil {
			return err
		}
		if f.Type == FieldEnum && !slices.Contains(f.Options, s) {
			return failBeyond("", "%q is not one of the options", s)
		}
	case FieldInteger:
		var n int64
		if err := decodeValue(f.Default, &n); err != nil {
			return fail("", "an integer default is a plain integer")
		}
		if n < -maxIntValue || n > maxIntValue {
			return fail("", "%d outside the exact integer range", n)
		}
		if f.Min != nil && n < *f.Min {
			return failBeyond("", "%d is below min %d", n, *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return failBeyond("", "%d is above max %d", n, *f.Max)
		}
	case FieldBoolean:
		var b bool
		if err := decodeValue(f.Default, &b); err != nil {
			return fail("", "a boolean default is true or false")
		}
	case FieldStringList:
		var items []json.RawMessage
		if err := decodeValue(f.Default, &items); err != nil || items == nil {
			return fail("", "a string_list default is a list of strings")
		}
		if len(items) > 256 {
			return fail("", "%d items, at most 256", len(items))
		}
		for i, it := range items {
			var s string
			if err := decodeValue(it, &s); err != nil {
				return fail(idx(i), "a list holds strings only")
			}
			if err := length(s, 0, maxString); err != nil {
				return under(idx(i), err)
			}
		}
	}
	return nil
}

// decodeValue decodes one JSON value of a plain kind: null is refused, and a
// number must be an integer literal for an integer target.
func decodeValue(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return errors.New("null")
	}
	return json.Unmarshal(raw, v)
}

func validateBudgets(bs []Budget) error {
	if bs == nil {
		return nil
	}
	if len(bs) < 1 || len(bs) > len(budgetFrontends) {
		return fail("", "%d entries, need 1..%d", len(bs), len(budgetFrontends))
	}
	seen := map[Frontend]bool{}
	for i, b := range bs {
		if !slices.Contains(budgetFrontends, b.Frontend) {
			return fail(idx(i)+".frontend", "unknown front end %q", string(b.Frontend))
		}
		if !slices.Contains(units, b.Unit) {
			return fail(idx(i)+".unit", "unknown unit %q", string(b.Unit))
		}
		if b.Limit < 1 || b.Limit > MaxBudgetLimit {
			return fail(idx(i)+".limit", "%d outside 1..%d", b.Limit, MaxBudgetLimit)
		}
		if seen[b.Frontend] {
			return failBeyond(idx(i)+".frontend", "front end %q has two budgets", string(b.Frontend))
		}
		seen[b.Frontend] = true
	}
	return nil
}

func validateRecvWaitLimits(ls []RecvWaitLimit) error {
	if ls == nil {
		return nil
	}
	// One entry at most per agent front end, so the bound follows the vocabulary.
	most := len(adapterproto.EnumTables()["Frontend"])
	if len(ls) < 1 || len(ls) > most {
		return fail("", "%d entries, need 1..%d", len(ls), most)
	}
	seen := map[adapterproto.Frontend]bool{}
	for i, l := range ls {
		if !l.Frontend.Valid() {
			return fail(idx(i)+".frontend", "unknown front end %q", string(l.Frontend))
		}
		if l.MaxS < 1 || l.MaxS > MaxRecvWaitSeconds {
			return fail(idx(i)+".max_s", "%d outside 1..%d", l.MaxS, MaxRecvWaitSeconds)
		}
		if seen[l.Frontend] {
			return failBeyond(idx(i)+".frontend", "front end %q has two receive wait limits", string(l.Frontend))
		}
		seen[l.Frontend] = true
	}
	return nil
}

func (a AckMarkers) validate() error {
	for _, g := range []struct {
		name    string
		markers []Marker
	}{{"replaced", a.Replaced}, {"truncated", a.Truncated}} {
		if g.markers != nil && (len(g.markers) < 1 || len(g.markers) > maxMarkers) {
			return fail(g.name, "%d markers, need 1..%d", len(g.markers), maxMarkers)
		}
		for i, m := range g.markers {
			if err := m.Validate(); err != nil {
				return under(g.name+idx(i), err)
			}
		}
	}
	return nil
}

// The Go vocabulary of operating systems and architectures, as the Go toolchain
// names them (GOOS and GOARCH), including ones no supported port uses today.
var (
	knownOS   = []string{"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js", "linux", "nacl", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "zos"}
	knownArch = []string{"386", "amd64", "amd64p32", "arm", "armbe", "arm64", "arm64be", "loong64", "mips", "mipsle", "mips64", "mips64le", "mips64p32", "mips64p32le", "ppc", "ppc64", "ppc64le", "riscv", "riscv64", "s390", "s390x", "sparc", "sparc64", "wasm"}
)

// KnownOS lists the accepted GOOS values.
func KnownOS() []string { return slices.Clone(knownOS) }

// KnownArch lists the accepted GOARCH values.
func KnownArch() []string { return slices.Clone(knownArch) }
