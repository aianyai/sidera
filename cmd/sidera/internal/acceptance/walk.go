package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/aianyai/sidera/internal/node/testkit/acceptance/recipes"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
)

// WalkScenario retains metadata only. Bind constructs fresh script evidence and
// drain progress for every run, while sharing them across that run's generations.
type WalkScenario struct {
	name     string
	steps    []string
	budget   int
	recovery []crashwalk.RecoveryCase
	triggers []crashwalk.Trigger
	needs    recipes.Needs
	bind     func(recipes.RecoveryOps) (recipes.ScriptRecipe, error)
}

// NewWalkScenario copies the descriptor's metadata without retaining its closures.
// Bind must construct all mutable recipe state anew for each Run.
func NewWalkScenario(descriptor recipes.ScriptRecipe, bind func(recipes.RecoveryOps) (recipes.ScriptRecipe, error)) *WalkScenario {
	needs := descriptor.Needs
	needs.ExtraInstances = append([]string(nil), needs.ExtraInstances...)
	needs.FSFaults = append([]recipes.FSFault(nil), needs.FSFaults...)
	return &WalkScenario{
		name: descriptor.Script.Name, steps: descriptor.Script.Scenario().Steps(),
		budget:   descriptor.Script.Crash.MaxPoints,
		recovery: append([]crashwalk.RecoveryCase(nil), descriptor.Script.Crash.Recovery...),
		triggers: append([]crashwalk.Trigger(nil), descriptor.RecoveryTriggers...),
		needs:    needs, bind: bind,
	}
}
func (s *WalkScenario) Name() string    { return s.name }
func (s *WalkScenario) Steps() []string { return append([]string(nil), s.steps...) }
func (s *WalkScenario) MaxPoints() int  { return s.budget }
func (s *WalkScenario) Recovery() []crashwalk.RecoveryCase {
	return append([]crashwalk.RecoveryCase(nil), s.recovery...)
}

// WalkBackend owns no cross-run program state. Root is a caller-owned short
// directory; each Run closes and removes its own child before returning.
type WalkBackend struct {
	root     string
	binaries Binaries
	opts     Options
}

// NewWalkBackend uses an existing absolute root owned and removed by the caller.
// Each Run creates a distinct child and owns its fixture through cleanup.
func NewWalkBackend(root string, binaries Binaries, opts Options) *WalkBackend {
	return &WalkBackend{root: root, binaries: binaries, opts: opts}
}

var _ crashwalk.Backend = (*WalkBackend)(nil)

func (b *WalkBackend) Run(ctx context.Context, s crashwalk.Scenario, plan crashwalk.RunPlan) (out crashwalk.RunResult, err error) {
	if !filepath.IsAbs(b.root) {
		return out, errors.New("acceptance: walk root must be absolute")
	}
	needs := recipes.Needs{}
	switch s := s.(type) {
	case *WalkScenario:
		needs = s.needs
	case *StartupWalkScenario:
		needs = s.Recipe.Needs
	case *TerminalWalkScenario:
		needs = s.Recipe.Needs
	default:
		return out, errors.New("acceptance: binary backend requires a binary scenario")
	}
	profile, err := profileFromNeeds(needs)
	if err != nil {
		return out, err
	}
	r, err := newBinaryRun(plan, needs.Damage)
	if err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	root, err := os.MkdirTemp(b.root, "w")
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(root)) }()
	f, err := newFixture(ctx, root, b.binaries, b.opts, profile)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	r.fixture = f
	switch s := s.(type) {
	case *WalkScenario:
		err = r.runScript(ctx, s)
	case *StartupWalkScenario:
		err = r.runStartup(ctx, s)
	case *TerminalWalkScenario:
		err = r.runTerminal(ctx, s)
	}
	if err == nil && plan.Trigger != nil && !r.triggered {
		err = errors.New("acceptance: recovery trigger was not reached")
	}
	return r.result, err
}

type binaryRun struct {
	fixture   *Fixture
	plan      crashwalk.RunPlan
	targets   map[int]int
	result    crashwalk.RunResult
	triggered bool
	damage    recoveryState
}

func newBinaryRun(plan crashwalk.RunPlan, damage recipes.DamageKind) (*binaryRun, error) {
	if plan.Record && (plan.Trigger != nil || len(plan.Targets) != 0) {
		return nil, errors.New("acceptance: recording cannot have targets or a trigger")
	}
	r := &binaryRun{plan: plan, targets: map[int]int{}, result: crashwalk.RunResult{Reached: make([]bool, len(plan.Targets))}, damage: recoveryState{kind: damage}}
	for i, t := range plan.Targets {
		if _, ok := r.targets[t.Gen]; t.Gen < 1 || ok {
			return nil, errors.New("acceptance: targets require distinct positive generations")
		}
		r.targets[t.Gen] = i
	}
	if tr := plan.Trigger; tr != nil {
		if _, ok := r.targets[1]; ok {
			return nil, errors.New("acceptance: trigger owns generation one")
		}
		switch tr.Kind {
		case crashwalk.TriggerCrashAt:
		case crashwalk.TriggerCorrupt:
			if damage == "" {
				return nil, errors.New("acceptance: scenario has no offline damage capability")
			}
		default:
			return nil, errors.New("acceptance: unknown recovery trigger")
		}
	}
	return r, nil
}
func (r *binaryRun) spec() GenerationSpec {
	gen := len(r.result.Hits) + 1
	spec := GenerationSpec{Number: gen}
	if i, ok := r.targets[gen]; ok && r.plan.Targets[i].AfterStep == "" {
		spec.Target = r.plan.Targets[i].Point.String()
	}
	if gen == 1 && r.plan.Trigger != nil && r.plan.Trigger.Kind == crashwalk.TriggerCrashAt {
		spec.Target = r.plan.Trigger.CrashAt.String()
	}
	return spec
}
func (r *binaryRun) finish(g *generation) error {
	trace, err := r.fixture.ReadTrace(g.spec.Number)
	if err != nil {
		return err
	}
	// The replacement's drain must see the interruption that abandoned its script.
	crashPoint := ""
	if trace.Reached {
		crashPoint = trace.Crash
	}
	if i, ok := r.targets[g.spec.Number]; ok && r.plan.Targets[i].AfterStep != "" && r.result.Reached[i] {
		crashPoint = "after step " + r.plan.Targets[i].AfterStep
	}
	if crashPoint != "" {
		if err := r.fixture.writer.Append(ledger.KindFixtureCrash, ledger.Attrs{CrashPoint: crashPoint, PIDs: []int64{int64(g.p.cmd.Process.Pid)}}); err != nil {
			return err
		}
	}
	r.result.Hits = append(r.result.Hits, trace.Hits)
	if i, ok := r.targets[g.spec.Number]; ok && r.plan.Targets[i].AfterStep == "" {
		r.result.Reached[i] = trace.Reached
	}
	if g.spec.Number == 1 && r.plan.Trigger != nil && r.plan.Trigger.Kind == crashwalk.TriggerCrashAt {
		r.triggered = trace.Reached
	}
	return nil
}

// A failed RPC is accepted as an interruption only after the matching marker
// and the actual abnormal exit have both been validated by the fixture.
func (r *binaryRun) interrupted(ctx context.Context, g *generation, cause error) (bool, error) {
	if g.spec.Target != "" {
		trace, _, err := readTrace(g.spec.Record, g.spec.Number, g.setup, r.fixture.table, true)
		if err != nil {
			return false, err
		}
		if trace.Crash == g.spec.Target {
			if err := g.p.wait(ctx); ctx.Err() != nil {
				return false, err
			}
			complete, err := r.fixture.ReadTrace(g.spec.Number)
			if err != nil {
				return false, err
			}
			if complete.Reached {
				return true, nil
			}
			return false, errors.New("acceptance: marker without abnormal process exit")
		}
	}
	if g.p.exited() {
		return false, errors.Join(cause, r.unexpectedExit(g))
	}
	if cause != nil {
		return false, cause
	}
	return false, nil
}
func (r *binaryRun) unexpectedExit(g *generation) error {
	if err := r.fixture.exitResult(g); err != nil {
		return err
	}
	return fmt.Errorf("acceptance: generation %d exited while service was required", g.spec.Number)
}
func (r *binaryRun) boundary(ctx context.Context, g *generation, step string) (bool, error) {
	i, ok := r.targets[g.spec.Number]
	if !ok || r.plan.Targets[i].AfterStep != step {
		return false, nil
	}
	// Kill's success, rather than an already completed Wait, proves this boundary.
	killed, err := g.p.killAndWait()
	if err != nil {
		return false, err
	}
	if !killed {
		return false, errors.New("acceptance: boundary process already exited")
	}
	if err = r.fixture.writer.Append(ledger.KindFixtureKill, ledger.Attrs{PIDs: []int64{int64(g.p.cmd.Process.Pid)}}); err != nil {
		return false, err
	}
	var abnormal *exec.ExitError
	if !errors.As(g.p.err, &abnormal) {
		return false, errors.New("acceptance: boundary kill did not terminate abnormally")
	}
	r.result.Reached[i] = true
	return true, nil
}
func (r *binaryRun) runScript(ctx context.Context, s *WalkScenario) error {
	if s.bind == nil {
		return errors.New("acceptance: script requires a fresh binding factory")
	}
	recipe, err := s.bind(r.damage.ops())
	if err != nil {
		return err
	}
	if err = recipe.Script.Validate(); err != nil {
		return err
	}
	needs := recipe.Needs
	needs.ExtraInstances = append([]string(nil), needs.ExtraInstances...)
	needs.FSFaults = append([]recipes.FSFault(nil), needs.FSFaults...)
	if recipe.Script.Name != s.Name() || recipe.Script.Crash.MaxPoints != s.MaxPoints() || !slices.Equal(recipe.Script.Scenario().Steps(), s.steps) || !slices.Equal(recipe.Script.Crash.Recovery, s.recovery) || !slices.Equal(recipe.RecoveryTriggers, s.triggers) || !reflect.DeepEqual(needs, s.needs) {
		return errors.New("acceptance: fresh script binding changed scenario metadata")
	}
	f := r.fixture
	if err = f.prepare(ctx); err != nil {
		return err
	}
	var env *environment
	first := true
	for {
		spec := r.spec()
		err = f.Start(ctx, spec)
		g := f.current
		if g == nil {
			return err
		}
		crashed, e := r.interrupted(ctx, g, err)
		if e != nil {
			return e
		}
		if !crashed {
			if env == nil {
				env = newEnvironment(ctx, f)
			} else if err = env.resetGeneration(ctx); err != nil {
				return err
			}
			if first {
				for _, name := range recipe.Script.Setup.Direct {
					if _, err = env.Direct(name); err != nil {
						return err
					}
				}
				if err = env.Quiesce(); err != nil {
					return err
				}
				for _, step := range recipe.Script.Steps {
					if err = env.BeginStep(step.Name); err != nil {
						return err
					}
					err = step.Do(env)
					if err == nil {
						err = env.Quiesce()
					}
					crashed, e = r.interrupted(ctx, g, err)
					if e != nil {
						return fmt.Errorf("step %q: %w", step.Name, e)
					}
					if !crashed {
						crashed, e = r.boundary(ctx, g, step.Name)
						if e != nil {
							return e
						}
					}
					if crashed {
						break
					}
				}
			}
			if !crashed && first && r.plan.Trigger != nil && r.plan.Trigger.Kind == crashwalk.TriggerCorrupt {
				if err = f.Stop(ctx); err != nil {
					return err
				}
				if err = r.damage.apply(f, *r.plan.Trigger); err != nil {
					return err
				}
				r.triggered, crashed = true, true
			}
			if !crashed {
				if recipe.Drain != nil {
					err = recipe.Drain(env)
				}
				if err == nil {
					err = env.Quiesce()
				}
				crashed, e = r.interrupted(ctx, g, err)
				if e != nil {
					return fmt.Errorf("drain: %w", e)
				}
			}
		}
		first = false
		if crashed {
			if err = r.finish(g); err != nil {
				return err
			}
			continue
		}
		log, err := f.Ledger()
		if err != nil {
			return err
		}
		r.violation(scenario.Check(log, recipe.Script.Expect))
		if recipe.Check != nil {
			r.violation(recipe.Check(env))
		}
		// Domain checks may query the serving daemon. Stop only after judging the
		// completed script; Stop excludes its own cleanup writes from the trace.
		if err = f.Stop(ctx); err != nil {
			return err
		}
		return r.finish(g)
	}
}
func (r *binaryRun) violation(err error) {
	if err != nil {
		r.result.Violations = append(r.result.Violations, err.Error())
	}
}
