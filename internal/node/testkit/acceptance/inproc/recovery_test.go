package inproc

import (
	"context"
	"fmt"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
	"sort"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

func TestRecoveryEvidenceRejectsLaterGenerationAndObservationOnly(t *testing.T) {
	key, err := fsx.ParseKey("quarantine/session.write")
	if err != nil {
		t.Fatal(err)
	}
	target := crashwalk.Target{Gen: 2, Point: fsx.Point{Key: key, Hit: 1}}
	hit := crashwalk.Hit{Gen: 3, Point: target.Point}
	if _, err := completeRecoveryGeneration(crashwalk.RunResult{Hits: [][]crashwalk.Hit{nil, nil, {hit}}}); err == nil {
		t.Fatal("third-generation evidence accepted as complete second generation")
	}
	hit.Gen = 2
	observed := crashwalk.RunResult{Hits: [][]crashwalk.Hit{nil, {hit}, nil}, Reached: []bool{false}}
	if err := verifyRecoveryTarget([]crashwalk.Hit{hit}, observed, target); err == nil {
		t.Fatal("observation without an actual second-generation crash accepted")
	}
}

func completeRecoveryGeneration(r crashwalk.RunResult) ([]crashwalk.Hit, error) {
	if len(r.Hits) != 2 || len(r.Violations) != 0 {
		return nil, fmt.Errorf("complete recovery requires two generations without violations: generations=%d violations=%v", len(r.Hits), r.Violations)
	}
	for _, h := range r.Hits[1] {
		if h.Gen != 2 {
			return nil, fmt.Errorf("second-generation evidence has generation %d", h.Gen)
		}
	}
	return r.Hits[1], nil
}

func verifyRecoveryTarget(recorded []crashwalk.Hit, r crashwalk.RunResult, target crashwalk.Target) error {
	if len(r.Reached) != 1 || !r.Reached[0] || len(r.Hits) != 3 || len(r.Violations) != 0 {
		return fmt.Errorf("target %v requires actual second crash and successful third generation: reached=%v generations=%d violations=%v", target, r.Reached, len(r.Hits), r.Violations)
	}
	for _, h := range r.Hits[1] {
		if h.Gen != 2 {
			return fmt.Errorf("target trace has generation %d", h.Gen)
		}
	}
	// CompareRuns describes a recording's first window. Rebase this complete
	// recovery window only for that comparison; stored evidence keeps generation 2.
	comparison := append([]crashwalk.Hit(nil), recorded...)
	for i := range comparison {
		comparison[i].Gen = 1
	}
	return crashwalk.CompareRuns(comparison, r.Hits[1], target)
}

func TestRecoveryRecordingsAndTargets(t *testing.T) {
	recoveryEvidence(t, nil)
}

type recoveryRecordID struct {
	Scenario string
	Trigger  crashwalk.Trigger
}

// recoveryEvidence records each recipe/trigger once, then reuses that complete
// second generation for all of its independently interrupted recovery points.
func recoveryEvidence(t *testing.T, clean map[string]crashwalk.RunResult) []crashwalk.Hit {
	t.Helper()
	recordings := map[recoveryRecordID][]crashwalk.Hit{}
	var all []crashwalk.Hit
	count := 0
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
			c := crashwalk.RecoveryCase{After: trigger}
			id := recoveryRecordID{s.Name(), c.After}
			if _, ok := recordings[id]; ok {
				continue
			}
			t.Run("record/"+s.Name()+"/"+fmt.Sprint(c.After), func(t *testing.T) {
				started := clock.System().Now()
				r, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, s.Name()), crashwalk.RunPlan{Trigger: &c.After})
				if err != nil {
					t.Fatal(err)
				}
				hits, err := completeRecoveryGeneration(r)
				if err != nil {
					t.Fatal(err)
				}
				assertDeclaredGenerations(t, r)
				if c.After.Kind == crashwalk.TriggerCrashAt && clean != nil {
					if err := crashwalk.CompareRuns(clean[s.Name()].Hits[0], r.Hits[0], crashwalk.Target{Gen: 1, Point: c.After.CrashAt}); err != nil {
						t.Fatal(err)
					}
				}
				recordings[id] = hits
				all = append(all, hits...)
				t.Logf("recovery-record scenario=%s trigger=%+v gen2_keys=%d elapsed=%s", s.Name(), c.After, len(hitKeys(hits)), clock.System().Now().Sub(started))
				t.Logf("recovery-record scenario=%s gen=2 keys=%v", s.Name(), hitKeys(hits))
			})
		}
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
				assertDeclaredGenerations(t, r)
				if err := verifyRecoveryTarget(recordings[recoveryRecordID{s.Name(), c.After}], r, target); err != nil {
					t.Fatal(err)
				}
				count++
				t.Logf("recovery-crash scenario=%s target=%s reached=true generations=3 elapsed=%s", s.Name(), target.Point, clock.System().Now().Sub(started))
			})
		}
	}
	if clean != nil {
		cleanSeen := map[fsx.Key]bool{}
		for _, r := range clean {
			for _, h := range r.Hits[0] {
				cleanSeen[h.Point.Key] = true
			}
		}
		normal := map[fsx.Key]bool{}
		for _, k := range harness.ProgramTable(t).Keys() {
			normal[k.Key] = k.Kind == txn.Normal
		}
		ids := make([]recoveryRecordID, 0, len(recordings))
		for id := range recordings {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return fmt.Sprint(ids[i]) < fmt.Sprint(ids[j]) })
		owner := map[fsx.Key]recoveryRecordID{}
		for _, id := range ids {
			for _, h := range recordings[id] {
				if normal[h.Point.Key] && !cleanSeen[h.Point.Key] {
					if _, exists := owner[h.Point.Key]; !exists {
						owner[h.Point.Key] = id
					}
				}
			}
		}
		keys := make([]fsx.Key, 0, len(owner))
		for key := range owner {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		derived := 0
		for _, key := range keys {
			id := owner[key]
			t.Logf("recovery-only-normal key=%s scenario=%s trigger=%+v gen=2", key, id.Scenario, id.Trigger)
			if crashwalk.IsSync(key.Step) {
				continue
			}
			t.Run("derived/"+key.String(), func(t *testing.T) {
				target := crashwalk.Target{Gen: 2, Point: fsx.Point{Key: key, Hit: 1}}
				started := clock.System().Now()
				r, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, id.Scenario), crashwalk.RunPlan{Trigger: &id.Trigger, Targets: []crashwalk.Target{target}})
				if err != nil {
					t.Fatal(err)
				}
				assertDeclaredGenerations(t, r)
				if err := verifyRecoveryTarget(recordings[id], r, target); err != nil {
					t.Fatal(err)
				}
				derived++
				t.Logf("recovery-normal-crash scenario=%s target=%s reached=true generations=3 elapsed=%s", id.Scenario, target.Point, clock.System().Now().Sub(started))
			})
		}
		var cleanHits []crashwalk.Hit
		for _, r := range clean {
			cleanHits = append(cleanHits, r.Hits[0]...)
		}
		union := append(append([]crashwalk.Hit(nil), cleanHits...), all...)
		c := crashwalk.Assess(harness.ProgramTable(t), union)
		t.Logf("coverage normal=%d/%d recovery=%d/%d recovery_completed=%d recovery_only_normal_keys=%d recovery_only_normal_completed=%d missing_normal=%v missing_recovery=%v", c.Normal.Reached, c.Normal.Declared, c.Recovery.Reached, c.Recovery.Declared, count, len(keys), derived, c.MissingNormal, c.MissingRecovery)
	}
	t.Logf("recovery completed=%d records=%d", count, len(recordings))
	reportCoverage(t, "complete-gen2", all)
	return all
}

func assertDeclaredGenerations(t testing.TB, r crashwalk.RunResult) {
	t.Helper()
	for i, hits := range r.Hits {
		c := crashwalk.Assess(harness.ProgramTable(t), hits)
		if len(c.Undeclared) != 0 {
			t.Fatalf("generation %d undeclared=%v", i+1, c.Undeclared)
		}
	}
}
