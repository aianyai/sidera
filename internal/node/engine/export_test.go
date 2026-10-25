package engine

import (
	"context"
	"testing"
)

// StallLoop makes the loop stop taking commands until the returned function is
// called, with the command that holds it already running.
func StallLoop(t testing.TB, e *Engine) (release func()) {
	t.Helper()
	t.Fatal("not implemented")
	return func() {}
}

// WaitInbox returns when n commands wait in the inbox.
func WaitInbox(t testing.TB, e *Engine, n int) {
	t.Helper()
	t.Fatal("not implemented")
}

// WaitingForRunning is how many adapter requests wait for the running phase.
func WaitingForRunning(e *Engine) int { return -1 }

// AdmitNewWork runs the capacity check as a command of the loop, the way the
// intake of new work does, for recipients recipients.
func AdmitNewWork(ctx context.Context, e *Engine, recipients int) error { return errStub }

// SpawnWorker starts a goroutine the engine owns and counts, as the workers of
// later streams are; fn returns when the worker is done.
func SpawnWorker(e *Engine, kind string, fn func(ctx context.Context)) {}

// ClearRollback is what clearing the pause of a wall clock set back does inside
// the loop; the management operation that persists it is a later stream's.
func ClearRollback(ctx context.Context, e *Engine) error { return errStub }
