package inproc

import (
	"context"
	"sort"
	"testing"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

// recordCatalog returns each actual clean run for the combined acceptance report.
// Recovery checks consume these same results instead of recording the catalog twice.
func recordCatalog(t *testing.T) map[string]crashwalk.RunResult {
	records := make(map[string]crashwalk.RunResult)
	for _, listed := range bindCatalog(t) {
		if !t.Run(listed.Name(), func(t *testing.T) {
			if _, exists := records[listed.Name()]; exists {
				t.Fatalf("duplicate recipe %q", listed.Name())
			}
			records[listed.Name()] = recordScenario(t, freshScenario(t, listed.Name()))
		}) {
			t.FailNow()
		}
	}
	return records
}

func recordScenario(t testing.TB, s crashwalk.Scenario) crashwalk.RunResult {
	t.Helper()
	start := clock.System().Now()
	r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
	elapsed := clock.System().Now().Sub(start)
	if err != nil {
		t.Fatalf("record scenario=%s elapsed=%s: %v", s.Name(), elapsed, err)
	}
	if len(r.Violations) != 0 || len(r.Hits) != 1 {
		t.Fatalf("record scenario=%s elapsed=%s generations=%d violations=%v", s.Name(), elapsed, len(r.Hits), r.Violations)
	}
	cov := crashwalk.Assess(harness.ProgramTable(t), r.Hits[0])
	if len(cov.Undeclared) != 0 || cov.Recovery.Reached != 0 {
		t.Fatalf("record scenario=%s undeclared=%v recovery=%d", s.Name(), cov.Undeclared, cov.Recovery.Reached)
	}
	if err := crashwalk.CheckBudget(s, r.Hits[0]); err != nil {
		t.Fatal(err)
	}
	setup := 0
	for _, h := range r.Hits[0] {
		if h.Gen != 1 {
			t.Fatalf("record scenario=%s wrong generation: %+v", s.Name(), h)
		}
		if h.Setup {
			setup++
		}
	}
	t.Logf("record scenario=%s points=%d budget=%d boundaries=%d setup_hits=%d normal_keys=%d elapsed=%s", s.Name(), len(crashwalk.Plan(r.Hits[0], crashwalk.Full, nil)), s.MaxPoints(), len(s.Steps()), setup, cov.Normal.Reached, elapsed)
	t.Logf("record scenario=%s keys=%v", s.Name(), hitKeys(r.Hits[0]))
	return r
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
