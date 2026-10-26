package fakeruntime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
)

// TestKillNativeIsAnsweredByTheProcessEndingItsConnection: a killed runtime process
// ends its connection rather than answer the knob. The connection's end reaches the
// call that waits for the answer before the connection reports itself done, so a
// controller that looked at the second to judge the first took a kill that worked
// for a failure now and then. The process here ends its connection from inside the
// knob, as the real one does, and the kill must be a success every time.
func TestKillNativeIsAnsweredByTheProcessEndingItsConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	srv, err := fakectl.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ctrl := fakeruntime.RemoteController{Server: srv}

	for i := range 500 {
		ref := fmt.Sprintf("ref-%d", i)
		client, err := fakectl.Dial(ctx, srv.Addr())
		if err != nil {
			t.Fatal(err)
		}
		client.HandleKnob(func(context.Context, string, json.RawMessage) (any, error) {
			_ = client.Close()
			return nil, nil
		})
		if err := client.Start(ctx, fakectl.HelloParams{Role: fakectl.RoleRuntime, ID: ref, PID: 1}); err != nil {
			t.Fatal(err)
		}
		if err := ctrl.KillNative(ref); err != nil {
			t.Fatalf("kill %d: %v", i, err)
		}
	}
}
