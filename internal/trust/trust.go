// Package trust decides who a node trusts.
//
// Trust between two nodes is either direct (a mutual-trust credential signed by
// both) or inherited through exactly one intermediate node that the local node
// trusts directly and that trusts the other node directly. Inherited trust is
// never stored: it is a pure function of the credentials, revocations, the
// locally revoked set, the intermediate's online vouchers and the local
// settings, all collected in a View. See README.md for the rules.
package trust

import (
	"bytes"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/timing"
)

// Timing values shared by every node. They are protocol constants because the
// grace period floor is derived from them and two nodes must agree on it.
const (
	// VoucherIssuePeriod is how often a node issues a voucher for each of its
	// direct trust edges.
	VoucherIssuePeriod = 3 * time.Minute
	// VoucherPushBudget is the time allowed for one hop to deliver a voucher to
	// a direct trust party.
	VoucherPushBudget = 1 * time.Minute
	// MinGracePeriod is the shortest accepted grace period. A shorter one could
	// reject a voucher that is merely in flight or stamped by a slightly fast
	// clock, suspending healthy paths.
	MinGracePeriod = VoucherIssuePeriod + VoucherPushBudget + timing.ClockTolerance
	// DefaultGracePeriod is the grace period of a node that never changed it.
	DefaultGracePeriod = 10 * time.Minute
)

// ErrGracePeriodTooShort is returned for a grace period below MinGracePeriod.
var ErrGracePeriodTooShort = errors.New("trust: grace period is below the minimum")

// ValidateGracePeriod rejects grace periods below MinGracePeriod.
func ValidateGracePeriod(d time.Duration) error {
	if d < MinGracePeriod {
		return ErrGracePeriodTooShort
	}
	return nil
}

// Settings are the local node's own trust settings.
type Settings struct {
	// AcceptsInheritance says whether the local node accepts inherited trust.
	// It is published in the node's declaration, and the other side of every
	// inherited relationship reads it there.
	AcceptsInheritance bool
	// GracePeriod is how old an intermediate's voucher may be before the
	// inherited trust that rests on it is suspended. Callers obtain it through
	// ValidateGracePeriod; Evaluate does not re-check it.
	GracePeriod time.Duration
}

// DefaultSettings returns the settings of a node that changed nothing.
func DefaultSettings() Settings {
	return Settings{AcceptsInheritance: true, GracePeriod: DefaultGracePeriod}
}

// CredentialDigest identifies a mutual-trust credential. Revocations and
// vouchers refer to a credential by its digest, so a new pairing between the
// same two nodes is a different edge.
type CredentialDigest [32]byte

// String returns the digest in lowercase hex.
func (d CredentialDigest) String() string { return hex.EncodeToString(d[:]) }

func compareDigest(a, b CredentialDigest) int { return bytes.Compare(a[:], b[:]) }

func compareNode(a, b ident.NodeID) int { return bytes.Compare(a.Bytes(), b.Bytes()) }

// Credential is the part of a mutual-trust credential that trust decisions
// read. The caller has already verified both signatures.
type Credential struct {
	Digest CredentialDigest
	A, B   ident.NodeID
	// NotAfter is the end of the validity, exclusive. The zero time means the
	// credential does not expire.
	NotAfter time.Time
}

// other returns the other end of the credential, given one end.
func (c Credential) other(n ident.NodeID) (ident.NodeID, bool) {
	switch n {
	case c.A:
		return c.B, true
	case c.B:
		return c.A, true
	}
	return ident.NodeID{}, false
}

func (c Credential) expired(now time.Time) bool {
	return !c.NotAfter.IsZero() && !now.Before(c.NotAfter)
}

// Voucher is the part of an online voucher that trust decisions read. The
// caller has already verified the signature. A voucher carries no expiry: the
// verifier computes validity from IssuedAt, its own clock and its own grace
// period.
type Voucher struct {
	// Issuer is the node vouching to be online. It is one end of Edge.
	Issuer ident.NodeID
	// Edge is the digest of the credential the voucher is for.
	Edge     CredentialDigest
	IssuedAt time.Time
	// Counter increases with every voucher the issuer signs for the edge.
	Counter uint64
}

// VoucherKey is the slot a voucher occupies: only the latest voucher per
// issuer and edge is kept.
type VoucherKey struct {
	Issuer ident.NodeID
	Edge   CredentialDigest
}

// Vouchers holds the latest accepted voucher per issuer and edge.
type Vouchers map[VoucherKey]Voucher

// NodeSet is a set of nodes.
type NodeSet map[ident.NodeID]struct{}

// DigestSet is a set of credential digests.
type DigestSet map[CredentialDigest]struct{}

// View is everything one node knows that trust decisions read. The caller
// assembles it from records it has already verified and keeps it up to date;
// the package never retains it.
type View struct {
	// Local is the node the decisions are made for.
	Local    ident.NodeID
	Settings Settings
	// LocallyRevoked holds the nodes the local owner stopped trusting.
	LocallyRevoked NodeSet
	// Retired holds the nodes whose identity key published a verified
	// self-revocation statement. Such an identity is void for good: it is
	// never trusted, not even through a path, and no credential with it can
	// carry trust. Unlike LocallyRevoked, no owner decides it and nothing
	// clears it. An empty or nil set means no node is retired. The local
	// node's own entry is not read: the decisions are about the other nodes.
	Retired NodeSet
	// Credentials are the mutual-trust credentials within two hops of Local:
	// Local's own and those of Local's direct trust parties. Digests are
	// unique.
	Credentials []Credential
	// RevokedCredentials holds the digests of revoked credentials. Revocation
	// is permanent for a digest.
	RevokedCredentials DigestSet
	Vouchers           Vouchers
	// DeclaredAcceptsInheritance maps a node to the inheritance switch in its
	// latest declaration. A node without a known declaration counts as not
	// accepting, so that inheritance is never assumed.
	DeclaredAcceptsInheritance map[ident.NodeID]bool
}

// usable reports whether a credential can carry trust at now: not revoked, not
// expired, and between two distinct nodes.
func (v View) usable(c Credential, now time.Time) bool {
	return c.A != c.B && !v.credentialRevoked(c) && !c.expired(now)
}

func (v View) credentialRevoked(c Credential) bool {
	_, revoked := v.RevokedCredentials[c.Digest]
	return revoked
}

// credential returns the credential with the given digest, if the view has it.
func (v View) credential(d CredentialDigest) (Credential, bool) {
	for _, c := range v.Credentials {
		if c.Digest == d {
			return c, true
		}
	}
	return Credential{}, false
}

func (v View) locallyRevoked(n ident.NodeID) bool {
	_, revoked := v.LocallyRevoked[n]
	return revoked
}

func (v View) retired(n ident.NodeID) bool {
	_, retired := v.Retired[n]
	return retired
}

// credentialsOf returns the credentials in which n is an end, ordered by
// digest, so that every result derived from them is deterministic.
func (v View) credentialsOf(n ident.NodeID) []Credential {
	var out []Credential
	for _, c := range v.Credentials {
		if c.A != c.B && (c.A == n || c.B == n) {
			out = append(out, c)
		}
	}
	sortCredentials(out)
	return out
}

func sortCredentials(cs []Credential) {
	slices.SortFunc(cs, func(a, b Credential) int { return compareDigest(a.Digest, b.Digest) })
}

func sortDigests(ds []CredentialDigest) { slices.SortFunc(ds, compareDigest) }

func sortNodes(ns []ident.NodeID) { slices.SortFunc(ns, compareNode) }
