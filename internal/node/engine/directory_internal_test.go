package engine

import (
	"context"
	"testing"
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
