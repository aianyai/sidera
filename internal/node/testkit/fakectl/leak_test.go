package fakectl_test

import (
	"bytes"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

// ownGoroutines returns the stacks of live goroutines that belong to a peer or to
// this package.
func ownGoroutines() []string {
	var buf bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
	var found []string
	for _, block := range strings.Split(buf.String(), "\n\n") {
		if strings.Contains(block, "sdk/go/peer.(*Peer).") || strings.Contains(block, "testkit/fakectl.") && !strings.Contains(block, "_test.go") {
			found = append(found, block)
		}
	}
	return found
}

func settled() []string {
	deadline := time.Now().Add(5 * time.Second)
	for {
		left := ownGoroutines()
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// leakGuard fails the test if a goroutine of a peer or of the package is still
// alive after the test and its cleanups. Tests using it must not run in parallel.
func leakGuard(t *testing.T) {
	t.Helper()
	if left := settled(); len(left) != 0 {
		t.Fatalf("%d goroutines alive before the test started:\n%s", len(left), strings.Join(left, "\n\n"))
	}
	t.Cleanup(func() {
		if left := settled(); len(left) != 0 {
			t.Errorf("%d goroutines leaked:\n%s", len(left), strings.Join(left, "\n\n"))
		}
	})
}
