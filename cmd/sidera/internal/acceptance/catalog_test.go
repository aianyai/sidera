//go:build acceptance

package acceptance_test

import (
	"os"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera/internal/acceptance"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/inproc"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

func TestCrashAtEveryCrashPointPreservesInvariants(t *testing.T) {
	started := time.Now()
	mode, err := crashwalk.ParseMode(os.Getenv(crashwalk.EnvMode))
	if err != nil {
		t.Fatal(err)
	}
	modeName := "smoke"
	if mode == crashwalk.Full {
		modeName = "full"
	}
	shard, err := crashwalk.ParseShard(os.Getenv(crashwalk.EnvShard))
	if err != nil {
		t.Fatal(err)
	}
	table, err := daemon.Table()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("responsibility source=inproc mode=%s shard=%d/%d", modeName, shard.Index, shard.Count)
	evidence, err := inproc.RecordCatalog(t)
	if err != nil {
		t.Fatal(err)
	}
	layer, err := recipes.New(recipes.Bindings{}).AnalyzeLayer(table, recipes.BinaryCapabilities(), evidence.Clean, evidence.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("responsibility source=inproc clean_recordings=%d recovery_recordings=%d elapsed=%s", len(evidence.Clean), len(evidence.Recovery), time.Since(started))
	t.Logf("responsibility required=%d keys=%v", len(layer.Reachable), layer.Reachable)
	t.Logf("responsibility covered_elsewhere=%d keys=%v", len(layer.InProcessOnly), layer.InProcessOnly)

	scenarios, triggers := acceptance.BindCatalogForTest()
	var runStarted time.Time
	observe := func(event crashwalk.Event) {
		if event.Skipped {
			t.Logf("binary phase=%s skipped=true reason=smoke planned=%d selected=0 runs=0", event.Phase, event.Planned)
			return
		}
		if !event.Done {
			runStarted = time.Now()
			t.Logf("binary phase=%s scenario=%s target=%+v trigger=%+v case=%+v started=true", event.Phase, event.Scenario, event.Target, event.Trigger, event.Case)
			return
		}
		t.Logf("binary phase=%s scenario=%s target=%+v trigger=%+v elapsed=%s", event.Phase, event.Scenario, event.Target, event.Trigger, time.Since(runStarted))
	}
	report, err := crashwalk.Walk(t.Context(), acceptance.WalkBackendForTest(t), table, scenarios, crashwalk.Options{
		Mode: mode, Shard: shard,
		Coverage:         &crashwalk.CoveragePartition{Required: layer.Reachable, CoveredElsewhere: layer.InProcessOnly},
		RecoveryTriggers: triggers, Observe: observe,
	})
	t.Logf("binary sources clean_normal=%d keys=%v", len(report.Sources.NormalInClean), report.Sources.NormalInClean)
	t.Logf("binary sources gen2_only_normal=%d keys=%v", len(report.Sources.NormalOnlyInRecovery), report.Sources.NormalOnlyInRecovery)
	t.Logf("binary sources gen2_recovery=%d keys=%v", len(report.Sources.RecoveryInRecovery), report.Sources.RecoveryInRecovery)
	observed := map[fsx.Key]bool{}
	for _, keys := range [][]fsx.Key{report.Sources.NormalInClean, report.Sources.NormalOnlyInRecovery, report.Sources.RecoveryInRecovery} {
		for _, key := range keys {
			observed[key] = true
		}
	}
	var missingDeclared, missingRequired []fsx.Key
	for _, declaration := range table.Keys() {
		if !observed[declaration.Key] {
			missingDeclared = append(missingDeclared, declaration.Key)
		}
	}
	for _, key := range layer.Reachable {
		if !observed[key] {
			missingRequired = append(missingRequired, key)
		}
	}
	t.Logf("binary coverage normal=%d/%d recovery=%d/%d missing_declared=%v missing_required=%v", report.Normal.Reached, report.Normal.Declared, report.Recovery.Reached, report.Recovery.Declared, missingDeclared, missingRequired)
	t.Logf("binary recordings clean=%d recovery=%d runs main=%d boundary=%d static_recovery=%d recovery_normal planned=%d selected=%d runs=%d skipped=%v mode=%s shard=%d/%d elapsed=%s", report.Recordings, report.RecoveryRecordings, report.MainRuns, report.BoundaryRuns, report.RecoveryRuns, report.RecoveryNormalPlanned, report.RecoveryNormalSelected, report.RecoveryNormalRuns, report.RecoveryNormalSkipped, modeName, shard.Index, shard.Count, time.Since(started))
	if err != nil {
		t.Fatal(err)
	}
}
