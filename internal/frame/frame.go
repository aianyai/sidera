// Package frame is the one rule by which Sidera builds the bytes that are
// hashed or signed: a domain, then every field behind its own length.
//
// Identifier derivation, record signatures, record digests and every later
// signed or hashed construction use Frame, so there is exactly one answer to
// "how are these inputs joined". The package imports nothing.
package frame

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Domain rules. A domain names the purpose of a hash or signature, so that a
// value made for one purpose can never be accepted for another.
const (
	// DomainPrefix starts every domain.
	DomainPrefix = "sidera/"
	// MaxDomainLen bounds a domain. Its length is the first byte of every
	// framed input, so a bound below 64 keeps that byte a single varint byte
	// below 64: it cannot equal the first byte, always a letter, of the
	// text-prefixed payloads libp2p signs with the same network key (the Noise,
	// TLS and HTTP peer-ID authentication handshakes).
	MaxDomainLen = 63
)

// Frame returns uvarint(len(domain)) || domain || (uvarint(len(f)) || f) for
// each field f in order.
//
// Every part carries its own length, so the encoding is injective: two
// different (domain, fields) inputs never produce the same bytes. That also
// makes the encoding prefix-free across domains: the framed bytes of one
// domain are never a prefix of those of another, so a domain added later cannot
// collide with an existing one. A nil field and an empty field are the same
// (length zero); a field that is absent differs from both, because the count of
// fields is visible in the bytes.
//
// domain must come from Domain. Frame does not check it.
func Frame(domain string, fields ...[]byte) []byte {
	n := binary.MaxVarintLen64 + len(domain)
	for _, f := range fields {
		n += binary.MaxVarintLen64 + len(f)
	}
	out := make([]byte, 0, n)
	out = binary.AppendUvarint(out, uint64(len(domain)))
	out = append(out, domain...)
	for _, f := range fields {
		out = binary.AppendUvarint(out, uint64(len(f)))
		out = append(out, f...)
	}
	return out
}

// Domain returns d after checking the domain rules: it starts with
// DomainPrefix and is at most MaxDomainLen bytes long. It panics otherwise, so
// a package declares its domains as variables initialised with Domain and a
// bad one fails the first test run, not a deployment. Uniqueness across the
// whole module is checked by this package's tests, which find every call of
// Domain; for that reason the argument must be a string literal.
func Domain(d string) string {
	if err := CheckDomain(d); err != nil {
		panic("frame: " + err.Error())
	}
	return d
}

// CheckDomain reports why d is not a valid domain, or nil.
func CheckDomain(d string) error {
	switch {
	case !strings.HasPrefix(d, DomainPrefix):
		return fmt.Errorf("domain %q does not start with %q", d, DomainPrefix)
	case len(d) > MaxDomainLen:
		return fmt.Errorf("domain %q is %d bytes, at most %d", d, len(d), MaxDomainLen)
	}
	return nil
}
