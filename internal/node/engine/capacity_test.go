package engine_test

import (
	"context"
	"encoding/json"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
)

// The one capacity check: a clock set back first, then the node's queue, then
// the free space. Each row is named after the rule it checks.
func TestCheckCapacityTable(t *testing.T) {
	const reserve = 256 << 20
	ok := engine.CapacityInput{Unfinished: 10, Recipients: 1, NodeQueueLimit: 100, Available: 10 * reserve, DiskReserve: reserve}
	with := func(f func(*engine.CapacityInput)) engine.CapacityInput {
		in := ok
		f(&in)
		return in
	}
	for _, tc := range []struct {
		name string
		in   engine.CapacityInput
		want engine.CapacityVerdict
	}{
		{"room in the queue and on the disk admits", ok, engine.CapacityVerdict{Admit: true}},
		{"a queue filled up to the limit exactly admits", with(func(i *engine.CapacityInput) { i.Unfinished = 99 }), engine.CapacityVerdict{Admit: true}},
		{"one delivery over the limit is refused", with(func(i *engine.CapacityInput) { i.Unfinished = 100 }), engine.CapacityVerdict{Reason: engine.CapacityNodeQueueFull}},
		{"every recipient counts", with(func(i *engine.CapacityInput) { i.Unfinished, i.Recipients = 95, 6 }), engine.CapacityVerdict{Reason: engine.CapacityNodeQueueFull}},
		{"the recipients that fit exactly admit", with(func(i *engine.CapacityInput) { i.Unfinished, i.Recipients = 95, 5 }), engine.CapacityVerdict{Admit: true}},
		{"free space equal to the reserve admits", with(func(i *engine.CapacityInput) { i.Available = reserve }), engine.CapacityVerdict{Admit: true}},
		{"free space one byte under the reserve is refused", with(func(i *engine.CapacityInput) { i.Available = reserve - 1 }), engine.CapacityVerdict{Reason: engine.CapacityStorageFull}},
		{"a clock set back is refused", with(func(i *engine.CapacityInput) { i.RollbackPaused = true }), engine.CapacityVerdict{Reason: engine.CapacityClockRollback}},
		{"the clock is judged before the queue", with(func(i *engine.CapacityInput) { i.RollbackPaused, i.Unfinished = true, 1000 }), engine.CapacityVerdict{Reason: engine.CapacityClockRollback}},
		{"the queue is judged before the disk", with(func(i *engine.CapacityInput) { i.Unfinished, i.Available = 1000, 0 }), engine.CapacityVerdict{Reason: engine.CapacityNodeQueueFull}},
		{"a queue limit that is not set refuses everything", with(func(i *engine.CapacityInput) { i.NodeQueueLimit = 0 }), engine.CapacityVerdict{Reason: engine.CapacityNodeQueueFull}},
		{"a reserve that is not set refuses everything", with(func(i *engine.CapacityInput) { i.DiskReserve = 0 }), engine.CapacityVerdict{Reason: engine.CapacityStorageFull}},
		{"a negative reserve refuses everything", with(func(i *engine.CapacityInput) { i.DiskReserve = -1 }), engine.CapacityVerdict{Reason: engine.CapacityStorageFull}},
		{"an empty volume is under any reserve", with(func(i *engine.CapacityInput) { i.Available = 0 }), engine.CapacityVerdict{Reason: engine.CapacityStorageFull}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := engine.CheckCapacity(tc.in); got != tc.want {
				t.Errorf("CheckCapacity(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// Work that names no recipient is a mistake of the caller and fails loudly.
func TestCheckCapacityPanicsForWorkWithoutRecipients(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("CheckCapacity of no recipients did not panic")
		}
	}()
	engine.CheckCapacity(engine.CapacityInput{Recipients: 0, NodeQueueLimit: 1, Available: 1 << 40, DiskReserve: 1})
}

func TestCapacityReasonsMapToTheEngineReasons(t *testing.T) {
	for _, tc := range []struct {
		in   engine.CapacityReason
		name string
		want engine.Reason
	}{
		{engine.CapacityClockRollback, "clock_rollback", engine.ReasonClockRollback},
		{engine.CapacityNodeQueueFull, "node_queue_full", engine.ReasonNodeQueueFull},
		{engine.CapacityStorageFull, "storage_full", engine.ReasonStorageFull},
		{engine.CapacityOK, "ok", 0},
	} {
		if tc.in.String() != tc.name || tc.in.Reason() != tc.want {
			t.Errorf("%v: name %q reason %v, want %q and %v", int(tc.in), tc.in.String(), tc.in.Reason(), tc.name, tc.want)
		}
	}
}

// The engine feeds the check from what it owns: the owner's settings, the free
// space of the volume and its own count of unfinished deliveries.
func TestAdmitNewWorkReadsTheSettingsTheDiskAndTheViewOfTheEngine(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	a := register(t, e, "a")
	r.restart()
	r.queue(1, 3, a)
	var op admin.OperationID
	op[0] = 5
	if _, err := r.cfg.SetNodeSettings(config.Request{Key: admin.KeyFor(admin.LocalOwner(), op), Expected: r.cfg.Node().Version},
		[]byte(`{"node_queue_limit":4}`), func(config.Node) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }); err != nil {
		t.Fatalf("SetNodeSettings: %v", err)
	}
	e = r.start()
	ctx := context.Background()

	if err := engine.AdmitNewWork(ctx, e, 1); err != nil {
		t.Errorf("3 unfinished of 4 and one more recipient: %v", err)
	}
	wantReason(t, engine.AdmitNewWork(ctx, e, 2), engine.ReasonNodeQueueFull)

	r.mem.SetAvailable(1 << 20) // far under the reserve
	wantReason(t, engine.AdmitNewWork(ctx, e, 1), engine.ReasonStorageFull)
	r.mem.SetAvailable(1 << 40)
	if err := engine.AdmitNewWork(ctx, e, 1); err != nil {
		t.Errorf("with the space back: %v", err)
	}

	// A volume that cannot be asked is not a volume with room.
	rule := r.faulty.Fail(matchT{Path: "state"}, syscall.EIO)
	wantReason(t, engine.AdmitNewWork(ctx, e, 1), engine.ReasonStorageUnavailable)
	if rule.Hits() == 0 {
		t.Error("the fault was not injected")
	}
}

var _ fsx.Step
