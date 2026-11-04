package adapterproto

import "time"

// The delivery timing an adapter reasons about. These are protocol constants,
// not node settings: the sender computes report_by from them and every target
// must reach the same value from the same envelope. They are defined here, the
// one module an adapter may import, and the node's own packages take their
// values from here, so a change to a value reaches both at once.

// ClockTolerance is the bound on clock skew between two nodes. Wherever a
// comparison across nodes must allow for clock skew, this value is used and no
// other, so two nodes never disagree about which side of a deadline a timestamp
// falls. Validity bounds of certificates, credentials and reachability proofs
// are compared without it: the tolerance only moves a window of days or hours
// by two minutes, and a failure there is explicit, not a silent disagreement.
const ClockTolerance = 2 * time.Minute

const (
	// DefaultAcceptBefore is the delivery deadline a sender uses when the
	// caller names none.
	DefaultAcceptBefore = 10 * time.Minute
	// MaxAcceptBefore is how far in the future a delivery deadline may lie.
	MaxAcceptBefore = time.Hour

	// MaxQueueCap is the protocol cap on any endpoint's longest queue time.
	MaxQueueCap = 2 * time.Hour

	// LeaseRenewalCap caps the total life of one lease, measured from the
	// moment it was issued, however often it is renewed.
	LeaseRenewalCap = 15 * time.Minute
	// MaxRedeliveries is how many times a message whose lease expired
	// unacknowledged is handed out again before its outcome is declared
	// unknown.
	MaxRedeliveries = 3
)

// MaxLeases is how many leases one message can receive: the first hand-out and
// its redeliveries.
const MaxLeases = 1 + MaxRedeliveries

// MaxRedeliveryPeriod is the longest time a message can spend under leases. It
// follows only from the lease life cap and the lease count: every one of the
// MaxLeases leases may live for LeaseRenewalCap.
const MaxRedeliveryPeriod = MaxLeases * LeaseRenewalCap

// ReportByOffset is the distance from accept_before to report_by: the longest
// queue time, then the longest time under leases, then the clock tolerance.
const ReportByOffset = MaxQueueCap + MaxRedeliveryPeriod + ClockTolerance

// SentKeyWindow is how long, counted from the moment an adapter starts a send
// that names no accept_before_s, the idempotency key of that send is certainly
// still held by the node. The node keeps a key until report_by, which is
// accept_before plus ReportByOffset, and accept_before is at least
// DefaultAcceptBefore after the moment the node accepts the send, which is no
// earlier than the adapter's start. An adapter that recovers after this window
// must not resend with the same key: the node may have forgotten it and would
// create a second message, and with it a second remote session.
const SentKeyWindow = DefaultAcceptBefore + ReportByOffset

// ReceivedRedeliveryWindow is how long, counted from the moment an adapter
// first receives a message, the node may still hand that message out to it
// again. A message is handed out until its report_by at the latest, which is its
// accept_before plus ReportByOffset; the sender may have chosen any accept_before
// up to MaxAcceptBefore, and ClockTolerance covers a sender clock that runs
// ahead of the node's. An adapter may forget the id of a message it processed
// once this window has passed.
const ReceivedRedeliveryWindow = MaxAcceptBefore + ReportByOffset + ClockTolerance
