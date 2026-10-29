package crashwalk

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

// RecoveryTrigger requests a complete recovery recording for a named scenario.
// Scenario.Recovery already supplies its triggers; additional ones need no case.
type RecoveryTrigger struct {
	Scenario string
	After    Trigger
}

// RecoveryRecord holds only generation 2 of a completed trigger-only run.
// The caller must establish completion and domain invariants before supplying it.
type RecoveryRecord struct {
	Scenario string
	After    Trigger
	Hits     []Hit
}

// RecoveryNormalTarget is an actual second-generation point and its baseline's
// scenario and trigger. Each Normal key absent from clean runs has one owner.
type RecoveryNormalTarget struct {
	Scenario string
	After    Trigger
	Target   Target
}

// Sources separates declaration kind from the generation that proves coverage.
// All slices are unique keys in lexical order.
type Sources struct {
	NormalInClean        []fsx.Key
	NormalOnlyInRecovery []fsx.Key
	RecoveryInRecovery   []fsx.Key
}

// SourceAnalysis reports coverage and interruptions derived from all recordings.
type SourceAnalysis struct {
	Sources  Sources
	Coverage Coverage
	Targets  []RecoveryNormalTarget
}

func triggerLess(a, b Trigger) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.CrashAt != b.CrashAt {
		return pointLess(a.CrashAt, b.CrashAt)
	}
	if a.Pattern != b.Pattern {
		return a.Pattern < b.Pattern
	}
	return a.Damage < b.Damage
}

func sourceLess(a, b RecoveryTrigger) bool {
	if a.Scenario != b.Scenario {
		return a.Scenario < b.Scenario
	}
	return triggerLess(a.After, b.After)
}

// AnalyzeSources derives coverage only from clean generation 1 and complete
// generation 2. Supply every clean recording before computing the difference:
// a later clean source removes a key from the recovery-only responsibilities.
func AnalyzeSources(table *txn.Table, clean []Hit, recovered []RecoveryRecord) (SourceAnalysis, error) {
	var out SourceAnalysis
	kinds := map[fsx.Key]txn.Kind{}
	for _, k := range table.Keys() {
		kinds[k.Key] = k.Kind
	}
	cleanKeys := map[fsx.Key]bool{}
	for _, h := range clean {
		if h.Gen != 1 {
			return out, fmt.Errorf("crashwalk: clean evidence has generation %d, want 1", h.Gen)
		}
		if kinds[h.Point.Key] == txn.Recovery {
			return out, fmt.Errorf("crashwalk: clean evidence reached recovery write %v", h.Point)
		}
		cleanKeys[h.Point.Key] = true
	}
	records := append([]RecoveryRecord(nil), recovered...)
	sort.SliceStable(records, func(i, j int) bool {
		return sourceLess(RecoveryTrigger{records[i].Scenario, records[i].After}, RecoveryTrigger{records[j].Scenario, records[j].After})
	})
	owners := map[fsx.Key]RecoveryNormalTarget{}
	var all []Hit
	all = append(all, clean...)
	recoveryKeys := map[fsx.Key]bool{}
	for _, r := range records {
		for _, h := range r.Hits {
			if h.Gen != 2 {
				return out, fmt.Errorf("crashwalk: recovery evidence for scenario %q has generation %d, want 2", r.Scenario, h.Gen)
			}
			all = append(all, h)
			recoveryKeys[h.Point.Key] = true
			if kind, ok := kinds[h.Point.Key]; ok && kind == txn.Normal && !cleanKeys[h.Point.Key] && !IsSync(h.Point.Step) {
				if _, chosen := owners[h.Point.Key]; !chosen {
					owners[h.Point.Key] = RecoveryNormalTarget{Scenario: r.Scenario, After: r.After, Target: Target{Gen: 2, Point: h.Point}}
				}
			}
		}
	}
	out.Coverage = Assess(table, all)
	for _, k := range table.Keys() {
		switch {
		case k.Kind == txn.Recovery && recoveryKeys[k.Key]:
			out.Sources.RecoveryInRecovery = append(out.Sources.RecoveryInRecovery, k.Key)
		case k.Kind == txn.Normal && cleanKeys[k.Key]:
			out.Sources.NormalInClean = append(out.Sources.NormalInClean, k.Key)
		case k.Kind == txn.Normal && recoveryKeys[k.Key]:
			out.Sources.NormalOnlyInRecovery = append(out.Sources.NormalOnlyInRecovery, k.Key)
		}
	}
	for _, keys := range [][]fsx.Key{out.Sources.NormalInClean, out.Sources.NormalOnlyInRecovery, out.Sources.RecoveryInRecovery} {
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	}
	for _, k := range out.Sources.NormalOnlyInRecovery {
		if tg, ok := owners[k]; ok {
			out.Targets = append(out.Targets, tg)
		}
	}
	return out, nil
}

// CheckRecoveryTarget establishes an actual second interruption and final third
// generation, then compares its prefix with the complete generation-2 baseline.
// CompareRuns owns path and interleaving rules; copies adapt its gen1 window.
func CheckRecoveryTarget(recorded []Hit, target Target, res RunResult) error {
	if target.Gen != 2 || target.AfterStep != "" {
		return fmt.Errorf("crashwalk: recovery target must name a generation-2 point")
	}
	if len(res.Reached) != 1 || !res.Reached[0] {
		return fmt.Errorf("crashwalk: recovery target %v was not reached", target.Point)
	}
	if len(res.Hits) != 3 {
		return fmt.Errorf("crashwalk: recovery target %v ended with %d generations, want 3", target.Point, len(res.Hits))
	}
	for i, hits := range res.Hits {
		for _, h := range hits {
			if h.Gen != i+1 {
				return fmt.Errorf("crashwalk: generation %d contains generation-%d evidence", i+1, h.Gen)
			}
		}
	}
	if len(res.Violations) > 0 {
		return fmt.Errorf("crashwalk: recovery target %v: %s", target.Point, strings.Join(res.Violations, "; "))
	}
	baseline := append([]Hit(nil), recorded...)
	for i := range baseline {
		if baseline[i].Gen != 2 {
			return fmt.Errorf("crashwalk: recovery baseline contains generation %d, want 2", baseline[i].Gen)
		}
		baseline[i].Gen = 1
	}
	return CompareRuns(baseline, res.Hits[1], target)
}
