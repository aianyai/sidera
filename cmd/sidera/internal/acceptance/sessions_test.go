package acceptance

import (
	"errors"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestSessionInspectionTracksRealCloseAndRetention(t *testing.T) {
	f, ctx := profiledFixture(t, fixtureProfile{mainInstance: "chat", preset: "default", runtimeAdapter: true})
	if err := f.Start(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	e := newEnvironment(ctx, f)
	if _, err := e.Direct("sender"); err != nil {
		t.Fatal(err)
	}
	runtime := runtimeAddress(e, "chat")
	a := e.Agent("sender")
	created, err := a.Control(ctx, fakeagent.ControlInput{To: runtime, Signal: adapterproto.SignalSessionCreate, Preset: "default"})
	if err != nil {
		t.Fatal(err)
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
		t.Fatalf("actual creation: %+v %v", status, err)
	}
	endpoint := string(status.ControlReply.Create.SessionEndpoint)
	if err := checkpointPut(e, "chat", adapterproto.CheckpointPutParams{Scope: adapterproto.ScopeSession, ID: endpoint, State: []byte(`{"cursor":7}`)}); err != nil {
		t.Fatal(err)
	}
	if err := e.Quiesce(); err != nil {
		t.Fatal(err)
	}
	scan, _, err := readSessionRecords(e)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cp := range scan.Checkpoints {
		if cp.Key.Scope == store.CheckpointScopeSession && cp.Key.ID == endpoint && string(cp.State) == `{"cursor":7}` {
			found = true
		}
	}
	if !found {
		t.Fatal("real checkpoint.put did not persist the session cursor")
	}
	if err := checkClosedSession(e, endpoint); err == nil {
		t.Fatal("open session passed the closed-session audit")
	}
	address, err := ident.ParseAddress(runtime)
	if err != nil {
		t.Fatal(err)
	}
	address.Endpoint, err = ident.ParseEndpointID(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := a.Control(ctx, fakeagent.ControlInput{To: address.String(), Signal: adapterproto.SignalSessionClose})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Quiesce(); err != nil {
		t.Fatal(err)
	}
	e.Advance(time.Minute)
	if err := e.Quiesce(); err != nil {
		t.Fatal(err)
	}
	status, err = a.Status(ctx, fakeagent.StatusInput{MessageID: closed.MessageID})
	if err != nil || status.ControlReply == nil || status.ControlReply.Close == nil || status.ControlReply.Close.Outcome != adapterproto.CloseReplyClosed {
		t.Fatalf("actual close: %+v %v", status, err)
	}
	if err := checkClosedSession(e, endpoint); err != nil {
		t.Fatal(err)
	}
	facts := recipes.NewDeliveryEvidence(func(scenario.Env) (ident.NodeID, error) { return e.localNode() })
	for range 2 {
		if err := facts.RecvAndAck(e, a); err != nil {
			t.Fatal(err)
		}
		e.Advance(time.Minute)
		if err := e.Quiesce(); err != nil {
			t.Fatal(err)
		}
	}
	maintenance := newMaintenanceOps()
	before, err := maintenance.InspectRetention(e, endpoint)
	if err != nil || !before.SessionPresent || before.Records == 0 {
		t.Fatalf("closed session before retention: %+v %v", before, err)
	}
	// The real periodic task writes summaries before removing these records.
	for _, delay := range []time.Duration{3 * 24 * time.Hour, time.Hour} {
		e.Advance(delay)
		if err := e.Quiesce(); err != nil {
			t.Fatal(err)
		}
	}
	archived, err := maintenance.InspectRetention(e, endpoint)
	if err != nil || archived.Records != 0 || archived.Bodies != 0 || archived.SessionPresent || archived.Archives == 0 || archived.Summaries == 0 {
		t.Fatalf("actual archived records: %+v %v", archived, err)
	}
	e.Advance(31 * 24 * time.Hour)
	if err := e.Quiesce(); err != nil {
		t.Fatal(err)
	}
	expired, err := maintenance.InspectRetention(e, endpoint)
	if err != nil || expired != (recipes.RetentionFacts{}) {
		t.Fatalf("expired archive: %+v %v", expired, err)
	}
	if err := checkClosedSession(e, endpoint); err == nil {
		t.Fatal("a swept endpoint passed the pre-retention closed-session audit")
	}
}

func TestBinaryDomainCallbacksPreserveClockFailureAndRejectFSFaults(t *testing.T) {
	f, ctx := fixture(t)
	if err := f.prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	e := newEnvironment(ctx, f)
	if err := f.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	e.Advance(time.Minute)
	failure := e.Quiesce()
	if failure == nil {
		t.Fatal("the killed daemon accepted a clock RPC")
	}
	for name, call := range map[string]func() error{
		"source update":     func() error { return newDeliveryOps().FailSourceUpdate(e) },
		"lost body":         func() error { return newDeliveryOps().RetainLostSourceBody(e) },
		"create settlement": func() error { return failCreateSettlement(e) },
		"checkpoint":        func() error { return checkpointPut(e, "chat", adapterproto.CheckpointPutParams{}) },
		"alert":             func() error { _, err := hasUnknownCreationAlert(e, "session"); return err },
		"closed":            func() error { return checkClosedSession(e, "session") },
		"retention":         func() error { _, err := newMaintenanceOps().InspectRetention(e, "session"); return err },
	} {
		if err := call(); err != failure {
			t.Errorf("%s lost the generation failure: %v", name, err)
		}
	}
	e = newEnvironment(ctx, f)
	for _, call := range []func(scenario.Env) error{newDeliveryOps().FailSourceUpdate, newDeliveryOps().RetainLostSourceBody, failCreateSettlement} {
		if err := call(e); !errors.Is(err, scenario.ErrNotAvailable) {
			t.Errorf("filesystem fault callback did not reject unsupported injection: %v", err)
		}
	}
	if _, err := newMaintenanceOps().InspectRetention(e, "session"); !errors.Is(err, scenario.ErrNotAvailable) {
		t.Fatalf("stopped node returned retention facts: %v", err)
	}
}
