package record

import "errors"

// Errors returned by this package. They are wrapped with context; match them
// with errors.Is.
var (
	// ErrOversize reports input longer than the caller's limit (or a limit that
	// is not positive, which leaves no room for any input).
	ErrOversize = errors.New("input exceeds the size limit")
	// ErrMalformed reports bytes that are not well-formed under the strict
	// decoding options: duplicate map keys, unknown fields, indefinite
	// lengths, tags, trailing bytes, wrong types.
	ErrMalformed = errors.New("malformed encoding")
	// ErrNotCanonical reports well-formed input that is not the unique
	// deterministic encoding of what it decodes to.
	ErrNotCanonical = errors.New("not the canonical encoding")
	// ErrType reports a record type string that is empty, too long, or does not
	// match the descriptor it was verified against.
	ErrType = errors.New("wrong record type")
	// ErrPayload reports a payload that the record type's rules refuse.
	ErrPayload = errors.New("invalid payload")
	// ErrKey reports a public key that is unset or invalid.
	ErrKey = errors.New("invalid public key")
	// ErrSignatureCount reports a record with no signatures or more than
	// MaxSignatures.
	ErrSignatureCount = errors.New("wrong number of signatures")
	// ErrDuplicateSigner reports the same public key signing twice.
	ErrDuplicateSigner = errors.New("duplicate signer")
	// ErrSignatureOrder reports signatures that are not sorted by the encoded
	// public key.
	ErrSignatureOrder = errors.New("signatures out of order")
	// ErrSignature reports a signature that does not verify.
	ErrSignature = errors.New("signature does not verify")
	// ErrSigner reports an injected Signer that returned an unusable signature
	// or an invalid key.
	ErrSigner = errors.New("signer failed")
	// ErrSignerNotAllowed reports a valid signature by a key the record type
	// does not allow to sign this payload.
	ErrSignerNotAllowed = errors.New("signer not allowed for this record")
	// ErrIncomplete reports a record that lacks signatures the record type
	// requires. Partial records are verified with VerifyPartial.
	ErrIncomplete = errors.New("required signatures are missing")
	// ErrDescriptor reports a zero or malformed Descriptor. It fails closed.
	ErrDescriptor = errors.New("invalid record descriptor")
	// ErrTime reports a time that cannot be put on the wire or read from it.
	ErrTime = errors.New("invalid time")
	// ErrLevel reports a level outside the wire table.
	ErrLevel = errors.New("invalid level")
	// ErrCounter reports a counter or serial that is not at least 1.
	ErrCounter = errors.New("counter must start at 1")
)
