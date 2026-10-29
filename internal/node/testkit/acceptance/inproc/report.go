package inproc

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
)

// CatalogEvidence contains validated clean and complete recovery recordings.
// It carries no fixture handles and does not prove any recovery interruption.
type CatalogEvidence struct {
	Clean    map[string][]crashwalk.Hit
	Recovery []crashwalk.RecoveryRecord
}

// RecordCatalog records the complete in-process catalog once per clean scenario
// and unique recovery trigger. It returns evidence only after every declaration
// is covered by clean generation 1 or complete generation 2. The test owns the
// harness resources; harness failures may still terminate the test through t.
func RecordCatalog(t testing.TB) (CatalogEvidence, error) {
	t.Helper()
	table := harness.ProgramTable(t)
	scenarios := bindCatalog(t)
	names := map[string]bool{}
	for _, s := range scenarios {
		if names[s.Name()] {
			return CatalogEvidence{}, fmt.Errorf("duplicate recipe %q", s.Name())
		}
		names[s.Name()] = true
	}
	if err := crashwalk.CheckRecoveryCases(table, scenarios); err != nil {
		return CatalogEvidence{}, err
	}
	evidence := CatalogEvidence{Clean: make(map[string][]crashwalk.Hit, len(scenarios))}
	var cleanHits []crashwalk.Hit
	for _, s := range scenarios {
		r, err := recordScenario(t, freshScenario(t, s.Name()))
		if err != nil {
			return CatalogEvidence{}, err
		}
		evidence.Clean[s.Name()] = r.Hits[0]
		cleanHits = append(cleanHits, r.Hits[0]...)
	}
	triggers := make([]crashwalk.RecoveryTrigger, 0)
	planned := map[crashwalk.RecoveryTrigger]bool{}
	addTrigger := func(name string, trigger crashwalk.Trigger) {
		id := crashwalk.RecoveryTrigger{Scenario: name, After: trigger}
		if !planned[id] {
			planned[id] = true
			triggers = append(triggers, id)
		}
	}
	catalog := recipes.New(newBindings())
	for _, s := range scenarios {
		for _, c := range s.Recovery() {
			addTrigger(s.Name(), c.After)
		}
		for _, r := range catalog.Scripts {
			if r.Script.Name == s.Name() {
				for _, trigger := range r.RecoveryTriggers {
					addTrigger(s.Name(), trigger)
				}
			}
		}
	}
	for _, id := range triggers {
		started := clock.System().Now()
		r, err := harness.NewWalkBackend(t).Run(context.Background(), freshScenario(t, id.Scenario), crashwalk.RunPlan{Trigger: &id.After})
		if err != nil {
			return CatalogEvidence{}, fmt.Errorf("record recovery scenario=%s trigger=%+v: %w", id.Scenario, id.After, err)
		}
		if err := crashwalk.CheckRecoveryRecord(evidence.Clean[id.Scenario], id.After, r); err != nil {
			return CatalogEvidence{}, fmt.Errorf("record recovery scenario=%s trigger=%+v: %w", id.Scenario, id.After, err)
		}
		if err := checkDeclaredWrites(table, r); err != nil {
			return CatalogEvidence{}, fmt.Errorf("record recovery scenario=%s trigger=%+v: %w", id.Scenario, id.After, err)
		}
		hits := r.Hits[1]
		evidence.Recovery = append(evidence.Recovery, crashwalk.RecoveryRecord{Scenario: id.Scenario, After: id.After, Hits: hits})
		t.Logf("recovery-record scenario=%s trigger=%+v gen2_keys=%d elapsed=%s", id.Scenario, id.After, len(hitKeys(hits)), clock.System().Now().Sub(started))
		t.Logf("recovery-record scenario=%s gen=2 keys=%v", id.Scenario, hitKeys(hits))
		reportProvenance(t, id.Scenario, fmt.Sprint(id.After), hits)
	}
	analysis, err := crashwalk.AnalyzeSources(table, cleanHits, evidence.Recovery)
	if err != nil {
		return CatalogEvidence{}, err
	}
	c := analysis.Coverage
	if len(c.MissingNormal) != 0 || len(c.MissingRecovery) != 0 || len(c.Undeclared) != 0 {
		return CatalogEvidence{}, fmt.Errorf("incomplete catalog evidence: missing_normal=%v missing_recovery=%v undeclared=%v", c.MissingNormal, c.MissingRecovery, c.Undeclared)
	}
	return evidence, nil
}

func recordScenario(t testing.TB, s crashwalk.Scenario) (crashwalk.RunResult, error) {
	t.Helper()
	start := clock.System().Now()
	r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
	elapsed := clock.System().Now().Sub(start)
	if err != nil {
		return crashwalk.RunResult{}, fmt.Errorf("record scenario=%s elapsed=%s: %w", s.Name(), elapsed, err)
	}
	if len(r.Violations) != 0 || len(r.Hits) != 1 {
		return crashwalk.RunResult{}, fmt.Errorf("record scenario=%s elapsed=%s generations=%d violations=%v", s.Name(), elapsed, len(r.Hits), r.Violations)
	}
	cov := crashwalk.Assess(harness.ProgramTable(t), r.Hits[0])
	if len(cov.Undeclared) != 0 || cov.Recovery.Reached != 0 {
		return crashwalk.RunResult{}, fmt.Errorf("record scenario=%s undeclared=%v recovery=%d", s.Name(), cov.Undeclared, cov.Recovery.Reached)
	}
	if err := crashwalk.CheckBudget(s, r.Hits[0]); err != nil {
		return crashwalk.RunResult{}, fmt.Errorf("record scenario=%s: %w", s.Name(), err)
	}
	setup := 0
	for _, h := range r.Hits[0] {
		if h.Gen != 1 {
			return crashwalk.RunResult{}, fmt.Errorf("record scenario=%s wrong generation: %+v", s.Name(), h)
		}
		if h.Setup {
			setup++
		}
	}
	t.Logf("record scenario=%s points=%d budget=%d boundaries=%d setup_hits=%d normal_keys=%d elapsed=%s", s.Name(), len(crashwalk.Plan(r.Hits[0], crashwalk.Full, nil)), s.MaxPoints(), len(s.Steps()), setup, cov.Normal.Reached, elapsed)
	t.Logf("record scenario=%s keys=%v", s.Name(), hitKeys(r.Hits[0]))
	reportProvenance(t, s.Name(), "clean", r.Hits[0])
	return r, nil
}

func hitKeys(hits []crashwalk.Hit) []string {
	seen := map[string]bool{}
	for _, h := range hits {
		seen[h.Point.Key.String()] = true
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func reportCoverage(t testing.TB, path string, hits []crashwalk.Hit) {
	t.Helper()
	c := crashwalk.Assess(harness.ProgramTable(t), hits)
	t.Logf("coverage path=%s normal=%d/%d recovery=%d/%d missing_normal=%v missing_recovery=%v undeclared=%v", path, c.Normal.Reached, c.Normal.Declared, c.Recovery.Reached, c.Recovery.Declared, c.MissingNormal, c.MissingRecovery, c.Undeclared)
}

func reportProvenance(t testing.TB, scenario, trigger string, hits []crashwalk.Hit) {
	t.Helper()
	first := map[string]crashwalk.Hit{}
	for _, h := range hits {
		key := h.Point.Key.String()
		if _, exists := first[key]; !exists {
			first[key] = h
		}
	}
	for _, key := range hitKeys(hits) {
		h := first[key]
		t.Logf("evidence scenario=%s trigger=%s gen=%d point=%s path=%q from=%q setup=%v", scenario, trigger, h.Gen, h.Point, h.Path, h.From, h.Setup)
	}
}

func checkDeclaredWrites(table *txn.Table, r crashwalk.RunResult) error {
	for i, hits := range r.Hits {
		c := crashwalk.Assess(table, hits)
		if len(c.Undeclared) != 0 {
			return fmt.Errorf("generation %d undeclared=%v", i+1, c.Undeclared)
		}
	}
	return nil
}
