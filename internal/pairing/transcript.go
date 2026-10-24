package pairing

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/frame"
	"github.com/aianyai/sidera/internal/record"
)

// NonceLen is the length of the random nonces both sides contribute.
const NonceLen = 32

// MaxProtocolIDLen bounds the negotiated protocol identifier that the
// transcript binds.
const MaxProtocolIDLen = 128

// Nonce is a random value one side contributes to the transcript: the
// initiator's nA (committed first, opened last) and the target's nB (revealed
// between the two).
type Nonce [NonceLen]byte

// NewNonce reads a nonce from r (crypto/rand.Reader in production). A nonce is
// fresh for every session, is never persisted before it is revealed and is
// never reused: a target that repeated nB would let an attacker learn it in a
// first session and choose its own nonce for the code offline in a second.
// A reader that returns zeros is refused.
func NewNonce(r io.Reader) (Nonce, error) {
	var n Nonce
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return Nonce{}, fmt.Errorf("%w: read randomness: %w", ErrNonce, err)
	}
	if n.IsZero() {
		return Nonce{}, fmt.Errorf("%w: all zero", ErrNonce)
	}
	return n, nil
}

// IsZero reports whether n is the unset all-zero nonce.
func (n Nonce) IsZero() bool { return n == Nonce{} }

// Commitment is C, the initiator's hash commitment to its nonce and to every
// negotiated parameter.
type Commitment [32]byte

// IsZero reports whether c is the unset zero commitment.
func (c Commitment) IsZero() bool { return c == Commitment{} }

// ID identifies one pairing: the digest of the whole transcript. Every request
// to confirm or reject names it, so a confirmation cannot land on another
// request, and two requests between the same nodes never share one.
type ID [32]byte

// IsZero reports whether id is the unset zero identifier.
func (id ID) IsZero() bool { return id == ID{} }

// Terms are the parameters both sides bind before any nonce is revealed: the
// negotiated protocol, both parties as this connection shows them, and the
// times the initiator proposes. The initiator commits to all of them; the
// target recomputes the commitment from its own view of the connection, so any
// disagreement between the two views fails the commitment.
type Terms struct {
	// Protocol is the libp2p protocol identifier negotiated for the stream, as
	// the stream reports it. Binding it makes a downgrade to another protocol
	// version change the code.
	Protocol string
	// Initiator and Target are the two ends. Each side takes its own end from
	// its own connection state and the other end from the authenticated peer,
	// never from a message.
	Initiator, Target Party
	// CreatedAt is when the initiator started, whole seconds. It becomes the
	// credential's creation time.
	CreatedAt time.Time
	// NotAfter is when the credential would stop carrying trust; the zero time
	// means never, and is written as an empty field.
	NotAfter time.Time
}

func (t Terms) validate() error {
	switch {
	case t.Protocol == "" || len(t.Protocol) > MaxProtocolIDLen || !utf8.ValidString(t.Protocol):
		return fmt.Errorf("%w: protocol identifier must be 1 to %d bytes of UTF-8", ErrTerms, MaxProtocolIDLen)
	case t.Initiator.IsZero() || t.Target.IsZero():
		return fmt.Errorf("%w: a party is unset", ErrTerms)
	case t.Initiator.node == t.Target.node:
		return fmt.Errorf("%w: a node cannot pair with itself", ErrTerms)
	}
	created, err := record.TimeToWire(t.CreatedAt)
	if err != nil || !t.CreatedAt.Equal(time.Unix(created, 0)) {
		return fmt.Errorf("%w: created_at must be whole seconds in range", ErrTerms)
	}
	if !t.NotAfter.IsZero() {
		notAfter, err := record.TimeToWire(t.NotAfter)
		if err != nil || !t.NotAfter.Equal(time.Unix(notAfter, 0)) {
			return fmt.Errorf("%w: not_after must be whole seconds in range", ErrTerms)
		}
		if notAfter <= created {
			return fmt.Errorf("%w: not_after must be after created_at", ErrTerms)
		}
	}
	return nil
}

// fields returns the framed fields of the terms in their fixed order: the
// protocol, the initiator's four fields, the target's four fields, created_at
// and not_after (each an 8-byte big-endian integer; not_after is an empty field
// when the credential does not expire).
func (t Terms) fields() [][]byte {
	out := [][]byte{[]byte(t.Protocol)}
	out = append(out, t.Initiator.fields()...)
	out = append(out, t.Target.fields()...)
	out = append(out, i64be(t.CreatedAt.Unix()))
	if t.NotAfter.IsZero() {
		out = append(out, nil)
	} else {
		out = append(out, i64be(t.NotAfter.Unix()))
	}
	return out
}

// Commit returns C = SHA-256(Frame(commit domain, terms..., nA)): what the
// initiator sends before it has seen the target's nonce.
func (t Terms) Commit(nA Nonce) (Commitment, error) {
	if err := t.validate(); err != nil {
		return Commitment{}, err
	}
	if nA.IsZero() {
		return Commitment{}, fmt.Errorf("%w: all zero", ErrNonce)
	}
	return sha256.Sum256(frame.Frame(commitDomain, append(t.fields(), nA[:])...)), nil
}

// Transcript is everything both sides have bound once the nonces are open:
// the terms, the commitment and both nonces. Every value a pairing needs is a
// function of it: the identifier, the verification code, the credential. It
// can only be made by Open, which checks the commitment, so a Transcript in
// hand is one whose commitment held.
type Transcript struct {
	terms      Terms
	commitment Commitment
	nB, nA     Nonce

	id         ID
	code       Code
	credential credential.Credential
}

// Open checks that nA, with the terms, hashes to the commitment c and returns
// the transcript. The target calls it on receiving M3, with its own view of the
// terms; the initiator calls it on receiving M2, with its own commitment. It
// fails when the commitment does not hold, when a nonce is zero or when no
// code can be drawn.
func Open(terms Terms, c Commitment, nB, nA Nonce) (Transcript, error) {
	if nB.IsZero() {
		return Transcript{}, fmt.Errorf("%w: nB is all zero", ErrNonce)
	}
	want, err := terms.Commit(nA)
	if err != nil {
		return Transcript{}, err
	}
	if want != c {
		return Transcript{}, ErrCommitment
	}
	t := Transcript{terms: terms, commitment: c, nB: nB, nA: nA}
	body := append(terms.fields(), c[:], nB[:], nA[:])
	t.id = sha256.Sum256(frame.Frame(transcriptDomain, body...))

	ikm := append(append([]byte(nil), nA[:]...), nB[:]...)
	prk, err := hkdfExtract(nil, ikm)
	if err != nil {
		return Transcript{}, err
	}
	okm, err := hkdfExpand(prk, frame.Frame(codeDomain, body...), 32)
	if err != nil {
		return Transcript{}, err
	}
	code, ok := sampleCode(okm)
	if !ok {
		return Transcript{}, ErrNoCode
	}
	t.code = code

	raw, err := hkdfExpand(prk, frame.Frame(nonceDomain, body...), credential.NonceLen)
	if err != nil {
		return Transcript{}, err
	}
	var nonce credential.Nonce
	copy(nonce[:], raw)
	t.credential = credential.New(terms.Initiator.node, terms.Target.node, terms.CreatedAt, terms.NotAfter, nonce)
	return t, nil
}

// Terms returns the terms the transcript binds.
func (t Transcript) Terms() Terms { return t.terms }

// Commitment returns C.
func (t Transcript) Commitment() Commitment { return t.commitment }

// Nonces returns the target's nonce nB and the initiator's nonce nA.
func (t Transcript) Nonces() (nB, nA Nonce) { return t.nB, t.nA }

// ID returns the pairing identifier: SHA-256(Frame(transcript domain, terms...,
// C, nB, nA)).
func (t Transcript) ID() ID { return t.id }

// Code returns the verification code both owners compare.
func (t Transcript) Code() Code { return t.code }

// Credential returns the credential both sides compute from the transcript and
// sign. It is never sent: each side builds it for itself, so neither can be
// made to sign bytes the other chose. Its nonce is derived from the same
// secret as the code, which makes two pairings between the same nodes in the
// same second differ.
func (t Transcript) Credential() credential.Credential { return t.credential }

// IsZero reports whether t is the unset zero transcript.
func (t Transcript) IsZero() bool { return t.id.IsZero() }

func u64be(v uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, v)
}

func i64be(v int64) []byte {
	return binary.BigEndian.AppendUint64(nil, uint64(v))
}
