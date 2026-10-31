package inproc

import (
	"context"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func TestCatalogEvidence(t *testing.T) {
	started := clock.System().Now()
	evidence, err := RecordCatalog(t)
	if err != nil {
		t.Fatal(err)
	}
	var all []crashwalk.Hit
	for _, listed := range bindCatalog(t) {
		all = append(all, evidence.Clean[listed.Name()]...)
	}
	reportCoverage(t, "clean-gen1", all)
	reportLayerEvidence(t, evidence)
	recoveryEvidence(t, evidence, all)
	t.Logf("runs elapsed=%s", clock.System().Now().Sub(started))
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

// A name on the list of declarations not yet walked is a declaration of the
// program: a name that is mistyped, or whose declaration was removed, would owe
// nothing and hide that it does not.
func TestEveryDeclarationNotYetWalkedIsDeclared(t *testing.T) {
	declared := map[string]bool{}
	for _, k := range harness.ProgramTable(t).Keys() {
		declared[k.Tag.Tx] = true
	}
	for name := range crashwalk.NotYetWalked {
		if !declared[name] {
			t.Errorf("NotYetWalked lists %q, which the program does not declare", name)
		}
	}
}
