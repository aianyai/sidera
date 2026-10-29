package inproc

import (
	"context"
	"errors"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

func TestReplyPreparationRejectsASourceWhoseReceiptAlreadyCommitted(t *testing.T) {
	s := freshScenario(t, "delivery-reply-after-write-failure").(*harness.WalkScenario)
	s.Script.Steps[0].Do = func(scenario.Env) error { return nil }
	_, err := harness.NewWalkBackend(t).Run(context.Background(), s, crashwalk.RunPlan{Record: true})
	if !errors.Is(err, recipes.ErrSourceAlreadyConfirmed) {
		t.Fatalf("reply preparation accepted an already confirmed source: %v", err)
	}
}
