package publication

import (
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// Verdict is the outcome of comparing an incoming declaration or reachability
// record with the highest (certificate serial, counter) already seen for the
// same node, compared in that order: a record signed under a higher certificate
// serial supersedes whatever its counter, and counters are compared only within
// one serial.
type Verdict int

const (
	// VerdictInvalid is the zero value: the comparison could not be made (the
	// incoming record has no node, counter or digest). Nothing is accepted.
	VerdictInvalid Verdict = iota
	// VerdictAccept: the certificate serial is higher than any seen, or the serial
	// is the same and the counter higher, or none has been seen. The caller keeps
	// this record and moves the state forward with the matching SeenOf function.
	VerdictAccept
	// VerdictStale: the certificate serial is lower than the highest seen, or the
	// serial is the same and the counter lower, or this is the very record seen
	// at that serial and counter. Nothing new.
	VerdictStale
	// VerdictConflict: the same serial and counter carry different content
	// (another digest). The network key signed two records under one counter,
	// which is a sign of a copied key or of a node restored from an old copy.
	// Keep what was seen and tell the owner.
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
	node ident.NodeID
	// serial is the serial of the network-key certificate the record was
	// verified under. Counters are only comparable within one serial: a key that
	// leaked before a rotation can sign any counter, MaxUint64 included, and
	// must not freeze the records of the certificate that replaced it.
	serial  uint64
	counter uint64
	digest  record.Digest
}

// decideCounter is the one comparison both records use. seen is nil when no
// record of the kind has been seen for the node.
func decideCounter(seen *counterState, incoming counterState) Verdict {
	if incoming.serial < 1 || incoming.counter < 1 || incoming.node.IsZero() || incoming.digest.IsZero() {
		return VerdictInvalid
	}
	if seen == nil {
		return VerdictAccept
	}
	switch {
	case seen.node != incoming.node:
		return VerdictWrongNode
	case incoming.serial > seen.serial:
		return VerdictAccept
	case incoming.serial < seen.serial:
		return VerdictStale
	case incoming.counter > seen.counter:
		return VerdictAccept
	case incoming.counter < seen.counter:
		return VerdictStale
	case incoming.digest != seen.digest:
		return VerdictConflict
	}
	return VerdictStale
}

// DeclarationSeen is the highest declaration seen for one node: the serial of
// the network-key certificate it was verified under, its counter and its
// digest. The caller keeps one per node and moves it forward with
// DeclarationSeenOf whenever DecideDeclaration answers accept.
type DeclarationSeen struct {
	Node ident.NodeID
	// Serial is the serial of the certificate the declaration was verified under.
	Serial  uint64
	Counter uint64
	Digest  record.Digest
}

// DeclarationSeenOf returns the state that records v as the highest declaration
// seen for its node. certSerial is the serial of the network-key certificate v
// was verified under (the accepted certificate of its descriptor): a
// declaration does not carry it, since the signing key is what the certificate
// names.
func DeclarationSeenOf(v record.Verified[Declaration], certSerial uint64) DeclarationSeen {
	return DeclarationSeen{Node: v.Payload().Node, Serial: certSerial, Counter: v.Payload().Counter, Digest: v.Digest()}
}

// DecideDeclaration applies the rule "keep only the declaration with the highest
// (certificate serial, counter)", compared in that order. seen is the state for
// the incoming declaration's node, or nil if none has been seen. incoming has
// passed record.Verify, which the type says, under the accepted certificate of
// serial certSerial.
func DecideDeclaration(seen *DeclarationSeen, incoming record.Verified[Declaration], certSerial uint64) Verdict {
	var s *counterState
	if seen != nil {
		s = &counterState{seen.Node, seen.Serial, seen.Counter, seen.Digest}
	}
	p := incoming.Payload()
	return decideCounter(s, counterState{p.Node, certSerial, p.Counter, incoming.Digest()})
}

// ReachabilitySeen is the highest reachability record seen for one node: the
// serial of the certificate it embeds (the one it was verified under), its
// counter and its digest. It is kept per node, apart from the declaration's
// state and apart from the record itself: it must outlive the record, so that an
// expired record's counter still stands and an older record cannot return.
type ReachabilitySeen struct {
	Node ident.NodeID
	// Serial is the serial of the certificate the record embeds.
	Serial  uint64
	Counter uint64
	Digest  record.Digest
}

// ReachabilitySeenOf returns the state that records v as the highest
// reachability record seen for its node.
func ReachabilitySeenOf(v record.Verified[ReachabilityRecord]) ReachabilitySeen {
	return ReachabilitySeen{Node: v.Payload().Node(), Serial: embeddedSerial(v.Payload()), Counter: v.Payload().Counter, Digest: v.Digest()}
}

// embeddedSerial is the serial of the certificate a reachability record embeds.
// The record is verified under exactly that certificate (its signer slot requires
// the embedded key, and every verifier builds the accepted key from the embedded
// certificate), so no serial needs to come from the caller.
func embeddedSerial(r ReachabilityRecord) uint64 { return r.certificate.Payload().Serial }

// DecideReachability applies the rule "keep only the reachability record with the
// highest (certificate serial, counter)" and judges the record's expiry at now.
// The serial and counter decide first, so a stale or conflicting record is
// reported as such whether or not it has expired; a record that they would
// accept but that has expired is VerdictExpired and moves nothing.
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
		s = &counterState{seen.Node, seen.Serial, seen.Counter, seen.Digest}
	}
	p := incoming.Payload()
	v := decideCounter(s, counterState{p.Node(), embeddedSerial(p), p.Counter, incoming.Digest()})
	if v == VerdictAccept && p.ExpiredAt(now) {
		return VerdictExpired
	}
	return v
}
