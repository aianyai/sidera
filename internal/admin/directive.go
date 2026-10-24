package admin

import (
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/timing"
)

// DirectiveKind is what an offline administrative directive asks for.
type DirectiveKind int

const (
	// DirectiveUnknown is the zero value, accepted for nothing.
	DirectiveUnknown DirectiveKind = iota
	// DirectiveRevokeTrustEdge asks the node to revoke one of its own trust
	// edges. It is the only kind a node ever executes.
	DirectiveRevokeTrustEdge
)

// Directive holds the fields of a signed offline directive that acceptance
// reads. The caller has already verified the signature and the signer's
// certificate chain; this package only judges what the verified facts entitle
// the signer to: the recipient, the kind and the signer's standing.
//
// Which trust credential the directive revokes, and whether that credential's
// edge involves this node, is not decided here: the directive names a
// credential digest and the trust package's directive function decides.
type Directive struct {
	Kind DirectiveKind
	// Node is the node the directive is addressed to: the node on whose list the
	// signer holds a level.
	Node ident.NodeID
	// SignedAt is the signing time claimed by the signer. The zero time is
	// rejected.
	SignedAt time.Time
	// Signer is the person who signed, with the facts about their certificate.
	Signer ident.PersonID
	Cert   DeviceCert
}

// Disposition is what a node does with a directive.
type Disposition int

const (
	// DispositionReject means the directive is discarded.
	DispositionReject Disposition = iota
	// DispositionExecute means the node revokes the edge.
	DispositionExecute
	// DispositionWarn means the directive comes from someone with no
	// administrative authority over this node. It is shown to the owner as a
	// suggestion and never executed.
	DispositionWarn
)

// DirectiveVerdict is the result of AcceptDirective.
type DirectiveVerdict struct {
	Disposition Disposition
	Reason      Reason
}

// AcceptDirective decides whether this node (local) executes an offline
// directive when it comes back online, at now. offlineSince is the start of the
// earliest offline period that is not yet synchronised with every directly
// trusted party, as this node itself recorded it; it is a parameter, not a
// field of Directive, because it is the node's own fact and not something the
// signer claims. It stays unchanged when the issuance gate lets issuance
// through while some parties are unreachable, so a directive signed during the
// first period and received only after a second one is still inside the window.
// The zero time is a caller bug and is rejected.
//
// Checks run in this order and the first failure decides: the signing time is
// present, not in the future and not before the offline window; the directive
// is addressed to this node; its kind is one a node executes; and only then the
// signer's standing. Structure comes first so that a malformed directive is
// rejected whoever signed it, and only a well-formed revoke directive from a
// signer without authority is worth showing to the owner as a warning.
//
// The signing time must not precede offlineSince by more than the clock
// tolerance. A directive is meant to cover the time the node could not be
// reached, so a signing time before that window is a backdated claim, for
// example an expired temporary key pretending to have signed while it was still
// valid. A stolen key that is unexpired and unrevoked can still sign during the
// window; that residual risk is accepted.
//
// The signer's authority is judged against the list as it stands now: the
// signer must be on it, unexpired, with at least the level the catalogue
// requires for issuing a directive (OpIssueOfflineDirective) after the
// certificate cap, so a removal or downgrade made between signing and execution
// takes effect. The certificate's validity window is judged at the signing time
// instead: the node may have been offline longer than a short-lived key lives,
// and a key that was valid when it signed did not forge anything by expiring
// later. Revocations count whenever they were issued, because a compromised key
// can claim any signing time.
//
// A signer who is not an administrator of this node (not listed, entry expired,
// level too low) yields DispositionWarn for a structurally valid directive: it
// is only a suggestion to show the owner. A revoked, unchecked or out-of-window
// certificate, or a directive for another node or of a kind other than revoking
// a trust edge, is rejected. A directive can only withdraw trust, so nothing a
// signer asks for can let a node in.
//
// DispositionExecute means this package's rules are met, not that the edge is
// revoked: the caller must then run the trust package's applicability check,
// which resolves the credential digest the directive names and decides whether
// it belongs to an edge of this node.
func AcceptDirective(local ident.NodeID, list List, d Directive, offlineSince, now time.Time) DirectiveVerdict {
	reject := func(r Reason) DirectiveVerdict { return DirectiveVerdict{Disposition: DispositionReject, Reason: r} }

	if offlineSince.IsZero() {
		return reject(ReasonOfflineSinceMissing)
	}
	switch {
	case d.SignedAt.IsZero():
		return reject(ReasonDirectiveSignedAtMissing)
	case d.SignedAt.After(now.Add(timing.ClockTolerance)):
		return reject(ReasonDirectiveSignedInFuture)
	case d.SignedAt.Before(offlineSince.Add(-timing.ClockTolerance)):
		return reject(ReasonDirectiveSignedBeforeOffline)
	}
	if local.IsZero() || d.Node != local {
		return reject(ReasonDirectiveNotForThisNode)
	}
	if d.Kind != DirectiveRevokeTrustEdge {
		return reject(ReasonDirectiveKindNotAllowed)
	}

	need := directiveLevel()
	if need == LevelNone {
		return reject(ReasonLocalOwnerOnly)
	}
	s := standing(list, d.Signer, d.Cert, now, d.SignedAt)
	switch s.Reason {
	case ReasonOK:
	case ReasonNotOnList, ReasonEntryExpired, ReasonDeviceCapTooLow:
		return DirectiveVerdict{Disposition: DispositionWarn, Reason: s.Reason}
	default:
		return reject(s.Reason)
	}
	if s.Level < need {
		reason := ReasonListLevelTooLow
		if s.ListLevel >= need {
			reason = ReasonDeviceCapTooLow
		}
		return DirectiveVerdict{Disposition: DispositionWarn, Reason: reason}
	}
	return DirectiveVerdict{Disposition: DispositionExecute, Reason: ReasonOK}
}

// directiveLevel is the list level the catalogue requires of a directive's
// signer, or LevelNone if no list level qualifies.
func directiveLevel() Level {
	req, _ := RequirementOf(Operation{Kind: OpIssueOfflineDirective})
	return req.level()
}
