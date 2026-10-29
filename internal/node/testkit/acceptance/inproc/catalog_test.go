package inproc

import (
	"context"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func TestCatalogNormalRecordings(t *testing.T) {
	records := recordCatalog(t)
	var all []crashwalk.Hit
	for _, listed := range bindCatalog(t) {
		all = append(all, records[listed.Name()].Hits[0]...)
	}
	reportCoverage(t, "clean-gen1", all)
}

func TestSendJudgementRejectsAnAcceptedBodyThatWasNotRead(t *testing.T) {
	s := freshScenario(t, "delivery-send").(*harness.WalkScenario)
	s.Drain = func(*harness.H) error { return nil }
	r, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Violations) != 1 || !strings.Contains(r.Violations[0], "has no visible body") {
		t.Fatalf("unread accepted body: violations=%v", r.Violations)
	}
}
