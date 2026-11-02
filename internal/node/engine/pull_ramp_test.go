package engine_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The sender hears of the reply to a control signal within seconds of the signal: a
// creation is the address the controller's next message goes to, and nobody
// waits for the report of an ordinary message (its waits are checked with the rule
// of the ramp, in pull_ramp_internal_test.go).
func TestAControlReplyIsPulledSoon(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	creation := signal(t, h, "a", h.RuntimeAddr(), adapterproto.SignalSessionCreate, "")
	quiet(t, h)

	h.Advance(controlReplyWait)
	quiet(t, h)
	if r := controlReply(t, h, "a", creation); r == nil || r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyCreated {
		t.Fatalf("the reply to the creation is %+v after %v, want a session", r, controlReplyWait)
	}
}
