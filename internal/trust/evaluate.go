package trust

import (
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// Outcome is the class of a trust decision. The zero value is Denied, so an
// unset verdict never grants access.
type Outcome int

const (
	// Denied: no valid path to the target exists, or the local owner refuses
	// it. Nothing recovers it except a new credential or a changed setting.
	Denied Outcome = iota
	// AllowedDirect: the target is a direct trust party.
	AllowedDirect
	// AllowedInherited: the target is reached through one intermediate node.
	AllowedInherited
	// Suspended: a path exists and fails only because an intermediate's
	// voucher is missing or outside the grace window. It recovers by itself
	// when a fresh voucher arrives, which is why callers treat it differently
	// from Denied.
	Suspended
)

func (o Outcome) String() string {
	switch o {
	case Denied:
		return "denied"
	case AllowedDirect:
		return "allowed-direct"
	case AllowedInherited:
		return "allowed-inherited"
	case Suspended:
		return "suspended"
	}
	return "unknown"
}

// Allowed reports whether the target may be treated as trusted.
func (o Outcome) Allowed() bool { return o == AllowedDirect || o == AllowedInherited }

// Reason says why a verdict came out as it did.
type Reason int

const (
	// ReasonNone is the zero value; no verdict carries it.
	ReasonNone Reason = iota

	// Reasons of allowed verdicts.
	ReasonDirect
	ReasonInherited

	// Reasons of Suspended verdicts: the voucher of an intermediate for one
	// edge of the path is ...
	ReasonVoucherMissing // not known at all
	ReasonVoucherStale   // older than the grace period
	// ReasonVoucherFuture: stamped later than now plus the clock tolerance.
	// Vouchers.Accept rejects such a voucher on receipt, so this only happens
	// when the verifier's clock moved back after receipt; it resolves itself as
	// the clock advances.
	ReasonVoucherFuture

	// Reasons of Denied verdicts. ReasonSelf and ReasonLocallyRevoked end the
	// evaluation at once. The rest are the failures of candidate paths, listed
	// from least to most specific: when several paths fail for different
	// reasons the most specific one is reported, because it names what would
	// have to change for some path to work.
	ReasonSelf                       // the target is the local node
	ReasonLocallyRevoked             // the target is in the locally revoked set
	ReasonNoPath                     // no credential path to the target at all
	ReasonCredentialRevoked          // every candidate path has a revoked credential
	ReasonCredentialExpired          // ... or an expired one
	ReasonIntermediateLocallyRevoked // the intermediate is in the locally revoked set
	ReasonInheritanceDisabled        // the local node or the target refuses inheritance
	ReasonDeclarationUnknown         // the target's declaration is not known, so its switch is unknown
)

var reasonNames = map[Reason]string{
	ReasonNone:                       "none",
	ReasonDirect:                     "direct",
	ReasonInherited:                  "inherited",
	ReasonVoucherMissing:             "voucher-missing",
	ReasonVoucherStale:               "voucher-stale",
	ReasonVoucherFuture:              "voucher-future",
	ReasonSelf:                       "self",
	ReasonLocallyRevoked:             "locally-revoked",
	ReasonNoPath:                     "no-path",
	ReasonCredentialRevoked:          "credential-revoked",
	ReasonCredentialExpired:          "credential-expired",
	ReasonIntermediateLocallyRevoked: "intermediate-locally-revoked",
	ReasonInheritanceDisabled:        "inheritance-disabled",
	ReasonDeclarationUnknown:         "declaration-unknown",
}

func (r Reason) String() string {
	if s, ok := reasonNames[r]; ok {
		return s
	}
	return "unknown"
}

// denialRank orders the reasons of failed candidate paths; larger is more
// specific. Only the reasons a single candidate path can fail with appear.
func denialRank(r Reason) int {
	switch r {
	case ReasonCredentialRevoked:
		return 1
	case ReasonCredentialExpired:
		return 2
	case ReasonIntermediateLocallyRevoked:
		return 3
	case ReasonInheritanceDisabled, ReasonDeclarationUnknown:
		return 4
	}
	return 0
}

// Path is one chain of credentials by which the local node trusts a node.
type Path struct {
	// Via is the intermediate node, or the zero NodeID for direct trust.
	Via ident.NodeID
	// To is the node reached.
	To ident.NodeID
	// Credentials are the digests the trust rests on, local node first: one
	// for direct trust, two (local-Via, Via-To) for inherited trust. A path
	// never has more than two.
	Credentials []CredentialDigest
}

// Verdict is the answer of Evaluate.
type Verdict struct {
	Outcome Outcome
	Reason  Reason
	// Paths is the source of the trust. For AllowedDirect it holds the valid
	// direct credentials, for AllowedInherited every valid path, and for
	// Suspended every path that only lacks a fresh voucher. It is empty for
	// Denied and ordered deterministically.
	Paths []Path
}

// Evaluate decides whether the local node trusts target at now.
//
// Direct trust needs a usable credential and never vouchers. Inherited trust
// needs, for at least one intermediate M: usable credentials local-M and
// M-target, M not locally revoked, inheritance accepted by the local settings
// and by the target's declaration, and a voucher from M for each of the two
// edges that is neither too old nor stamped ahead of the clock tolerance. A target in the
// locally revoked set is refused whatever the credentials say.
func Evaluate(v View, target ident.NodeID, now time.Time) Verdict {
	if target == v.Local {
		return Verdict{Outcome: Denied, Reason: ReasonSelf}
	}
	if v.locallyRevoked(target) {
		return Verdict{Outcome: Denied, Reason: ReasonLocallyRevoked}
	}

	denial := ReasonNoPath
	deny := func(r Reason) {
		if denialRank(r) > denialRank(denial) {
			denial = r
		}
	}

	local := v.credentialsOf(v.Local)

	var direct []Path
	for _, c := range local {
		if other, _ := c.other(v.Local); other != target {
			continue
		}
		if r := v.credentialFault(c, now); r != ReasonNone {
			deny(r)
			continue
		}
		direct = append(direct, Path{To: target, Credentials: []CredentialDigest{c.Digest}})
	}
	if len(direct) > 0 {
		return Verdict{Outcome: AllowedDirect, Reason: ReasonDirect, Paths: direct}
	}

	var inherited, suspended []Path
	suspension := ReasonNone
	for _, first := range local {
		via, _ := first.other(v.Local)
		if via == target {
			continue
		}
		for _, second := range v.credentialsOf(via) {
			if end, _ := second.other(via); end != target {
				continue
			}
			path := Path{Via: via, To: target, Credentials: []CredentialDigest{first.Digest, second.Digest}}
			r := v.inheritedFault(first, second, via, target, now)
			switch r {
			case ReasonNone:
				inherited = append(inherited, path)
			case ReasonVoucherMissing, ReasonVoucherStale, ReasonVoucherFuture:
				suspended = append(suspended, path)
				if suspension == ReasonNone {
					suspension = r
				}
			default:
				deny(r)
			}
		}
	}
	if len(inherited) > 0 {
		sortPaths(inherited)
		return Verdict{Outcome: AllowedInherited, Reason: ReasonInherited, Paths: inherited}
	}
	if len(suspended) > 0 {
		sortPaths(suspended)
		return Verdict{Outcome: Suspended, Reason: suspension, Paths: suspended}
	}
	return Verdict{Outcome: Denied, Reason: denial}
}

// credentialFault returns why a credential cannot carry trust, or ReasonNone.
func (v View) credentialFault(c Credential, now time.Time) Reason {
	switch {
	case v.credentialRevoked(c):
		return ReasonCredentialRevoked
	case c.expired(now):
		return ReasonCredentialExpired
	}
	return ReasonNone
}

// inheritedFault returns why the path local-via-target cannot carry inherited
// trust, or ReasonNone. The checks run from permanent to recoverable, so a
// voucher reason is returned only when nothing else is wrong with the path.
func (v View) inheritedFault(first, second Credential, via, target ident.NodeID, now time.Time) Reason {
	for _, c := range []Credential{first, second} {
		if r := v.credentialFault(c, now); r != ReasonNone {
			return r
		}
	}
	if v.locallyRevoked(via) {
		return ReasonIntermediateLocallyRevoked
	}
	if !v.Settings.AcceptsInheritance {
		return ReasonInheritanceDisabled
	}
	accepts, declared := v.DeclaredAcceptsInheritance[target]
	switch {
	case !declared:
		return ReasonDeclarationUnknown
	case !accepts:
		return ReasonInheritanceDisabled
	}
	for _, c := range []Credential{first, second} {
		if r := v.voucherFault(via, c.Digest, now); r != ReasonNone {
			return r
		}
	}
	return ReasonNone
}

// voucherFault returns why the latest voucher of issuer for edge does not
// count at now, or ReasonNone. The window is computed here by the verifier from
// its own clock and grace period; the voucher carries no expiry.
func (v View) voucherFault(issuer ident.NodeID, edge CredentialDigest, now time.Time) Reason {
	vo, ok := v.Vouchers[VoucherKey{Issuer: issuer, Edge: edge}]
	switch {
	case !ok:
		return ReasonVoucherMissing
	case vo.IssuedAt.After(now.Add(ClockTolerance)):
		// Vouchers.Accept rejects these on receipt, so this only happens when
		// the verifier's clock moved back since.
		return ReasonVoucherFuture
	case now.Sub(vo.IssuedAt) > v.Settings.GracePeriod:
		return ReasonVoucherStale
	}
	return ReasonNone
}

func sortPaths(ps []Path) {
	slices.SortFunc(ps, func(a, b Path) int {
		if c := compareNode(a.Via, b.Via); c != 0 {
			return c
		}
		for i := 0; i < len(a.Credentials) && i < len(b.Credentials); i++ {
			if c := compareDigest(a.Credentials[i], b.Credentials[i]); c != 0 {
				return c
			}
		}
		return len(a.Credentials) - len(b.Credentials)
	})
}
