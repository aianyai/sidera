package adapterhost

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/peer"
)

// A zero field of Params takes its default, a field that is set is kept, and a cap
// below the first wait is raised to it.
func TestParamsZeroFieldsTakeTheirDefaults(t *testing.T) {
	d := DefaultParams()
	if got := (Params{}).withDefaults(); got != d {
		t.Fatalf("withDefaults of the zero value = %+v, want %+v", got, d)
	}
	set := Params{
		NotificationBuffer: 7, PingInterval: time.Second, PingTimeout: 2 * time.Second,
		RestartBackoffMin: 3 * time.Second, RestartBackoffMax: 4 * time.Second, StableAfter: 5 * time.Second,
		HandshakeTimeout: 6 * time.Second, StartWait: 7 * time.Second, ShutdownWait: 8 * time.Second,
		Peer: peer.Params{MaxPendingNotifications: 9},
	}
	if got := set.withDefaults(); got != set {
		t.Fatalf("withDefaults changed a set value: %+v, want %+v", got, set)
	}
	low := Params{RestartBackoffMin: 10 * time.Second, RestartBackoffMax: time.Second}.withDefaults()
	if low.RestartBackoffMax != low.RestartBackoffMin {
		t.Fatalf("a cap below the first wait stayed: %v < %v", low.RestartBackoffMax, low.RestartBackoffMin)
	}
	if err := (Params{PingInterval: -1}).validate(); err == nil {
		t.Fatal("a negative interval was accepted")
	}
	if err := (Params{}).validate(); err != nil {
		t.Fatalf("the zero value was refused: %v", err)
	}
}
