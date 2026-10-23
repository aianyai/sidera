package adapterproto

import "fmt"

// Protocol versions this package implements. A version is a positive integer; a
// peer announces a closed range and the two sides use the highest version both
// support.
const (
	// ProtocolMin is the lowest version this package implements.
	ProtocolMin = 1
	// ProtocolMax is the highest version this package implements.
	ProtocolMax = 1
	// MaxVersion bounds the version numbers a range may name.
	MaxVersion = 65535
)

// Range is a closed range of protocol versions.
type Range struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// SupportedRange is the range of versions this package implements.
func SupportedRange() Range { return Range{ProtocolMin, ProtocolMax} }

// Validate checks that the range is within 1..MaxVersion and not empty.
func (r Range) Validate() error {
	var c checker
	c.field("min", intRange(r.Min, 1, MaxVersion))
	c.field("max", intRange(r.Max, 1, MaxVersion))
	c.beyond(r.Min <= r.Max, "min %d is greater than max %d", r.Min, r.Max)
	return c.err
}

// Contains reports whether v is in the range.
func (r Range) Contains(v int) bool { return r.Min <= v && v <= r.Max }

// UnsupportedVersionData is the data of an unsupported-protocol-version error:
// the range the daemon asked for and the range the adapter supports.
type UnsupportedVersionData struct {
	Requested Range `json:"requested"`
	Supported Range `json:"supported"`
}

// Validate checks both ranges.
func (d UnsupportedVersionData) Validate() error {
	var c checker
	c.field("requested", d.Requested.Validate())
	c.field("supported", d.Supported.Validate())
	return c.err
}

// NoCommonVersionError is returned by Negotiate when the two ranges do not
// overlap. Its Data is what the unsupported-protocol-version error carries.
type NoCommonVersionError struct{ Data UnsupportedVersionData }

func (e *NoCommonVersionError) Error() string {
	return fmt.Sprintf("adapterproto: no common protocol version: requested %d..%d, supported %d..%d",
		e.Data.Requested.Min, e.Data.Requested.Max, e.Data.Supported.Min, e.Data.Supported.Max)
}

// Negotiate picks the protocol version for a connection: the highest version in
// both the requested range (what the daemon announced) and the supported range
// (what the adapter implements). When the ranges do not overlap it returns a
// *NoCommonVersionError; the adapter answers it with CodeUnsupportedProtocolVersion
// and the error's Data. A range that is itself invalid is a *ValidationError.
func Negotiate(requested, supported Range) (int, error) {
	if err := requested.Validate(); err != nil {
		return 0, at("requested", err)
	}
	if err := supported.Validate(); err != nil {
		return 0, at("supported", err)
	}
	v := min(requested.Max, supported.Max)
	if v < requested.Min || v < supported.Min {
		return 0, &NoCommonVersionError{UnsupportedVersionData{Requested: requested, Supported: supported}}
	}
	return v, nil
}
