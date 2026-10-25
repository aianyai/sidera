package crashwalk_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

// system is the program every Walk test runs, with the scenario that describes it.
func system(t *testing.T) (*fakeBackend, scenario) {
	t.Helper()
	p := program{steps: []programStep{
		{"setup", cat(replaced("status", "file", "state/status.rec"), []rawHit{commit("status")})},
		{"send", cat(replaced("intake", "body", "state/body/m1.tmp"), replaced("intake", "out", "state/out/m1.rec"), []rawHit{commit("intake")})},
		{"log", []rawHit{{"journal/events.open", "archive/a.jsonl"}, {"journal/events.write", "archive/a.jsonl"}, {"journal/events.sync", "archive/a.jsonl"}, {"journal/events.sync-dir", "archive"}, commit("journal")}},
	}}
	// A real run of the recovery transactions: what the restart after a crash writes.
	recoveryRuns := map[string][]rawHit{}
	for _, item := range []string{"orphans/body.remove", "orphans/commit", "replay/state.create", "replay/state.write", "replay/state.rename", "replay/commit"} {
		switch {
		case strings.HasPrefix(item, "orphans"):
			recoveryRuns[item] = []rawHit{{"orphans/body.remove", "state/body/m1.tmp"}, {"orphans/dir.sync-dir", "state/body"}, {"orphans/commit", ""}}
		default:
			recoveryRuns[item] = cat(replaced("replay", "state", "state/replay.rec")[:4], []rawHit{commit("replay")})
		}
	}
	b := &fakeBackend{t: t, progs: map[string]program{"send": p}, recovery: recoveryRuns}
	var cases []crashwalk.RecoveryCase
	for _, item := range []string{"orphans/body.remove", "orphans/commit", "replay/state.create", "replay/state.write", "replay/state.rename", "replay/commit"} {
		cases = append(cases, crashwalk.RecoveryCase{Item: key(t, item), After: crashwalk.CrashAt(point(t, "intake/out.rename#1"))})
	}
	return b, scenario{name: "send", steps: []string{"send", "log"}, max: 40, recovery: cases}
}

func walk(t *testing.T, b crashwalk.Backend, sc scenario, mode crashwalk.Mode, shard crashwalk.Shard) (crashwalk.Report, error) {
	t.Helper()
	return crashwalk.Walk(context.Background(), b, table(t), []crashwalk.Scenario{sc}, crashwalk.Options{Mode: mode, Shard: shard})
}

var wholeRun = crashwalk.Shard{Index: 1, Count: 1}

func TestWalkFullRunsEveryPlannedTargetAndTheSecondLayer(t *testing.T) {
	b, sc := system(t)
	rep, err := walk(t, b, sc, crashwalk.Full, wholeRun)
	if err != nil {
		t.Fatal(err)
	}
	hits := program{steps: b.progs["send"].steps}
	rec, _ := hits.run(t, 1, fsx.Point{}, "")
	want := crashwalk.Plan(rec, crashwalk.Full, sc.steps)
	var mainTargets, boundary int
	for _, tg := range want {
		if tg.AfterStep != "" {
			boundary++
		} else {
			mainTargets++
		}
	}
	if rep.Recordings != 1 || rep.MainRuns != mainTargets || rep.BoundaryRuns != boundary || rep.RecoveryRuns != len(sc.recovery) {
		t.Fatalf("report %+v: want 1 recording, %d main runs, %d boundary runs, %d recovery runs", rep, mainTargets, boundary, len(sc.recovery))
	}
	// The backend was asked exactly that: the recording, the planned targets and
	// the recovery cases, with no target skipped.
	asked := 0
	for _, r := range b.runs {
		asked += len(r.Targets)
	}
	if asked != mainTargets+boundary+len(sc.recovery) {
		t.Fatalf("the backend was asked for %d targets, want %d", asked, mainTargets+boundary+len(sc.recovery))
	}
}

func TestWalkSmokeRunsOnePerTransactionAndOneRecoveryCaseEach(t *testing.T) {
	b, sc := system(t)
	rep, err := walk(t, b, sc, crashwalk.Smoke, wholeRun)
	if err != nil {
		t.Fatal(err)
	}
	// intake, journal (the status writes of the setup are outside the window)
	if rep.MainRuns != 2 || rep.BoundaryRuns != 0 || rep.RecoveryRuns != 2 {
		t.Fatalf("report %+v, want 2 main runs, no boundary runs, 2 recovery runs (orphans, replay)", rep)
	}
}

func TestWalkSecondLayerCrashesTheRecoveryAfterTheMainCrash(t *testing.T) {
	b, sc := system(t)
	if _, err := walk(t, b, sc, crashwalk.Smoke, wholeRun); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, r := range b.runs {
		if r.Trigger == nil {
			continue
		}
		seen++
		if len(r.Targets) != 1 || r.Targets[0].Gen != 2 || r.Targets[0].Point.Hit != 1 {
			t.Fatalf("a recovery run targets %+v: want the first hit of the item in generation 2", r.Targets)
		}
		if r.Trigger.Kind != crashwalk.TriggerCrashAt {
			t.Fatalf("trigger %+v", r.Trigger)
		}
	}
	if seen != 2 {
		t.Fatalf("%d recovery runs", seen)
	}
}

func TestWalkFailsWhenATargetIsNotReached(t *testing.T) {
	b, sc := system(t)
	b.skipTarget = func(tg crashwalk.Target) bool { return tg.Point.String() == "intake/out.rename#1" }
	_, err := walk(t, b, sc, crashwalk.Full, wholeRun)
	if err == nil || !strings.Contains(err.Error(), "intake/out.rename#1") || !strings.Contains(err.Error(), "not reached") {
		t.Fatalf("a target that was never reached did not fail the walk, or did not say which: %v", err)
	}
}

func TestWalkFailsWhenARecoveryTargetIsNotReached(t *testing.T) {
	b, sc := system(t)
	b.skipTarget = func(tg crashwalk.Target) bool { return tg.Gen == 2 && tg.Point.Key.String() == "orphans/body.remove" }
	_, err := walk(t, b, sc, crashwalk.Smoke, wholeRun)
	if err == nil || !strings.Contains(err.Error(), "orphans/body.remove") {
		t.Fatalf("got %v", err)
	}
}

func TestWalkFailsOnAnInvariantTheBackendReports(t *testing.T) {
	b, sc := system(t)
	b.violate = func(p crashwalk.RunPlan) []string {
		if len(p.Targets) == 1 && p.Targets[0].Point.String() == "intake/body.rename#1" {
			return []string{"an acknowledged message is missing"}
		}
		return nil
	}
	_, err := walk(t, b, sc, crashwalk.Full, wholeRun)
	if err == nil || !strings.Contains(err.Error(), "an acknowledged message is missing") || !strings.Contains(err.Error(), "intake/body.rename#1") {
		t.Fatalf("got %v", err)
	}
}

func TestWalkFailsWhenARerunDivergesFromTheRecording(t *testing.T) {
	b, sc := system(t)
	b.alter = func(tg crashwalk.Target, hits *[]crashwalk.Hit) {
		if tg.Point.String() == "intake/out.rename#1" {
			for i := range *hits {
				if (*hits)[i].Point.String() == "intake/body.rename#1" {
					(*hits)[i].Path = "state/body/changed.tmp"
				}
			}
		}
	}
	_, err := walk(t, b, sc, crashwalk.Full, wholeRun)
	if err == nil || !strings.Contains(err.Error(), "not deterministic") {
		t.Fatalf("got %v", err)
	}
}

func TestWalkFailsWhenACleanRunWritesARecoveryPath(t *testing.T) {
	b, sc := system(t)
	b.cleanExtra = []rawHit{{"orphans/body.remove", "state/body/x"}}
	_, err := walk(t, b, sc, crashwalk.Smoke, wholeRun)
	if err == nil || !strings.Contains(err.Error(), "orphans/body.remove") || !strings.Contains(err.Error(), "recovery") {
		t.Fatalf("got %v", err)
	}
}

func TestWalkFailsOnAWriteNoDeclarationLists(t *testing.T) {
	b, sc := system(t)
	b.cleanExtra = []rawHit{{"rogue/file.rename", "state/rogue"}}
	_, err := walk(t, b, sc, crashwalk.Smoke, wholeRun)
	if err == nil || !strings.Contains(err.Error(), "rogue/file.rename") || !strings.Contains(err.Error(), "undeclared") {
		t.Fatalf("got %v", err)
	}
}

func TestWalkFailsWhenADeclaredNormalWriteIsNeverReached(t *testing.T) {
	b, sc := system(t)
	prog := b.progs["send"]
	// Drop the status writes of the setup: status/file.* is never hit.
	prog.steps = prog.steps[1:]
	b.progs["send"] = prog
	_, err := walk(t, b, sc, crashwalk.Smoke, wholeRun)
	if err == nil || !strings.Contains(err.Error(), "status/file.rename") || !strings.Contains(err.Error(), "never") {
		t.Fatalf("got %v", err)
	}
}

func TestWalkFailsWhenTheScenarioOutgrowsItsBudget(t *testing.T) {
	b, sc := system(t)
	sc.max = 3
	_, err := walk(t, b, sc, crashwalk.Smoke, wholeRun)
	if err == nil || !strings.Contains(err.Error(), "send") || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("got %v", err)
	}
}

func TestWalkFailsWhenTheRecoveryListsDoNotCoverTheDeclaredRecoveryWrites(t *testing.T) {
	b, sc := system(t)
	sc.recovery = sc.recovery[:len(sc.recovery)-1]
	_, err := walk(t, b, sc, crashwalk.Smoke, wholeRun)
	if err == nil || !strings.Contains(err.Error(), "replay/commit") {
		t.Fatalf("got %v", err)
	}
}

// TestWalkShardsRunDisjointTargetsAndTogetherAllOfThem: a shard still records
// every scenario and checks coverage (the recording is cheap and the assertion is
// about the whole), and the main and recovery runs of the shards add up to a whole
// walk.
func TestWalkShardsRunDisjointTargetsAndTogetherAllOfThem(t *testing.T) {
	_, sc := system(t)
	var main, boundary, recoveries int
	for i := 1; i <= 3; i++ {
		b, _ := system(t)
		rep, err := walk(t, b, sc, crashwalk.Full, crashwalk.Shard{Index: i, Count: 3})
		if err != nil {
			t.Fatalf("shard %d: %v", i, err)
		}
		if rep.Recordings != 1 {
			t.Fatalf("shard %d made %d recordings, want 1", i, rep.Recordings)
		}
		main, boundary, recoveries = main+rep.MainRuns, boundary+rep.BoundaryRuns, recoveries+rep.RecoveryRuns
	}
	b, _ := system(t)
	whole, err := walk(t, b, sc, crashwalk.Full, wholeRun)
	if err != nil {
		t.Fatal(err)
	}
	if main != whole.MainRuns || boundary != whole.BoundaryRuns || recoveries != whole.RecoveryRuns {
		t.Fatalf("three shards ran %d/%d/%d, the whole walk %d/%d/%d", main, boundary, recoveries, whole.MainRuns, whole.BoundaryRuns, whole.RecoveryRuns)
	}
}

func TestWalkReportsEveryRunToTheObserver(t *testing.T) {
	b, sc := system(t)
	var events []crashwalk.Event
	rep, err := crashwalk.Walk(context.Background(), b, table(t), []crashwalk.Scenario{sc}, crashwalk.Options{Mode: crashwalk.Smoke, Shard: wholeRun, Observe: func(e crashwalk.Event) { events = append(events, e) }})
	if err != nil {
		t.Fatal(err)
	}
	starts, ends := 0, 0
	for _, e := range events {
		if e.Done {
			ends++
		} else {
			starts++
		}
	}
	runs := rep.Recordings + rep.MainRuns + rep.BoundaryRuns + rep.RecoveryRuns
	if starts != runs || ends != runs {
		t.Fatalf("%d starts and %d ends for %d runs: the caller times each run from these", starts, ends, runs)
	}
}

func TestWalkStopsWhenItsContextEnds(t *testing.T) {
	b, sc := system(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := crashwalk.Walk(ctx, b, table(t), []crashwalk.Scenario{sc}, crashwalk.Options{Mode: crashwalk.Full, Shard: wholeRun}); err == nil {
		t.Fatal("a walk with a finished context ran")
	}
}
