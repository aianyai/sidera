//go:build acceptance

package acceptance

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
)

func TestBinaryManagementBindingsPreserveRequestsAndEffects(t *testing.T) {
	f, ctx := profiledFixture(t, fixtureProfile{mainInstance: "chat", preset: "default", extraInstances: []string{"extra"}})
	if err := f.Start(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	e := newEnvironment(ctx, f)
	ops := newManagementOps()
	versions, err := ops.Versions(e)
	if err != nil {
		t.Fatal(err)
	}
	q := recipes.ManagementRequest{ID: admin.OperationID{41}, Expected: versions.Node}
	changed, err := ops.NodeSet(e, q, `{"node_queue_limit":9000}`)
	if err != nil || changed.Value != "9000" || changed.Version != q.Expected+1 || changed.Replayed {
		t.Fatalf("node set: %+v %v", changed, err)
	}
	stale, err := ops.NodeSet(e, recipes.ManagementRequest{ID: admin.OperationID{42}, Expected: q.Expected}, `{"node_queue_limit":8000}`)
	if err != nil || stale.Code != "version_conflict" {
		t.Fatalf("stale: %+v %v", stale, err)
	}
	node, err := ops.InspectNode(e)
	if err != nil || node.QueueLimit != 9000 || node.Version != changed.Version {
		t.Fatalf("node facts: %+v %v", node, err)
	}
	added, err := ops.PresetAdd(e, recipes.ManagementRequest{ID: admin.OperationID{43}, Expected: versions.Presets}, "acceptance", `{"local_sources":"deny"}`)
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := ops.InspectPreset(e, "acceptance"); err != nil || !exists {
		t.Fatalf("added preset: %v %v", exists, err)
	}
	if _, err := ops.PresetRemove(e, recipes.ManagementRequest{ID: admin.OperationID{44}, Expected: added.Version}, "acceptance"); err != nil {
		t.Fatal(err)
	}
	if exists, err := ops.InspectPreset(e, "acceptance"); err != nil || exists {
		t.Fatalf("removed preset: %v %v", exists, err)
	}
	address, err := ops.DirectOn(e, "extra", "managed")
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ops.EndpointSet(e, recipes.ManagementRequest{ID: admin.OperationID{45}, Expected: versions.Instances}, address, `{"backlog_limit":9}`)
	if err != nil || endpoint.Value != "9" {
		t.Fatalf("endpoint set: %+v %v", endpoint, err)
	}
	if facts, err := ops.InspectEndpoint(e, address); err != nil || facts.BacklogLimit != 9 || facts.Closed || facts.TokenCleared {
		t.Fatalf("endpoint facts: %+v %v", facts, err)
	}
	disabled, err := ops.DisableAdapter(e, recipes.ManagementRequest{ID: admin.OperationID{71}, Expected: endpoint.Version}, "extra")
	if err != nil {
		t.Fatal(err)
	}
	if facts, err := ops.InspectInstance(e, "extra"); err != nil || facts.Enabled || facts.OpenEndpoints != 0 {
		t.Fatalf("disabled facts: %+v %v", facts, err)
	}
	if facts, err := ops.InspectEndpoint(e, address); err != nil || !facts.Closed || !facts.TokenCleared {
		t.Fatalf("disabled endpoint: %+v %v", facts, err)
	}
	if _, err := ops.EnableAdapter(e, recipes.ManagementRequest{ID: admin.OperationID{72}, Expected: disabled.Version}, "extra", "extra"); err != nil {
		t.Fatal(err)
	}
	if err := e.Quiesce(); err != nil {
		t.Fatal(err)
	}
	if facts, err := ops.InspectInstance(e, "extra"); err != nil || !facts.Enabled {
		t.Fatalf("enabled facts: %+v %v", facts, err)
	}
	address, err = e.Direct("closing")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := ops.CloseEndpoint(e, recipes.ManagementRequest{ID: admin.OperationID{61}}, address); err != nil || out.Value != "closed" {
		t.Fatalf("close: %+v %v", out, err)
	}
	if out, err := ops.ConfirmBackup(e, recipes.ManagementRequest{ID: admin.OperationID{52}}); err != nil || out.Value != "confirmed" {
		t.Fatalf("backup: %+v %v", out, err)
	}
	e.Set(f.Now().Add(-time.Hour))
	if err := e.Quiesce(); err != nil {
		t.Fatal(err)
	}
	if out, err := ops.ClearClock(e, recipes.ManagementRequest{ID: admin.OperationID{53}}); err != nil || out.Value != "true" {
		t.Fatalf("clock: %+v %v", out, err)
	}
	if node, err := ops.InspectNode(e); err != nil || !node.BackupConfirmed || node.ClockPaused {
		t.Fatalf("maintenance facts: %+v %v", node, err)
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
	replay, err := ops.NodeSet(e, q, `{"node_queue_limit":9000}`)
	if err != nil || !replay.Replayed || replay.Value != changed.Value || replay.Version != changed.Version {
		t.Fatalf("restart replay: %+v %v", replay, err)
	}
}

func TestBinaryVersionAlertResumesCommittedEnable(t *testing.T) {
	f, ctx := profiledFixture(t, fixtureProfile{mainInstance: "chat", preset: "default", probeWarning: true})
	const target = "adapter-enable/commit#2"
	if err := f.Start(ctx, GenerationSpec{Number: 1, Target: target}); err != nil {
		t.Fatal(err)
	}
	e := newEnvironment(ctx, f)
	ops := newManagementOps()
	if _, err := ops.CreateVersionAlert(e); err == nil {
		t.Fatal("targeted enable unexpectedly answered")
	}
	if err := f.current.p.wait(ctx); err == nil {
		t.Fatal("targeted daemon exited successfully")
	}
	trace, err := f.ReadTrace(1)
	if err != nil || !trace.Reached || trace.Crash != target {
		t.Fatalf("enable crash: %+v %v", trace, err)
	}
	if err := f.Start(ctx, GenerationSpec{Number: 2}); err != nil {
		t.Fatal(err)
	}
	if err := e.resetGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	alert, err := ops.CreateVersionAlert(e)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := ops.CreateVersionAlert(e); err != nil || again != alert {
		t.Fatalf("repeat alert: %q %v", again, err)
	}
	if out, err := ops.AcknowledgeAlert(e, recipes.ManagementRequest{ID: admin.OperationID{51}}, alert); err != nil || out.Value != alert {
		t.Fatalf("ack: %+v %v", out, err)
	}
	if acked, err := ops.InspectAlert(e, alert); err != nil || !acked {
		t.Fatalf("alert acknowledgement: %v %v", acked, err)
	}
}
