package delivery

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// LeaseIDLen is the length of a LeaseID in bytes.
const LeaseIDLen = 16

// LeaseIDTextLen is the length of the text form of a LeaseID: lowercase hex,
// two characters per byte. It is the longest lease identifier any text format
// has to make room for.
const LeaseIDTextLen = 2 * LeaseIDLen

// Errors returned by NewLeaseID and ParseLeaseID.
var (
	// ErrLeaseIDZero reports the all-zero value, which means "unset".
	ErrLeaseIDZero = errors.New("delivery: lease ID is all zero")
	// ErrLeaseIDText reports text that is not the form LeaseID.String produces.
	ErrLeaseIDText = errors.New("delivery: lease ID is not 32 lowercase hex characters")
)

// LeaseID identifies one lease of one message. The target generates it when it
// issues the lease (see NewLeaseID); the agent's side hands it back with an
// acknowledgement, and the target accepts the acknowledgement only if the ID
// belongs to a lease issued for that message. The zero value means "unset" and
// is never a valid ID.
type LeaseID struct{ b [LeaseIDLen]byte }

// NewLeaseID reads a new random LeaseID from r. Callers pass crypto/rand.Reader;
// the package itself performs no hidden I/O. It fails if r fails or yields all
// zero bytes.
func NewLeaseID(r io.Reader) (LeaseID, error) {
	var id LeaseID
	if _, err := io.ReadFull(r, id.b[:]); err != nil {
		return LeaseID{}, fmt.Errorf("delivery: lease ID: read randomness: %w", err)
	}
	if id.IsZero() {
		return LeaseID{}, ErrLeaseIDZero
	}
	return id, nil
}

// ParseLeaseID parses the form produced by LeaseID.String: exactly
// LeaseIDTextLen lowercase hex characters, not all zero.
func ParseLeaseID(s string) (LeaseID, error) {
	var id LeaseID
	if len(s) != LeaseIDTextLen {
		return LeaseID{}, ErrLeaseIDText
	}
	for _, c := range []byte(s) {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return LeaseID{}, ErrLeaseIDText
		}
	}
	if _, err := hex.Decode(id.b[:], []byte(s)); err != nil {
		return LeaseID{}, ErrLeaseIDText
	}
	if id.IsZero() {
		return LeaseID{}, ErrLeaseIDZero
	}
	return id, nil
}

// IsZero reports whether id is the unset zero value.
func (id LeaseID) IsZero() bool { return id == LeaseID{} }

// String returns the lowercase hex form, LeaseIDTextLen characters long.
func (id LeaseID) String() string { return hex.EncodeToString(id.b[:]) }
