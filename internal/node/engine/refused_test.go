package engine_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/transport"
)

// A refusal that comes from another node reaches the engine as a
// transport.Refused with the target engine's reason by name. A target that holds
// another request under the identifier of a message ends the sending of that
// message exactly as the same refusal from a target in this process does; no
// other name changes what the sender does, and a name it does not know is a
// refusal like any other error of the target, which is sent again.
func TestARemoteRefusalStopsTheResendingOfAConflictedMessage(t *testing.T) {
	conflict := engine.ReasonMessageConflict.String()
	for _, c := range []struct {
		name        string
		err         error
		wantStopped bool
	}{
		{"a local refusal as a conflict", &engine.Refusal{Reason: engine.ReasonMessageConflict}, true},
		{"a remote refusal naming the conflict", transport.Refused{Reason: conflict}, true},
		{"a remote refusal naming the conflict, wrapped", fmt.Errorf("calling: %w", transport.Refused{Reason: conflict}), true},
		{"a remote refusal naming another reason this node knows", transport.Refused{Reason: engine.ReasonNodeQueueFull.String()}, false},
		{"a remote refusal naming a reason this node does not know", transport.Refused{Reason: "no_such_reason"}, false},
		{"an error that is no refusal", errors.New("no answer"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := nodeWith(t, harness.Options{}, "a", "b")
			h.Unreachable(true) // the loopback itself answers nothing while the call is made by hand
			msg := send(t, h, "a", "b", "its identifier may be held by another request")
			quiet(t, h)
			name := outOf(t, h, msg).Name()
			var holds bool
			err := engine.InLoop(h.Ctx(), h.Engine(), func(l engine.LoopSide) {
				l.SendInFlight(name)
				l.SendFailedWith(name, harness.NodeID(), delivery.MessageID(msg), c.err)
				holds = l.OutboxHolds(name)
			})
			if err != nil {
				t.Fatal(err)
			}
			if holds == c.wantStopped {
				t.Errorf("the outbox holds the record: %v, want %v", holds, !c.wantStopped)
			}
		})
	}
}
