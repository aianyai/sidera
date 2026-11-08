package engine_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A control reply is stored once and signed each time it is sent, and a sender reads
// a signed record of at most record.DefaultMaxInput bytes. A reply that would sign
// to more is not committed: nothing is stored, the failure is logged at the node
// that made it, and the signal is not answered, so the sender asks again and is
// told the same until the node's presets are fewer.
func TestAControlReplyNoReaderCouldTakeIsNotCommitted(t *testing.T) {
	presets := map[string]string{}
	for i := range 1100 { // more than a reader takes of the status reply
		presets[fmt.Sprintf("%s%04d", strings.Repeat("p", 60), i)] = `{"local_sources":"allow"}`
	}
	h := sessionNode(t, harness.Options{StoredPresets: presets}, "a")
	res, err := h.Agent("a").Control(h.Ctx(), fakeagent.ControlInput{To: h.RuntimeAddr(), Signal: adapterproto.SignalEndpointStatus})
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, h)

	for _, c := range scanOf(t, h).Ctl {
		t.Errorf("the target holds the control record %s (reply: %v, %d bytes, state %v)", c.Kind, c.Dedup.HasReply, len(c.Dedup.Reply), c.Record.State)
	}
	if !slices.Contains(h.Log().Messages(), "a call from another node failed for a reason that is no refusal") {
		t.Errorf("the target logged nothing of the reply it could not make: %q", h.Log().Messages())
	}
	if r := controlReply(t, h, "a", res.MessageID); r != nil {
		t.Errorf("the sender was told %+v", r)
	}
}

// The error that refuses the reply is the one the log carries, so it names the
// signal and says how large the reply would be.
func TestTheRefusalOfAControlReplyNamesTheSignalAndTheSize(t *testing.T) {
	h := sessionNode(t, harness.Options{}, "a")
	status := &adapterproto.ControlStatusReply{Activity: adapterproto.ActivityIdle, AsOf: 1}
	for i := range 1100 {
		status.Presets = append(status.Presets, adapterproto.PresetAvailability{Name: fmt.Sprintf("%s%04d", strings.Repeat("p", 60), i)})
	}
	rt, err := ident.ParseAddress(h.RuntimeAddr())
	if err != nil {
		t.Fatal(err)
	}
	key := store.TargetKey{SenderNode: rt.Node, Sender: rt.Endpoint, Message: "a-message", Recipient: rt.Endpoint}
	var perr error
	if lerr := engine.InLoop(h.Ctx(), h.Engine(), func(l engine.LoopSide) {
		_, perr = l.ControlReplyPayload(key, envelope.ControlResult{Signal: adapterproto.SignalEndpointStatus, Status: status})
	}); lerr != nil {
		t.Fatal(lerr)
	}
	if !errors.Is(perr, envelope.ErrControlReplyTooLarge) {
		t.Fatalf("err = %v, want a reply that is too large", perr)
	}
	if msg := perr.Error(); !strings.Contains(msg, "a-message") || !strings.Contains(msg, "signed it is") {
		t.Errorf("err = %q, want the id of the signal and the size", msg)
	}
}
