package crashwalk

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	// RecoveryTriggers adds trigger-only recordings to those supplied by cases.
	RecoveryTriggers []RecoveryTrigger
	// Observe receives run start/end pairs and a Skipped notification for the
	// dynamic group omitted by Smoke. Only non-Skipped events time real runs;
	// the walk reads no clock.
	Observe func(Event)
}

// Event tells the observer of a run or an explicitly skipped group.
type Event struct {
	// Phase is "record", "recovery-record", "main", "boundary", "recovery"
	// or "recovery-normal". A skipped event describes a group, not a run.
	Phase    string
	Scenario string
	Target   Target
	Case     RecoveryCase
	Trigger  *Trigger
	// Skipped marks the dynamic group omitted by Smoke; Planned is the global
	// target count for that notification. Both are zero for real run events.
	// A Skipped event is a single notification, with no corresponding run start.
	Skipped bool
	Planned int
	// Done is false when the run starts and true when it ends.
	Done bool
}

// Report counts what a walk did.
type Report struct {
	Recordings   int
	MainRuns     int
	BoundaryRuns int
	RecoveryRuns int
	// Sources and coverage use all clean and complete second-generation records.
	Sources            Sources
	RecoveryRecordings int
	// Planned is global, Selected belongs to this shard, Runs counts executions.
	RecoveryNormalPlanned, RecoveryNormalSelected, RecoveryNormalRuns int
	RecoveryNormalSkipped                                             bool
	Normal, Recovery                                                  PathCount
	// Triggers, in a full walk only, say which main crash point made the system
	// write which recovery writes when it restarted: what a scenario's author
	// needs to fill its recovery cases. Only the runs of the shard are in it.
	Triggers []Triggered
}

// Triggered is a main crash point and the recovery writes the restart after it
// reached.
type Triggered struct {
	At    fsx.Point
	Items []fsx.Key
}

// Walk first records every scenario cleanly and every unique recovery trigger
// completely. All shards use those same sources to analyze coverage and dynamic
// targets. It then runs the shard's main, boundary and static recovery targets;
// Full also runs recovery-only Normal targets and requires complete coverage of
// both declaration kinds. Smoke reports but skips the dynamic group.
// A target miss, divergent prefix, undeclared write or domain failure ends the walk.
func Walk(ctx context.Context, b Backend, table *txn.Table, scenarios []Scenario, opts Options) (Report, error) {
	var rep Report
	// The runs are numbered in the order of the scenarios' names, so that every
	// shard numbers them alike whatever order the caller lists the scenarios in.
	scenarios = append([]Scenario(nil), scenarios...)
	sort.SliceStable(scenarios, func(i, j int) bool { return scenarios[i].Name() < scenarios[j].Name() })
	for i := 1; i < len(scenarios); i++ {
		if scenarios[i].Name() == scenarios[i-1].Name() {
			return rep, fmt.Errorf("crashwalk: two scenarios are named %q", scenarios[i].Name())
		}
	}
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
	w := walker{ctx: ctx, b: b, opts: opts, kinds: kinds, rep: &rep, chosen: chosenRecovery(scenarios, opts.Mode)}
	triggers, err := recordingTriggers(scenarios, opts.RecoveryTriggers)
	if err != nil {
		return rep, err
	}
	recordings := map[string][]Hit{}
	byName := map[string]Scenario{}
	var recorded []Hit
	for _, s := range scenarios {
		byName[s.Name()] = s
		hits, err := w.record(s)
		if err != nil {
			return rep, err
		}
		recordings[s.Name()] = hits
		recorded = append(recorded, hits...)
	}
	for _, tr := range triggers {
		r, err := w.recoveryRecord(byName[tr.Scenario], tr.After)
		if err != nil {
			return rep, err
		}
		w.recovered = append(w.recovered, r)
	}
	analysis, err := AnalyzeSources(table, recorded, w.recovered)
	if err != nil {
		return rep, err
	}
	rep.Sources = analysis.Sources
	rep.Normal, rep.Recovery = analysis.Coverage.Normal, analysis.Coverage.Recovery
	rep.RecoveryNormalPlanned = len(analysis.Targets)
	if len(analysis.Coverage.MissingNormal) > 0 {
		return rep, missingWrites("declared writes were never reached by any recording", analysis.Coverage.MissingNormal)
	}
	if opts.Mode == Full && len(analysis.Coverage.MissingRecovery) > 0 {
		return rep, missingWrites("recovery writes were never reached by any complete generation-2 recording", analysis.Coverage.MissingRecovery)
	}
	for _, s := range scenarios {
		if err := w.mainRuns(s, recordings[s.Name()]); err != nil {
			return rep, err
		}
		if err := w.recoveryRuns(s); err != nil {
			return rep, err
		}
	}
	if opts.Mode == Smoke {
		rep.RecoveryNormalSkipped = true
		w.observe(Event{Phase: "recovery-normal", Skipped: true, Planned: len(analysis.Targets), Done: true})
	} else {
		for i := range analysis.Targets {
			if opts.Shard.Keep(w.next + i) {
				rep.RecoveryNormalSelected++
			}
		}
		for _, tg := range analysis.Targets {
			if err := w.recoveryNormalRun(byName[tg.Scenario], tg); err != nil {
				return rep, err
			}
		}
	}
	sort.Slice(rep.Triggers, func(i, j int) bool { return pointLess(rep.Triggers[i].At, rep.Triggers[j].At) })
	return rep, nil
}

type walker struct {
	ctx   context.Context
	b     Backend
	opts  Options
	kinds map[fsx.Key]txn.Kind
	rep   *Report
	next  int // runs planned so far, for sharding
	// chosen are the recovery writes the mode runs a case for, over all scenarios.
	chosen    map[fsx.Key]bool
	recovered []RecoveryRecord
}

// chosenRecovery picks the recovery writes a walk of the mode crashes. The choice
// is made over the cases of all scenarios together: a recovery transaction is
// crashed once, however its writes are shared among scenarios.
func chosenRecovery(scenarios []Scenario, mode Mode) map[fsx.Key]bool {
	var all []RecoveryCase
	for _, s := range scenarios {
		all = append(all, s.Recovery()...)
	}
	chosen := map[fsx.Key]bool{}
	for _, c := range PlanRecovery(all, mode) {
		chosen[c.Item] = true
	}
	return chosen
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
	w.observe(Event{Phase: phase, Scenario: s.Name(), Target: tg, Case: c, Trigger: p.Trigger})
	res, err := w.b.Run(w.ctx, s, p)
	w.observe(Event{Phase: phase, Scenario: s.Name(), Target: tg, Case: c, Trigger: p.Trigger, Done: true})
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
		w.noteTriggers(tg, res)
	}
	return nil
}

// noteTriggers records, in a full walk, the recovery writes the restart after a
// main crash point reached.
func (w *walker) noteTriggers(tg Target, res RunResult) {
	var items []fsx.Key
	seen := map[fsx.Key]bool{}
	for _, gen := range res.Hits[min(1, len(res.Hits)):] {
		for _, h := range gen {
			if w.kinds[h.Point.Key] == txn.Recovery && !seen[h.Point.Key] {
				seen[h.Point.Key] = true
				items = append(items, h.Point.Key)
			}
		}
	}
	if w.opts.Mode == Full && tg.AfterStep == "" && len(items) > 0 {
		w.rep.Triggers = append(w.rep.Triggers, Triggered{At: tg.Point, Items: items})
	}
}

func (w *walker) recoveryRuns(s Scenario) error {
	for _, c := range PlanRecovery(s.Recovery(), Full) {
		if !w.chosen[c.Item] {
			continue
		}
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
		if err := CheckRecoveryTarget(w.baseline(s.Name(), trigger), tg, res); err != nil {
			return fmt.Errorf("crashwalk: scenario %q: %w", s.Name(), err)
		}
	}
	return nil
}

func missingWrites(message string, keys []fsx.Key) error {
	var names []string
	for _, k := range keys {
		names = append(names, k.String())
	}
	return fmt.Errorf("crashwalk: %s: %s", message, strings.Join(names, ", "))
}

// recordingTriggers validates all input before any backend call and fixes record
// order independently of scenario order or repeated trigger metadata.
func recordingTriggers(scenarios []Scenario, extra []RecoveryTrigger) ([]RecoveryTrigger, error) {
	names := map[string]bool{}
	var inputs []RecoveryTrigger
	for _, s := range scenarios {
		names[s.Name()] = true
		for _, c := range s.Recovery() {
			inputs = append(inputs, RecoveryTrigger{s.Name(), c.After})
		}
	}
	inputs = append(inputs, extra...)
	seen := map[RecoveryTrigger]bool{}
	var out []RecoveryTrigger
	for _, tr := range inputs {
		if !names[tr.Scenario] {
			return nil, fmt.Errorf("crashwalk: recovery trigger names unknown scenario %q", tr.Scenario)
		}
		t := tr.After
		valid := false
		switch t.Kind {
		case TriggerCrashAt:
			_, err := fsx.ParsePoint(t.CrashAt.String())
			valid = err == nil && t.Pattern == "" && t.Damage == ""
		case TriggerCorrupt:
			valid = t.CrashAt == (fsx.Point{}) && t.Pattern != "" && t.Damage != ""
		}
		if !valid {
			return nil, fmt.Errorf("crashwalk: scenario %q has an invalid recovery trigger: %+v", tr.Scenario, t)
		}
		if !seen[tr] {
			seen[tr] = true
			out = append(out, tr)
		}
	}
	sort.Slice(out, func(i, j int) bool { return sourceLess(out[i], out[j]) })
	return out, nil
}

func (w *walker) recoveryRecord(s Scenario, trigger Trigger) (RecoveryRecord, error) {
	r := RecoveryRecord{Scenario: s.Name(), After: trigger}
	res, err := w.run("recovery-record", s, RunPlan{Trigger: &trigger}, Target{}, RecoveryCase{})
	if err != nil {
		return r, err
	}
	w.rep.RecoveryRecordings++
	if len(res.Hits) != 2 || len(res.Reached) != 0 {
		return r, fmt.Errorf("crashwalk: scenario %q complete recovery recording must end in generation 2 without targets", s.Name())
	}
	if len(res.Violations) > 0 {
		return r, fmt.Errorf("crashwalk: scenario %q failed its complete recovery recording: %s", s.Name(), strings.Join(res.Violations, "; "))
	}
	for i, hits := range res.Hits {
		for _, h := range hits {
			if h.Gen != i+1 {
				return r, fmt.Errorf("crashwalk: scenario %q recovery recording generation %d contains generation-%d evidence", s.Name(), i+1, h.Gen)
			}
		}
	}
	r.Hits = append([]Hit(nil), res.Hits[1]...)
	return r, nil
}

func (w *walker) baseline(scenario string, trigger Trigger) []Hit {
	for _, r := range w.recovered {
		if r.Scenario == scenario && r.After == trigger {
			return r.Hits
		}
	}
	return nil
}

func (w *walker) recoveryNormalRun(s Scenario, t RecoveryNormalTarget) error {
	k := w.next
	w.next++
	if !w.opts.Shard.Keep(k) {
		return nil
	}
	trigger := t.After
	res, err := w.run("recovery-normal", s, RunPlan{Trigger: &trigger, Targets: []Target{t.Target}}, t.Target, RecoveryCase{})
	if err != nil {
		return err
	}
	w.rep.RecoveryNormalRuns++
	if err := CheckRecoveryTarget(w.baseline(s.Name(), trigger), t.Target, res); err != nil {
		return fmt.Errorf("crashwalk: scenario %q: %w", s.Name(), err)
	}
	return nil
}
