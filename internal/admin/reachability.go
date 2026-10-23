package admin

import (
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// ReachabilityProof is the verified content of a proof that an administrator
// of Node may query the reachability records of that node. The caller has
// already verified the signature; this package only judges the facts.
//
// A proof grants no management right: it allows reading reachability records
// and nothing else, and it says nothing about the holder's level.
type ReachabilityProof struct {
	// Node is the node whose reachability records the proof opens.
	Node ident.NodeID
	// Person is the person the proof was issued to.
	Person ident.PersonID
	// NotAfter is when the proof stops being valid, at that instant.
	NotAfter time.Time
}

// ProofVerdict is the result of the reachability-proof checks.
type ProofVerdict struct {
	Allowed bool
	Reason  Reason
}

// MayIssueReachabilityProof decides whether this node may issue a proof to
// person that is valid until notAfter. The person must be on the list with an
// unexpired entry, at any level, and the proof must not outlive the entry:
// notAfter may equal the entry's expiry but not exceed it, so a proof never
// survives the removal that the expiry stands for. An entry without an expiry
// bounds nothing.
func MayIssueReachabilityProof(list List, person ident.PersonID, notAfter, now time.Time) ProofVerdict {
	entry, ok := list.Lookup(person)
	switch {
	case !ok:
		return ProofVerdict{Reason: ReasonNotOnList}
	case entry.expiredAt(now):
		return ProofVerdict{Reason: ReasonEntryExpired}
	case !entry.ExpiresAt.IsZero() && notAfter.After(entry.ExpiresAt):
		return ProofVerdict{Reason: ReasonProofOutlivesEntry}
	default:
		return ProofVerdict{Allowed: true, Reason: ReasonOK}
	}
}

// CheckReachabilityProof decides whether p lets requester query the
// reachability records of node at now. The proof must name exactly this node
// and this requester, and is valid up to but excluding NotAfter. An unset node
// or requester never matches.
func CheckReachabilityProof(p ReachabilityProof, node ident.NodeID, requester ident.PersonID, now time.Time) ProofVerdict {
	switch {
	case node.IsZero() || p.Node != node:
		return ProofVerdict{Reason: ReasonProofNodeMismatch}
	case requester.IsZero() || p.Person != requester:
		return ProofVerdict{Reason: ReasonProofPersonMismatch}
	case !now.Before(p.NotAfter):
		return ProofVerdict{Reason: ReasonProofExpired}
	default:
		return ProofVerdict{Allowed: true, Reason: ReasonOK}
	}
}
