package acceptance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
)

func TestUnobservedPreparationLeavesGenerationOneForDaemon(t *testing.T) {
	f, ctx := fixture(t)
	if err := f.prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.gens) != 0 {
		t.Fatalf("preparation registered observed generations: %v", f.gens)
	}
	if err := f.Start(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	if err := f.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	trace, err := f.ReadTrace(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Hits) == 0 {
		t.Fatal("daemon generation one has no actual trace")
	}
}

func TestEnvironmentRetainsClockFailureUntilNewGeneration(t *testing.T) {
	f, ctx := fixture(t)
	if err := f.prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	e := newEnvironment(ctx, f)
	if _, err := e.Direct("sender"); err != nil {
		t.Fatal(err)
	}
	if err := f.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	to := f.Now().Add(time.Minute)
	e.Set(to)
	failure := e.Quiesce()
	if failure == nil {
		t.Fatal("lost clock RPC did not fail quiet")
	}
	if !f.Now().Equal(to) {
		t.Fatal("clock target lost after failed RPC")
	}
	if _, err := e.Adapter("fake"); err != failure {
		t.Fatalf("operation did not retain clock failure: %v", err)
	}
	if _, err := e.Agent("sender").Send(ctx, fakeagent.SendInput{To: []string{e.Addr("sender")}, Body: "The retained clock error must be returned."}); err != failure {
		t.Fatalf("first agent call did not return clock failure: %v", err)
	}
	if err := e.resetGeneration(ctx); err == nil {
		t.Fatal("same generation cleared failure")
	}
	if err := f.Start(ctx, GenerationSpec{Number: 2}); err != nil {
		t.Fatal(err)
	}
	if e.Quiesce() != failure {
		t.Fatal("start implicitly cleared clock failure")
	}
	if err := e.resetGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.Quiesce(); err != nil {
		t.Fatal(err)
	}
}

func profiledFixture(t *testing.T, profile fixtureProfile) (*Fixture, context.Context) {
	t.Helper()
	// Real CLI calls in race builds also wait for each instrumented child to exit.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f, err := newFixture(ctx, runRoot(t), testBinaries, Options{Seed: t.Name()}, profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := f.prepare(ctx); err != nil {
		t.Fatal(err)
	}
	return f, ctx
}

func TestProfileInstallsExtraAndWarningInputsWithoutRaisingAlert(t *testing.T) {
	f, ctx := profiledFixture(t, fixtureProfile{mainInstance: "chat", preset: "default", extraInstances: []string{"extra"}, probeWarning: true})
	if err := f.Start(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	raw, err := f.Manage(ctx, "adapters.list", manageapi.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Adapters []struct {
			ID string `json:"id"`
		} `json:"adapters"`
		Instances []instance `json:"instances"`
	}
	if err := resultBody(raw, &listed); err != nil {
		t.Fatal(err)
	}
	installed := map[string]bool{}
	for _, a := range listed.Adapters {
		installed[a.ID] = true
	}
	if !installed["chat"] || !installed["extra"] || !installed[versionWarningInstance] {
		t.Fatalf("installed inputs: %+v", listed.Adapters)
	}
	for _, in := range listed.Instances {
		if in.Instance == versionWarningInstance && in.Enabled {
			t.Fatal("warning input enabled before recipe action")
		}
	}
	raw, err = f.Manage(ctx, "alerts.list", manageapi.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var alerts struct {
		Alerts []json.RawMessage `json:"alerts"`
	}
	if err := resultBody(raw, &alerts); err != nil {
		t.Fatal(err)
	}
	if len(alerts.Alerts) != 0 {
		t.Fatalf("warning input raised alerts during preparation: %s", raw)
	}
	e := newEnvironment(ctx, f)
	if _, err := e.directOn("extra", "outside"); err != nil {
		t.Fatal(err)
	}
	if err := f.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(ctx, GenerationSpec{Number: 2}); err != nil {
		t.Fatal(err)
	}
	if err := e.resetGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	ctl, err := e.Adapter("extra")
	if err != nil {
		t.Fatal(err)
	}
	if err := ctl.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}
