package inproc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
)

func TestCatalogRecoveryAssignments(t *testing.T) {
	scenarios := bindCatalog(t)
	if err := crashwalk.CheckRecoveryCases(harness.ProgramTable(t), scenarios); err != nil {
		t.Fatal(err)
	}
	expected, assigned := 0, 0
	for _, key := range harness.ProgramTable(t).Keys() {
		if key.Kind == txn.Recovery && !crashwalk.IsSync(key.Key.Step) {
			expected++
		}
	}
	for _, s := range scenarios {
		assigned += len(s.Recovery())
	}
	t.Logf("recovery assigned=%d/%d", assigned, expected)
}

// recoveryEvidence records each recipe/trigger once, then reuses that complete
// second generation for all of its independently interrupted recovery points.
func recoveryEvidence(t *testing.T, clean map[string]crashwalk.RunResult, cleanHits []crashwalk.Hit) crashwalk.SourceAnalysis {
	t.Helper()
	recordings := map[crashwalk.RecoveryTrigger][]crashwalk.Hit{}
	planned := map[crashwalk.RecoveryTrigger]bool{}
	expectedTargets := 0
	for _, s := range bindCatalog(t) {
		expectedTargets += len(s.Recovery())
	}
	var all []crashwalk.Hit
	var recovered []crashwalk.RecoveryRecord
	count := 0
	derivedCount := 0
	for _, s := range bindCatalog(t) {
		triggers := []crashwalk.Trigger{}
		for _, c := range s.Recovery() {
			triggers = append(triggers, c.After)
		}
		for _, r := range recipes.New(newBindings()).Scripts {
			if r.Script.Name == s.Name() {
				triggers = append(triggers, r.RecoveryTriggers...)
			}
		}
		for _, trigger := range triggers {
			id := crashwalk.RecoveryTrigger{Scenario: s.Name(), After: trigger}
			if planned[id] {
				continue
			}
			planned[id] = true
			t.Run("record/"+s.Name()+"/"+fmt.Sprint(trigger), func(t *testing.T) {
				started := clock.System().Now()
				r, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, s.Name()), crashwalk.RunPlan{Trigger: &trigger})
				if err != nil {
					t.Fatal(err)
				}
				if len(r.Hits) != 2 || len(r.Violations) != 0 {
					t.Fatalf("complete recovery requires two generations without violations: generations=%d violations=%v", len(r.Hits), r.Violations)
				}
				hits := r.Hits[1]
				assertDeclaredWrites(t, r)
				if err := compareFirstCrash(clean, id, r); err != nil {
					t.Fatal(err)
				}
				recordings[id] = hits
				recovered = append(recovered, crashwalk.RecoveryRecord{Scenario: s.Name(), After: trigger, Hits: hits})
				all = append(all, hits...)
				t.Logf("recovery-record scenario=%s trigger=%+v gen2_keys=%d elapsed=%s", s.Name(), trigger, len(hitKeys(hits)), clock.System().Now().Sub(started))
				t.Logf("recovery-record scenario=%s gen=2 keys=%v", s.Name(), hitKeys(hits))
				reportProvenance(t, s.Name(), fmt.Sprint(trigger), hits)
			})
		}
	}
	analysis, err := crashwalk.AnalyzeSources(harness.ProgramTable(t), cleanHits, recovered)
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
				if err := compareFirstCrash(clean, crashwalk.RecoveryTrigger{Scenario: s.Name(), After: c.After}, r); err != nil {
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
			if err := compareFirstCrash(clean, id, r); err != nil {
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
	if len(recordings) != len(planned) {
		t.Errorf("complete recovery recordings=%d expected=%d", len(recordings), len(planned))
	}
	if count != expectedTargets {
		t.Errorf("Recovery targets completed=%d expected=%d", count, expectedTargets)
	}
	if len(c.MissingNormal) != 0 || len(c.MissingRecovery) != 0 || len(c.Undeclared) != 0 {
		t.Errorf("incomplete catalog evidence: missing_normal=%v missing_recovery=%v undeclared=%v", c.MissingNormal, c.MissingRecovery, c.Undeclared)
	}
	t.Logf("recovery completed=%d records=%d", count, len(recordings))
	t.Logf("runs record=%d recovery_record=%d recovery_crash=%d recovery_normal_crash=%d total=%d", len(clean), len(recordings), count, derivedCount, len(clean)+len(recordings)+count+derivedCount)
	reportCoverage(t, "complete-gen2", all)
	return analysis
}

func assertDeclaredWrites(t testing.TB, r crashwalk.RunResult) {
	t.Helper()
	for i, hits := range r.Hits {
		c := crashwalk.Assess(harness.ProgramTable(t), hits)
		if len(c.Undeclared) != 0 {
			t.Fatalf("generation %d undeclared=%v", i+1, c.Undeclared)
		}
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

func compareFirstCrash(clean map[string]crashwalk.RunResult, id crashwalk.RecoveryTrigger, r crashwalk.RunResult) error {
	if id.After.Kind != crashwalk.TriggerCrashAt {
		return nil
	}
	recorded, ok := clean[id.Scenario]
	if !ok || len(recorded.Hits) != 1 || len(r.Hits) == 0 {
		return fmt.Errorf("missing clean first-generation evidence for %s", id.Scenario)
	}
	return crashwalk.CompareRuns(recorded.Hits[0], r.Hits[0], crashwalk.Target{Gen: 1, Point: id.After.CrashAt})
}
