package publication

import "errors"

// Reasons a payload of this package is refused. A descriptor wraps them in
// record.ErrPayload; match them with errors.Is.
var (
	// ErrIdentifier reports an identifier that is unset.
	ErrIdentifier = errors.New("unset identifier")
	// ErrBootNonce reports a boot nonce that is not BootNonceLen bytes or is all
	// zero.
	ErrBootNonce = errors.New("invalid boot nonce")
	// ErrQueueTime reports a longest queue time outside (0, delivery.MaxQueueCap]
	// or not a whole number of seconds.
	ErrQueueTime = errors.New("queue time outside the protocol limit")
	// ErrLeaseRenewal reports a lease renewal time outside
	// (0, delivery.LeaseRenewalCap] or not a whole number of seconds.
	ErrLeaseRenewal = errors.New("lease renewal time outside the protocol limit")
	// ErrEndpoint reports an invalid endpoint entry, or endpoint entries that are
	// too many, repeated or out of order.
	ErrEndpoint = errors.New("invalid endpoint entry")
	// ErrAddress reports an address list that is too long, or an address that is
	// empty or too long.
	ErrAddress = errors.New("invalid address")
	// ErrRelay reports a relay list that is too long or holds an unset node.
	ErrRelay = errors.New("invalid relay list")
	// ErrCertificate reports an embedded network-key certificate that is not a
	// well-formed, correctly signed certificate record.
	ErrCertificate = errors.New("invalid embedded network-key certificate")
)
