package fakeadapter

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// TestAnInitializeThatRunsAfterTheAdapterWasReleasedTakesNoLock: Serve releases the
// lock when it returns, but the handler of a request that was in flight when the
// connection ended may still be running. If it took the lock after the release,
// nobody would ever release it, and the next adapter started in this process would
// be refused for good.
func TestAnInitializeThatRunsAfterTheAdapterWasReleasedTakesNoLock(t *testing.T) {
	mem := ledger.NewMemory()
	now := func() time.Time { return time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC) }
	w, err := mem.Open("adapter/fake", 0, now)
	if err != nil {
		t.Fatal(err)
	}
	a := &adapter{opts: DefaultManifestOptions(), led: w, cfg: Config{InstanceID: "fake", Now: now, Ledger: w}}
	path := filepath.Join(t.TempDir(), "instance.lock")
	params, err := json.Marshal(adapterproto.InitializeParams{
		Protocol:     adapterproto.SupportedRange(),
		Daemon:       adapterproto.DaemonInfo{Version: "0.1.0"},
		Instance:     adapterproto.InstanceInfo{ID: "fake", LockPath: path},
		Capabilities: adapterproto.DaemonCapabilities{},
	})
	if err != nil {
		t.Fatal(err)
	}

	a.releaseLock() // Serve has returned
	if _, err := a.initialize(context.Background(), params); err == nil {
		t.Fatal("an adapter that was released answered initialize")
	}
	release, err := lockInstance(path)
	if err != nil {
		t.Fatalf("the lock is held by an adapter that no longer exists: %v", err)
	}
	release()
}
