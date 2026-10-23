package ident

import (
	"crypto/sha256"
	"fmt"

	"github.com/aianyai/sidera/internal/frame"
)

// Derivation domains. They separate node identifiers from person identifiers,
// so one key used in both roles yields two unrelated identifiers, and they are
// versioned so that a changed derivation is a new domain, not a silent change.
var (
	nodeDomain   = frame.Domain("sidera/id/node/1")
	personDomain = frame.Domain("sidera/id/person/1")
)

// NodeIDFromKey derives the node identifier of the long-term identity public
// key k: SHA-256 of frame.Frame(nodeDomain, algorithm tag as one byte, canonical
// key bytes). The tag keeps a second algorithm from colliding with Ed25519 keys
// of the same length. It fails for the zero key.
func NodeIDFromKey(k PublicKey) (NodeID, error) {
	var id NodeID
	sum, err := derive(nodeDomain, k)
	if err != nil {
		return id, fmt.Errorf("node id: %w", err)
	}
	return id, fill("node id", id.b[:], sum)
}

// PersonIDFromKey derives the person identifier of the root public key k in the
// same way as NodeIDFromKey, under the person domain.
func PersonIDFromKey(k PublicKey) (PersonID, error) {
	var id PersonID
	sum, err := derive(personDomain, k)
	if err != nil {
		return id, fmt.Errorf("person id: %w", err)
	}
	return id, fill("person id", id.b[:], sum)
}

func derive(domain string, k PublicKey) ([]byte, error) {
	if k.IsZero() {
		return nil, fmt.Errorf("%w: zero public key", ErrZero)
	}
	sum := sha256.Sum256(frame.Frame(domain, []byte{byte(k.alg)}, k.b[:]))
	return sum[:], nil
}
