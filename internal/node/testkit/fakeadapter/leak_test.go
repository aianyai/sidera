package fakeadapter_test

import (
	"bytes"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"
)

// ownGoroutines returns the stacks of live goroutines that belong to a peer or to
// the fake adapter or runtime.
func ownGoroutines() []string {
	var buf bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
	var found []string
	for _, block := range strings.Split(buf.String(), "\n\n") {
		if strings.Contains(block, "sdk/go/peer.(*Peer).") ||
			(strings.Contains(block, "testkit/fakeadapter.") || strings.Contains(block, "testkit/fakeruntime.")) && !strings.Contains(block, "_test.go") {
			found = append(found, block)
		}
	}
	return found
}

func settledGoroutines() []string {
	deadline := time.Now().Add(5 * time.Second)
	for {
		left := ownGoroutines()
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var guarded sync.Map

// leakGuard fails the test if a goroutine of a peer, of the fake adapter or of the
// fake runtime is alive after the test and its cleanups. It is registered once per
// test, however many rigs the test builds, and the tests that use it do not run in
// parallel.
func leakGuard(t *testing.T) {
	t.Helper()
	if _, again := guarded.LoadOrStore(t, true); again {
		return
	}
	if left := settledGoroutines(); len(left) != 0 {
		t.Fatalf("%d goroutines alive before the test started:\n%s", len(left), strings.Join(left, "\n\n"))
	}
	t.Cleanup(func() {
		guarded.Delete(t)
		if left := settledGoroutines(); len(left) != 0 {
			t.Errorf("%d goroutines leaked:\n%s", len(left), strings.Join(left, "\n\n"))
		}
	})
}
