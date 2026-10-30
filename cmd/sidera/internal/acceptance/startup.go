package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/disk"
	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/diskfacts"
)

// StartupWalkScenario observes one real CLI entry point, without script bootstrap.
type StartupWalkScenario struct{ Recipe recipes.StartupRecipe }

func (s *StartupWalkScenario) Name() string                       { return s.Recipe.Name }
func (s *StartupWalkScenario) Steps() []string                    { return s.Recipe.Steps() }
func (s *StartupWalkScenario) MaxPoints() int                     { return s.Recipe.MaxPoints }
func (s *StartupWalkScenario) Recovery() []crashwalk.RecoveryCase { return s.Recipe.Recovery }

// startLifecycle waits for the public running state of a bare daemon. It never
// installs packages, enables adapters or creates endpoints to prepare a script.
func (r *binaryRun) startLifecycle(ctx context.Context, spec GenerationSpec) error {
	f := r.fixture
	g, err := f.begin(spec, "daemon")
	if err != nil {
		return err
	}
	f.current = g
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if g.p.exited() {
			return f.exitResult(g)
		}
		raw, err := f.Manage(ctx, "status", manageapi.Request{})
		if err == nil {
			var status struct {
				Daemon struct {
					State string `json:"state"`
				} `json:"daemon"`
			}
			if err = resultBody(raw, &status); err != nil {
				return err
			}
			if status.Daemon.State == "running" {
				if err := f.checkTable(ctx); err != nil {
					return err
				}
				return f.Quiesce(ctx)
			}
		}
		select {
		case <-g.p.done:
			return f.exitResult(g)
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
func (r *binaryRun) runStartup(ctx context.Context, s *StartupWalkScenario) error {
	recipe := s.Recipe
	if recipe.Name == "" || recipe.MaxPoints <= 0 || (recipe.Kind != recipes.Init && recipe.Kind != recipes.Start) {
		return errors.New("acceptance: invalid startup scenario")
	}
	if recipe.StartAfter < 0 || recipe.AdvanceAfterStart < 0 || (recipe.Kind != recipes.Start && (recipe.StartAfter != 0 || recipe.AdvanceAfterStart != 0)) {
		return errors.New("acceptance: startup clock advances require Start and non-negative durations")
	}
	f := r.fixture
	if recipe.Kind == recipes.Start {
		if err := f.prepare(ctx); err != nil {
			return err
		}
		f.clockMu.Lock()
		f.now = f.now.Add(recipe.StartAfter)
		f.clockMu.Unlock()
	}
	minimum := f.Now().Add(recipe.AdvanceAfterStart)
	var preserved diskfacts.InitPreservation
	for {
		spec := r.spec()
		if recipe.Kind == recipes.Init {
			if _, err := os.Stat(f.home); err == nil {
				d, err := nodeDisk(f)
				if err != nil {
					return err
				}
				if err = preserved.Remember(d); err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		var err error
		if recipe.Kind == recipes.Init {
			err = f.Init(ctx, spec)
		} else {
			err = r.startLifecycle(ctx, spec)
		}
		g := f.gens[spec.Number]
		if g == nil {
			return err
		}
		crashed := false
		if recipe.Kind == recipes.Init {
			trace, e := f.ReadTrace(spec.Number)
			if e != nil {
				return e
			}
			crashed = trace.Reached
			if !crashed && err != nil {
				d, e := nodeDisk(f)
				if e != nil {
					return e
				}
				done, e := keys.Initialized(d)
				if e != nil {
					return e
				}
				diagnostic, e := os.ReadFile(g.stderr)
				if e != nil {
					return e
				}
				if !done || !strings.Contains(string(diagnostic), daemon.ErrAlreadyInitialized.Error()) {
					return err
				}
			}
		} else {
			var e error
			crashed, e = r.interrupted(ctx, g, err)
			if e != nil {
				return e
			}
			if !crashed {
				if recipe.AdvanceAfterStart != 0 {
					err = f.SetTime(ctx, f.Now().Add(recipe.AdvanceAfterStart))
					if err == nil {
						err = f.Quiesce(ctx)
					}
					crashed, e = r.interrupted(ctx, g, err)
					if e != nil {
						return e
					}
				}
				if !crashed {
					crashed, e = r.boundary(ctx, g, "complete")
					if e != nil {
						return e
					}
				}
				if !crashed {
					if err = f.Stop(ctx); err != nil {
						return err
					}
				}
			}
		}
		if err = r.finish(g); err != nil {
			return err
		}
		d, err := nodeDisk(f)
		if err != nil {
			return err
		}
		if recipe.Kind == recipes.Init {
			if err = preserved.Check(d); err != nil {
				r.violation(err)
				return nil
			}
		}
		if crashed {
			continue
		}
		if spec.Number == 1 && r.plan.Trigger != nil && r.plan.Trigger.Kind == crashwalk.TriggerCorrupt {
			if err = r.damage.apply(f, *r.plan.Trigger); err != nil {
				return err
			}
			r.triggered = true
			continue
		}
		done, err := keys.Initialized(d)
		if err != nil || !done {
			r.violation(fmt.Errorf("acceptance: startup initialized=%v: %v", done, err))
		}
		r.violation(disk.CheckLayout(d))
		if recipe.AdvanceAfterStart != 0 {
			state, err := keys.ReadClock(d)
			if err != nil {
				return err
			}
			if state.HighWater.Before(minimum) || state.Paused() {
				r.violation(fmt.Errorf("acceptance: persisted clock did not reach %s without pause", minimum))
			}
		}
		return nil
	}
}
