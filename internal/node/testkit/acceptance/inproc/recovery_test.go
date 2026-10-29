package inproc

import (
	"context"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func TestCatalogRecoveryAssignments(t *testing.T) {
	scenarios := bindCatalog(t)
	if err := crashwalk.CheckRecoveryCases(harness.ProgramTable(t), scenarios); err != nil {
		t.Fatal(err)
	}
}

// recoveryEvidence interrupts recovery using the catalog's complete recordings.
func recoveryEvidence(t *testing.T, evidence CatalogEvidence, cleanHits []crashwalk.Hit) crashwalk.SourceAnalysis {
	t.Helper()
	cleanRecordings := evidence.Clean
	recordings := map[crashwalk.RecoveryTrigger][]crashwalk.Hit{}
	var all []crashwalk.Hit
	for _, r := range evidence.Recovery {
		id := crashwalk.RecoveryTrigger{Scenario: r.Scenario, After: r.After}
		recordings[id] = r.Hits
		all = append(all, r.Hits...)
	}
	expectedTargets := 0
	for _, s := range bindCatalog(t) {
		expectedTargets += len(s.Recovery())
	}
	count := 0
	derivedCount := 0
	analysis, err := crashwalk.AnalyzeSources(harness.ProgramTable(t), cleanHits, evidence.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range bindCatalog(t) {
		for _, c := range s.Recovery() {
			t.Run("target/"+s.Name()+"/"+c.Item.String(), func(t *testing.T) {
				target := crashwalk.Target{Gen: 2, Point: fsx.Point{Key: c.Item, Hit: 1}}
				started := clock.System().Now()
				r, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, s.Name()), crashwalk.RunPlan{Trigger: &c.After, Targets: []crashwalk.Target{target}})
				if err != nil {
					t.Fatal(err)
				}
				assertDeclaredWrites(t, r)
				if err := crashwalk.CheckRecoveryTrigger(cleanRecordings[s.Name()], c.After, r); err != nil {
					t.Fatal(err)
				}
				if err := crashwalk.CheckRecoveryTarget(recordings[crashwalk.RecoveryTrigger{Scenario: s.Name(), After: c.After}], target, r); err != nil {
					t.Fatal(err)
				}
				count++
				t.Logf("recovery-crash scenario=%s target=%s reached=true generations=3 elapsed=%s", s.Name(), target.Point, clock.System().Now().Sub(started))
			})
		}
	}
	for _, key := range analysis.Sources.NormalOnlyInRecovery {
		t.Logf("recovery-only-normal key=%s gen=2", key)
	}
	for _, derived := range analysis.Targets {
		id := crashwalk.RecoveryTrigger{Scenario: derived.Scenario, After: derived.After}
		target := derived.Target
		t.Run("derived/"+target.Point.Key.String(), func(t *testing.T) {
			started := clock.System().Now()
			r, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, derived.Scenario), crashwalk.RunPlan{Trigger: &derived.After, Targets: []crashwalk.Target{target}})
			if err != nil {
				t.Fatal(err)
			}
			assertDeclaredWrites(t, r)
			if err := crashwalk.CheckRecoveryTrigger(cleanRecordings[derived.Scenario], derived.After, r); err != nil {
				t.Fatal(err)
			}
			if err := crashwalk.CheckRecoveryTarget(recordings[id], target, r); err != nil {
				t.Fatal(err)
			}
			derivedCount++
			t.Logf("recovery-normal-crash scenario=%s target=%s reached=true generations=3 elapsed=%s", derived.Scenario, target.Point, clock.System().Now().Sub(started))
		})
	}
	if derivedCount != len(analysis.Targets) {
		t.Errorf("recovery-only Normal targets completed=%d expected=%d", derivedCount, len(analysis.Targets))
	}
	sources := analysis.Sources
	t.Logf("coverage-columns clean_gen1_normal=%d recovery_only_gen2_normal=%d complete_gen2_recovery=%d", len(sources.NormalInClean), len(sources.NormalOnlyInRecovery), len(sources.RecoveryInRecovery))
	c := analysis.Coverage
	t.Logf("coverage normal=%d/%d recovery=%d/%d recovery_completed=%d recovery_only_normal_keys=%d recovery_only_normal_completed=%d missing_normal=%v missing_recovery=%v", c.Normal.Reached, c.Normal.Declared, c.Recovery.Reached, c.Recovery.Declared, count, len(sources.NormalOnlyInRecovery), derivedCount, c.MissingNormal, c.MissingRecovery)
	if count != expectedTargets {
		t.Errorf("Recovery targets completed=%d expected=%d", count, expectedTargets)
	}
	if len(c.MissingNormal) != 0 || len(c.MissingRecovery) != 0 || len(c.Undeclared) != 0 {
		t.Errorf("incomplete catalog evidence: missing_normal=%v missing_recovery=%v undeclared=%v", c.MissingNormal, c.MissingRecovery, c.Undeclared)
	}
	t.Logf("recovery completed=%d records=%d", count, len(recordings))
	t.Logf("runs record=%d recovery_record=%d recovery_crash=%d recovery_normal_crash=%d total=%d", len(evidence.Clean), len(recordings), count, derivedCount, len(evidence.Clean)+len(recordings)+count+derivedCount)
	reportCoverage(t, "complete-gen2", all)
	return analysis
}

func assertDeclaredWrites(t testing.TB, r crashwalk.RunResult) {
	t.Helper()
	if err := checkDeclaredWrites(harness.ProgramTable(t), r); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryJudgementRejectsAnUnreadLossNotice(t *testing.T) {
	s := freshScenario(t, "recovery-target").(*harness.WalkScenario)
	s.Drain = func(*harness.H) error { return nil }
	trigger := crashwalk.CorruptOffline("target", "unreadable")
	r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Trigger: &trigger})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Violations) != 1 || !strings.Contains(r.Violations[0], "has no visible body or corresponding loss feedback") {
		t.Fatalf("unread loss notice violations=%v", r.Violations)
	}
}
