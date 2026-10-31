package network

import (
	"context"

	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/pairing"
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

	// PairingOpened takes a pairing stream the peer opened, the target's end
	// of an online segment or of a result fetch. peer holds the facts of the
	// connection and s is the engine's way to write to the stream; the engine
	// gives the stream the session identifier it is known by from now on. An
	// error refuses the stream: it is reset, and neither PairingFrame nor
	// PairingClosed follows.
	PairingOpened(ctx context.Context, peer PairingPeer, s PairingStream) (pairing.SessionID, error)

	// PairingFrame hands over one frame of the stream of a session, on either
	// side: the engine knows from the session identifier which side it is.
	// Frames of a session arrive one at a time and in order, and the next frame
	// (or the end of the stream) is read only after PairingFrame returned, so
	// the frames and the Close that answer a frame must have been issued
	// through the PairingStream by then: a result fetch is one frame, its
	// half-close and the answer, and the half-close is read as the end of the
	// stream as soon as the frame is handled. An error means the engine will
	// take no more frames of the session; the stream is reset and PairingClosed
	// follows.
	PairingFrame(ctx context.Context, sid pairing.SessionID, frame []byte) error

	// PairingClosed tells the engine that the stream of a session ended without
	// the engine's own Close: the peer closed or reset it, a frame was not
	// valid, a limit was reached, or the network is closing. It is called once
	// per stream and never after the engine called Close, so a session the
	// engine closed itself is not reported. A session identifier the engine does
	// not know (a stream it never took, or one it already forgot) is ignored.
	PairingClosed(ctx context.Context, sid pairing.SessionID)
}
