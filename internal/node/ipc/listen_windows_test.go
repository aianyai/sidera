package ipc

import (
	"runtime"
	"testing"
)

// A pipe name is claimed by the first listener only. Otherwise a process that
// starts earlier than the daemon could serve the name and collect tokens.
func TestSecondListenerOnSamePipeNameFails(t *testing.T) {
	addr, err := Locate(t.TempDir(), KindAgent)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	first, err := Listen(addr, heldLock(true), Options{})
	if err != nil {
		t.Fatalf("first Listen on %s (%s): %v", addr.Name(), runtime.GOOS, err)
	}
	defer first.Close()

	second, err := Listen(addr, heldLock(true), Options{})
	if err == nil {
		second.Close()
		t.Fatalf("a second listener on %s was accepted while the first is open", addr.Name())
	}
	t.Logf("second Listen on %s failed as required: %v", addr.Name(), err)
}
