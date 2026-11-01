//go:build acceptance

package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestFixtureCloseReclaimsRuntimeAcrossDaemonGenerations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := runRoot(t)
	f, err := newFixture(ctx, root, testBinaries, Options{Seed: t.Name()}, fixtureProfile{mainInstance: "chat", preset: "default", runtimeAdapter: true})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if err := f.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	if err := f.prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	e := newEnvironment(ctx, f)
	if _, err := e.Direct("sender"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Agent("sender").Control(ctx, fakeagent.ControlInput{To: runtimeAddress(e, "chat"), Signal: adapterproto.SignalSessionCreate, Preset: "default"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Quiesce(); err != nil {
		t.Fatal(err)
	}
	log, err := f.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	natives := log.Kind(ledger.KindRuntimeSessionCreated)
	if len(natives) != 1 {
		t.Fatalf("created runtime count = %d", len(natives))
	}
	ref := natives[0].Attrs.NativeRef
	conn, err := f.control.Wait(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	pid := conn.Hello().PID
	if err := f.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Runtime().SessionEnv(ref); err != nil {
		t.Fatalf("runtime did not survive daemon kill: %v", err)
	}
	if err := f.Start(ctx, GenerationSpec{Number: 2}); err != nil {
		t.Fatal(err)
	}
	conn, err = f.control.Wait(ctx, ref)
	if err != nil || conn.Hello().PID != pid {
		t.Fatalf("restart replaced runtime pid %d: %v", pid, err)
	}
	if _, err := f.Runtime().SessionEnv(ref); err != nil {
		t.Fatalf("runtime after daemon restart: %v", err)
	}
	// Close owns cleanup even after the scenario's context closes the listener.
	cancel()
	err = f.Close()
	closed = true
	if err != nil {
		t.Fatal(err)
	}
	log, err = ledger.ReadDir(filepath.Join(root, "l"))
	if err != nil {
		t.Fatal(err)
	}
	if kills := log.Kind(ledger.KindFixtureKill); len(kills) != 1 {
		t.Fatalf("final cleanup recorded a scenario kill: %+v", kills)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("fixture root remains in use after Close: %v", err)
	}
}
