package process

import (
	"context"
	"errors"
	"os"
	"testing"
)

// An adapter that forgot HandleHelperInvocation finds out on any platform, at its
// first Interrupt, and no signal is sent: on Windows the missing call would
// otherwise show only when the helper is first started.
func TestInterruptWithoutTheHelperCallFails(t *testing.T) {
	was := helperHandled.Swap(false)
	defer helperHandled.Store(was)
	self, found, err := Lookup(os.Getpid())
	if err != nil || !found {
		t.Fatalf("Lookup(self) = %+v, %v, %v", self, found, err)
	}
	if err := Interrupt(context.Background(), self.Identity); !errors.Is(err, errMissingHelperCall) {
		t.Fatalf("Interrupt = %v, want errMissingHelperCall", err)
	}
}
