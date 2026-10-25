package crashwalk

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

// RunPlan is what a backend is asked to do in one run.
type RunPlan struct {
	// Record asks for a run without a crash, to collect every hit.
	Record bool
	// Targets are the kills of the run, one per process generation that is to be
	// killed; the generation after the last target runs on to the end. A target for
	// a generation the run never reaches is a failed run.
	Targets []Target
	// Trigger, when set, first brings the system to the state a recovery case needs
	// (a crash, or damage made while it is stopped); the targets are then for the
	// generations after it.
	Trigger *Trigger
}

// RunResult is what a run did.
type RunResult struct {
	// Hits are the crash points each process generation reached, with the first
	// generation first; a generation that was killed ends at its target.
	Hits [][]Hit
	// Reached says, for each target of the plan, whether the system got to it.
	Reached []bool
	// Violations are the invariants that failed after the run was restarted and
	// drained: each a sentence that says what was promised and was not kept.
	Violations []string
}

// Backend runs a scenario as the plan says. It owns the script, the system, the
// kill, the restart, the drain and the invariants; the walk owns which runs to
// make and what to make of their hits.
type Backend interface {
	Run(ctx context.Context, s Scenario, p RunPlan) (RunResult, error)
}

// Options are the settings of a walk.
type Options struct {
	Mode  Mode
	Shard Shard
	// Observe, when set, is called when a run starts and when it ends, so that the
	// caller can time each run and print the times: the walk reads no clock.
	Observe func(Event)
}

// Event tells the observer of a run.
type Event struct {
	// Phase is "record", "main", "boundary" or "recovery".
	Phase    string
	Scenario string
	Target   Target
	Case     RecoveryCase
	// Done is false when the run starts and true when it ends.
	Done bool
}

// Report counts what a walk did.
type Report struct {
	Recordings   int
	MainRuns     int
	BoundaryRuns int
	RecoveryRuns int
}

// Walk runs every scenario: a recording without a crash, then the planned runs of
// the mode that belong to the shard, then the recovery cases. It fails on the first
// run that does not reach its target, whose rerun differs from the recording or
// that breaks an invariant, and, after all runs, on a write no declaration lists
// and on a declared write of a normal transaction that the recordings never
// reached.
//
// Every shard records every scenario, however the runs are shared: the recording is
// cheap, and the coverage assertion is about the whole program.
func Walk(ctx context.Context, b Backend, table *txn.Table, scenarios []Scenario, opts Options) (Report, error) {
	var rep Report
	if opts.Mode != Smoke && opts.Mode != Full {
		return rep, errors.New("crashwalk: Options.Mode is not set")
	}
	if opts.Shard.Count == 0 {
		opts.Shard = Shard{Index: 1, Count: 1}
	}
	if err := CheckRecoveryCases(table, scenarios); err != nil {
		return rep, err
	}
	kinds := map[fsx.Key]txn.Kind{}
	for _, k := range table.Keys() {
		kinds[k.Key] = k.Kind
	}
	w := walker{ctx: ctx, b: b, opts: opts, kinds: kinds, rep: &rep}
	var recorded []Hit
	for _, s := range scenarios {
		hits, err := w.record(s)
		if err != nil {
			return rep, err
		}
		recorded = append(recorded, hits...)
		if err := w.mainRuns(s, hits); err != nil {
			return rep, err
		}
		if err := w.recoveryRuns(s); err != nil {
			return rep, err
		}
	}
	cov := Assess(table, recorded)
	if len(cov.MissingNormal) > 0 {
		var names []string
		for _, k := range cov.MissingNormal {
			names = append(names, k.String())
		}
		return rep, fmt.Errorf("crashwalk: declared writes were never reached by any recording: %s", strings.Join(names, ", "))
	}
	return rep, nil
}

type walker struct {
	ctx   context.Context
	b     Backend
	opts  Options
	kinds map[fsx.Key]txn.Kind
	rep   *Report
	next  int // runs planned so far, for sharding
}

func (w *walker) observe(e Event) {
	if w.opts.Observe != nil {
		w.opts.Observe(e)
	}
}

// run makes one run, checks what every run must satisfy, and returns it.
func (w *walker) run(phase string, s Scenario, p RunPlan, tg Target, c RecoveryCase) (RunResult, error) {
	if err := w.ctx.Err(); err != nil {
		return RunResult{}, err
	}
	w.observe(Event{Phase: phase, Scenario: s.Name(), Target: tg, Case: c})
	res, err := w.b.Run(w.ctx, s, p)
	w.observe(Event{Phase: phase, Scenario: s.Name(), Target: tg, Case: c, Done: true})
	if err != nil {
		return res, fmt.Errorf("crashwalk: scenario %q: %w", s.Name(), err)
	}
	// A write that no declaration lists ends the walk at once: it means a write
	// that does not go through a declared transaction, and running thousands of
	// kills before saying so would waste the time of whoever reads the failure.
	var undeclared []string
	seen := map[fsx.Key]bool{}
	for _, gen := range res.Hits {
		for _, h := range gen {
			if _, declared := w.kinds[h.Point.Key]; !declared && !seen[h.Point.Key] {
				seen[h.Point.Key] = true
				undeclared = append(undeclared, h.Point.Key.String())
			}
		}
	}
	if len(undeclared) > 0 {
		return res, fmt.Errorf("crashwalk: scenario %q made undeclared writes: %s; every write goes through a declared transaction", s.Name(), strings.Join(undeclared, ", "))
	}
	return res, nil
}

func (w *walker) record(s Scenario) ([]Hit, error) {
	res, err := w.run("record", s, RunPlan{Record: true}, Target{}, RecoveryCase{})
	if err != nil {
		return nil, err
	}
	w.rep.Recordings++
	if len(res.Violations) > 0 {
		return nil, fmt.Errorf("crashwalk: scenario %q failed its own clean run: %s", s.Name(), strings.Join(res.Violations, "; "))
	}
	if len(res.Hits) != 1 {
		return nil, fmt.Errorf("crashwalk: the clean run of scenario %q has %d process generations, want 1", s.Name(), len(res.Hits))
	}
	for _, h := range res.Hits[0] {
		if w.kinds[h.Point.Key] == txn.Recovery {
			return nil, fmt.Errorf("crashwalk: the clean run of scenario %q reached the recovery write %v: a clean state has nothing to recover, so either the code writes where it should not or the declaration is wrong", s.Name(), h.Point)
		}
	}
	if err := CheckBudget(s, res.Hits[0]); err != nil {
		return nil, err
	}
	return res.Hits[0], nil
}

func (w *walker) mainRuns(s Scenario, rec []Hit) error {
	for _, tg := range Plan(rec, w.opts.Mode, s.Steps()) {
		k := w.next
		w.next++
		if !w.opts.Shard.Keep(k) {
			continue
		}
		phase := "main"
		if tg.AfterStep != "" {
			phase = "boundary"
		}
		res, err := w.run(phase, s, RunPlan{Targets: []Target{tg}}, tg, RecoveryCase{})
		if err != nil {
			return err
		}
		if phase == "main" {
			w.rep.MainRuns++
		} else {
			w.rep.BoundaryRuns++
		}
		name := tg.Point.String()
		if tg.AfterStep != "" {
			name = "the end of step " + tg.AfterStep
		}
		if len(res.Reached) != 1 || !res.Reached[0] || len(res.Hits) < 1 {
			return fmt.Errorf("crashwalk: scenario %q: the target %s was not reached: the run ended before it, so the scenario no longer writes what the recording did", s.Name(), name)
		}
		if len(res.Violations) > 0 {
			return fmt.Errorf("crashwalk: scenario %q killed at %s: %s", s.Name(), name, strings.Join(res.Violations, "; "))
		}
		if tg.AfterStep == "" {
			if err := CompareRuns(rec, res.Hits[0], tg); err != nil {
				return fmt.Errorf("scenario %q: %w", s.Name(), err)
			}
		}
	}
	return nil
}

func (w *walker) recoveryRuns(s Scenario) error {
	for _, c := range PlanRecovery(s.Recovery(), w.opts.Mode) {
		k := w.next
		w.next++
		if !w.opts.Shard.Keep(k) {
			continue
		}
		trigger := c.After
		tg := Target{Gen: 2, Point: fsx.Point{Key: c.Item, Hit: 1}}
		res, err := w.run("recovery", s, RunPlan{Targets: []Target{tg}, Trigger: &trigger}, tg, c)
		if err != nil {
			return err
		}
		w.rep.RecoveryRuns++
		if len(res.Reached) != 1 || !res.Reached[0] {
			return fmt.Errorf("crashwalk: scenario %q: the recovery write %v was not reached after its trigger: the trigger no longer causes it, or the recovery code changed", s.Name(), c.Item)
		}
		if len(res.Violations) > 0 {
			return fmt.Errorf("crashwalk: scenario %q, a crash in the recovery write %v: %s", s.Name(), c.Item, strings.Join(res.Violations, "; "))
		}
	}
	return nil
}
