package pairing

import "errors"

// Errors returned by this package. They are wrapped with context; match them
// with errors.Is. Protocol outcomes (a refusal, an abort, a denied request) are
// not errors: they are the typed reasons and actions of the state machines.
var (
	// ErrParty reports a party that cannot take part in a pairing: a network key
	// the connection does not authenticate, a certificate that is not valid, or
	// keys that are unset.
	ErrParty = errors.New("invalid pairing party")
	// ErrTerms reports negotiated terms that no pairing can use: no protocol,
	// the same node on both sides, or a time that is not whole seconds in range.
	ErrTerms = errors.New("invalid pairing terms")
	// ErrNonce reports a nonce that is all zero or could not be read.
	ErrNonce = errors.New("invalid nonce")
	// ErrCommitment reports an opened nonce that does not hash to the commitment.
	ErrCommitment = errors.New("nonce does not match the commitment")
	// ErrNoCode reports a transcript for which all eight candidates of the
	// verification code were rejected by the sampling. Both sides reach this
	// together, with probability about 6.6e-30, and abort.
	ErrNoCode = errors.New("no verification code can be drawn from this transcript")
	// ErrCode reports text that is not a verification code.
	ErrCode = errors.New("invalid verification code")
	// ErrKDF reports a failure of the key derivation function.
	ErrKDF = errors.New("key derivation failed")
)
