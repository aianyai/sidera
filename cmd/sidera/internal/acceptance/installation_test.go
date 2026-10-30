package acceptance

import (
	"slices"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
)

func TestBinaryInstallationBindingsUpgradeAndReplayAcrossGeneration(t *testing.T) {
	f, ctx := profiledFixture(t, fixtureProfile{mainInstance: "chat", preset: "default", packageInput: true})
	if err := f.Start(ctx, GenerationSpec{Number: 1}); err != nil {
		t.Fatal(err)
	}
	e := newEnvironment(ctx, f)
	ops := newInstallOps()
	v1 := recipes.PackageSpec{ID: "acceptance-package", Version: "1.0.0"}
	v2 := recipes.PackageSpec{ID: v1.ID, Version: "2.0.0", NestedFile: true}
	for i, spec := range []recipes.PackageSpec{v1, v2, v2} {
		out, err := ops.Install(e, admin.OperationID{byte(31 + i)}, spec)
		if err != nil || out.ID != spec.ID || out.Version != spec.Version || out.AlreadyCurrent != (i == 2) || out.Replayed || out.Code != "" {
			t.Fatalf("install: %+v %v", out, err)
		}
		facts, err := ops.Inspect(e, spec.ID)
		if err != nil || facts.Current != spec.Version || !slices.Equal(facts.Versions, []string{spec.Version}) {
			t.Fatalf("package: %+v %v", facts, err)
		}
	}
	foreign := recipes.PackageSpec{ID: "acceptance-foreign", Version: "1.0.0", Unsupported: true}
	if out, err := ops.Install(e, admin.OperationID{36}, foreign); err != nil || out.Code != "platform_unsupported" {
		t.Fatalf("foreign: %+v %v", out, err)
	}
	if facts, err := ops.Inspect(e, foreign.ID); err != nil || facts.Current != "" || len(facts.Versions) != 0 {
		t.Fatalf("foreign residue: %+v %v", facts, err)
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
	out, err := ops.Install(e, admin.OperationID{32}, v2)
	if err != nil || !out.Replayed || out.AlreadyCurrent || out.ID != v2.ID || out.Version != v2.Version {
		t.Fatalf("restart replay: %+v %v", out, err)
	}
}
