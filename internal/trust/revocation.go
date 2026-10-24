package trust

import (
	"errors"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// ErrRevokeSelf is returned when the local node is asked to revoke itself.
var ErrRevokeSelf = errors.New("trust: a node cannot revoke itself")

// RevocationEffects is what revoking a node locally does. The caller carries
// them out; this package only decides them.
type RevocationEffects struct {
	// Node is added to the locally revoked set. Once in it, the node is
	// refused whatever path leads to it, and it is never an intermediate of
	// an inherited path.
	Node ident.NodeID
	// AlreadyRevoked says Node was in the set before; adding it again changes
	// nothing.
	AlreadyRevoked bool
	// StopVouching lists the direct edges for which the local node must stop
	// issuing vouchers immediately. Other nodes that reach Node through the
	// local node then lose it once their grace period runs out, even before
	// they learn of the credential revocation.
	StopVouching []CredentialDigest
	// RevokeCredentials lists the credentials the local node must revoke with
	// its identity key. Only the two parties of a credential can revoke it.
	// The lists are empty when Node is not a direct trust party.
	RevokeCredentials []CredentialDigest
}

// RevokeLocally decides the effects of the local owner revoking node x.
// Credentials with x that are already revoked need nothing further.
func RevokeLocally(v View, x ident.NodeID) (RevocationEffects, error) {
	if x == v.Local {
		return RevocationEffects{}, ErrRevokeSelf
	}
	e := RevocationEffects{Node: x, AlreadyRevoked: v.locallyRevoked(x)}
	for _, c := range v.credentialsOf(v.Local) {
		if other, _ := c.other(v.Local); other == x && !v.credentialRevoked(c) {
			e.StopVouching = append(e.StopVouching, c.Digest)
			e.RevokeCredentials = append(e.RevokeCredentials, c.Digest)
		}
	}
	return e, nil
}

// ClearEffect is what removing a node from the locally revoked set does.
type ClearEffect struct {
	// WasRevoked says the node was in the set.
	WasRevoked bool
	// NewPairingRequired says the local node has had a direct credential with
	// the node but none is usable now: the credential was revoked or has
	// expired, so clearing the entry alone restores nothing and the two nodes
	// must pair again. When false, clearing may restore trust, directly
	// through a usable credential or by inheritance.
	NewPairingRequired bool
	// Retired says the node is a retired node. Its identity is void for good,
	// so clearing the entry restores nothing and no pairing can bring it back:
	// only a new identity, a different node ID, can join. NewPairingRequired is
	// always false when Retired is true, because a new pairing with a retired
	// identity is not the way forward.
	Retired bool
}

// ClearLocallyRevoked decides the effect of removing x from the locally
// revoked set. Who is allowed to do it is decided elsewhere: clearing
// loosens, so it is a privileged operation.
func ClearLocallyRevoked(v View, x ident.NodeID, now time.Time) ClearEffect {
	e := ClearEffect{WasRevoked: v.locallyRevoked(x), Retired: v.retired(x)}
	had, usable := false, false
	for _, c := range v.credentialsOf(v.Local) {
		if other, _ := c.other(v.Local); other == x {
			had = true
			usable = usable || v.usable(c, now)
		}
	}
	e.NewPairingRequired = had && !usable && !e.Retired
	return e
}

// VouchableEdges returns the digests of the direct edges for which the local
// node issues vouchers at now: usable credentials whose other end is neither
// locally revoked nor retired. A locally revoked party is excluded even while
// the credential revocation is still unsigned or unpropagated. A retired party
// is excluded because a voucher for its edge would help nobody: no node accepts
// trust through a retired intermediate.
func VouchableEdges(v View, now time.Time) []CredentialDigest {
	var out []CredentialDigest
	for _, c := range v.credentialsOf(v.Local) {
		other, _ := c.other(v.Local)
		if v.usable(c, now) && !v.locallyRevoked(other) && !v.retired(other) {
			out = append(out, c.Digest)
		}
	}
	return out
}

// EdgeRevocationDirective is an offline administrator's instruction to revoke
// one trust edge. The caller has verified the signature, the signer's level
// and the signer's standing; this package decides only whether the directive
// concerns the local node.
type EdgeRevocationDirective struct {
	Credential CredentialDigest
	// A and B are the two ends of the credential.
	A, B ident.NodeID
}

// DirectiveDisposition is the answer for one directive.
type DirectiveDisposition int

const (
	// DirectiveApply: carry out Effects.
	DirectiveApply DirectiveDisposition = iota + 1
	// DirectiveAlreadyEffective: the credential is already revoked; the
	// directive is done and needs nothing further.
	DirectiveAlreadyEffective
	// DirectiveNotForThisNode: the edge does not involve the local node. Only
	// the two ends of a trust edge may act on its revocation; others merely
	// forward the directive.
	DirectiveNotForThisNode
	// DirectiveMismatch: the directive's claimed ends do not match the ends of
	// the credential it names, so it is not executed. Only the two parties of
	// a credential may revoke it. There are no effects.
	DirectiveMismatch
)

func (d DirectiveDisposition) String() string {
	switch d {
	case DirectiveApply:
		return "apply"
	case DirectiveAlreadyEffective:
		return "already-effective"
	case DirectiveNotForThisNode:
		return "not-for-this-node"
	case DirectiveMismatch:
		return "mismatch"
	}
	return "unknown"
}

// DirectiveEffects are the only things a directive can do: revoke an edge.
// There is deliberately nothing here that creates trust or loosens a
// restriction.
type DirectiveEffects struct {
	StopVouching      []CredentialDigest
	RevokeCredentials []CredentialDigest
}

// DirectiveVerdict is the answer of ApplyDirective.
type DirectiveVerdict struct {
	Disposition DirectiveDisposition
	Effects     DirectiveEffects
}

// ApplyDirective decides what a verified offline directive does on the local
// node. It is the one place that decides whether a directive concerns the
// local node's own trust; callers pass the directive as claimed and add no
// check of their own on its ends. When the credential is in the view, the
// directive's claimed ends must match the credential's ends, in either order.
// A credential not in the view cannot be checked, and the claimed ends decide;
// the effects then carry its digest only, and the payload needed to sign the
// revocation comes from the pairing initiator's record.
func ApplyDirective(v View, d EdgeRevocationDirective) DirectiveVerdict {
	if c, known := v.credential(d.Credential); known && !endsMatch(c, d.A, d.B) {
		return DirectiveVerdict{Disposition: DirectiveMismatch}
	}
	if d.A != v.Local && d.B != v.Local {
		return DirectiveVerdict{Disposition: DirectiveNotForThisNode}
	}
	if _, revoked := v.RevokedCredentials[d.Credential]; revoked {
		return DirectiveVerdict{Disposition: DirectiveAlreadyEffective}
	}
	return DirectiveVerdict{
		Disposition: DirectiveApply,
		Effects: DirectiveEffects{
			StopVouching:      []CredentialDigest{d.Credential},
			RevokeCredentials: []CredentialDigest{d.Credential},
		},
	}
}

// endsMatch reports whether the credential's two ends are a and b, in either
// order.
func endsMatch(c Credential, a, b ident.NodeID) bool {
	return (c.A == a && c.B == b) || (c.A == b && c.B == a)
}
