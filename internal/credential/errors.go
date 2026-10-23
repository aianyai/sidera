package credential

import "errors"

// Reasons a payload of this package is refused. They are wrapped by
// record.ErrPayload when a descriptor refuses a payload; match them with
// errors.Is.
var (
	// ErrEnds reports a credential or directive whose two ends are not two
	// different nodes in ascending order of their identifier bytes.
	ErrEnds = errors.New("the two ends must be different nodes in ascending order")
	// ErrNonce reports a credential nonce that is not NonceLen bytes or is all
	// zero.
	ErrNonce = errors.New("invalid credential nonce")
	// ErrWindow reports a credential that ends before it starts.
	ErrWindow = errors.New("not_after must be after created_at")
	// ErrIdentifier reports an identifier or digest that is unset.
	ErrIdentifier = errors.New("unset identifier")
	// ErrCertificate reports an embedded device certificate that is not a
	// well-formed, correctly signed certificate record.
	ErrCertificate = errors.New("invalid embedded device certificate")
	// ErrEmbedded reports an embedded credential payload that is not a valid
	// credential.
	ErrEmbedded = errors.New("invalid embedded credential payload")
)
