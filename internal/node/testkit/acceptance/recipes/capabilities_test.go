package recipes_test

import (
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/node/txn"
	"testing"
	"time"
)

func TestSelectRequiresEveryDeclaredCapability(t *testing.T) {
	needs := []recipes.Needs{{RuntimeAdapter: true}, {ExtraInstances: []string{"extra"}}, {ArchiveSweepEvery: time.Hour}, {PackageInput: true}, {Damage: recipes.DamageSource}, {FSFaults: []recipes.FSFault{recipes.FaultSourceUpdate, recipes.FaultRetainLostBody}}}
	catalog := recipes.Catalog{}
	for i, n := range needs {
		catalog.Scripts = append(catalog.Scripts, recipes.ScriptRecipe{Needs: n, Script: scenario.Script{Name: string(rune('a' + i))}})
	}
	if got := catalog.Select(recipes.Capabilities{}); len(got.Scripts) != 0 {
		t.Fatalf("unsupported recipes selected: %+v", got)
	}
	if got := catalog.Select(recipes.BinaryCapabilities()); len(got.Scripts) != len(needs)-1 {
		t.Fatalf("binary selection: %+v", got)
	}
	capabilities := recipes.BinaryCapabilities()
	capabilities.FSFaults = []recipes.FSFault{recipes.FaultSourceUpdate}
	if capabilities.Supports(needs[len(needs)-1]) {
		t.Fatal("only one of two required faults was sufficient")
	}
	catalog.Startups = []recipes.StartupRecipe{{Needs: recipes.Needs{Damage: recipes.DamageLayout}}}
	if len(catalog.Select(recipes.Capabilities{}).Startups) != 0 || len(catalog.Select(recipes.BinaryCapabilities()).Startups) != 1 {
		t.Fatal("startup needs ignored")
	}
}

func TestLayerEvidenceUsesAnyReachableSourceAndKeepsTargetProvenance(t *testing.T) {
	table, err := txn.NewTable(&txn.Decl{Name: "write", Kind: txn.Normal, Writes: []txn.Write{{Role: "file", Op: txn.OpReplace}}})
	if err != nil {
		t.Fatal(err)
	}
	hit := func(step fsx.Step, gen int) crashwalk.Hit {
		return crashwalk.Hit{Gen: gen, Point: fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "write", Role: "file"}, Step: step}, Hit: 3}}
	}
	catalog := recipes.Catalog{Scripts: []recipes.ScriptRecipe{
		{Script: scenario.Script{Name: "a-fault"}, Needs: recipes.Needs{FSFaults: []recipes.FSFault{recipes.FaultSourceUpdate}}},
		{Script: scenario.Script{Name: "z-binary"}},
	}}
	clean := map[string][]crashwalk.Hit{"a-fault": {hit(fsx.StepCreate, 1), hit(fsx.StepWrite, 1)}, "z-binary": {hit(fsx.StepCreate, 1)}}
	trigger := crashwalk.CorruptOffline("source", "unreadable")
	recovered := []crashwalk.RecoveryRecord{{Scenario: "a-fault", After: trigger, Hits: []crashwalk.Hit{hit(fsx.StepRename, 2)}}, {Scenario: "z-binary", After: trigger, Hits: []crashwalk.Hit{hit(fsx.StepRename, 2)}}}
	evidence, err := catalog.AnalyzeLayer(table, recipes.BinaryCapabilities(), clean, recovered)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Reachable) != 2 || len(evidence.InProcessOnly) != 1 || evidence.InProcessOnly[0].Step != fsx.StepWrite {
		t.Fatalf("layer keys: %+v", evidence)
	}
	if len(evidence.Analysis.Targets) != 1 || evidence.Analysis.Targets[0].Scenario != "z-binary" || evidence.Analysis.Targets[0].After != trigger || evidence.Analysis.Targets[0].Target.Point.Hit != 3 {
		t.Fatalf("target lost reachable provenance: %+v", evidence.Analysis.Targets)
	}
}

func TestTerminalDescriptionsParticipateInSelection(t *testing.T) {
	catalog := recipes.Catalog{Terminals: []recipes.TerminalRecipe{{Name: "terminal", Needs: recipes.Needs{RuntimeAdapter: true}}}}
	if len(catalog.Select(recipes.Capabilities{}).Terminals) != 0 || len(catalog.Select(recipes.BinaryCapabilities()).Terminals) != 1 {
		t.Fatal("terminal capability selection was skipped")
	}
}
