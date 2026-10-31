//go:build acceptance && diagnostic

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

const ubuntuCostSeed = "TestCrashAtEveryCrashPointPreservesInvariants"

var ubuntuCostAttempt int

// This disposable sample keeps the complete catalog recipe and its oracle. The
// test runner's -count supplies repetitions; this entry point runs each pair once.
func TestUbuntuRunCostSample(t *testing.T) {
	ubuntuCostAttempt++
	attempt := ubuntuCostAttempt
	scenarios, _ := bindCatalog()
	for _, sample := range []struct{ scenario, target string }{
		{"maintenance-retention", "ack/body.remove#2"},
		{"recovery-body-feedback", "clock-high-water/clock.rename#2"},
	} {
		var selected crashwalk.Scenario
		for _, s := range scenarios {
			if s.Name() == sample.scenario {
				if selected != nil {
					t.Fatal("duplicate diagnostic scenario")
				}
				selected = s
			}
		}
		if selected == nil {
			t.Fatal("required diagnostic scenario is missing")
		}
		backend := NewWalkBackend(runRoot(t), testBinaries, Options{Seed: ubuntuCostSeed})
		record := newUbuntuCostRecorder(t, attempt, sample.scenario, sample.target, "record")
		backend.cost = record.callbacks()
		clean, err := backend.Run(t.Context(), selected, crashwalk.RunPlan{Record: true})
		var budgetErr error
		if len(clean.Hits) == 1 {
			budgetErr = crashwalk.CheckBudget(selected, clean.Hits[0])
		}
		record.judgement(clean, err, budgetErr)
		checkRun(t, clean, err, 1)
		if budgetErr != nil {
			t.Fatal(budgetErr)
		}
		target := crashwalk.Target{Gen: 1, Point: point(t, sample.target)}
		main := newUbuntuCostRecorder(t, attempt, sample.scenario, sample.target, "main")
		backend.cost = main.callbacks()
		result, runErr := backend.Run(t.Context(), selected, crashwalk.RunPlan{Targets: []crashwalk.Target{target}})
		var compareErr error
		if len(result.Hits) > 0 {
			compareErr = crashwalk.CompareRuns(clean.Hits[0], result.Hits[0], target)
		}
		main.judgement(result, runErr, compareErr)
		checkRun(t, result, runErr, 2)
		if len(result.Reached) != 1 {
			t.Fatal("diagnostic target must have exactly one result")
		}
		if compareErr != nil {
			t.Fatal(compareErr)
		}
	}
}

// No result, error, path, ledger attribute or environment map is serialized.
// All string fields below come from this test's fixed table or fixed labels.
type ubuntuCostRow struct {
	Attempt     int    `json:"attempt"`
	Scenario    string `json:"scenario"`
	Target      string `json:"target"`
	Mode        string `json:"mode"`
	Generation  int    `json:"generation"`
	Phase       string `json:"phase"`
	Event       string `json:"event"`
	OffsetNS    int64  `json:"offset_ns"`
	ElapsedNS   int64  `json:"elapsed_ns"`
	ResultClass string `json:"result_class"`
	Generations int    `json:"generations,omitempty"`
	Reached     int    `json:"reached,omitempty"`
	Violations  int    `json:"violations,omitempty"`
}

type ubuntuCostSpan struct {
	row   ubuntuCostRow
	start time.Time
}

type ubuntuCostRecorder struct {
	t      *testing.T
	base   ubuntuCostRow
	origin time.Time
	spans  []ubuntuCostSpan
	rows   []ubuntuCostRow
	file   *os.File
}

func newUbuntuCostRecorder(t *testing.T, attempt int, scenario, target, mode string) *ubuntuCostRecorder {
	t.Helper()
	r := &ubuntuCostRecorder{t: t, base: ubuntuCostRow{Attempt: attempt, Scenario: scenario, Target: target, Mode: mode}}
	if dir := os.Getenv("SIDERA_UBUNTU_COST_EXPORT"); dir != "" {
		file, err := os.OpenFile(filepath.Join(dir, "timings.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		r.file = file
		t.Cleanup(func() {
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	return r
}

func (r *ubuntuCostRecorder) emit(row ubuntuCostRow) {
	r.t.Helper()
	data, err := json.Marshal(row)
	if err != nil {
		r.t.Fatal(err)
	}
	fmt.Printf("[DEBUG-ubuntu-cost] %s\n", data)
	if r.file != nil {
		if _, err := fmt.Fprintf(r.file, "%s\n", data); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (r *ubuntuCostRecorder) callbacks() *walkCost {
	return &walkCost{begin: func(phase string, generation int) int {
		now := time.Now()
		if phase == "run" {
			r.origin = now
		}
		row := r.base
		row.Phase, row.Generation, row.Event = phase, generation, "start"
		row.OffsetNS = now.Sub(r.origin).Nanoseconds()
		r.spans = append(r.spans, ubuntuCostSpan{row: row, start: now})
		if phase == "run" {
			r.emit(row)
		} else {
			r.rows = append(r.rows, row)
		}
		return len(r.spans) - 1
	}, finish: func(id int, err error) {
		span := r.spans[id]
		row := span.row
		row.Event = "end"
		row.ElapsedNS = time.Since(span.start).Nanoseconds()
		row.OffsetNS = span.row.OffsetNS + row.ElapsedNS
		row.ResultClass = "ok"
		if err != nil {
			row.ResultClass = "error"
		}
		r.rows = append(r.rows, row)
		if row.Phase == "run" {
			r.remainder("run-remainder", row.ElapsedNS, "fixture", "scenario", "close", "remove")
			for _, ended := range r.rows {
				if ended.Phase == "scenario" && ended.Event == "end" {
					r.remainder("scenario-remainder", ended.ElapsedNS, "prepare", "start", "stop")
					break
				}
			}
			for _, buffered := range r.rows {
				r.emit(buffered)
			}
		}
	}}
}

// A remainder is a duration sum, not a contiguous span. Its offset is the run's
// final boundary; only sibling phases are subtracted, so nested time is not added twice.
func (r *ubuntuCostRecorder) remainder(phase string, total int64, children ...string) {
	row := r.base
	row.Phase, row.Event = phase, "remainder"
	row.OffsetNS = r.rows[len(r.rows)-1].OffsetNS
	row.ElapsedNS = total
	for _, measured := range r.rows {
		if measured.Event != "end" {
			continue
		}
		for _, child := range children {
			if measured.Phase == child {
				row.ElapsedNS -= measured.ElapsedNS
			}
		}
	}
	r.rows = append(r.rows, row)
}

func (r *ubuntuCostRecorder) judgement(result crashwalk.RunResult, runErr, checkErr error) {
	row := r.base
	row.Phase, row.Event = "judgement", "end"
	row.OffsetNS = time.Since(r.origin).Nanoseconds()
	row.Generations, row.Violations = len(result.Hits), len(result.Violations)
	for _, reached := range result.Reached {
		if reached {
			row.Reached++
		}
	}
	row.ResultClass = "ok"
	wantGenerations, wantReached := 1, 0
	if row.Mode == "main" {
		wantGenerations, wantReached = 2, 1
	}
	if runErr != nil {
		row.ResultClass = "run_error"
	} else if checkErr != nil {
		row.ResultClass = "check_error"
	} else if row.Generations != wantGenerations || row.Reached != wantReached || row.Violations != 0 {
		row.ResultClass = "invariant_error"
	}
	r.emit(row)
}
