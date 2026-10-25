package engine

// CapacityReason says why the capacity check refused. The zero value is no
// refusal.
type CapacityReason uint8

// The reasons, in the order the check judges them.
const (
	CapacityOK CapacityReason = iota
	// CapacityClockRollback: the wall clock was set back and not cleared.
	CapacityClockRollback
	// CapacityNodeQueueFull: the node holds as many unfinished deliveries as it
	// may.
	CapacityNodeQueueFull
	// CapacityStorageFull: the free space is below the reserve.
	CapacityStorageFull
)

// String returns the name of the reason.
func (r CapacityReason) String() string {
	switch r {
	case CapacityOK:
		return "ok"
	case CapacityClockRollback:
		return "clock_rollback"
	case CapacityNodeQueueFull:
		return "node_queue_full"
	case CapacityStorageFull:
		return "storage_full"
	}
	return "invalid"
}

// Reason is the reason of the engine that a refusal of the check is reported
// as. CapacityOK has none.
func (r CapacityReason) Reason() Reason {
	switch r {
	case CapacityClockRollback:
		return ReasonClockRollback
	case CapacityNodeQueueFull:
		return ReasonNodeQueueFull
	case CapacityStorageFull:
		return ReasonStorageFull
	}
	return 0
}

// CapacityInput is everything the capacity check reads.
type CapacityInput struct {
	// RollbackPaused: the wall clock was set back beyond the tolerance and the
	// owner has not cleared it.
	RollbackPaused bool
	// Unfinished is how many deliveries the node holds that are not final.
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

// CapacityVerdict is the answer of the check: whether the new work is admitted,
// and if not why.
type CapacityVerdict struct {
	Admit  bool
	Reason CapacityReason
}

// CheckCapacity is the one capacity check, the judgment of whether the node
// takes in new work: a request to send, to reply or to control a session, from
// an endpoint on this node. It is made where the work enters the store, and
// nowhere else; what the node already holds is moved to its end whatever the
// answer, because a delivery that was accepted is never dropped for lack of room.
//
// The clock comes first because nothing that depends on a deadline can be
// accepted at all, then the queue, then the disk. A bound that is not set (the
// zero value) refuses: the settings of the node are validated to hold both, so a
// zero here is a defect, and a node that admits work without a bound fills its
// disk. It panics for new work that adds no delivery.
func CheckCapacity(in CapacityInput) CapacityVerdict {
	if in.Recipients < 1 {
		panic("engine: the capacity check of work that adds no delivery")
	}
	switch {
	case in.RollbackPaused:
		return CapacityVerdict{Reason: CapacityClockRollback}
	case in.NodeQueueLimit < 1 || in.Unfinished+in.Recipients > in.NodeQueueLimit:
		return CapacityVerdict{Reason: CapacityNodeQueueFull}
	case in.DiskReserve < 1 || in.Available < uint64(in.DiskReserve):
		return CapacityVerdict{Reason: CapacityStorageFull}
	}
	return CapacityVerdict{Admit: true}
}
