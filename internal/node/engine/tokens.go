package engine

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// tokenIndex finds the endpoint that holds a token. It is keyed by the SHA-256
// of the token, so that a lookup does not compare the presented token with the
// tokens of other endpoints one after another, and the one candidate it finds is
// compared in constant time. Only the loop uses it.
type tokenIndex struct {
	byHash     map[[sha256.Size]byte]ident.EndpointID
	byEndpoint map[ident.EndpointID][]byte
}

func newTokenIndex() *tokenIndex {
	return &tokenIndex{
		byHash:     map[[sha256.Size]byte]ident.EndpointID{},
		byEndpoint: map[ident.EndpointID][]byte{},
	}
}

// add makes token the token of the endpoint, in place of the one it had.
func (x *tokenIndex) add(id ident.EndpointID, token []byte) {
	x.remove(id)
	t := append([]byte(nil), token...)
	x.byEndpoint[id] = t
	x.byHash[sha256.Sum256(t)] = id
}

// remove forgets the token of the endpoint, if it has one.
func (x *tokenIndex) remove(id ident.EndpointID) {
	if t, ok := x.byEndpoint[id]; ok {
		delete(x.byHash, sha256.Sum256(t))
		delete(x.byEndpoint, id)
	}
}

// lookup finds the endpoint of a token as presented, in its text form. Text that
// is not the unpadded base64url of 32 bytes is no token.
func (x *tokenIndex) lookup(t adapterproto.Token) (ident.EndpointID, bool) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(string(t))
	if err != nil || len(raw) != store.TokenLen {
		return ident.EndpointID{}, false
	}
	id, ok := x.byHash[sha256.Sum256(raw)]
	if !ok || subtle.ConstantTimeCompare(x.byEndpoint[id], raw) != 1 {
		return ident.EndpointID{}, false
	}
	return id, true
}

// mintToken reads a new token from r. A token of zero bytes is refused: a reader
// that gives it is broken, and that must show.
func mintToken(r io.Reader) ([]byte, error) {
	t := make([]byte, store.TokenLen)
	if _, err := io.ReadFull(r, t); err != nil {
		return nil, err
	}
	for _, b := range t {
		if b != 0 {
			return t, nil
		}
	}
	return nil, errors.New("engine: the random source gave a token of zero bytes")
}
