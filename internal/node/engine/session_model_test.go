package engine_test

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// modelChoiceNode is a node whose adapter declares model_choice with the efforts
// given, or declares none when efforts is nil.
func modelChoiceNode(t *testing.T, efforts []adapterproto.Effort) *harness.H {
	t.Helper()
	m := harness.RuntimeAdapterManifest(adapterproto.MaxDirectEndpoints)
	if efforts != nil {
		m.ModelChoice = &adapterproto.ModelChoiceCapability{Efforts: efforts}
	}
	return sessionNode(t, harness.Options{Adapter: &harness.AdapterOptions{
		InstanceOptions: map[string]harness.AdapterInstanceOptions{harness.Instance: {Manifest: m}},
	}}, "a")
}

// createChoosing sends the runtime endpoint a session.create that chooses the model
// and the effort, as the envelope a node receives carries them, and returns the
// message id.
func createChoosing(t testing.TB, h *harness.H, id, model string, effort adapterproto.Effort) adapterproto.MessageID {
	t.Helper()
	to := ident.Address{Node: harness.NodeID(), Endpoint: runtimeEndpoint(t, h)}
	now := h.Clock().Now().Truncate(time.Second)
	accept := now.Add(delivery.DefaultAcceptBefore)
	env := envelope.Envelope{
		ID: delivery.MessageID(id), To: []ident.Address{to}, Kind: envelope.KindSessionCreate, Preset: "default",
		Model: model, Effort: effort,
		Seq: map[ident.EndpointID]uint64{to.Endpoint: 1}, SentAt: now, AcceptBefore: accept, ReportBy: delivery.ReportBy(accept),
	}
	data, err := env.Encode(envelope.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), endpointOf(t, h, "a"), data, 0); err != nil {
		t.Fatal(err)
	}
	return adapterproto.MessageID(id)
}

// pulledReply is the control reply the node holds for a creation sent by createChoosing.
func pulledReply(t testing.TB, h *harness.H, msg adapterproto.MessageID) adapterproto.ControlReply {
	t.Helper()
	rc, err := pullFor(h, msg, runtimeEndpoint(t, h))
	if err != nil || rc.ControlReply == nil {
		t.Fatalf("pull = %+v, %v, want the control reply", rc, err)
	}
	r, err := h.VerifyControlReply(*rc.ControlReply)
	if err != nil {
		t.Fatal(err)
	}
	return r.Result.AdapterForm()
}

// A creation that chooses a model or an effort goes to the adapter only if it
// declared model_choice and lists the effort; otherwise the node refuses it at
// once with the reason of a choice that is not allowed, creating nothing, and a
// creation the adapter takes reaches it with the choice as it was made.
func TestSessionCreateChoosingAModel(t *testing.T) {
	lowHigh := []adapterproto.Effort{adapterproto.EffortLow, adapterproto.EffortHigh}
	for _, tc := range []struct {
		name    string
		efforts []adapterproto.Effort
		model   string
		effort  adapterproto.Effort
		taken   bool
	}{
		{"the adapter declared nothing, a model", nil, "sonnet", "", false},
		{"the adapter declared nothing, an effort", nil, "", adapterproto.EffortLow, false},
		{"the adapter does not list the effort", lowHigh, "sonnet", adapterproto.EffortMax, false},
		{"a model alone", lowHigh, "sonnet[1m]", "", true},
		{"an effort alone", lowHigh, "", adapterproto.EffortHigh, true},
		{"a model and an effort", lowHigh, "sonnet[1m]", adapterproto.EffortLow, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := modelChoiceNode(t, tc.efforts)
			msg := createChoosing(t, h, "choice-1", tc.model, tc.effort)
			quiet(t, h)
			if !tc.taken {
				wantFailed(t, ptr(pulledReply(t, h, msg)), adapterproto.CreateReasonModelChoiceNotAllowed)
				nothingWasMade(t, h)
				if n := createRequests(t, h); n != 0 {
					t.Errorf("%d creation requests reached the adapter, want none", n)
				}
				if eps := sessionEndpoints(t, h); len(eps) != 0 {
					t.Errorf("%d session endpoints for a creation refused at intake, want none", len(eps))
				}
				if c := ctlRecords(t, h); len(c) != 1 || c[0].Intent != nil {
					t.Errorf("control records = %+v, want one without a creation intent", c)
				}
			} else {
				h.Advance(controlReplyWait)
				quiet(t, h)
				if r := pulledReply(t, h, msg); r.Create == nil || r.Create.Outcome != adapterproto.CreateReplyCreated {
					t.Fatalf("the reply is %+v, want created", r.Create)
				}
				log, err := h.Ledger()
				if err != nil {
					t.Fatal(err)
				}
				got := log.Kind("session.create.received")
				if len(got) != 1 || got[0].Attrs.Model != tc.model || got[0].Attrs.Effort != string(tc.effort) {
					t.Errorf("the adapter received %+v, want model %q and effort %q", got, tc.model, tc.effort)
				}
			}
			// The control record keeps the choice, taken or refused, as the request had it.
			ctl := ctlRecords(t, h)
			if len(ctl) != 1 || ctl[0].Model != tc.model || ctl[0].Effort != tc.effort {
				t.Errorf("control records = %+v, want one holding model %q and effort %q", ctl, tc.model, tc.effort)
			}
		})
	}
}

// An adapter that is away is reported as away, not as one that does not take the
// choice: what an adapter takes is known only while it is admitted.
func TestSessionCreateChoosingAModelWhileTheAdapterIsOffline(t *testing.T) {
	h := modelChoiceNode(t, nil)
	adapterEnded(t, h, h.AdapterLaunches(harness.Instance)[0])
	msg := createChoosing(t, h, "choice-offline", "sonnet", "")
	engineQuiet(t, h)
	wantFailed(t, ptr(pulledReply(t, h, msg)), adapterproto.CreateReasonAdapterOffline)
}
