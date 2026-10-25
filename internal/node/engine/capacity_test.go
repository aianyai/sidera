package engine_test

import (
	"context"
	"encoding/json"
	"syscall"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
)

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
