package network

import (
	"context"

	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/record"
)

// Node is what the network layer calls on the engine. The network layer defines
// it because it is the consumer; the engine implements it and imports nothing of
// this package.
type Node interface {
	// Handler takes what arrived: an envelope, a pull and a directory query,
	// each told the node the connection authenticated.
	transport.Handler

	// NotePeerCertificate hands over the certificate a member's exchange
	// presented, before the connection becomes a member's, so that the engine
	// can keep it as the node's accepted certificate when its serial is not
	// below the stored one. rec is the signed record cert was verified from:
	// the engine stores the record, since a verified certificate does not carry
	// the signature that checking it again needs. seen is the serial state to
	// store when it is higher. An error closes the connection.
	NotePeerCertificate(ctx context.Context, cert record.Verified[record.NetworkKeyCertificate], rec record.Record, seen record.SerialSeen) error

	// RefusalReason returns the name of the refusal reason in err's chain, as
	// the answer of a call carries it, and false when err is not a refusal of
	// the engine. A refused call is answered with that name; any other error is
	// answered as a failure that says nothing more.
	RefusalReason(err error) (string, bool)
}
