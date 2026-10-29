package crashwalk_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

type backendFunc func(context.Context, crashwalk.Scenario, crashwalk.RunPlan) (crashwalk.RunResult, error)

func (f backendFunc) Run(ctx context.Context, s crashwalk.Scenario, p crashwalk.RunPlan) (crashwalk.RunResult, error) {
	return f(ctx, s, p)
}

func recoveryOnlySystem(t *testing.T) (*fakeBackend, scenario) {
	b, s := system(t)
	p := b.progs[s.name]
	p.steps = p.steps[1:]
	b.progs[s.name] = p
	b.recoveryRecord = cat(replaced("status", "file", "state/status.rec"), []rawHit{commit("status")}, []rawHit{{"orphans/body.remove", "state/body/m1.tmp"}, {"orphans/dir.sync-dir", "state/body"}, commit("orphans")}, replaced("replay", "state", "state/replay.rec"), []rawHit{commit("replay")})
	return b, s
}

func TestWalkDerivesAndExecutesRecoveryNormalTargetsOnlyInFull(t *testing.T) {
	for _, mode := range []crashwalk.Mode{crashwalk.Smoke, crashwalk.Full} {
		b, s := recoveryOnlySystem(t)
		var skipped bool
		rep, err := crashwalk.Walk(context.Background(), b, table(t), []crashwalk.Scenario{s}, crashwalk.Options{Mode: mode, Observe: func(e crashwalk.Event) {
			if e.Skipped {
				skipped = true
				if e.Phase != "recovery-normal" || e.Planned != 4 {
					t.Fatalf("skip event %+v", e)
				}
			}
		}})
		if err != nil {
			t.Fatal(err)
		}
		if rep.RecoveryRecordings != 1 || len(rep.Sources.NormalOnlyInRecovery) != 6 || rep.RecoveryNormalPlanned != 4 || rep.Normal.Reached != rep.Normal.Declared {
			t.Fatalf("sources %+v", rep)
		}
		want := 4
		if mode == crashwalk.Smoke {
			want = 0
		}
		if rep.RecoveryNormalRuns != want || rep.RecoveryNormalSelected != want || skipped != (mode == crashwalk.Smoke) || rep.RecoveryNormalSkipped != skipped {
			t.Fatalf("execution %+v", rep)
		}
	}
}

func TestWalkRecordsDuplicateTriggersOnceAndPartitionsAllTargets(t *testing.T) {
	run := func(index, count int, reverse bool) map[string]bool {
		b, s := recoveryOnlySystem(t)
		tr := crashwalk.RecoveryTrigger{Scenario: s.name, After: s.recovery[0].After}
		extra := crashwalk.RecoveryTrigger{Scenario: s.name, After: crashwalk.CorruptOffline("state/x", "bad")}
		triggers := []crashwalk.RecoveryTrigger{tr, extra, tr}
		if reverse {
			triggers = []crashwalk.RecoveryTrigger{extra, tr}
		}
		rep, err := crashwalk.Walk(context.Background(), b, table(t), []crashwalk.Scenario{s}, crashwalk.Options{Mode: crashwalk.Full, Shard: crashwalk.Shard{Index: index, Count: count}, RecoveryTriggers: triggers})
		if err != nil {
			t.Fatal(err)
		}
		if rep.RecoveryRecordings != 2 {
			t.Fatalf("recordings %d, want 2 unique sources", rep.RecoveryRecordings)
		}
		got := map[string]bool{}
		for _, p := range b.runs {
			if len(p.Targets) == 0 {
				continue
			}
			name := fmt.Sprintf("%+v/%+v", p.Trigger, p.Targets[0])
			if got[name] {
				t.Fatalf("duplicate execution %s", name)
			}
			got[name] = true
		}
		return got
	}
	whole := run(1, 1, false)
	union := map[string]bool{}
	for shard := 1; shard <= 3; shard++ {
		targets := run(shard, 3, false)
		if !reflect.DeepEqual(targets, run(shard, 3, true)) {
			t.Fatal("trigger input order changed shard membership")
		}
		for target := range targets {
			if union[target] {
				t.Fatalf("shards intersect at %s", target)
			}
			union[target] = true
		}
	}
	if !reflect.DeepEqual(whole, union) {
		t.Fatalf("shards do not partition full target set: %d versus %d", len(union), len(whole))
	}
}

func TestWalkAnalyzesAfterEveryCleanRecording(t *testing.T) {
	b, s := recoveryOnlySystem(t)
	later := s
	later.name = "z-later"
	later.recovery = nil
	original, _ := system(t)
	b.progs[later.name] = original.progs[s.name]
	rep, err := crashwalk.Walk(context.Background(), b, table(t), []crashwalk.Scenario{s, later}, crashwalk.Options{Mode: crashwalk.Full})
	if err != nil || rep.RecoveryNormalPlanned != 0 || len(rep.Sources.NormalOnlyInRecovery) != 0 {
		t.Fatalf("later clean source was ignored: %+v %v", rep, err)
	}
}

func TestWalkRejectsInvalidTriggerInputsBeforeRunning(t *testing.T) {
	for _, tr := range []crashwalk.RecoveryTrigger{{Scenario: "unknown", After: crashwalk.CorruptOffline("x", "bad")}, {Scenario: "send"}, {Scenario: "send", After: crashwalk.CorruptOffline("", "bad")}} {
		b, s := system(t)
		_, err := crashwalk.Walk(context.Background(), b, table(t), []crashwalk.Scenario{s}, crashwalk.Options{Mode: crashwalk.Full, RecoveryTriggers: []crashwalk.RecoveryTrigger{tr}})
		if err == nil || len(b.runs) != 0 {
			t.Fatalf("invalid input ran backend: %+v %v", tr, err)
		}
	}
}

func TestWalkRejectsThirdGenerationEvidenceAndObservedButUninterruptedTargets(t *testing.T) {
	for _, fault := range []string{"third generation", "observation only", "complete recording domain failure"} {
		t.Run(fault, func(t *testing.T) {
			b, s := recoveryOnlySystem(t)
			backend := backendFunc(func(ctx context.Context, sc crashwalk.Scenario, p crashwalk.RunPlan) (crashwalk.RunResult, error) {
				r, err := b.Run(ctx, sc, p)
				if p.Trigger != nil && len(p.Targets) == 0 {
					if fault == "third generation" {
						for i := range r.Hits[1] {
							r.Hits[1][i].Gen = 3
						}
						r.Hits = append(r.Hits[:1], nil, r.Hits[1])
					}
					if fault == "complete recording domain failure" {
						r.Violations = []string{"message lost"}
					}
				}
				if fault == "observation only" && len(p.Targets) == 1 && p.Targets[0].Gen == 2 && p.Targets[0].Point.Tag.Tx == "status" {
					r.Reached = []bool{false}
				}
				return r, err
			})
			_, err := crashwalk.Walk(context.Background(), backend, table(t), []crashwalk.Scenario{s}, crashwalk.Options{Mode: crashwalk.Full})
			if err == nil {
				t.Fatal("invalid provenance or unexecuted interruption accepted")
			}
			if fault == "observation only" && !strings.Contains(err.Error(), "not reached") {
				t.Fatalf("wrong failure: %v", err)
			}
		})
	}
}
