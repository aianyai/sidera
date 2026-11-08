package engine_test

import (
	"crypto/ed25519"
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// statusReplyMargin is the room the largest status reply a node can make must leave
// under what a reader takes of a record, for what the reply holds besides the
// presets: the signal's id, the conditions, the envelope of the record.
const statusReplyMargin = 8 << 10

// The largest status reply the limit on presets allows, every preset with the
// longest name, a default model and effort and a full allowlist of the longest
// model names with all five efforts, is signed to a record that a reader takes with
// room to spare. This guards the limit's derivation: a raise of the limit, of the
// number or the length of the models, or of the names, fails it before a node can
// make a reply that no reader takes.
func TestTheLargestStatusReplyTheLimitOnPresetsAllowsFitsInOneSignedRecord(t *testing.T) {
	longest := func(prefix string, n int, width int) string {
		return prefix + strings.Repeat("x", width-len(prefix)-4) + fmt.Sprintf("%04d", n)
	}
	var efforts []adapterproto.Effort
	for _, e := range []adapterproto.Effort{adapterproto.EffortLow, adapterproto.EffortMedium, adapterproto.EffortHigh, adapterproto.EffortXHigh, adapterproto.EffortMax} {
		efforts = append(efforts, e)
	}
	status := &adapterproto.ControlStatusReply{
		Activity: adapterproto.ActivityIdle, Queued: 1 << 30, AsOf: 253402300799,
		Conditions: []adapterproto.ControlCondition{1, 2, 3, 4, 5},
	}
	for i := range config.MaxPresets {
		p := adapterproto.PresetAvailability{
			Name: longest("p", i, adapterproto.MaxPresetNameLen), Available: true,
			Model: longest("m", 0, adapterproto.MaxModelNameLen), Effort: adapterproto.EffortMedium, // "medium" is the longest word of the scale
		}
		for j := range adapterproto.MaxAllowedModels {
			p.AllowedModels = append(p.AllowedModels, adapterproto.ModelAllowance{Model: longest("m", j, adapterproto.MaxModelNameLen), Efforts: efforts})
		}
		status.Presets = append(status.Presets, p)
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("the largest status reply is not a valid one: %v", err)
	}

	h := sessionNode(t, harness.Options{}, "a")
	rt, err := ident.ParseAddress(h.RuntimeAddr())
	if err != nil {
		t.Fatal(err)
	}
	longID := strings.Repeat("i", envelope.MaxIDLen)
	key := store.TargetKey{SenderNode: rt.Node, Sender: rt.Endpoint, Message: delivery.MessageID(longID), Recipient: rt.Endpoint}
	var payload []byte
	var perr error
	if err := engine.InLoop(h.Ctx(), h.Engine(), func(l engine.LoopSide) {
		payload, perr = l.ControlReplyPayload(key, envelope.ControlResult{Signal: adapterproto.SignalEndpointStatus, Status: status})
	}); err != nil {
		t.Fatal(err)
	}
	if perr != nil {
		t.Fatalf("the largest status reply is refused: %v", perr)
	}
	// The signed form, as it goes over the wire: the type string and the node's
	// signature around the payload, as a reader decodes it.
	signer, err := record.NewEd25519Signer(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := record.Sign(envelope.ControlReplyType, payload, signer)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	signed := len(wire)
	t.Logf("the largest status reply of %d presets is %d bytes signed, of %d a reader takes", config.MaxPresets, signed, record.DefaultMaxInput)
	if signed > record.DefaultMaxInput-statusReplyMargin {
		t.Errorf("the largest status reply is %d bytes signed, more than %d, the most that leaves %d bytes of room", signed, record.DefaultMaxInput-statusReplyMargin, statusReplyMargin)
	}
}
