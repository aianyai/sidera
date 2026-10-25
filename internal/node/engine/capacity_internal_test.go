package engine

import "testing"

// The one capacity check: a retired identity first, then a clock set back, then
// the node's queue, then the free space. Each row is named after the rule it
// checks.
func TestCheckCapacityTable(t *testing.T) {
	const reserve = 256 << 20
	ok := capacityInput{Unfinished: 10, Recipients: 1, NodeQueueLimit: 100, Available: 10 * reserve, DiskReserve: reserve}
	with := func(f func(*capacityInput)) capacityInput {
		in := ok
		f(&in)
		return in
	}
	for _, tc := range []struct {
		name string
		in   capacityInput
		want capacityVerdict
	}{
		{"room in the queue and on the disk admits", ok, capacityVerdict{Admit: true}},
		{"a queue filled up to the limit exactly admits", with(func(i *capacityInput) { i.Unfinished = 99 }), capacityVerdict{Admit: true}},
		{"one delivery over the limit is refused", with(func(i *capacityInput) { i.Unfinished = 100 }), capacityVerdict{Reason: capacityNodeQueueFull}},
		{"every recipient counts", with(func(i *capacityInput) { i.Unfinished, i.Recipients = 95, 6 }), capacityVerdict{Reason: capacityNodeQueueFull}},
		{"the recipients that fit exactly admit", with(func(i *capacityInput) { i.Unfinished, i.Recipients = 95, 5 }), capacityVerdict{Admit: true}},
		{"free space equal to the reserve admits", with(func(i *capacityInput) { i.Available = reserve }), capacityVerdict{Admit: true}},
		{"free space one byte under the reserve is refused", with(func(i *capacityInput) { i.Available = reserve - 1 }), capacityVerdict{Reason: capacityStorageFull}},
		{"a retired identity is refused", with(func(i *capacityInput) { i.Retired = true }), capacityVerdict{Reason: capacityIdentityRetired}},
		{"the identity is judged before the clock", with(func(i *capacityInput) { i.Retired, i.RollbackPaused, i.Unfinished = true, true, 1000 }), capacityVerdict{Reason: capacityIdentityRetired}},
		{"a clock set back is refused", with(func(i *capacityInput) { i.RollbackPaused = true }), capacityVerdict{Reason: capacityClockRollback}},
		{"the clock is judged before the queue", with(func(i *capacityInput) { i.RollbackPaused, i.Unfinished = true, 1000 }), capacityVerdict{Reason: capacityClockRollback}},
		{"the queue is judged before the disk", with(func(i *capacityInput) { i.Unfinished, i.Available = 1000, 0 }), capacityVerdict{Reason: capacityNodeQueueFull}},
		{"a queue limit that is not set refuses everything", with(func(i *capacityInput) { i.NodeQueueLimit = 0 }), capacityVerdict{Reason: capacityNodeQueueFull}},
		{"a reserve that is not set refuses everything", with(func(i *capacityInput) { i.DiskReserve = 0 }), capacityVerdict{Reason: capacityStorageFull}},
		{"a negative reserve refuses everything", with(func(i *capacityInput) { i.DiskReserve = -1 }), capacityVerdict{Reason: capacityStorageFull}},
		{"an empty volume is under any reserve", with(func(i *capacityInput) { i.Available = 0 }), capacityVerdict{Reason: capacityStorageFull}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkCapacity(tc.in); got != tc.want {
				t.Errorf("checkCapacity(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// Work that names no recipient is a mistake of the caller and fails loudly.
func TestCheckCapacityPanicsForWorkWithoutRecipients(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("checkCapacity of no recipients did not panic")
		}
	}()
	checkCapacity(capacityInput{Recipients: 0, NodeQueueLimit: 1, Available: 1 << 40, DiskReserve: 1})
}

func TestCapacityReasonsMapToTheEngineReasons(t *testing.T) {
	for _, tc := range []struct {
		in   capacityReason
		name string
		want Reason
	}{
		{capacityIdentityRetired, "identity_retired", ReasonIdentityRetired},
		{capacityClockRollback, "clock_rollback", ReasonClockRollback},
		{capacityNodeQueueFull, "node_queue_full", ReasonNodeQueueFull},
		{capacityStorageFull, "storage_full", ReasonStorageFull},
		{capacityOK, "ok", 0},
	} {
		if tc.in.String() != tc.name || tc.in.Reason() != tc.want {
			t.Errorf("%v: name %q reason %v, want %q and %v", int(tc.in), tc.in.String(), tc.in.Reason(), tc.name, tc.want)
		}
	}
}
