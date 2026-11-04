package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aianyai/sidera/internal/node/transport"
)

// The task's delivery boundary has one rule separate from the public timeout:
// cancellation has abandoned the answer, so completion cannot deliver it later.
func TestAbandonedDirectoryTaskDiscardsALateAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	task := &directoryTask{ctx: ctx, cancel: cancel, answer: newCall[DirectoryPage]()}
	e := &Engine{outbox: newOutbox()}
	e.outbox.queue = []outWork{{kind: workDirectory, directory: task}}
	e.abandonDirectory(task)
	if _, ok := e.outbox.next(); ok {
		t.Fatal("an abandoned directory task remained queued")
	}
	e.finishDirectory(task, DirectoryPage{NextCursor: "late"}, nil)
	select {
	case result := <-task.answer.ch:
		t.Fatalf("an abandoned task received a late result: %+v", result)
	default:
	}
}

// A caller of Directory is told a reason, never the transport's own error: the
// reason a target refused for is kept, and any other failure of the call is that
// the directory is unavailable.
func TestDirectoryFailureIsAReason(t *testing.T) {
	for _, c := range []struct {
		name       string
		err        error
		want       Reason
		fromTarget bool
	}{
		{"a refusal of this process", refuse(ReasonRecovering), ReasonRecovering, false},
		{"a refusal a remote target named", transport.Refused{Reason: "clock_rollback"}, ReasonClockRollback, true},
		{"a refusal a remote target named, wrapped", fmt.Errorf("asking: %w", transport.Refused{Reason: "recovering"}), ReasonRecovering, true},
		{"a refusal whose name is not known", transport.Refused{Reason: "from_a_newer_node"}, ReasonDirectoryUnavailable, false},
		{"a target that could not be reached", fmt.Errorf("%w: down", transport.ErrUnreachable), ReasonDirectoryUnavailable, false},
		{"a target that did not answer", transport.ErrTimeout, ReasonDirectoryUnavailable, false},
		{"a target that says it failed", errors.New("network: the target could not handle the call"), ReasonDirectoryUnavailable, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := directoryFailure(c.err)
			got, ok := ReasonOf(err)
			if !ok || got != c.want {
				t.Fatalf("reason = %v (found %v), want %v", got, ok, c.want)
			}
			if ref := err.(*Refusal); ref.FromTarget != c.fromTarget {
				t.Fatalf("FromTarget = %v, want %v", ref.FromTarget, c.fromTarget)
			}
		})
	}
}
