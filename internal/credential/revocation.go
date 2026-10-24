package credential

import (
	"errors"
	"fmt"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/trust"
)

// RevocationType is the type string of a credential revocation. It names the
// credential type it embeds: a revocation of a credential of another type is a
// different record type.
const RevocationType = "sidera/trust-credential-revocation/1"

// Revocation ends one credential. It embeds the whole payload of the revoked
// credential and not only its digest, for two reasons. The verifier recomputes
// the digest from the payload, so the revocation cannot name a digest nobody
// can check. And the payload shows the credential's two ends, which are the only
// nodes allowed to sign: a revocation that named only a digest could not be
// checked against anyone, and a third party could cut any edge whose digest
// it had seen.
type Revocation struct {
	// Credential is the revoked credential's payload. It is the one input:
	// encoding reads only this field.
	Credential Credential
	// digest is the digest of the revoked credential, recomputed from the
	// embedded payload. It is derived, never taken from the sender: NewRevocation
	// and decoding set it, and it is unexported so that it cannot disagree with
	// Credential. Read it with Digest.
	digest trust.CredentialDigest
}

// Digest returns the digest of the revoked credential, the entry for
// trust.View.RevokedCredentials. It is zero on a Revocation that was neither
// built by NewRevocation nor produced by verification.
func (r Revocation) Digest() trust.CredentialDigest { return r.digest }

// NewRevocation builds the revocation of c.
func NewRevocation(c Credential) (Revocation, error) {
	d, err := c.Digest()
	if err != nil {
		return Revocation{}, err
	}
	return Revocation{Credential: c, digest: trust.CredentialDigest(d)}, nil
}

// IssueRevocation revokes c and signs the revocation with identity, the identity
// key of one of c's two ends. A signer that is neither end is refused here, as
// the verifier would refuse it.
func IssueRevocation(c Credential, identity record.Signer) (record.Record, error) {
	rev, err := NewRevocation(c)
	if err != nil {
		return record.Record{}, err
	}
	return record.Seal(RevocationDescriptor, rev, identity)
}

type revocationWire struct {
	Credential []byte `cbor:"1,keyasint"`
}

// errNotAnEnd is what the revocation's signer slot answers for a key that is
// neither end of the credential.
var errNotAnEnd = errors.New("not the identity key of either end of the credential")

// eitherEnd is the slot for the identity key of node a or of node b.
func eitherEnd(a, b ident.NodeID) record.SignerSlot {
	ka, kb := record.NodeIdentityKey(a), record.NodeIdentityKey(b)
	return func(k ident.PublicKey) error {
		if ka(k) == nil || kb(k) == nil {
			return nil
		}
		return errNotAnEnd
	}
}

// RevocationDescriptor describes the revocation record. Exactly one signature
// is required, by the identity key of either end of the embedded credential: a
// record with both ends' signatures has a signer too many.
var RevocationDescriptor = record.NewDescriptor(RevocationType,
	revocationToWire,
	revocationFromWire,
	func(r Revocation) []record.SignerSlot {
		return []record.SignerSlot{eitherEnd(r.Credential.A, r.Credential.B)}
	},
)

func revocationToWire(r Revocation) (revocationWire, error) {
	b, err := r.Credential.Payload()
	if err != nil {
		return revocationWire{}, err
	}
	return revocationWire{Credential: b}, nil
}

func revocationFromWire(w revocationWire) (Revocation, error) {
	c, err := decodePayload(w.Credential)
	if err != nil {
		return Revocation{}, fmt.Errorf("%w: %w", ErrEmbedded, err)
	}
	return Revocation{Credential: c, digest: trust.CredentialDigest(record.DigestOf(Type, w.Credential))}, nil
}
