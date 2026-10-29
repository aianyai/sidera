package inproc

import (
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func bindTerminal(r recipes.TerminalRecipe) *harness.TerminalWalkScenario {
	var operation harness.TerminalOperation
	switch r.Kind {
	case recipes.Stop:
		operation = harness.TerminalStop
	case recipes.Retire:
		operation = harness.TerminalRetire
	}
	return &harness.TerminalWalkScenario{Label: r.Name, Operation: operation, RequestID: r.RequestID, PointBudget: r.MaxPoints, Check: func(f harness.TerminalFacts) error {
		facts := recipes.TerminalFacts{Node: f.Node, RequestID: f.RequestID, Response: f.Response, Persisted: f.Persisted, Exited: f.Exited, Retired: f.Retired, RecoveryRejected: f.RecoveryRejected, SigningUnchanged: f.SigningUnchanged}
		if a := f.Audit; a != nil {
			facts.Audit = &recipes.TerminalAudit{Attempted: a.Attempted, Started: a.Started, IdentityRetired: a.IdentityRetired, SigningUnchanged: a.SigningUnchanged, Trace: a.Trace, Query: a.Query, Replay: a.Replay}
			if a.StartError != nil {
				facts.Audit.StartError = a.StartError.Error()
			}
			if a.ExitError != nil {
				facts.Audit.ExitError = a.ExitError.Error()
			}
		}
		return r.Check(facts)
	}}
}
