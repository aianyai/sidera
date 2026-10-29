package crashwalk_test

import (
	"reflect"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

func TestAnalyzeSourcesUsesOnlyCompleteSecondGenerationsAndAllCleanSources(t *testing.T) {
	h := crashwalk.Hit{Gen: 2, Point: point(t, "status/file.write#4"), Path: "state/status.rec"}
	r := crashwalk.RecoveryRecord{Scenario: "z", After: crashwalk.CorruptOffline("state/x", "bad"), Hits: []crashwalk.Hit{h}}
	a, err := crashwalk.AnalyzeSources(table(t), nil, []crashwalk.RecoveryRecord{r})
	if err != nil || len(a.Targets) != 1 || a.Targets[0].Target.Point != h.Point || a.Coverage.Normal.Reached != 1 {
		t.Fatalf("analysis %+v, %v", a, err)
	}
	clean := h
	clean.Gen = 1
	a, err = crashwalk.AnalyzeSources(table(t), []crashwalk.Hit{clean}, []crashwalk.RecoveryRecord{r})
	if err != nil || len(a.Targets) != 0 || len(a.Sources.NormalOnlyInRecovery) != 0 {
		t.Fatalf("later clean coverage must remove dynamic responsibility: %+v, %v", a, err)
	}
	for _, gen := range []int{1, 3} {
		r.Hits[0].Gen = gen
		if _, err := crashwalk.AnalyzeSources(table(t), nil, []crashwalk.RecoveryRecord{r}); err == nil {
			t.Fatalf("generation %d accepted as complete gen2 evidence", gen)
		}
	}
	for _, h := range []crashwalk.Hit{{Gen: 2, Point: clean.Point}, {Gen: 1, Point: point(t, "orphans/body.remove#1")}} {
		if _, err := crashwalk.AnalyzeSources(table(t), []crashwalk.Hit{h}, nil); err == nil {
			t.Fatalf("invalid clean evidence accepted: %+v", h)
		}
	}
}

func TestAnalyzeSourcesChoosesStableActualResponsibility(t *testing.T) {
	h := crashwalk.Hit{Gen: 2, Point: point(t, "status/file.write#4")}
	a := crashwalk.RecoveryRecord{Scenario: "a", After: crashwalk.CorruptOffline("state/a", "bad"), Hits: []crashwalk.Hit{h}}
	z := a
	z.Scenario = "z"
	first, err := crashwalk.AnalyzeSources(table(t), nil, []crashwalk.RecoveryRecord{z, a})
	if err != nil {
		t.Fatal(err)
	}
	second, err := crashwalk.AnalyzeSources(table(t), nil, []crashwalk.RecoveryRecord{a, z})
	if err != nil || !reflect.DeepEqual(first, second) || len(first.Targets) != 1 || first.Targets[0].Scenario != "a" {
		t.Fatalf("unstable responsibility: %+v %+v %v", first, second, err)
	}
}

func TestCheckRecoveryTargetRejectsObservationWithoutActualInterruption(t *testing.T) {
	h := crashwalk.Hit{Gen: 2, Point: point(t, "status/file.rename#4"), Path: "state/status.rec", From: "state/.tmp-*"}
	tg := crashwalk.Target{Gen: 2, Point: h.Point}
	good := crashwalk.RunResult{Reached: []bool{true}, Hits: [][]crashwalk.Hit{nil, {h}, nil}}
	if err := crashwalk.CheckRecoveryTarget([]crashwalk.Hit{h}, tg, good); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unreached", "wrong end", "no third", "domain failure", "changed source"} {
		t.Run(name, func(t *testing.T) {
			r := good
			r.Hits = append([][]crashwalk.Hit(nil), good.Hits...)
			switch name {
			case "unreached":
				r.Reached = []bool{false}
			case "wrong end":
				x := h
				x.Point.Hit++
				r.Hits[1] = []crashwalk.Hit{x}
			case "no third":
				r.Hits = r.Hits[:2]
			case "domain failure":
				r.Violations = []string{"message lost"}
			case "changed source":
				x := h
				x.From = "other"
				r.Hits[1] = []crashwalk.Hit{x}
			}
			if err := crashwalk.CheckRecoveryTarget([]crashwalk.Hit{h}, tg, r); err == nil {
				t.Fatal("accepted a run without the promised second interruption and convergence")
			}
		})
	}
}
