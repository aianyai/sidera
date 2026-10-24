package pairing

import (
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"
)

// hkdfExtract is HKDF-Extract with SHA-256 (RFC 5869 section 2.2). An empty
// salt stands for HashLen zero bytes, as the RFC says.
func hkdfExtract(salt, ikm []byte) ([]byte, error) {
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, fmt.Errorf("%w: extract: %w", ErrKDF, err)
	}
	return prk, nil
}

// hkdfExpand is HKDF-Expand with SHA-256 (RFC 5869 section 2.3).
func hkdfExpand(prk, info []byte, length int) ([]byte, error) {
	okm, err := hkdf.Expand(sha256.New, prk, string(info), length)
	if err != nil {
		return nil, fmt.Errorf("%w: expand: %w", ErrKDF, err)
	}
	return okm, nil
}
