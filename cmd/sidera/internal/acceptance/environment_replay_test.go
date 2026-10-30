//go:build acceptance

package acceptance

import (
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestDirectControlReplayAcrossGenerationClosesNativeSessionOnce(t *testing.T) {
	f, ctx := profiledFixture(t, fixtureProfile{mainInstance: "chat", preset: "default", runtimeAdapter: true})
	if err := f.Start(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	e := newEnvironment(ctx, f)
	if _, err := e.Direct("sender"); err != nil {
		t.Fatal(err)
	}
	raw, err := f.Manage(ctx, "endpoints.list", manageapi.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var endpoints struct {
		Endpoints []struct{ Address, Kind, Instance, State string } `json:"endpoints"`
	}
	if err := resultBody(raw, &endpoints); err != nil {
		t.Fatal(err)
	}
	var runtime string
	for _, ep := range endpoints.Endpoints {
		if ep.Kind == "runtime" && ep.Instance == "chat" && ep.State == "open" {
			if runtime != "" {
				t.Fatal("runtime profile has multiple open runtime endpoints")
			}
			runtime = ep.Address
		}
	}
	if runtime == "" {
		t.Fatal("runtime profile has no open runtime endpoint")
	}
	a := e.Agent("sender")
	created, err := a.Control(ctx, fakeagent.ControlInput{To: runtime, Signal: adapterproto.SignalSessionCreate, Preset: "default"})
	if err != nil || created.Replay != fakeagent.ReplayFresh || !reflect.DeepEqual(created.To, []string{runtime}) {
		t.Fatalf("unkeyed creation: %+v %v", created, err)
	}
	if err := e.Quiesce(); err != nil {
		t.Fatal(err)
	}
	e.Advance(time.Minute)
	if err := e.Quiesce(); err != nil {
		t.Fatal(err)
	}
	status, err := a.Status(ctx, fakeagent.StatusInput{MessageID: created.MessageID})
	if err != nil || status.ControlReply == nil || status.ControlReply.Create == nil || status.ControlReply.Create.Outcome != adapterproto.CreateReplyCreated {
		t.Fatalf("actual session creation: %+v %v", status, err)
	}
	address, err := ident.ParseAddress(runtime)
	if err != nil {
		t.Fatal(err)
	}
	address.Endpoint, err = ident.ParseEndpointID(string(status.ControlReply.Create.SessionEndpoint))
	if err != nil {
		t.Fatal(err)
	}
	// The close step and its recovery drain use this exact keyed request.
	request := fakeagent.ControlInput{To: address.String(), Signal: adapterproto.SignalSessionClose, IdemKey: "retention-close"}
	closed, err := a.Control(ctx, request)
	if err != nil || closed.MessageID.Validate() != nil || closed.Replay != fakeagent.ReplayUnknown || !reflect.DeepEqual(closed.To, []string{request.To}) {
		t.Fatalf("keyed close: %+v %v", closed, err)
	}
	repeat := func() {
		t.Helper()
		again, err := a.Control(ctx, request)
		if err != nil || again.MessageID != closed.MessageID || again.Replay != fakeagent.ReplayUnknown || !reflect.DeepEqual(again.To, closed.To) {
			t.Fatalf("repeated keyed close: %+v %v", again, err)
		}
	}
	repeat()
	settled := func() {
		t.Helper()
		if err := e.Quiesce(); err != nil {
			t.Fatal(err)
		}
		e.Advance(time.Minute)
		if err := e.Quiesce(); err != nil {
			t.Fatal(err)
		}
		status, err := a.Status(ctx, fakeagent.StatusInput{MessageID: closed.MessageID})
		if err != nil || status.ControlReply == nil || status.ControlReply.Close == nil || status.ControlReply.Close.Outcome != adapterproto.CloseReplyClosed {
			t.Fatalf("actual close result: %+v %v", status, err)
		}
	}
	settled()
	log, err := e.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	createdNative := log.Kind(ledger.KindRuntimeSessionCreated)
	if len(createdNative) != 1 {
		t.Fatalf("native session creations: %+v", createdNative)
	}
	writer := createdNative[0].Writer
	closeCalls := len(log.Kind(ledger.KindCloseReceived))
	assertNativeClosedOnce := func() {
		t.Helper()
		log, err := e.Ledger()
		if err != nil {
			t.Fatal(err)
		}
		closes := log.Kind(ledger.KindRuntimeClosed)
		if len(closes) != 1 || closes[0].Writer != writer {
			t.Fatalf("the created native session did not close exactly once: %+v", closes)
		}
		if len(log.Kind(ledger.KindCloseReceived)) != closeCalls {
			t.Fatalf("keyed replay dispatched another adapter close: %+v", log.Kind(ledger.KindCloseReceived))
		}
	}
	assertNativeClosedOnce()
	if err := f.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadTrace(1); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, GenerationSpec{Number: 2}); err != nil {
		t.Fatal(err)
	}
	if err := e.resetGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	repeat()
	settled()
	assertNativeClosedOnce()
}
