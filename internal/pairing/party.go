package pairing

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// Party is one end of a pairing as the connection shows it: the node, the
// identity key that derives the node, the network key the connection is
// authenticated with, and the serial of the network-key certificate that bound
// the two. All four go into the transcript, so a code made for one set of keys
// cannot be shown for another.
//
// A Party is built only from a verified certificate (NewParty) or read back
// from a record this package wrote, and then always passes the same checks:
// the node derives from the identity key, the network key is not the identity
// key, the serial starts at 1. The zero Party is unset and never valid.
type Party struct {
	node     ident.NodeID
	identity ident.PublicKey
	network  ident.PublicKey
	serial   uint64
}

// NewParty returns the party that cert, the network-key certificate shown on
// this connection, describes. connectionKey is the network key the connection
// itself is authenticated with: for the remote end the key libp2p verified in
// the handshake, for the local end the key of this connection, not the key the
// configuration holds now. revocation is the result of the caller's lookup of
// cert in the revocation records, and highestSerial the highest certificate
// serial the caller has seen for cert's node (cert's own included).
//
// The binding of the connection key to the node is decided by
// record.NetworkKeyBelongs, the one place that answers it, so a certificate
// that is expired, revoked, never looked up, below the highest serial seen, for
// another node or for another key is refused here. The identity key is the one signer of the certificate,
// which record.Verify already showed derives the node.
func NewParty(cert record.Verified[record.NetworkKeyCertificate], revocation record.KeyRevocationStatus, highestSerial uint64, connectionKey ident.PublicKey, now time.Time) (Party, error) {
	if cert.Digest().IsZero() {
		return Party{}, fmt.Errorf("%w: no verified certificate", ErrParty)
	}
	c := cert.Payload()
	accepted := record.AcceptedNetworkKey{Cert: &c, Revocation: revocation, HighestSerial: highestSerial}
	if b := record.NetworkKeyBelongs(accepted, c.Node, connectionKey, now); b != record.BindingOK {
		return Party{}, fmt.Errorf("%w: network key of the connection is not the certificate's: %s", ErrParty, b)
	}
	signers := cert.Signers()
	if len(signers) != 1 {
		return Party{}, fmt.Errorf("%w: certificate has %d signers, want 1", ErrParty, len(signers))
	}
	p, err := partyOf(signers[0], c.NetworkKey, c.Serial)
	if err != nil {
		return Party{}, err
	}
	if p.node != c.Node {
		return Party{}, fmt.Errorf("%w: certificate signer does not derive its node", ErrParty)
	}
	return p, nil
}

// partyOf builds a party from its keys and applies the rules every party
// obeys. It is the one constructor, used for certificates and for records read
// back from storage.
func partyOf(identity, network ident.PublicKey, serial uint64) (Party, error) {
	if identity.IsZero() || network.IsZero() {
		return Party{}, fmt.Errorf("%w: key unset", ErrParty)
	}
	if identity == network {
		return Party{}, fmt.Errorf("%w: the network key is the identity key", ErrParty)
	}
	if serial < 1 {
		return Party{}, fmt.Errorf("%w: certificate serial must start at 1", ErrParty)
	}
	node, err := ident.NodeIDFromKey(identity)
	if err != nil {
		return Party{}, fmt.Errorf("%w: %w", ErrParty, err)
	}
	return Party{node: node, identity: identity, network: network, serial: serial}, nil
}

// Node returns the node identifier derived from the identity key.
func (p Party) Node() ident.NodeID { return p.node }

// Identity returns the node's identity key.
func (p Party) Identity() ident.PublicKey { return p.identity }

// Network returns the network key of the connection.
func (p Party) Network() ident.PublicKey { return p.network }

// Serial returns the serial of the network-key certificate.
func (p Party) Serial() uint64 { return p.serial }

// IsZero reports whether p is the unset zero party.
func (p Party) IsZero() bool { return p.node.IsZero() }

// fields returns the party's four framed transcript fields: the node, the
// identity key, the network key (each key in the public-key encoding of the
// record format, with its algorithm tag) and the serial as 8 bytes big-endian.
func (p Party) fields() [][]byte {
	return [][]byte{p.node.Bytes(), keyBytes(p.identity), keyBytes(p.network), u64be(p.serial)}
}

// keyBytes is the public-key encoding of the record format: the canonical CBOR
// map {1: algorithm, 2: key}, 38 bytes for Ed25519.
func keyBytes(k ident.PublicKey) []byte {
	b, err := record.MarshalCanonical(record.KeyToWire(k))
	if err != nil {
		panic("pairing: encoding a public key cannot fail: " + err.Error())
	}
	return b
}
