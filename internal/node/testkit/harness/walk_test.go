package harness_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func sendWalkScenario() *harness.WalkScenario {
	return &harness.WalkScenario{Script: scenario.Script{
		Name: "send", Setup: scenario.Setup{Direct: []string{"sender", "receiver"}},
		Crash: scenario.CrashPlan{MaxPoints: 150},
		Steps: []scenario.Step{{Name: "send", Do: func(e scenario.Env) error {
			_, err := e.Agent("sender").Send(e.Ctx(), fakeagent.SendInput{To: []string{e.Addr("receiver")}, Body: "kept across a crash"})
			return err
		}}},
	}, Drain: func(h *harness.H) error {
		return scenario.RecvAndAckAll("receiver")(h)
	}}

}

func walkPoint(t testing.TB, name string) fsx.Point {
	t.Helper()
	p, err := fsx.ParsePoint(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestWalkBackendCutsARealSendAndRecoversTheDiskItLeft(t *testing.T) {
	s := sendWalkScenario()
	s.Options.Adapter = &harness.AdapterOptions{}
	b := harness.NewWalkBackend(t)
	ctx := context.Background()
	recorded, err := b.Run(ctx, s, crashwalk.RunPlan{Record: true})
	if err != nil {
		t.Fatal(err)
	}
	cov := crashwalk.Assess(harness.ProgramTable(t), recorded.Hits[0])
	t.Logf("send recording: normal %d/%d, recovery %d/%d, planned main points %d", cov.Normal.Reached, cov.Normal.Declared, cov.Recovery.Reached, cov.Recovery.Declared, len(crashwalk.Plan(recorded.Hits[0], crashwalk.Full, nil)))
	point, err := fsx.ParsePoint("intake/out.rename#1")
	if err != nil {
		t.Fatal(err)
	}
	target := crashwalk.Target{Gen: 1, Point: point}
	crashed, err := b.Run(ctx, s, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
	if err != nil {
		t.Fatal(err)
	}
	if len(crashed.Reached) != 1 || !crashed.Reached[0] || len(crashed.Hits) != 2 {
		t.Fatalf("the send was not cut and restarted: %d generations, reached %v", len(crashed.Hits), crashed.Reached)
	}
	if len(crashed.Violations) != 0 {
		t.Fatalf("recovered invariants: %v", crashed.Violations)
	}
	if err := crashwalk.CompareRuns(recorded.Hits[0], crashed.Hits[0], target); err != nil {
		t.Fatal(err)
	}
	var path string
	for _, hit := range recorded.Hits[0] {
		if hit.Point == point {
			path = hit.Path
		}
	}
	if !strings.HasPrefix(path, "state/out/") {
		t.Fatalf("the crash point has no source-record fingerprint: %q", path)
	}
	last := crashed.Hits[0][len(crashed.Hits[0])-1]
	if last.Point != point || last.Path != path || !strings.Contains(last.From, ".tmp-*") {
		t.Fatalf("the first generation did not end at the real rename: %+v", last)
	}
}

func TestWalkBackendDamagesOnlyTheStoppedDiskThenCrashesItsRecovery(t *testing.T) {
	s := sendWalkScenario()
	trigger := crashwalk.CorruptOffline("state/out", "flip-checksum")
	var damaged string
	s.Damage = func(h *harness.H, got crashwalk.Trigger) error {
		if got != trigger {
			return fmt.Errorf("wrong offline trigger: %+v", got)
		}
		// A down node refuses calls; its disk is the one startup will repair.
		_, err := h.Agent("sender").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("receiver")}, Body: "must not be accepted"})
		if fakeagent.CategoryOf(err) != fakeagent.CategoryUnavailable || fakeagent.CodeOf(err) != "daemon_not_running" {
			return fmt.Errorf("the damage callback's node is not down: %w", err)
		}
		files, err := h.Disk().ReadDir(got.Pattern)
		if err != nil {
			return err
		}
		for _, file := range files {
			if !strings.HasSuffix(file.Name, ".rec") {
				continue
			}
			damaged = got.Pattern + "/" + file.Name
			data, err := h.Disk().ReadFile(damaged)
			if err != nil {
				return err
			}
			data[len(data)-1] ^= 0xff
			return fsx.Replace(h.Disk(), fsx.Tag{Tx: "offline", Role: "damage"}, damaged, data, fsx.ModePrivate)
		}
		return fmt.Errorf("no source record to damage")
	}
	target := crashwalk.Target{Gen: 2, Point: walkPoint(t, "quarantine/overwrite.rename#1")}
	res, err := harness.NewWalkBackend(t).Run(bg(), s, crashwalk.RunPlan{Trigger: &trigger, Targets: []crashwalk.Target{target}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 3 || len(res.Reached) != 1 || !res.Reached[0] || len(res.Violations) != 0 {
		t.Fatalf("offline repair: %d generations, reached %v, violations %v", len(res.Hits), res.Reached, res.Violations)
	}
	last := res.Hits[1][len(res.Hits[1])-1]
	if last.Point != target.Point || last.Path != damaged {
		t.Fatalf("the stopped disk's damaged record was not the repair target: %+v", last)
	}
}

func TestWalkBackendCrashesTheRecoveryOfARealSendAtItsCommit(t *testing.T) {
	s := sendWalkScenario()
	b := harness.NewWalkBackend(t)
	trigger := crashwalk.CrashAt(walkPoint(t, "intake/out.rename#1"))
	target := crashwalk.Target{Gen: 2, Point: walkPoint(t, "txn-cleanup/commit#1")}
	res, err := b.Run(bg(), s, crashwalk.RunPlan{Trigger: &trigger, Targets: []crashwalk.Target{target}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 3 || len(res.Reached) != 1 || !res.Reached[0] || len(res.Violations) != 0 {
		t.Fatalf("recovery: %d generations, reached %v, violations %v", len(res.Hits), res.Reached, res.Violations)
	}
	for i, hits := range res.Hits {
		for _, hit := range hits {
			if hit.Gen != i+1 {
				t.Fatalf("callback escaped its generation: %+v", hit)
			}
			if i > 0 && hit.Setup {
				t.Fatalf("recovery is outside its window: %+v", hit)
			}
		}
	}
	last := res.Hits[1][len(res.Hits[1])-1]
	if last.Point != target.Point || last.Path != "" || last.From != "" {
		t.Fatalf("recovery did not close at its logical commit: %+v", last)
	}
	removed := false
	for _, hit := range res.Hits[1] {
		if hit.Point.Tag.Tx == "txn-cleanup" && hit.Point.Step == fsx.StepRemove {
			removed = strings.HasPrefix(hit.Path, "state/out/.tmp-*")
		}
	}
	if !removed {
		t.Fatal("recovery did not observe removal of the send's actual temporary file")
	}
}

func TestWalkBackendDrainsAMessageThatWasCommittedBeforeTheCrash(t *testing.T) {
	for _, target := range []crashwalk.Target{
		{Gen: 1, Point: walkPoint(t, "intake/commit#1")},
		{Gen: 1, AfterStep: "send"},
	} {
		t.Run(fmt.Sprint(target), func(t *testing.T) {
			s := sendWalkScenario()
			checked := false
			s.Check = func(h *harness.H) error {
				checked = true
				log, err := h.Ledger()
				if err != nil {
					return err
				}
				seen := log.Kind(ledger.KindModelSaw)
				if len(seen) != 1 || seen[0].Attrs.BodyDigest != ledger.Digest([]byte("kept across a crash")) {
					return fmt.Errorf("committed body was not read exactly once: %+v", seen)
				}
				return nil
			}
			res, err := harness.NewWalkBackend(t).Run(bg(), s, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
			if err != nil {
				t.Fatal(err)
			}
			if !checked || len(res.Reached) != 1 || !res.Reached[0] || len(res.Violations) != 0 {
				t.Fatalf("committed message: %d generations, reached %v, violations %v", len(res.Hits), res.Reached, res.Violations)
			}
		})
	}
}

func TestWalkBackendUsesADrainPointAsARecoveryTrigger(t *testing.T) {
	s := sendWalkScenario()
	b := harness.NewWalkBackend(t)
	recorded, err := b.Run(bg(), s, crashwalk.RunPlan{Record: true})
	if err != nil {
		t.Fatal(err)
	}
	var point fsx.Point
	for _, hit := range recorded.Hits[0] {
		if hit.Point.Tag.Tx == "ack" && hit.Point.Step == fsx.StepRename {
			point = hit.Point
			break
		}
	}
	if point == (fsx.Point{}) {
		t.Fatal("the drain did not record an acknowledgement rename")
	}
	trigger := crashwalk.CrashAt(point)
	target := crashwalk.Target{Gen: 2, Point: walkPoint(t, "txn-cleanup/commit#1")}
	s.Script.Crash.Recovery = []crashwalk.RecoveryCase{{Item: target.Point.Key, After: trigger}}
	recovery := s.Recovery()[0]
	res, err := b.Run(bg(), s, crashwalk.RunPlan{Trigger: &recovery.After, Targets: []crashwalk.Target{target}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 3 || !res.Reached[0] || len(res.Violations) != 0 {
		t.Fatalf("drain recovery: %d generations, reached %v, violations %v", len(res.Hits), res.Reached, res.Violations)
	}
	if err := crashwalk.CompareRuns(recorded.Hits[0], res.Hits[0], crashwalk.Target{Gen: 1, Point: point}); err != nil {
		t.Fatal(err)
	}
	if last := res.Hits[1][len(res.Hits[1])-1]; last.Point != target.Point {
		t.Fatalf("startup recovery was not cut at its commit: %+v", last)
	}
}

func TestWalkBackendClonesItsInitialDiskForEveryRun(t *testing.T) {
	initial := harness.NewStopped(t, harness.Options{}).Disk()
	before := snapshotOf(t, initial)
	s := sendWalkScenario()
	s.Options.Disk = initial
	b := harness.NewWalkBackend(t)
	first, err := b.Run(bg(), s, crashwalk.RunPlan{Record: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.Run(bg(), s, crashwalk.RunPlan{Record: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Hits, second.Hits) {
		t.Error("recording changed on the same initial disk")
	}
	target := crashwalk.Target{Gen: 1, Point: walkPoint(t, "intake/out.rename#1")}
	replay, err := b.Run(bg(), s, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Reached[0] || len(replay.Violations) != 0 {
		t.Fatalf("replay: %+v", replay)
	}
	if err := crashwalk.CompareRuns(first.Hits[0], replay.Hits[0], target); err != nil {
		t.Error(err)
	}
	if got := snapshotOf(t, initial); !reflect.DeepEqual(got, before) {
		t.Error("a run changed the supplied initial disk")
	}
}

func TestWalkBackendReturnsAFailedFinalJudgement(t *testing.T) {
	s := sendWalkScenario()
	failure := errors.New("the final oracle rejected this run")
	checks := 0
	s.Check = func(*harness.H) error {
		checks++
		return failure
	}
	res, err := harness.NewWalkBackend(t).Run(bg(), s, crashwalk.RunPlan{Record: true})
	if err != nil {
		t.Fatal(err)
	}
	if checks != 1 || !reflect.DeepEqual(res.Violations, []string{failure.Error()}) {
		t.Fatalf("final judgement called %d times, violations %v", checks, res.Violations)
	}
}

func TestWalkBackendReturnsExecutionErrorsBeforeJudgingTheRun(t *testing.T) {
	for _, phase := range []string{"step", "drain"} {
		t.Run(phase, func(t *testing.T) {
			s := sendWalkScenario()
			failure := errors.New("the scenario could not complete")
			if phase == "step" {
				s.Script.Steps[0].Do = func(scenario.Env) error { return failure }
			} else {
				s.Drain = func(*harness.H) error { return failure }
			}
			checked := false
			s.Check = func(*harness.H) error { checked = true; return nil }
			_, err := harness.NewWalkBackend(t).Run(bg(), s, crashwalk.RunPlan{Record: true})
			if !errors.Is(err, failure) || checked {
				t.Fatalf("execution error %v, checked %v", err, checked)
			}
		})
	}
}
