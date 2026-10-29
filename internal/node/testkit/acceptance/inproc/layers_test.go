package inproc

import (
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func reportLayerEvidence(t *testing.T, clean map[string]crashwalk.RunResult, recovered []crashwalk.RecoveryRecord) {
	t.Helper()
	recordings := map[string][]crashwalk.Hit{}
	for name, result := range clean {
		recordings[name] = result.Hits[0]
	}
	catalog := recipes.New(newBindings())
	for _, layer := range []struct {
		name         string
		capabilities recipes.Capabilities
	}{{"inproc", inProcessCapabilities()}, {"binary", recipes.BinaryCapabilities()}} {
		evidence, err := catalog.AnalyzeLayer(harness.ProgramTable(t), layer.capabilities, recordings, recovered)
		if err != nil {
			t.Fatal(err)
		}
		coverage := evidence.Analysis.Coverage
		t.Logf("layer=%s normal=%d/%d recovery=%d/%d reachable=%d inproc_only=%d recovery_normal_targets=%d", layer.name, coverage.Normal.Reached, coverage.Normal.Declared, coverage.Recovery.Reached, coverage.Recovery.Declared, len(evidence.Reachable), len(evidence.InProcessOnly), len(evidence.Analysis.Targets))
		t.Logf("layer=%s inproc_only_keys=%v", layer.name, evidence.InProcessOnly)
		for _, target := range evidence.Analysis.Targets {
			t.Logf("layer=%s recovery_normal_target=%s scenario=%s trigger=%+v", layer.name, target.Target.Point, target.Scenario, target.After)
		}
		if len(evidence.Reachable)+len(evidence.InProcessOnly) != len(harness.ProgramTable(t).Keys()) {
			t.Errorf("layer %s does not partition complete catalog evidence", layer.name)
		}
		if layer.name == "inproc" && len(evidence.InProcessOnly) != 0 {
			t.Errorf("in-process binding cannot provide complete evidence: %v", evidence.InProcessOnly)
		}
	}
}
