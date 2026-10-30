//go:build acceptance

package acceptance

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

func TestBinarySessionLifecycleResumesInterruptedFourthIntake(t *testing.T) {
	const seed = "TestCrashAtEveryCrashPointPreservesInvariants"
	scenarios, _ := bindCatalog()
	for _, scenario := range scenarios {
		if scenario.Name() != "session-lifecycle" {
			continue
		}
		backend := NewWalkBackend(runRoot(t), testBinaries, Options{Seed: seed})
		target := crashwalk.Target{Gen: 1, Point: point(t, "intake/out.create#4")}
		started := time.Now()
		result, err := backend.Run(t.Context(), scenario, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
		t.Logf("seed=%s scenario=%s target=%+v reached=%v generations=%d violations=%v elapsed=%s error=%v", seed, scenario.Name(), target, result.Reached, len(result.Hits), result.Violations, time.Since(started), err)
		checkRun(t, result, err, 2)
		return
	}
	t.Fatal("session-lifecycle recipe is missing")
}

func TestBinarySessionLifecycleResumesInterruptedClose(t *testing.T) {
	const seed = "TestCrashAtEveryCrashPointPreservesInvariants"
	scenarios, _ := bindCatalog()
	for _, scenario := range scenarios {
		if scenario.Name() != "session-lifecycle" {
			continue
		}
		backend := NewWalkBackend(runRoot(t), testBinaries, Options{Seed: seed})
		clean, err := backend.Run(t.Context(), scenario, crashwalk.RunPlan{Record: true})
		checkRun(t, clean, err, 1)
		// These ordinals belong to the original close, after creation and interrupt.
		for _, at := range []string{
			"intake/out.write#4", "intake/out.sync#4", "intake/out.rename#4", "intake/out.sync-dir#4", "intake/commit#4",
			"control-intake/ctl.create#3", "control-intake/ctl.rename#3", "control-intake/commit#3",
			"control-result/ctl.create#3", "control-result/ctl.rename#3", "control-result/commit#3",
		} {
			t.Run(at, func(t *testing.T) {
				target := crashwalk.Target{Gen: 1, Point: point(t, at)}
				result, err := backend.Run(t.Context(), scenario, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
				t.Logf("target=%+v reached=%v generations=%d violations=%v", target, result.Reached, len(result.Hits), result.Violations)
				checkRun(t, result, err, 2)
				if err := crashwalk.CompareRuns(clean.Hits[0], result.Hits[0], target); err != nil {
					t.Fatal(err)
				}
			})
		}
		return
	}
	t.Fatal("session-lifecycle recipe is missing")
}

func TestBinarySessionLifecycleResumesInterruptedRecoveryReceive(t *testing.T) {
	const seed = "TestCrashAtEveryCrashPointPreservesInvariants"
	scenarios, _ := bindCatalog()
	for _, scenario := range scenarios {
		if scenario.Name() != "session-lifecycle" {
			continue
		}
		backend := NewWalkBackend(runRoot(t), testBinaries, Options{Seed: seed})
		clean, err := backend.Run(t.Context(), scenario, crashwalk.RunPlan{Record: true})
		checkRun(t, clean, err, 1)
		trigger := crashwalk.CrashAt(point(t, "intake/out.create#4"))
		baseline, err := backend.Run(t.Context(), scenario, crashwalk.RunPlan{Trigger: &trigger})
		checkRun(t, baseline, err, 2)
		if err := crashwalk.CheckRecoveryRecord(clean.Hits[0], trigger, baseline); err != nil {
			t.Fatal(err)
		}
		for _, at := range []string{"lease/in.rename#1", "ack/in.rename#1"} {
			t.Run(at, func(t *testing.T) {
				target := crashwalk.Target{Gen: 2, Point: point(t, at)}
				result, err := backend.Run(t.Context(), scenario, crashwalk.RunPlan{Trigger: &trigger, Targets: []crashwalk.Target{target}})
				t.Logf("trigger=%+v target=%+v reached=%v generations=%d violations=%v", trigger, target, result.Reached, len(result.Hits), result.Violations)
				checkRun(t, result, err, 3)
				if err := crashwalk.CheckRecoveryTrigger(clean.Hits[0], trigger, result); err != nil {
					t.Fatal(err)
				}
				if err := crashwalk.CheckRecoveryTarget(baseline.Hits[1], target, result); err != nil {
					t.Fatal(err)
				}
			})
		}
		return
	}
	t.Fatal("session-lifecycle recipe is missing")
}
