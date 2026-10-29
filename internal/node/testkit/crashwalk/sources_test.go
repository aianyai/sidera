package crashwalk_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

func TestCheckRecoveryRecordRequiresCompleteConsistentEvidence(t *testing.T) {
	first := crashwalk.Hit{Gen: 1, Point: point(t, "status/file.rename#1"), Path: "state/status.rec", From: "state/.tmp-*"}
	second := crashwalk.Hit{Gen: 2, Point: point(t, "orphans/body.remove#1"), Path: "state/body/m1.tmp"}
	clean := []crashwalk.Hit{first}
	trigger := crashwalk.CrashAt(first.Point)
	good := func() crashwalk.RunResult {
		return crashwalk.RunResult{Hits: [][]crashwalk.Hit{{first}, {second}}}
	}
	if err := crashwalk.CheckRecoveryRecord(clean, trigger, good()); err != nil {
		t.Fatal(err)
	}
	if err := crashwalk.CheckRecoveryRecord(nil, crashwalk.CorruptOffline("state/x", "bad"), good()); err != nil {
		t.Fatalf("offline damage has no first-generation cutoff: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*crashwalk.RunResult)
		want   string
	}{
		{"missing recovery", func(r *crashwalk.RunResult) { r.Hits = r.Hits[:1] }, "must end in generation 2 without targets"},
		{"target result", func(r *crashwalk.RunResult) { r.Reached = []bool{false} }, "must end in generation 2 without targets"},
		{"domain failure", func(r *crashwalk.RunResult) { r.Violations = []string{"message lost"} }, "failed complete recovery recording: message lost"},
		{"first provenance", func(r *crashwalk.RunResult) { r.Hits[0][0].Gen = 2 }, "generation 1 contains generation-2 evidence"},
		{"second provenance", func(r *crashwalk.RunResult) { r.Hits[1][0].Gen = 3 }, "generation 2 contains generation-3 evidence"},
		{"trigger cutoff", func(r *crashwalk.RunResult) { r.Hits[0] = nil }, "did not end at its target"},
		{"trigger prefix", func(r *crashwalk.RunResult) { r.Hits[0][0].From = "state/other" }, "another path operand"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := good()
			tc.mutate(&r)
			if err := crashwalk.CheckRecoveryRecord(clean, trigger, r); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CheckRecoveryRecord = %v, want %q", err, tc.want)
			}
		})
	}
}

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
