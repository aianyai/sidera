package process_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
)

func TestTerminateEndsTheProcess(t *testing.T) {
	c := startChild(t, "hold", nil)
	c.waitOutput(t, "ready")

	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := process.Terminate(ctx, c.Identity, wait); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if err := waitDone(t, c.Done); err == nil {
		t.Fatal("a terminated process reported a clean exit")
	}
	if running, err := process.Running(c.Identity); err != nil || running {
		t.Fatalf("Running after Terminate = %v, %v", running, err)
	}
	if err := process.Terminate(ctx, c.Identity, wait); !errors.Is(err, process.ErrNotRunning) {
		t.Fatalf("Terminate after the end = %v, want ErrNotRunning", err)
	}
}

// A PID that another process holds now is not the process the identity names: it
// is neither signalled nor ended.
func TestTerminateOfAnotherProcessWithTheSamePIDIsRefused(t *testing.T) {
	c := startChild(t, "hold", nil)
	c.waitOutput(t, "ready")

	stale := process.Identity{PID: c.PID, StartKey: c.StartKey + "0"}
	if err := process.Terminate(context.Background(), stale, time.Millisecond); !errors.Is(err, process.ErrNotRunning) {
		t.Fatalf("Terminate with a stale key = %v, want ErrNotRunning", err)
	}
	time.Sleep(200 * time.Millisecond)
	if running, _ := process.Running(c.Identity); !running {
		t.Fatal("the process was ended by an identity that is not its own")
	}
}
