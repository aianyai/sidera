package process_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func wantContainerKinds() []adapterproto.ContainerKind {
	return []adapterproto.ContainerKind{adapterproto.ContainerNone}
}

func checkGroupLeader(t *testing.T, _ int) {
	t.Helper()
	t.Fatal("Windows has no process groups to report")
}

// A process without a console of its own has none to send Ctrl-C to: the request
// fails and says so, and is not taken for a process that ended.
func TestInterruptOfAProcessStartedWithoutAConsoleFails(t *testing.T) {
	c := startChild(t, "hold", nil)
	c.waitOutput(t, "ready")
	err := process.Interrupt(context.Background(), c.Identity)
	if err == nil || errors.Is(err, process.ErrNotRunning) {
		t.Fatalf("Interrupt = %v, want an error that is not ErrNotRunning", err)
	}
	if running, _ := process.Running(c.Identity); !running {
		t.Fatal("the process ended")
	}
}

// A process gets its console while it starts, so an Interrupt right after Start
// reaches a process that has none yet. The request is delivered once the console
// is there, not refused as it would be for a process that has none. What the
// child does with an event that reaches it before it has finished starting is its
// own: a Go program on windows/arm64 can be left deaf to it, so only the delivery
// is checked here, and the end of a process that is up is checked by
// TestInterruptStopsTheProcess.
func TestInterruptRightAfterStartIsDelivered(t *testing.T) {
	c := startChild(t, "hold", func(s *process.Spec) { s.Interruptible = true })
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := process.Interrupt(ctx, c.Identity); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
}
