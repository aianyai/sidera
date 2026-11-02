package manageapi

import (
	"context"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
)

// sessions.attach is authorized as the attachment of a session, which is for the local
// owner and is not replay protected: its request has the envelope of a read, so an
// operation identifier or a version is refused, it does not come through the door of
// operations, and its answer has the words of a read with the token of the endpoint.
func TestSessionsAttachIsAnsweredWithTheEndpointTheTokenAndTheWakeState(t *testing.T) {
	r := newRig(t)
	endpoint := ident.Address{Node: ownNode, Endpoint: endpointOf(7)}
	r.svc.attach = func(_ context.Context, q engine.AttachRequest) (engine.AttachResult, error) {
		return engine.AttachResult{
			Endpoint: endpoint, Instance: "chat", Token: "dG9rZW4",
			Wake: engine.WakeReport{State: engine.WakeUnavailable, Reason: "wake_unavailable", Detail: "runtime too old"},
		}, nil
	}
	status, out := r.post("sessions.attach", `{"params":{"adapter":"chat","instance":"chat","native_ref":"native-a","display_name":"Mine","wake":{"WAKE_KEY":"wake-value"}}}`)
	want := `{"result":{"endpoint":"` + endpoint.String() + `","instance":"chat","token":"dG9rZW4","wake":{"state":"unavailable","reason":"wake_unavailable","detail":"runtime too old"}}}`
	if status != 200 || strings.TrimSpace(string(out)) != want {
		t.Fatalf("answered %d %s\nwant %s", status, out, want)
	}
	got := r.svc.attaches
	if len(got) != 1 || got[0].Adapter != "chat" || got[0].Instance != "chat" || got[0].NativeRef != "native-a" || got[0].DisplayName != "Mine" ||
		len(got[0].Wake) != 1 || got[0].Wake["WAKE_KEY"] != "wake-value" {
		t.Errorf("the engine was asked %+v", got)
	}
	if n := len(r.svc.calls()); n != 0 {
		t.Errorf("the door of operations was asked %d times", n)
	}

	// A ready state has no reason or remark, and the optional params may be left out.
	r.svc.attach = func(context.Context, engine.AttachRequest) (engine.AttachResult, error) {
		return engine.AttachResult{Endpoint: endpoint, Instance: "chat", Token: "dG9rZW4", Wake: engine.WakeReport{State: engine.WakeReady}}, nil
	}
	status, out = r.post("sessions.attach", `{"params":{"adapter":"chat","native_ref":"native-a"}}`)
	want = `{"result":{"endpoint":"` + endpoint.String() + `","instance":"chat","token":"dG9rZW4","wake":{"state":"ready"}}}`
	if status != 200 || strings.TrimSpace(string(out)) != want {
		t.Fatalf("answered %d %s\nwant %s", status, out, want)
	}
	if got := r.svc.attaches[1]; got.Instance != "" || got.DisplayName != "" || got.Wake != nil || got.FindOnly {
		t.Errorf("the engine was asked %+v for a request that left the optional params out", got)
	}

	// create: false only finds, and create: true is the default said out loud.
	for body, findOnly := range map[string]bool{
		`{"params":{"adapter":"chat","native_ref":"native-a","create":false}}`: true,
		`{"params":{"adapter":"chat","native_ref":"native-a","create":true}}`:  false,
	} {
		r.post("sessions.attach", body)
		if got := r.svc.attaches[len(r.svc.attaches)-1]; got.FindOnly != findOnly {
			t.Errorf("%s asked the engine %+v, want find only %v", body, got, findOnly)
		}
	}
}

func TestSessionsAttachTakesStrictParams(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		name, body, path string
	}{
		{"a member the method does not take", `{"params":{"adapter":"chat","native_ref":"n","preset":"default"}}`, "params.preset"},
		{"a wake of the wrong type", `{"params":{"adapter":"chat","native_ref":"n","wake":["x"]}}`, "params.wake"},
		{"a wake value of the wrong type", `{"params":{"adapter":"chat","native_ref":"n","wake":{"K":1}}}`, "params.wake.K"},
		{"create of the wrong type", `{"params":{"adapter":"chat","native_ref":"n","create":"no"}}`, "params.create"},
		{"null for the native session", `{"params":{"adapter":"chat","native_ref":null}}`, "params.native_ref"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, out := r.post("sessions.attach", tc.body)
			e := r.wantError(400, "invalid_argument", "malformed_request", status, out)
			if e.Error.Details["path"] != tc.path {
				t.Errorf("the fault is placed at %v, want %q", e.Error.Details["path"], tc.path)
			}
			if strings.Contains(string(out), "wake-value") {
				t.Errorf("a value of the wake information is in the answer: %s", out)
			}
		})
	}
	if n := len(r.svc.attaches); n != 0 {
		t.Errorf("a refused request reached the engine %d times", n)
	}
}

// The refusals of the engine are worded by the table every method shares; what is the
// method's own is that a request with several instances is told to name one in the
// field `instance`, and that no value of the wake information is in the answer.
func TestSessionsAttachNamesTheInstanceFieldWhenSeveralCouldTakeTheSession(t *testing.T) {
	r := newRig(t)
	r.svc.attach = func(context.Context, engine.AttachRequest) (engine.AttachResult, error) {
		return engine.AttachResult{}, &engine.Refusal{Reason: engine.ReasonInvalidArgument, Field: "instance", Detail: engine.DetailInstanceRequired}
	}
	status, out := r.post("sessions.attach", `{"params":{"adapter":"chat","native_ref":"n","wake":{"WAKE_KEY":"wake-value"}}}`)
	e := r.wantError(400, "invalid_argument", "instance_required", status, out)
	if got, _ := e.Error.Details["field"].(string); got != "instance" {
		t.Errorf("the field is %q, want %q", got, "instance")
	}
	if strings.Contains(string(out), "wake-value") {
		t.Errorf("a value of the wake information is in the answer: %s", out)
	}
}

// The owner's list names how a session endpoint came to be, and says nothing of the
// kind about the others.
func TestEndpointsListNamesTheOriginOfASessionEndpointOnly(t *testing.T) {
	r := newRig(t)
	view := func(b byte, kind adapterreg.EndpointKind, o adapterreg.SessionOrigin) engine.EndpointView {
		return engine.EndpointView{
			ID: endpointOf(b), Address: ident.Address{Node: ownNode, Endpoint: endpointOf(b)}, Kind: kind, Instance: "chat",
			Status: store.EndpointOpen, Origin: o,
		}
	}
	r.svc.endpoints = []engine.EndpointView{
		view(1, adapterreg.KindDirect, adapterreg.OriginCreated),
		view(2, adapterreg.KindSession, adapterreg.OriginCreated),
		view(3, adapterreg.KindSession, adapterreg.OriginAttached),
	}
	status, out := r.post("endpoints.list", `{}`)
	if status != 200 {
		t.Fatalf("answered %d %s", status, out)
	}
	got := string(out)
	for _, want := range []string{
		`"address":"` + addressOf(1) + `","kind":"direct","instance":"chat","state":"open","queued":0,"draining":false}`,
		`"address":"` + addressOf(2) + `","kind":"session","instance":"chat","state":"open","queued":0,"draining":false,"origin":"created"}`,
		`"address":"` + addressOf(3) + `","kind":"session","instance":"chat","state":"open","queued":0,"draining":false,"origin":"attached"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the list lacks %s\n%s", want, got)
		}
	}
}
