// Package ident defines the identifiers that name nodes, people and endpoints.
//
// All identifiers are fixed-length value types. They are comparable and usable
// as map keys, and the zero value means "unset": it is never a valid
// identifier. See README.md for the invariants.
package ident

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Byte lengths of the identifiers.
const (
	NodeIDLen     = 32
	PersonIDLen   = 32
	EndpointIDLen = 16
)

// Errors returned by constructors and parsers. They are wrapped with the
// identifier name, so callers match them with errors.Is.
var (
	// ErrLength reports input of the wrong length.
	ErrLength = errors.New("wrong length")
	// ErrZero reports the all-zero value, which means "unset".
	ErrZero = errors.New("all-zero value")
	// ErrText reports text that is not the canonical lowercase hex form.
	ErrText = errors.New("malformed text")
)

// NodeID identifies a node. It will be a digest of the node's long-term
// identity public key.
type NodeID struct{ b [NodeIDLen]byte }

// PersonID identifies a person. It will be a digest of the person's root
// public key.
type PersonID struct{ b [PersonIDLen]byte }

// EndpointID identifies an endpoint on its node. It is generated randomly by
// the node (see NewEndpointID) and never changes once issued.
type EndpointID struct{ b [EndpointIDLen]byte }

// Address names an endpoint on a node.
type Address struct {
	Node     NodeID
	Endpoint EndpointID
}

// NodeIDFromBytes returns the NodeID with the given bytes. It fails if b is not
// exactly NodeIDLen bytes or is all zero.
func NodeIDFromBytes(b []byte) (NodeID, error) {
	var id NodeID
	return id, fill("node id", id.b[:], b)
}

// ParseNodeID parses the lowercase hex form produced by NodeID.String.
func ParseNodeID(s string) (NodeID, error) {
	var id NodeID
	return id, parse("node id", id.b[:], s)
}

// Bytes returns a copy of the identifier's bytes.
func (id NodeID) Bytes() []byte { return append([]byte(nil), id.b[:]...) }

// IsZero reports whether id is the unset zero value.
func (id NodeID) IsZero() bool { return isZero(id.b[:]) }

// String returns the lowercase hex form.
func (id NodeID) String() string { return hex.EncodeToString(id.b[:]) }

// PersonIDFromBytes returns the PersonID with the given bytes. It fails if b is
// not exactly PersonIDLen bytes or is all zero.
func PersonIDFromBytes(b []byte) (PersonID, error) {
	var id PersonID
	return id, fill("person id", id.b[:], b)
}

// ParsePersonID parses the lowercase hex form produced by PersonID.String.
func ParsePersonID(s string) (PersonID, error) {
	var id PersonID
	return id, parse("person id", id.b[:], s)
}

// Bytes returns a copy of the identifier's bytes.
func (id PersonID) Bytes() []byte { return append([]byte(nil), id.b[:]...) }

// IsZero reports whether id is the unset zero value.
func (id PersonID) IsZero() bool { return isZero(id.b[:]) }

// String returns the lowercase hex form.
func (id PersonID) String() string { return hex.EncodeToString(id.b[:]) }

// EndpointIDFromBytes returns the EndpointID with the given bytes. It fails if
// b is not exactly EndpointIDLen bytes or is all zero.
func EndpointIDFromBytes(b []byte) (EndpointID, error) {
	var id EndpointID
	return id, fill("endpoint id", id.b[:], b)
}

// ParseEndpointID parses the lowercase hex form produced by EndpointID.String.
func ParseEndpointID(s string) (EndpointID, error) {
	var id EndpointID
	return id, parse("endpoint id", id.b[:], s)
}

// NewEndpointID reads a new random EndpointID from r. Callers pass
// crypto/rand.Reader; the package itself performs no hidden I/O. It fails if r
// fails or yields all zero bytes.
func NewEndpointID(r io.Reader) (EndpointID, error) {
	var id EndpointID
	if _, err := io.ReadFull(r, id.b[:]); err != nil {
		return EndpointID{}, fmt.Errorf("endpoint id: read randomness: %w", err)
	}
	if id.IsZero() {
		return EndpointID{}, fmt.Errorf("endpoint id: %w", ErrZero)
	}
	return id, nil
}

// Bytes returns a copy of the identifier's bytes.
func (id EndpointID) Bytes() []byte { return append([]byte(nil), id.b[:]...) }

// IsZero reports whether id is the unset zero value.
func (id EndpointID) IsZero() bool { return isZero(id.b[:]) }

// String returns the lowercase hex form.
func (id EndpointID) String() string { return hex.EncodeToString(id.b[:]) }

// ParseAddress parses the form produced by Address.String: "<node>/<endpoint>".
func ParseAddress(s string) (Address, error) {
	node, endpoint, ok := strings.Cut(s, "/")
	if !ok {
		return Address{}, fmt.Errorf("address: %w: missing '/'", ErrText)
	}
	n, err := ParseNodeID(node)
	if err != nil {
		return Address{}, fmt.Errorf("address: %w", err)
	}
	e, err := ParseEndpointID(endpoint)
	if err != nil {
		return Address{}, fmt.Errorf("address: %w", err)
	}
	return Address{Node: n, Endpoint: e}, nil
}

// IsZero reports whether either part of the address is unset.
func (a Address) IsZero() bool { return a.Node.IsZero() || a.Endpoint.IsZero() }

// String returns "<node>/<endpoint>".
func (a Address) String() string { return a.Node.String() + "/" + a.Endpoint.String() }

func isZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// fill copies src into dst after checking length and non-zero.
func fill(name string, dst, src []byte) error {
	if len(src) != len(dst) {
		return fmt.Errorf("%s: %w: got %d bytes, want %d", name, ErrLength, len(src), len(dst))
	}
	if isZero(src) {
		return fmt.Errorf("%s: %w", name, ErrZero)
	}
	copy(dst, src)
	return nil
}

// parse decodes the canonical lowercase hex form of s into dst.
func parse(name string, dst []byte, s string) error {
	if len(s) != 2*len(dst) {
		return fmt.Errorf("%s: %w: got %d characters, want %d", name, ErrLength, len(s), 2*len(dst))
	}
	raw, err := hex.DecodeString(s)
	if err != nil || hex.EncodeToString(raw) != s {
		return fmt.Errorf("%s: %w: want lowercase hex", name, ErrText)
	}
	return fill(name, dst, raw)
}
