package trust

import "time"

// VoucherReason says why a voucher was accepted or rejected.
type VoucherReason int

const (
	// VoucherAccepted: the voucher is newer than the stored one, or none was
	// stored.
	VoucherAccepted VoucherReason = iota + 1
	// VoucherCounterNotAdvancing: the counter is not greater than the stored
	// one for the same issuer and edge. Accepting it would let a replayed old
	// voucher, with its old and therefore still-recent-looking timestamp,
	// refresh trust that the issuer stopped vouching for.
	VoucherCounterNotAdvancing
	// VoucherFromFuture: the voucher is stamped later than the receiver's
	// clock plus ClockTolerance. An issuer whose clock runs fast must not have
	// its last voucher become valid later, once the receiver's clock reaches
	// the stamp: that would bring an intermediate that went offline back to
	// life for about one grace period.
	VoucherFromFuture
)

func (r VoucherReason) String() string {
	switch r {
	case VoucherAccepted:
		return "accepted"
	case VoucherCounterNotAdvancing:
		return "counter-not-advancing"
	case VoucherFromFuture:
		return "from-future"
	}
	return "unknown"
}

// VoucherDecision is the answer of Vouchers.Accept.
type VoucherDecision struct {
	Accepted bool
	Reason   VoucherReason
}

// Accept stores v if it is not stamped beyond now plus ClockTolerance and its
// counter is greater than the stored one for its issuer and edge, replacing
// it, so only the latest voucher is kept. The map must not be nil. now is the
// moment of receipt. Whether an accepted voucher is still valid is not decided
// here: validity depends on the moment of use and is computed by Evaluate.
//
// A rejected voucher leaves no trace: neither the slot nor the counter
// changes. That is fine because vouchers come one hop over an authenticated
// channel and an issuer that returns sends fresh ones.
//
// The caller has verified the signature and that the issuer is an end of the
// edge's credential.
func (s Vouchers) Accept(v Voucher, now time.Time) VoucherDecision {
	if v.IssuedAt.After(now.Add(ClockTolerance)) {
		return VoucherDecision{Accepted: false, Reason: VoucherFromFuture}
	}
	key := VoucherKey{Issuer: v.Issuer, Edge: v.Edge}
	if stored, ok := s[key]; ok && v.Counter <= stored.Counter {
		return VoucherDecision{Accepted: false, Reason: VoucherCounterNotAdvancing}
	}
	s[key] = v
	return VoucherDecision{Accepted: true, Reason: VoucherAccepted}
}
