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

// interleaved is the program of system with the writes of its first step in
// another order across transactions: each pair of transaction and role writes the
// same steps in the same order, as CompareRuns allows, but the transactions
// interleave differently.
func interleaved(t *testing.T, intakeFirst bool) (*fakeBackend, scenario) {
	t.Helper()
	b, sc := system(t)
	sc.steps = []string{"work", "log"}
	intake := cat(replaced("intake", "body", "state/body/m1.tmp"), replaced("intake", "out", "state/out/m1.rec"), []rawHit{commit("intake")})
	status := cat(replaced("status", "file", "state/status.rec"), []rawHit{commit("status")})
	work := cat(status, intake)
	if intakeFirst {
		work = cat(intake, status)
	}
	if !intakeFirst {
		// the pairs also interleave inside the step
		work = cat(status[:4], intake[:5], status[4:], intake[5:])
	}
	b.progs["send"] = program{steps: []programStep{
		{"work", work},
		{"log", []rawHit{{"journal/events.open", "archive/a.jsonl"}, {"journal/events.write", "archive/a.jsonl"}, {"journal/events.sync", "archive/a.jsonl"}, {"journal/events.sync-dir", "archive"}, commit("journal")}},
	}}
	return b, sc
}

// mainTargets are the targets of the runs a backend was asked to make that are
// neither the recording nor a recovery case.
func mainTargets(b *fakeBackend) []string {
	var out []string
	for _, r := range b.runs {
		if r.Record || r.Trigger != nil {
			continue
		}
		for _, tg := range r.Targets {
			if tg.AfterStep != "" {
				out = append(out, "after:"+tg.AfterStep)
			} else {
				out = append(out, tg.Point.String())
			}
		}
	}
	return out
}

// TestWalkShardsWhoseRecordingsInterleaveDifferentlyRunEveryTargetOnce: a shard
// numbers the targets of a scenario from its own recording. Writes of different
// transactions may interleave differently from one process to the next, so the
// number must follow an order that does not depend on the interleaving, or two
// shards would run one target twice and another never.
func TestWalkShardsWhoseRecordingsInterleaveDifferentlyRunEveryTargetOnce(t *testing.T) {
	for _, mode := range []crashwalk.Mode{crashwalk.Full, crashwalk.Smoke} {
		for count := 2; count <= 4; count++ {
			ran := map[string]int{}
			for i := 1; i <= count; i++ {
				b, sc := interleaved(t, i%2 == 0)
				if _, err := walk(t, b, sc, mode, crashwalk.Shard{Index: i, Count: count}); err != nil {
					t.Fatalf("mode %v shard %d/%d: %v", mode, i, count, err)
				}
				for _, tg := range mainTargets(b) {
					ran[tg]++
				}
			}
			whole, sc := interleaved(t, true)
			if _, err := walk(t, whole, sc, mode, wholeRun); err != nil {
				t.Fatal(err)
			}
			want := mainTargets(whole)
			if len(ran) != len(want) {
				t.Errorf("mode %v, %d shards: the shards ran %d different targets, the whole walk %d", mode, count, len(ran), len(want))
			}
			for _, tg := range want {
				if ran[tg] != 1 {
					t.Errorf("mode %v, %d shards: target %s ran %d times, want exactly once", mode, count, tg, ran[tg])
				}
			}
		}
	}
}

// TestWalkNumbersRunsByScenarioNameNotByTheOrderTheyAreListed: the shards of one
// CI run build their scenario lists independently.
func TestWalkNumbersRunsByScenarioNameNotByTheOrderTheyAreListed(t *testing.T) {
	two := func(first, second string) (*fakeBackend, []crashwalk.Scenario) {
		b, sc := system(t)
		a, c := sc, sc
		a.name, c.name = first, second
		a.recovery, c.recovery = nil, nil
		b.progs[first], b.progs[second] = b.progs["send"], b.progs["send"]
		// the recovery cases belong to one scenario
		if first < second {
			a.recovery = sc.recovery
		} else {
			c.recovery = sc.recovery
		}
		return b, []crashwalk.Scenario{a, c}
	}
	order := func(first, second string, list func(a, c crashwalk.Scenario) []crashwalk.Scenario) []string {
		b, sc := two(first, second)
		if _, err := crashwalk.Walk(context.Background(), b, table(t), list(sc[0], sc[1]), crashwalk.Options{Mode: crashwalk.Smoke, Shard: crashwalk.Shard{Index: 2, Count: 3}}); err != nil {
			t.Fatal(err)
		}
		return mainTargets(b)
	}
	sorted := order("alpha", "beta", func(a, c crashwalk.Scenario) []crashwalk.Scenario { return []crashwalk.Scenario{a, c} })
	swapped := order("alpha", "beta", func(a, c crashwalk.Scenario) []crashwalk.Scenario { return []crashwalk.Scenario{c, a} })
	if strings.Join(sorted, " ") != strings.Join(swapped, " ") {
		t.Fatalf("listing the scenarios in another order changed the runs of a shard:\n %v\n %v", sorted, swapped)
	}
}

func TestWalkRefusesTwoScenariosWithTheSameName(t *testing.T) {
	b, sc := system(t)
	_, err := crashwalk.Walk(context.Background(), b, table(t), []crashwalk.Scenario{sc, sc}, crashwalk.Options{Mode: crashwalk.Smoke, Shard: wholeRun})
	if err == nil || !strings.Contains(err.Error(), "named") {
		t.Fatalf("err = %v, want a refusal of the repeated name", err)
	}
}

// TestWalkSmokeCrashesARecoveryTransactionOnceWhateverScenarioListsItsWrites: two
// scenarios that each list a write of the same recovery transaction run one case
// between them in smoke, not one each.
func TestWalkSmokeCrashesARecoveryTransactionOnceWhateverScenarioListsItsWrites(t *testing.T) {
	b, sc := system(t)
	var orphans, replay []crashwalk.RecoveryCase
	for _, c := range sc.recovery {
		if c.Item.Tag.Tx == "orphans" {
			orphans = append(orphans, c)
		} else {
			replay = append(replay, c)
		}
	}
	// Both scenarios list one write of "replay".
	first, second := sc, sc
	first.name, second.name = "a-first", "b-second"
	first.recovery = append(append([]crashwalk.RecoveryCase(nil), orphans...), replay[:2]...)
	second.recovery = replay[2:]
	b.progs["a-first"], b.progs["b-second"] = b.progs["send"], b.progs["send"]
	rep, err := crashwalk.Walk(context.Background(), b, table(t), []crashwalk.Scenario{first, second}, crashwalk.Options{Mode: crashwalk.Smoke, Shard: wholeRun})
	if err != nil {
		t.Fatal(err)
	}
	if rep.RecoveryRuns != 2 {
		t.Fatalf("%d recovery runs in smoke, want one per recovery transaction (2)", rep.RecoveryRuns)
	}
}

// TestWalkFullReportsWhichMainCrashPointMadeWhichRecoveryWrites: the author of a
// scenario fills its recovery cases from this.
func TestWalkFullReportsWhichMainCrashPointMadeWhichRecoveryWrites(t *testing.T) {
	b, sc := system(t)
	b.afterMain = func(tg crashwalk.Target) []rawHit {
		if tg.Point.String() == "intake/out.rename#1" {
			return []rawHit{{"orphans/body.remove", "state/body/m1.tmp"}, {"orphans/dir.sync-dir", "state/body"}, {"orphans/commit", ""}}
		}
		return nil
	}
	rep, err := walk(t, b, sc, crashwalk.Full, wholeRun)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Triggers) != 1 || rep.Triggers[0].At.String() != "intake/out.rename#1" {
		t.Fatalf("Triggers = %+v, want one entry for intake/out.rename#1", rep.Triggers)
	}
	var items []string
	for _, k := range rep.Triggers[0].Items {
		items = append(items, k.String())
	}
	if want := "orphans/body.remove orphans/dir.sync-dir orphans/commit"; strings.Join(items, " ") != want {
		t.Fatalf("items %v, want %s", items, want)
	}
	b, sc = system(t)
	rep, err = walk(t, b, sc, crashwalk.Smoke, wholeRun)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Triggers) != 0 {
		t.Fatalf("a smoke walk reported triggers %+v: only a full walk keeps them", rep.Triggers)
	}
}

// TestWalkReportsCoverageOfNormalAndRecoveryPathsApart: the two kinds are reached
// by different runs, and a count that mixed them would hide a recovery write that
// no restart reached.
func TestWalkReportsCoverageOfNormalAndRecoveryPathsApart(t *testing.T) {
	b, sc := system(t)
	rep, err := walk(t, b, sc, crashwalk.Full, wholeRun)
	if err != nil {
		t.Fatal(err)
	}
	// The table declares 9 recovery writes (orphans: remove, dir sync, commit;
	// replay: create, write, sync, rename, dir sync, commit) and the scripted
	// restarts of system() write all but the directory sync of the replay: 8. The normal writes are all reached by the recording.
	if rep.Normal.Declared != rep.Normal.Reached || rep.Normal.Declared == 0 {
		t.Errorf("normal paths %+v, want all declared writes reached", rep.Normal)
	}
	if rep.Recovery.Declared != 9 || rep.Recovery.Reached != 8 {
		t.Errorf("recovery paths %+v, want 8 of 9", rep.Recovery)
	}
}
