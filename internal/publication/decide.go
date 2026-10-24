package publication

import (
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// Verdict is the outcome of comparing an incoming declaration or reachability
// record with the highest counter already seen for the same node.
type Verdict int

const (
	// VerdictInvalid is the zero value: the comparison could not be made (the
	// incoming record has no node, counter or digest). Nothing is accepted.
	VerdictInvalid Verdict = iota
	// VerdictAccept: the counter is higher than any seen, or none has been seen.
	// The caller keeps this record and moves the state forward with the matching
	// SeenOf function.
	VerdictAccept
	// VerdictStale: the counter is lower than the highest seen, or this is the
	// very record seen at that counter. Nothing new.
	VerdictStale
	// VerdictConflict: the same counter carries different content (another
	// digest). The network key signed two records under one counter, which is a
	// sign of a copied key or of a node restored from an old copy. Keep what was
	// seen and tell the owner.
	VerdictConflict
	// VerdictWrongNode: the record is about another node than the state; the
	// caller looked up the wrong state. Nothing is accepted.
	VerdictWrongNode
	// VerdictExpired: the record would be accepted by its counter but has
	// expired. Only a reachability record can expire. Nothing is accepted and the
	// state does not move, unlike record.DecideSerial, which advances on an
	// expired higher certificate: see DecideReachability for the reason.
	VerdictExpired
)

func (v Verdict) String() string {
	switch v {
	case VerdictAccept:
		return "accept"
	case VerdictStale:
		return "stale"
	case VerdictConflict:
		return "conflict"
	case VerdictWrongNode:
		return "wrong-node"
	case VerdictExpired:
		return "expired"
	}
	return "invalid"
}

// counterState is what the comparison reads of a record or of the state kept
// for it.
type counterState struct {
	node    ident.NodeID
	counter uint64
	digest  record.Digest
}

// decideCounter is the one comparison both records use. seen is nil when no
// record of the kind has been seen for the node.
func decideCounter(seen *counterState, incoming counterState) Verdict {
	if incoming.counter < 1 || incoming.node.IsZero() || incoming.digest.IsZero() {
		return VerdictInvalid
	}
	if seen == nil {
		return VerdictAccept
	}
	switch {
	case seen.node != incoming.node:
		return VerdictWrongNode
	case incoming.counter > seen.counter:
		return VerdictAccept
	case incoming.counter < seen.counter:
		return VerdictStale
	case incoming.digest != seen.digest:
		return VerdictConflict
	}
	return VerdictStale
}

// DeclarationSeen is the highest declaration counter seen for one node, with the
// digest of the declaration seen at it. The caller keeps one per node and moves
// it forward with DeclarationSeenOf whenever DecideDeclaration answers accept.
type DeclarationSeen struct {
	Node    ident.NodeID
	Counter uint64
	Digest  record.Digest
}

// DeclarationSeenOf returns the state that records v as the highest declaration
// seen for its node.
func DeclarationSeenOf(v record.Verified[Declaration]) DeclarationSeen {
	return DeclarationSeen{Node: v.Payload().Node, Counter: v.Payload().Counter, Digest: v.Digest()}
}

// DecideDeclaration applies the rule "keep only the declaration with the highest
// counter". seen is the state for the incoming declaration's node, or nil if
// none has been seen. incoming has passed record.Verify, which the type says.
func DecideDeclaration(seen *DeclarationSeen, incoming record.Verified[Declaration]) Verdict {
	var s *counterState
	if seen != nil {
		s = &counterState{seen.Node, seen.Counter, seen.Digest}
	}
	p := incoming.Payload()
	return decideCounter(s, counterState{p.Node, p.Counter, incoming.Digest()})
}

// ReachabilitySeen is the highest reachability counter seen for one node, with
// the digest of the record seen at it. It is kept per node, apart from the
// declaration's state and apart from the record itself: it must outlive the
// record, so that an expired record's counter still stands and an older record
// cannot return.
type ReachabilitySeen struct {
	Node    ident.NodeID
	Counter uint64
	Digest  record.Digest
}

// ReachabilitySeenOf returns the state that records v as the highest
// reachability record seen for its node.
func ReachabilitySeenOf(v record.Verified[ReachabilityRecord]) ReachabilitySeen {
	return ReachabilitySeen{Node: v.Payload().Node(), Counter: v.Payload().Counter, Digest: v.Digest()}
}

// DecideReachability applies the rule "keep only the reachability record with the
// highest counter" and judges the record's expiry at now. The counter decides
// first, so a stale or conflicting record is reported as such whether or not it
// has expired; a record that the counter would accept but that has expired is
// VerdictExpired and moves nothing.
//
// The state does not advance on an expired record, although record.DecideSerial
// does advance on an expired higher certificate, because the two states guard
// different things. A higher serial is the identity key's statement that every
// lower certificate is void, whether or not the new one is still valid; it is a
// fact that does not age. A reachability record says only "reach me here until
// the expiry", and whether it has expired is judged by this peer's own clock: a
// peer whose clock runs ahead would otherwise raise the bar on a record that is
// still valid by real time, and then answer "stale" to that very record once its
// clock is corrected. An expired record carries no information, so it is
// dropped as if never received. What it costs: a lower-counter record that is
// still unexpired can be accepted after a higher one expired; that is a statement
// the node itself signed and it ends at its own expiry.
func DecideReachability(seen *ReachabilitySeen, incoming record.Verified[ReachabilityRecord], now time.Time) Verdict {
	var s *counterState
	if seen != nil {
		s = &counterState{seen.Node, seen.Counter, seen.Digest}
	}
	p := incoming.Payload()
	v := decideCounter(s, counterState{p.Node(), p.Counter, incoming.Digest()})
	if v == VerdictAccept && p.ExpiredAt(now) {
		return VerdictExpired
	}
	return v
}
