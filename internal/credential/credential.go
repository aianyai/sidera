package credential

import (
	"bytes"
	"fmt"
	"io"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// Type is the type string of a mutual-trust credential.
const Type = "sidera/trust-credential/1"

// NonceLen is the length of the credential nonce.
const NonceLen = 16

// Nonce makes two credentials between the same two nodes, created in the same
// second, differ: it is part of the payload and therefore of the digest. The
// caller supplies it; the pairing protocol derives it from material both sides
// share, so both compute the same payload.
type Nonce [NonceLen]byte

// NewNonce reads a nonce from r (crypto/rand.Reader in production).
func NewNonce(r io.Reader) (Nonce, error) {
	var n Nonce
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return Nonce{}, fmt.Errorf("credential nonce: read randomness: %w", err)
	}
	if n == (Nonce{}) {
		return Nonce{}, fmt.Errorf("credential nonce: %w", ErrNonce)
	}
	return n, nil
}

// Credential is the payload of a mutual-trust credential: the two nodes that
// trust each other, when the trust was established, optionally when it ends,
// and a nonce. Both nodes' identity keys sign it.
//
// The payload is a pure function of its fields, and New puts the two ends in a
// fixed order, so two nodes that know the same facts compute the same bytes
// without exchanging them. That is what lets the pairing protocol sign a payload
// each side computed for itself.
type Credential struct {
	// A and B are the two ends. A is the smaller identifier, compared as 32
	// bytes from the first byte, so the order does not depend on who started
	// the pairing. They must differ.
	A, B ident.NodeID
	// CreatedAt is when the trust was established.
	CreatedAt time.Time
	// NotAfter is when the credential stops carrying trust. The zero time means
	// it never does and is written by leaving the field out of the payload. This
	// package passes the time on as a fact; package trust judges it, so an
	// expired credential is still a valid record.
	NotAfter time.Time
	Nonce    Nonce
}

// New builds the credential between x and y, in either order. Times keep the
// precision of the wire only after a round trip: seconds.
func New(x, y ident.NodeID, createdAt, notAfter time.Time, nonce Nonce) Credential {
	a, b := x, y
	if bytes.Compare(a.Bytes(), b.Bytes()) > 0 {
		a, b = b, a
	}
	return Credential{A: a, B: b, CreatedAt: createdAt, NotAfter: notAfter, Nonce: nonce}
}

type credentialWire struct {
	A         []byte `cbor:"1,keyasint"`
	B         []byte `cbor:"2,keyasint"`
	CreatedAt int64  `cbor:"3,keyasint"`
	NotAfter  *int64 `cbor:"4,keyasint,omitempty"`
	Nonce     []byte `cbor:"5,keyasint"`
}

func credentialToWire(c Credential) (credentialWire, error) {
	created, err := record.TimeToWire(c.CreatedAt)
	if err != nil {
		return credentialWire{}, fmt.Errorf("created_at: %w", err)
	}
	notAfter, err := record.OptionalTimeToWire(c.NotAfter)
	if err != nil {
		return credentialWire{}, fmt.Errorf("not_after: %w", err)
	}
	return credentialWire{A: c.A.Bytes(), B: c.B.Bytes(), CreatedAt: created, NotAfter: notAfter, Nonce: c.Nonce[:]}, nil
}

func credentialFromWire(w credentialWire) (Credential, error) {
	a, err := ident.NodeIDFromBytes(w.A)
	if err != nil {
		return Credential{}, fmt.Errorf("end a: %w", err)
	}
	b, err := ident.NodeIDFromBytes(w.B)
	if err != nil {
		return Credential{}, fmt.Errorf("end b: %w", err)
	}
	if bytes.Compare(w.A, w.B) >= 0 {
		return Credential{}, ErrEnds
	}
	var nonce Nonce
	if len(w.Nonce) != NonceLen {
		return Credential{}, fmt.Errorf("%w: %d bytes, want %d", ErrNonce, len(w.Nonce), NonceLen)
	}
	copy(nonce[:], w.Nonce)
	if nonce == (Nonce{}) {
		return Credential{}, fmt.Errorf("%w: all zero", ErrNonce)
	}
	created, err := record.TimeFromWire(w.CreatedAt)
	if err != nil {
		return Credential{}, fmt.Errorf("created_at: %w", err)
	}
	notAfter, err := record.OptionalTimeFromWire(w.NotAfter)
	if err != nil {
		return Credential{}, fmt.Errorf("not_after: %w", err)
	}
	if !notAfter.IsZero() && !notAfter.After(created) {
		return Credential{}, ErrWindow
	}
	return Credential{A: a, B: b, CreatedAt: created, NotAfter: notAfter, Nonce: nonce}, nil
}

// Descriptor describes the credential record. Its signer rule has two slots:
// the identity key of A and the identity key of B. The record is complete only
// when both have signed, and a third key can never sign it.
//
// The initiating side signs with record.Seal and sends the partial record; the
// other side checks it with record.VerifyPartial and completes it with
// record.CoSign.
var Descriptor = record.NewDescriptor(Type,
	credentialToWire,
	credentialFromWire,
	func(c Credential) []record.SignerSlot {
		return []record.SignerSlot{record.NodeIdentityKey(c.A), record.NodeIdentityKey(c.B)}
	},
)

// Payload returns the exact payload bytes of c, the bytes both signers sign. It
// is the descriptor's own conversion (credentialToWire) written canonically and
// read back through the descriptor (record.Peek), so it refuses a credential
// that no receiver would accept and cannot drift from what Seal writes.
func (c Credential) Payload() ([]byte, error) {
	w, err := credentialToWire(c)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", record.ErrPayload, err)
	}
	b, err := record.MarshalCanonical(w)
	if err != nil {
		return nil, err
	}
	if _, err := decodePayload(b); err != nil {
		return nil, err
	}
	return b, nil
}

// Digest returns the digest of the credential record c becomes: the same
// whether the record has one signature, two or none yet, so both sides know it
// before the second signature exists.
func (c Credential) Digest() (record.Digest, error) {
	b, err := c.Payload()
	if err != nil {
		return record.Digest{}, err
	}
	return record.DigestOf(Type, b), nil
}

// decodePayload reads credential payload bytes through the descriptor, the one
// decoding path. The revocation record uses it for the payload it embeds.
func decodePayload(b []byte) (Credential, error) {
	u, err := record.Peek(Descriptor, record.Record{Type: Type, Payload: b})
	if err != nil {
		return Credential{}, err
	}
	return u.Payload(), nil
}

// Facts returns what package trust reads of a verified credential: the digest,
// the two ends and the end of validity, with the zero time standing for "never
// expires", which is how package trust reads a zero NotAfter too.
func Facts(v record.Verified[Credential]) trust.Credential {
	return trust.Credential{
		Digest:   trust.CredentialDigest(v.Digest()),
		A:        v.Payload().A,
		B:        v.Payload().B,
		NotAfter: v.Payload().NotAfter,
	}
}
