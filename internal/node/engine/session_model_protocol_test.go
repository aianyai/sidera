package engine_test

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// choosingEnvelope is the encoded envelope of a session.create for the runtime
// endpoint that chooses the model and the effort, as a node that sends one makes it.
func choosingEnvelope(t testing.TB, h *harness.H, id, model string, effort adapterproto.Effort) []byte {
	t.Helper()
	to := ident.Address{Node: harness.NodeID(), Endpoint: runtimeEndpoint(t, h)}
	now := h.Clock().Now().Truncate(time.Second)
	accept := now.Add(delivery.DefaultAcceptBefore)
	data, err := envelope.Envelope{
		ID: delivery.MessageID(id), To: []ident.Address{to}, Kind: envelope.KindSessionCreate, Preset: "default",
		Model: model, Effort: effort,
		Seq: map[ident.EndpointID]uint64{to.Endpoint: 1}, SentAt: now, AcceptBefore: accept, ReportBy: delivery.ReportBy(accept),
	}.Encode(envelope.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A request that chooses a model or an effort is taken in only if the protocol it
// travelled on carries the choice. One that did not is invalid, and nothing of it is
// kept; only a sender that sends it anyway does this, and it learns it at once.
func TestChoiceOnAProtocolThatDoesNotCarryItIsInvalid(t *testing.T) {
	for _, tc := range []struct {
		name     string
		features transport.Features
		model    string
		effort   adapterproto.Effort
		invalid  bool
	}{
		{"a model, without the feature", 0, "sonnet", "", true},
		{"an effort, without the feature", 0, "", adapterproto.EffortLow, true},
		{"both, without the feature", 0, "sonnet", adapterproto.EffortLow, true},
		{"a model, with the feature", transport.FeatureModelChoice, "sonnet", "", false},
		{"no choice, without the feature", 0, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := modelChoiceNode(t, []adapterproto.Effort{adapterproto.EffortLow})
			from := transport.RemoteOrigin(modelChoiceSender(t))
			data := choosingEnvelope(t, h, "choice-protocol", tc.model, tc.effort)
			_, err := h.Engine().Inbound(h.Ctx(), from, endpointOf(t, h, "a"), data, tc.features)
			if !tc.invalid {
				if err != nil {
					t.Fatalf("Inbound: %v", err)
				}
				return
			}
			if ref := wantReason(t, err, engine.ReasonInvalidArgument); ref.Field != "envelope" {
				t.Errorf("the refusal is for the field %q, want envelope", ref.Field)
			}
			if c := ctlRecords(t, h); len(c) != 0 {
				t.Errorf("%d control records for an envelope that is invalid, want none", len(c))
			}
		})
	}
}

// modelChoiceSender is a node that is not this one.
func modelChoiceSender(t testing.TB) ident.NodeID {
	t.Helper()
	id, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{9}, ident.NodeIDLen))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// A session.create that chooses a model, sent to a node that runs a version that
// serves no protocol carrying it, ends at once as rejected for node_unsupported:
// the sender's node refuses it without sending, tells its sender, and does not send
// it again. The same node still takes a creation that chooses nothing.
func TestChoiceToANodeThatCannotTakeItIsRejectedAtOnce(t *testing.T) {
	h := modelChoiceNode(t, []adapterproto.Effort{adapterproto.EffortLow})
	h.Unsupported(transport.FeatureModelChoice)
	to := h.RuntimeAddr()

	chosen, err := h.Agent("a").Control(h.Ctx(), fakeagent.ControlInput{To: to, Signal: adapterproto.SignalSessionCreate, Preset: "default", Model: "sonnet", Effort: adapterproto.EffortLow})
	if err != nil {
		t.Fatalf("control: %v", err)
	}
	quiet(t, h)
	if d := sourceDelivery(t, h, chosen.MessageID); d.State != delivery.StateRejected || d.Reject != delivery.RejectNodeUnsupported {
		t.Fatalf("the creation is %s (%s), want rejected (node_unsupported)", d.State, d.Reject)
	}
	st, err := h.Agent("a").Status(h.Ctx(), fakeagent.StatusInput{MessageID: chosen.MessageID})
	if err != nil || len(st.Recipients) != 1 || st.Recipients[0].State != "rejected" || st.Recipients[0].Reason != "node_unsupported" {
		t.Errorf("status = %+v, %v, want rejected for node_unsupported", st.Recipients, err)
	}
	if got := feedbackOf(t, h, chosen.MessageID); !reflect.DeepEqual(got, []delivery.Kind{delivery.KindRejected}) {
		t.Errorf("the sender is to be told %v, want that the message was rejected", got)
	}

	// It is not sent again: time passes over many resends, and the target is never called.
	for range 5 {
		h.Advance(resendBackoff)
		quiet(t, h)
	}
	if n := knobHits(t, h)["feature-unsupported"]; n != 1 {
		t.Errorf("the send was refused %d times, want once and not tried again", n)
	}
	if c := ctlRecords(t, h); len(c) != 0 {
		t.Errorf("the target holds %d control records, want none: nothing reached it", len(c))
	}

	// A creation that chooses nothing goes through to the same node.
	plain, err := h.Agent("a").Control(h.Ctx(), fakeagent.ControlInput{To: to, Signal: adapterproto.SignalSessionCreate, Preset: "default"})
	if err != nil {
		t.Fatalf("control: %v", err)
	}
	quiet(t, h)
	h.Advance(controlReplyWait)
	quiet(t, h)
	if r := controlReply(t, h, "a", plain.MessageID); r == nil || r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyCreated {
		t.Errorf("the creation that chooses nothing got %+v, want created", r)
	}
}

// The status of a runtime endpoint lists, for each preset, what the adapter's last
// probe resolved of it (the model and effort it sets and the models a requester may
// choose) only when the request travelled on a protocol that carries them. A preset
// the probe did not report has none of them either way.
func TestStatusRepliesWithTheModelChoiceOfEachPresetOnlyWhereItIsCarried(t *testing.T) {
	m := harness.RuntimeAdapterManifest(adapterproto.MaxDirectEndpoints)
	m.ModelChoice = &adapterproto.ModelChoiceCapability{Efforts: []adapterproto.Effort{adapterproto.EffortLow, adapterproto.EffortHigh}}
	h := sessionNode(t, harness.Options{
		Presets: map[string]string{"fast": `{"local_sources":"allow"}`},
		Adapter: &harness.AdapterOptions{InstanceOptions: map[string]harness.AdapterInstanceOptions{harness.Instance: {Manifest: m}}},
	}, "a")
	allowed := []adapterproto.ModelAllowance{
		{Model: "opus", Efforts: []adapterproto.Effort{adapterproto.EffortLow, adapterproto.EffortHigh}},
		{Model: "sonnet", Efforts: []adapterproto.Effort{adapterproto.EffortLow}},
	}
	probe := adapterproto.ProbeResult{
		Auth: adapterproto.AuthValid, Dependencies: []adapterproto.Dependency{}, ToolProbes: []adapterproto.ToolProbeResult{},
		Runtime: &adapterproto.RuntimeInfo{Name: "fake", Version: "1"},
		Presets: []adapterproto.PresetStatus{{Name: "default", Usable: true, Model: "sonnet", Effort: adapterproto.EffortHigh, AllowedModels: allowed}},
	}
	if err := h.Engine().AdapterProbed(h.Ctx(), harness.Instance, probe); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	withChoice := adapterproto.PresetAvailability{Name: "default", Available: true, Model: "sonnet", Effort: adapterproto.EffortHigh, AllowedModels: allowed}
	bare := func(name string) adapterproto.PresetAvailability {
		return adapterproto.PresetAvailability{Name: name, Available: true}
	}
	for _, tc := range []struct {
		name     string
		features transport.Features
		want     []adapterproto.PresetAvailability
	}{
		{"on a protocol that carries the choice", transport.FeatureModelChoice, []adapterproto.PresetAvailability{withChoice, bare("fast")}},
		{"on a protocol that does not", 0, []adapterproto.PresetAvailability{bare("default"), bare("fast")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got *adapterproto.ControlStatusReply
			if err := engine.InLoop(h.Ctx(), h.Engine(), func(l engine.LoopSide) {
				got = l.StatusReply(runtimeEndpoint(t, h), tc.features)
			}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Presets, tc.want) {
				t.Errorf("the presets are %+v, want %+v", got.Presets, tc.want)
			}
		})
	}
}
