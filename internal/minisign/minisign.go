// Package minisign verifies minisign signatures: it parses a public key and a
// signature file and checks a message against them. It signs nothing and reads
// no files; see README.md for the accepted format and the reasons.
package minisign

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

var (
	// ErrMalformed means a key or signature file does not have the exact
	// format described in README.md, or a key is unusable.
	ErrMalformed = errors.New("minisign: malformed")
	// ErrLegacyFormat means the signature is in the legacy format, which signs
	// the message itself rather than its hash. It is never accepted.
	ErrLegacyFormat = errors.New("minisign: legacy signature format")
	// ErrUnknownKey means no given key has the key id the signature names.
	ErrUnknownKey = errors.New("minisign: signature is by an unknown key")
	// ErrBadSignature means the signature, or the signature of the trusted
	// comment, does not verify with the key.
	ErrBadSignature = errors.New("minisign: signature does not verify")
)

const (
	untrustedPrefix = "untrusted comment: "
	trustedPrefix   = "trusted comment: "

	algEd25519         = "Ed" // in a public key; in a signature, the legacy format
	algEd25519Prehash  = "ED"
	keyIDSize          = 8
	publicKeyFileSize  = len(algEd25519) + keyIDSize + ed25519.PublicKeySize
	signatureFileSize  = len(algEd25519Prehash) + keyIDSize + ed25519.SignatureSize
	signatureFileLines = 4
)

// PublicKey is a minisign public key.
type PublicKey struct {
	ID  [8]byte
	Key ed25519.PublicKey
}

// Signature is a parsed prehashed signature file. It is not verified yet.
type Signature struct {
	KeyID [8]byte
	// Sig is the signature of the BLAKE2b-512 hash of the message.
	Sig [64]byte
	// TrustedComment is the text after "trusted comment: ".
	TrustedComment string
	// Global is the signature of Sig followed by TrustedComment.
	Global [64]byte
}

// FormatKeyID returns the key id the way minisign prints it: the eight bytes in
// reverse order, in upper-case hexadecimal.
func FormatKeyID(id [8]byte) string {
	var s strings.Builder
	for i := len(id) - 1; i >= 0; i-- {
		fmt.Fprintf(&s, "%02X", id[i])
	}
	return s.String()
}

// ParsePublicKey parses a public key file, "untrusted comment: ..." and the key
// line, or the key line alone, the form the install guides quote.
func ParsePublicKey(file []byte) (PublicKey, error) {
	lines, err := splitLines(file)
	if err != nil {
		return PublicKey{}, err
	}
	switch len(lines) {
	case 1:
	case 2:
		if !strings.HasPrefix(lines[0], untrustedPrefix) {
			return PublicKey{}, fmt.Errorf("%w: public key: first line does not start with %q", ErrMalformed, untrustedPrefix)
		}
		lines = lines[1:]
	default:
		return PublicKey{}, fmt.Errorf("%w: public key: %d lines, want 1 or 2", ErrMalformed, len(lines))
	}
	raw, err := decode(lines[0], publicKeyFileSize)
	if err != nil {
		return PublicKey{}, fmt.Errorf("%w: public key: %v", ErrMalformed, err)
	}
	if string(raw[:2]) != algEd25519 {
		return PublicKey{}, fmt.Errorf("%w: public key: algorithm %q, want %q", ErrMalformed, raw[:2], algEd25519)
	}
	var key PublicKey
	copy(key.ID[:], raw[2:])
	key.Key = ed25519.PublicKey(raw[2+keyIDSize:])
	return key, nil
}

// ParseSignature parses a signature file: the untrusted comment, the signature,
// the trusted comment and the signature of the trusted comment. It accepts only
// the prehashed format and returns ErrLegacyFormat for the legacy one.
func ParseSignature(file []byte) (Signature, error) {
	lines, err := splitLines(file)
	if err != nil {
		return Signature{}, err
	}
	if len(lines) != signatureFileLines {
		return Signature{}, fmt.Errorf("%w: signature: %d lines, want %d", ErrMalformed, len(lines), signatureFileLines)
	}
	if !strings.HasPrefix(lines[0], untrustedPrefix) {
		return Signature{}, fmt.Errorf("%w: signature: first line does not start with %q", ErrMalformed, untrustedPrefix)
	}
	raw, err := decode(lines[1], signatureFileSize)
	if err != nil {
		return Signature{}, fmt.Errorf("%w: signature: %v", ErrMalformed, err)
	}
	switch string(raw[:2]) {
	case algEd25519Prehash:
	case algEd25519:
		return Signature{}, ErrLegacyFormat
	default:
		return Signature{}, fmt.Errorf("%w: signature: algorithm %q, want %q", ErrMalformed, raw[:2], algEd25519Prehash)
	}
	comment, ok := strings.CutPrefix(lines[2], trustedPrefix)
	if !ok {
		return Signature{}, fmt.Errorf("%w: signature: third line does not start with %q", ErrMalformed, trustedPrefix)
	}
	global, err := decode(lines[3], ed25519.SignatureSize)
	if err != nil {
		return Signature{}, fmt.Errorf("%w: signature: global signature: %v", ErrMalformed, err)
	}
	sig := Signature{TrustedComment: comment}
	copy(sig.KeyID[:], raw[2:])
	copy(sig.Sig[:], raw[2+keyIDSize:])
	copy(sig.Global[:], global)
	return sig, nil
}

// Verify checks that sig is a signature of message by the key in keys whose ID
// the signature names, and that the trusted comment is signed by the same key,
// and returns the trusted comment. A signature that names no key in keys is
// ErrUnknownKey; keys lists every key that is accepted, so a rotation is two.
func Verify(keys []PublicKey, message []byte, sig Signature) (string, error) {
	var key *PublicKey
	for i := range keys {
		if keys[i].ID == sig.KeyID {
			key = &keys[i]
			break
		}
	}
	if key == nil {
		return "", fmt.Errorf("%w: %s", ErrUnknownKey, FormatKeyID(sig.KeyID))
	}
	if len(key.Key) != ed25519.PublicKeySize {
		return "", fmt.Errorf("%w: key %s is %d bytes, want %d", ErrMalformed, FormatKeyID(key.ID), len(key.Key), ed25519.PublicKeySize)
	}
	hash := blake2b.Sum512(message)
	if !ed25519.Verify(key.Key, hash[:], sig.Sig[:]) {
		return "", fmt.Errorf("%w: signature of the message", ErrBadSignature)
	}
	signed := append(sig.Sig[:len(sig.Sig):len(sig.Sig)], sig.TrustedComment...)
	if !ed25519.Verify(key.Key, signed, sig.Global[:]) {
		return "", fmt.Errorf("%w: signature of the trusted comment", ErrBadSignature)
	}
	return sig.TrustedComment, nil
}

// splitLines splits a file into lines. Lines end in "\n", the last one may omit
// it, and a carriage return anywhere is refused so that no two readers can
// disagree about where a line ends.
func splitLines(file []byte) ([]string, error) {
	text := string(file)
	if strings.Contains(text, "\r") {
		return nil, fmt.Errorf("%w: carriage return", ErrMalformed)
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n"), nil
}

// decode decodes a base64 line that must be exactly size bytes long.
func decode(line string, size int) ([]byte, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(line)
	if err != nil {
		return nil, err
	}
	if len(raw) != size {
		return nil, fmt.Errorf("%d bytes, want %d", len(raw), size)
	}
	return raw, nil
}
