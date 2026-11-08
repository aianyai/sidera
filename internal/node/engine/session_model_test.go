package engine_test

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
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

// A creation that chose a model is judged again before each call. When an earlier
// call may have made a session (the adapter kept a checkpoint of the attempt) and
// the instance has since come back as an adapter that does not take the choice,
// the creation cannot be said to have made nothing: it ends as an outcome nobody can
// vouch for, with the alert of a session that may remain, and the adapter is not
// called again.
func TestSessionCreateChoosingAModelMeetsAnAdapterThatNoLongerTakesItAfterACall(t *testing.T) {
	h := modelChoiceNode(t, []adapterproto.Effort{adapterproto.EffortLow, adapterproto.EffortHigh})
	ctrl, err := h.Adapter(harness.Instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.SetOutcome(h.Ctx(), fakeadapter.RequestCreate, fakeadapter.Outcome{Kind: fakeadapter.OutcomeIndeterminate}); err != nil {
		t.Fatal(err)
	}
	msg := createChoosing(t, h, "choice-downgrade", "sonnet", "")
	quiet(t, h)
	if n := createRequests(t, h); n != 1 {
		t.Fatalf("%d creation requests reached the adapter, want the one that could not tell", n)
	}

	// The instance is admitted again, now declaring no model choice.
	o := harness.RuntimeAdapterManifest(adapterproto.MaxDirectEndpoints)
	adm, err := adapterreg.Admit(o.Capabilities(), o.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	info := adapterproto.InitializeResult{ProtocolVersion: 1, Adapter: adapterproto.AdapterInfo{ID: harness.Instance, Version: "1.0.0"}, Capabilities: adm.Capabilities()}
	if _, err := h.Engine().AdapterAdmitted(h.Ctx(), harness.Instance, adm, info); err != nil {
		t.Fatal(err)
	}
	h.Advance(engine.DefaultParams().CreateRetryBackoff + time.Second)
	quiet(t, h)
	h.Advance(controlReplyWait)
	quiet(t, h)

	wantFailed(t, ptr(pulledReply(t, h, msg)), adapterproto.CreateReasonControlUnknown)
	if n := createRequests(t, h); n != 1 {
		t.Errorf("%d creation requests reached the adapter, want still the first", n)
	}
	alerts, err := h.Engine().Alerts(h.Ctx())
	if err != nil {
		t.Fatal(err)
	}
	var told bool
	for _, a := range alerts {
		told = told || a.Kind == "control_unknown"
	}
	if !told {
		t.Errorf("the alerts are %+v, want one of kind control_unknown", alerts)
	}
}
