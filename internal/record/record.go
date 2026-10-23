package record

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/aianyai/sidera/internal/frame"
	"github.com/aianyai/sidera/internal/ident"
)

// Domains. A signature or digest covers the domain first, so a signature made
// for a record can never be read as a signature over anything else that uses
// the same key, and a digest of a record is never equal to a hash used for
// another purpose. The domain rules (the sidera/ prefix, less than 64 bytes)
// and the distinctness of all domains are enforced by package frame; see the
// records reference for why the length bound matters for libp2p's own
// signatures made with the same network key.
var (
	signingDomain = frame.Domain("sidera/record-sig")
	digestDomain  = frame.Domain("sidera/record-digest")
)

// Limits of the envelope.
const (
	// MaxSignatures bounds the signature list. The record types use one or two
	// signers; the bound keeps the cost of verifying hostile input small.
	MaxSignatures = 8
	// MaxTypeLen bounds the record type string.
	MaxTypeLen = 128
	// SignatureLen is the length of an Ed25519 signature.
	SignatureLen = ed25519.SignatureSize
)

// Signer produces signatures for one key. Callers inject it, so key storage
// (a file, the OS keystore, a hardware token) stays outside this package.
type Signer interface {
	// Public returns the signer's public key.
	Public() ident.PublicKey
	// Sign signs msg. The result must verify under Public.
	Sign(msg []byte) ([]byte, error)
}

// Signature is one signer's signature over a record's signed bytes.
type Signature struct {
	Key ident.PublicKey
	Sig []byte
}

// Record is the signed envelope: a type string, the exact payload bytes, and
// the signatures over them. Signatures are sorted by the encoded public key and
// no key appears twice; Sign and CoSign keep it so, and Encode refuses a record
// that is not.
type Record struct {
	Type       string
	Payload    []byte
	Signatures []Signature
}

// Digest is the SHA-256 of a record's type and payload under the digest domain. It
// does not cover the signatures, so every complete or partial form of a record
// has the same digest and a malleable signature cannot create a second one.
// Its underlying type is [32]byte, so a digest converts directly to the
// digest types of the packages that consume records.
type Digest [32]byte

// String returns the digest in lowercase hex.
func (d Digest) String() string { return hex.EncodeToString(d[:]) }

// IsZero reports whether d is the unset zero digest.
func (d Digest) IsZero() bool { return d == Digest{} }

// SignedBytes returns the bytes every signer of a record signs.
func SignedBytes(typ string, payload []byte) []byte {
	return frame.Frame(signingDomain, []byte(typ), payload)
}

// DigestOf returns the digest of a record with the given type and payload.
func DigestOf(typ string, payload []byte) Digest {
	return sha256.Sum256(frame.Frame(digestDomain, []byte(typ), payload))
}

// Digest returns the record's digest.
func (r Record) Digest() Digest { return DigestOf(r.Type, r.Payload) }

// Sign builds a record signed by all of signers. Pass a single signer for a
// record that a second party will co-sign (see CoSign).
func Sign(typ string, payload []byte, signers ...Signer) (Record, error) {
	if err := checkTypeString(typ); err != nil {
		return Record{}, err
	}
	if len(payload) == 0 {
		return Record{}, fmt.Errorf("%w: empty payload", ErrPayload)
	}
	r := Record{Type: typ, Payload: append([]byte(nil), payload...)}
	for _, s := range signers {
		var err error
		if r, err = r.CoSign(s); err != nil {
			return Record{}, err
		}
	}
	if len(r.Signatures) == 0 {
		return Record{}, fmt.Errorf("%w: no signer", ErrSignatureCount)
	}
	return r, nil
}

// CoSign returns a copy of r with s's signature added. The first party signs
// with Sign and sends the record; the second party verifies it with
// VerifyPartial, then calls CoSign. The signature is checked before it is
// added, so a broken Signer fails here and not at the far end.
func (r Record) CoSign(s Signer) (Record, error) {
	pub := s.Public()
	if pub.IsZero() {
		return Record{}, fmt.Errorf("%w: signer has no key", ErrKey)
	}
	for _, have := range r.Signatures {
		if have.Key == pub {
			return Record{}, ErrDuplicateSigner
		}
	}
	if len(r.Signatures) >= MaxSignatures {
		return Record{}, fmt.Errorf("%w: at most %d", ErrSignatureCount, MaxSignatures)
	}
	msg := SignedBytes(r.Type, r.Payload)
	sig, err := s.Sign(msg)
	if err != nil {
		return Record{}, fmt.Errorf("%w: %v", ErrSigner, err)
	}
	if !pub.Verify(msg, sig) {
		return Record{}, fmt.Errorf("%w: signature does not verify under the signer's key", ErrSigner)
	}
	out := Record{Type: r.Type, Payload: r.Payload, Signatures: make([]Signature, 0, len(r.Signatures)+1)}
	out.Signatures = append(out.Signatures, r.Signatures...)
	out.Signatures = append(out.Signatures, Signature{Key: pub, Sig: sig})
	sortSignatures(out.Signatures)
	return out, nil
}

// KeyWire is the wire form of a public key: {1: algorithm tag, 2: key bytes}.
// Record payloads that carry a key use it too.
type KeyWire struct {
	Algorithm uint64 `cbor:"1,keyasint"`
	Key       []byte `cbor:"2,keyasint"`
}

// KeyToWire returns the wire form of k.
func KeyToWire(k ident.PublicKey) KeyWire {
	return KeyWire{Algorithm: uint64(k.Algorithm()), Key: k.Bytes()}
}

// KeyFromWire validates a wire key: Ed25519 only, canonical, not small order.
func KeyFromWire(w KeyWire) (ident.PublicKey, error) {
	if w.Algorithm > 0xff {
		return ident.PublicKey{}, fmt.Errorf("%w: %w: tag %d", ErrKey, ident.ErrAlgorithm, w.Algorithm)
	}
	k, err := ident.NewPublicKey(ident.Algorithm(w.Algorithm), w.Key)
	if err != nil {
		return ident.PublicKey{}, fmt.Errorf("%w: %w", ErrKey, err)
	}
	return k, nil
}

type signatureWire struct {
	Key KeyWire `cbor:"1,keyasint"`
	Sig []byte  `cbor:"2,keyasint"`
}

type recordWire struct {
	Type       string          `cbor:"1,keyasint"`
	Payload    []byte          `cbor:"2,keyasint"`
	Signatures []signatureWire `cbor:"3,keyasint"`
}

// sortKey is the encoded public key, the order in which signatures are listed.
func sortKey(k ident.PublicKey) []byte {
	b, err := MarshalCanonical(KeyToWire(k))
	if err != nil {
		panic("record: encoding a public key cannot fail: " + err.Error())
	}
	return b
}

func sortSignatures(s []Signature) {
	sort.Slice(s, func(i, j int) bool { return bytes.Compare(sortKey(s[i].Key), sortKey(s[j].Key)) < 0 })
}

func checkTypeString(typ string) error {
	if typ == "" || len(typ) > MaxTypeLen {
		return fmt.Errorf("%w: length %d, want 1..%d", ErrType, len(typ), MaxTypeLen)
	}
	return nil
}

// shape checks everything about a record that does not need its type's rules:
// a usable type and payload, between 1 and MaxSignatures signatures of the
// right length by valid keys, strictly ascending by encoded key (which also
// excludes a signer listed twice).
func (r Record) shape() error {
	if err := checkTypeString(r.Type); err != nil {
		return err
	}
	if len(r.Payload) == 0 {
		return fmt.Errorf("%w: empty payload", ErrPayload)
	}
	if n := len(r.Signatures); n < 1 || n > MaxSignatures {
		return fmt.Errorf("%w: %d", ErrSignatureCount, n)
	}
	var prev []byte
	for i, s := range r.Signatures {
		if s.Key.IsZero() {
			return fmt.Errorf("%w: signature %d has no key", ErrKey, i)
		}
		if len(s.Sig) != SignatureLen {
			return fmt.Errorf("%w: signature %d is %d bytes, want %d", ErrSignature, i, len(s.Sig), SignatureLen)
		}
		cur := sortKey(s.Key)
		switch c := bytes.Compare(prev, cur); {
		case i > 0 && c == 0:
			return ErrDuplicateSigner
		case i > 0 && c > 0:
			return ErrSignatureOrder
		}
		prev = cur
	}
	return nil
}

// Encode returns the record's wire bytes.
func (r Record) Encode() ([]byte, error) {
	if err := r.shape(); err != nil {
		return nil, err
	}
	w := recordWire{Type: r.Type, Payload: r.Payload, Signatures: make([]signatureWire, len(r.Signatures))}
	for i, s := range r.Signatures {
		w.Signatures[i] = signatureWire{Key: KeyToWire(s.Key), Sig: s.Sig}
	}
	return MarshalCanonical(w)
}

// Decode parses a record from wire bytes of at most maxLen bytes, strictly.
// It checks the shape of the envelope, not the signatures or the payload: those
// are checked by Verify with the record type's descriptor.
func Decode(data []byte, maxLen int) (Record, error) {
	var w recordWire
	if err := UnmarshalStrict(data, &w, maxLen); err != nil {
		return Record{}, err
	}
	r := Record{Type: w.Type, Payload: w.Payload, Signatures: make([]Signature, len(w.Signatures))}
	for i, s := range w.Signatures {
		k, err := KeyFromWire(s.Key)
		if err != nil {
			return Record{}, err
		}
		r.Signatures[i] = Signature{Key: k, Sig: s.Sig}
	}
	if err := r.shape(); err != nil {
		return Record{}, err
	}
	return r, nil
}

// NewEd25519Signer returns a Signer that signs with an in-memory Ed25519 key.
// It is meant for tests and for callers that load a key from a file.
func NewEd25519Signer(priv ed25519.PrivateKey) (Signer, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: private key is %d bytes", ErrKey, len(priv))
	}
	pub, err := ident.NewPublicKey(ident.AlgorithmEd25519, priv.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKey, err)
	}
	return memorySigner{priv: priv, pub: pub}, nil
}

type memorySigner struct {
	priv ed25519.PrivateKey
	pub  ident.PublicKey
}

func (s memorySigner) Public() ident.PublicKey { return s.pub }

func (s memorySigner) Sign(msg []byte) ([]byte, error) { return ed25519.Sign(s.priv, msg), nil }
