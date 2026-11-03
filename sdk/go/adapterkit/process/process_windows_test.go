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
