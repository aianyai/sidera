package acceptance

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/diskfacts"
)

// TerminalWalkScenario has no boundary after its real management operation exits.
type TerminalWalkScenario struct{ Recipe recipes.TerminalRecipe }

func (s *TerminalWalkScenario) Name() string                       { return s.Recipe.Name }
func (s *TerminalWalkScenario) Steps() []string                    { return nil }
func (s *TerminalWalkScenario) MaxPoints() int                     { return s.Recipe.MaxPoints }
func (s *TerminalWalkScenario) Recovery() []crashwalk.RecoveryCase { return nil }

func (r *binaryRun) runTerminal(ctx context.Context, s *TerminalWalkScenario) error {
	recipe := s.Recipe
	if recipe.Name == "" || recipe.MaxPoints <= 0 || recipe.RequestID.IsZero() || (recipe.Kind != recipes.Stop && recipe.Kind != recipes.Retire) {
		return errors.New("acceptance: invalid terminal scenario")
	}
	f := r.fixture
	if err := f.prepare(ctx); err != nil {
		return err
	}
	d, err := nodeDisk(f)
	if err != nil {
		return err
	}
	signing, err := diskfacts.ReadSigning(d)
	if err != nil {
		return err
	}
	// Loading keys validates bytes only; it neither signs nor repairs them.
	k, err := keys.Load(newReadWriter(f, d))
	if err != nil {
		return err
	}
	facts := recipes.TerminalFacts{Node: k.Node(), RequestID: recipe.RequestID}
	request := manageapi.Request{OperationID: recipe.RequestID}
	method := "daemon.stop"
	if recipe.Kind == recipes.Retire {
		method = "identity.retire"
		request.Params = map[string]string{"node": facts.Node.String()}
	}
	for {
		spec := r.spec()
		err = r.startLifecycle(ctx, spec)
		g := f.current
		if g == nil {
			return err
		}
		crashed := false
		if err != nil && g.p.exited() && recipe.Kind == recipes.Retire && spec.Number > 1 {
			retired, e := r.retiredRefusal(g)
			if e != nil {
				return e
			}
			if !retired {
				return err
			}
			facts.RecoveryRejected = true
			facts.SigningUnchanged, err = signing.Unchanged(d)
			if err != nil {
				return err
			}
		} else {
			var e error
			crashed, e = r.interrupted(ctx, g, err)
			if e != nil {
				return e
			}
			if !crashed {
				// Startup is setup only in the first generation; recovery remains visible.
				if spec.Number == 1 {
					_, n, e := readTrace(g.spec.Record, spec.Number, 0, f.table, true)
					if e != nil {
						return e
					}
					g.setup = n
				}
				response, callErr := f.Manage(ctx, method, request)
				if callErr == nil {
					facts.Response = response
				}
				if callErr == nil || g.spec.Target != "" {
					if e := g.p.wait(ctx); ctx.Err() != nil {
						return e
					}
				}
				trace, e := f.ReadTrace(spec.Number)
				if e != nil {
					return errors.Join(callErr, e)
				}
				crashed = trace.Reached
				if !crashed {
					if callErr != nil {
						return callErr
					}
					if g.p.err != nil {
						return f.exitResult(g)
					}
					facts.Exited = true
				}
			}
		}
		if err = r.finish(g); err != nil {
			return err
		}
		if crashed {
			continue
		}
		break
	}
	_, scan, err := scanDisk(f)
	if err != nil {
		return err
	}
	terminal, err := diskfacts.InspectTerminal(d, scan, facts.Node, recipe.RequestID)
	if err != nil {
		return err
	}
	facts.Persisted, facts.Retired = terminal.Persisted, terminal.Retired
	if !facts.RecoveryRejected {
		facts.Audit, err = r.auditTerminal(ctx, recipe, request, method, signing)
		if err != nil {
			r.violation(err)
		}
	}
	r.violation(recipe.Check(facts))
	return nil
}
func (r *binaryRun) retiredRefusal(g *generation) (bool, error) {
	if !g.p.exited() || g.p.err == nil {
		return false, nil
	}
	raw, err := os.ReadFile(g.stderr)
	if err != nil {
		return false, err
	}
	return strings.Contains(string(raw), keys.ErrIdentityRetired.Error()), nil
}
func (r *binaryRun) auditTerminal(ctx context.Context, recipe recipes.TerminalRecipe, request manageapi.Request, method string, signing diskfacts.SigningSnapshot) (*recipes.TerminalAudit, error) {
	f := r.fixture
	a := &recipes.TerminalAudit{Attempted: true}
	spec := GenerationSpec{Number: len(r.result.Hits) + 1}
	startErr := r.startLifecycle(ctx, spec)
	g := f.current
	if g == nil {
		return a, startErr
	}
	if startErr != nil {
		a.StartError = startErr.Error()
		var err error
		a.IdentityRetired, err = r.retiredRefusal(g)
		if err != nil {
			return a, err
		}
		if !a.IdentityRetired {
			return a, startErr
		}
	} else {
		a.Started = true
		var err error
		if recipe.Kind == recipes.Stop {
			a.Query, err = f.Manage(ctx, "operation.result", manageapi.Request{Params: map[string]string{"operation_id": hex.EncodeToString(recipe.RequestID[:])}})
			if err == nil {
				a.Replay, err = f.Manage(ctx, method, request)
			}
			if err != nil {
				return a, err
			}
			if err = g.p.wait(ctx); err != nil {
				a.ExitError = err.Error()
				return a, err
			}
		} else {
			if err = f.Stop(ctx); err != nil {
				return a, err
			}
		}
	}
	trace, err := f.ReadTrace(spec.Number)
	if err != nil {
		return a, err
	}
	a.Trace = trace.Hits
	d, err := nodeDisk(f)
	if err != nil {
		return a, err
	}
	a.SigningUnchanged, err = signing.Unchanged(d)
	return a, err
}
