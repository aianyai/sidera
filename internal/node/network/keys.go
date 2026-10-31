package network

import (
	"errors"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/aianyai/sidera/internal/ident"
)

// publicKeyOf is the network key a peer identifier embeds. A peer whose key is
// not an Ed25519 key cannot present a certificate and is refused at the
// handshake.
func publicKeyOf(id peer.ID) (ident.PublicKey, error) {
	pub, err := id.ExtractPublicKey()
	if err != nil {
		return ident.PublicKey{}, err
	}
	if pub.Type() != ic.Ed25519 {
		return ident.PublicKey{}, errors.New("not an Ed25519 key")
	}
	raw, err := pub.Raw()
	if err != nil {
		return ident.PublicKey{}, err
	}
	return ident.NewPublicKey(ident.AlgorithmEd25519, raw)
}

// peerIDOf is the peer identifier of a network key: the way back from a
// certificate to the peer to dial.
func peerIDOf(key ident.PublicKey) (peer.ID, error) {
	pub, err := ic.UnmarshalEd25519PublicKey(key.Bytes())
	if err != nil {
		return "", err
	}
	return peer.IDFromPublicKey(pub)
}
