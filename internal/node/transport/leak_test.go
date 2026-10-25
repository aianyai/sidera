package transport_test

import (
	"bytes"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

// transportGoroutines returns the stacks of live goroutines that are inside the
// loopback or the knobs. The package starts none of its own, so a stack that
// shows one after a test and its cleanups is a call that never came back: a
// delay whose context was not honoured, or a lock never released.
func transportGoroutines() []string {
	var buf bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
	var found []string
	for _, block := range strings.Split(buf.String(), "\n\n") {
		if strings.Contains(block, "internal/node/transport.(*Loopback).") || strings.Contains(block, "internal/node/transport.(*Knobs).") {
			found = append(found, block)
		}
	}
	return found
}

// guardLeaks fails the test if a call into the package is still running after the
// test and its cleanups. It waits a short while for a goroutine that has been
// told to stop.
func guardLeaks(t testing.TB) {
	t.Helper()
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			left := transportGoroutines()
			if len(left) == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("%d goroutines still inside the transport:\n%s", len(left), strings.Join(left, "\n\n"))
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}
