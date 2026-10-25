package engine

// capacityReason says why the capacity check refused. The zero value is no
// refusal.
type capacityReason uint8

// The reasons, in the order the check judges them.
const (
	capacityOK capacityReason = iota
	// capacityIdentityRetired: the owner retired the identity of the node.
	capacityIdentityRetired
	// capacityClockRollback: the wall clock was set back and not cleared.
	capacityClockRollback
	// capacityNodeQueueFull: the node holds as many unfinished deliveries as it
	// may.
	capacityNodeQueueFull
	// capacityStorageFull: the free space is below the reserve.
	capacityStorageFull
)

// String returns the name of the reason.
func (r capacityReason) String() string {
	switch r {
	case capacityOK:
		return "ok"
	case capacityIdentityRetired:
		return "identity_retired"
	case capacityClockRollback:
		return "clock_rollback"
	case capacityNodeQueueFull:
		return "node_queue_full"
	case capacityStorageFull:
		return "storage_full"
	}
	return "invalid"
}

// Reason is the reason of the engine that a refusal of the check is reported
// as. capacityOK has none.
func (r capacityReason) Reason() Reason {
	switch r {
	case capacityIdentityRetired:
		return ReasonIdentityRetired
	case capacityClockRollback:
		return ReasonClockRollback
	case capacityNodeQueueFull:
		return ReasonNodeQueueFull
	case capacityStorageFull:
		return ReasonStorageFull
	}
	return 0
}

// capacityInput is everything the capacity check reads.
type capacityInput struct {
	// Retired: the owner retired the identity of the node, which will not start
	// again, so nothing it took in now would ever be sent.
	Retired bool
	// RollbackPaused: the wall clock was set back beyond the tolerance and the
	// owner has not cleared it.
	RollbackPaused bool
	// Unfinished is how many deliveries the node holds that still have work behind
	// them: messages waiting to be handed out, accepted control signals and sent
	// messages that no receipt answered.
	Unfinished int
	// Recipients is how many deliveries the new work adds; at least one.
	Recipients int
	// NodeQueueLimit is the most unfinished deliveries the node holds.
	NodeQueueLimit int
	// Available is the free space of the volume, in bytes.
	Available uint64
	// DiskReserve is the free space below which new work is refused.
	DiskReserve int64
}

// capacityVerdict is the answer of the check: whether the new work is admitted,
// and if not why.
type capacityVerdict struct {
	Admit  bool
	Reason capacityReason
}

// checkCapacity is the one capacity check, the judgment of whether the node
// takes in new work: a request to send, to reply or to control a session, from
// an endpoint on this node. It is made where the work enters the store, and
// nowhere else; what the node already holds is moved to its end whatever the
// answer, because a delivery that was accepted is never dropped for lack of room.
//
// The identity comes first, because a retired node never starts again and an
// acknowledgement of new work would be a promise nothing keeps; then the clock,
// because nothing that depends on a deadline can be accepted at all; then the
// queue, then the disk. A bound that is not set (the
// zero value) refuses: with no room in the queue every delivery is over it, and
// a reserve that is not set is refused by name, because the settings of the node
// are validated to hold both, so a zero here is a defect, and a node that admits
// work without a bound fills its disk. It panics for new work that adds no
// delivery.
func checkCapacity(in capacityInput) capacityVerdict {
	if in.Recipients < 1 {
		panic("engine: the capacity check of work that adds no delivery")
	}
	switch SignRefusalOf(in.Retired, in.RollbackPaused) {
	case ReasonIdentityRetired:
		return capacityVerdict{Reason: capacityIdentityRetired}
	case ReasonClockRollback:
		return capacityVerdict{Reason: capacityClockRollback}
	}
	switch {
	case in.Unfinished+in.Recipients > in.NodeQueueLimit:
		return capacityVerdict{Reason: capacityNodeQueueFull}
	case in.DiskReserve < 1 || in.Available < uint64(in.DiskReserve):
		return capacityVerdict{Reason: capacityStorageFull}
	}
	return capacityVerdict{Admit: true}
}
