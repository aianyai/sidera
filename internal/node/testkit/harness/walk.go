package harness

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/internal/node/txn"
)

// WalkScenario is the in-process implementation of a crash-walk scenario.
// Every run makes a fresh fixture, runs Script, and finishes with Drain and
// the script's ledger judgements. Callbacks return errors, never fail a test
// from a program goroutine.
type WalkScenario struct {
	Script  scenario.Script
	Options Options
	// Drain finishes outstanding work after the script or a crash. It may run
	// again if a target interrupts it, and must tolerate a partially finished run.
	Drain func(*H) error
	// Damage interprets an offline trigger on the stopped fixture. The scenario
	// owns the meaning of its pattern and damage; no live file is modified.
	Damage func(*H, crashwalk.Trigger) error
	// Check adds a scenario-specific judgement after the drain.
	Check func(*H) error
}

func (s *WalkScenario) Name() string                       { return s.Script.Name }
func (s *WalkScenario) Steps() []string                    { return s.Script.Scenario().Steps() }
func (s *WalkScenario) MaxPoints() int                     { return s.Script.Crash.MaxPoints }
func (s *WalkScenario) Recovery() []crashwalk.RecoveryCase { return s.Script.Crash.Recovery }

// WalkBackend runs scripts against the real engine, store and transaction layer
// on the fixture's crashable disk. Runs are sequential, as fixture leak checks
// inspect goroutines belonging to the whole test process.
type WalkBackend struct{ t testing.TB }

func NewWalkBackend(t testing.TB) *WalkBackend { return &WalkBackend{t: t} }

var _ crashwalk.Backend = (*WalkBackend)(nil)

// walkTest confines fixture cleanup to one run instead of retaining every
// engine, runtime and ledger until a walk's thousands of reruns have finished.
type walkTest struct {
	testing.TB
	cleanups []func()
}

func (t *walkTest) Cleanup(f func()) { t.cleanups = append(t.cleanups, f) }
func (t *walkTest) finish() {
	for i := len(t.cleanups) - 1; i >= 0; i-- {
		t.cleanups[i]()
	}
}

func (b *WalkBackend) Run(ctx context.Context, script crashwalk.Scenario, plan crashwalk.RunPlan) (res crashwalk.RunResult, err error) {
	s, ok := script.(*WalkScenario)
	if !ok {
		return res, errors.New("harness: the walk backend requires a WalkScenario")
	}
	if err := s.Script.Validate(); err != nil {
		return res, err
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if s.Options.FS != MemFS {
		return res, errors.New("harness: a walk requires an in-memory disk")
	}
	if plan.Record && (len(plan.Targets) != 0 || plan.Trigger != nil) {
		return res, errors.New("harness: a recording cannot have a crash or damage trigger")
	}
	byGen := map[int]int{}
	for i, target := range plan.Targets {
		if _, exists := byGen[target.Gen]; target.Gen < 1 || exists {
			return res, errors.New("harness: targets require distinct positive generations")
		}
		byGen[target.Gen] = i
	}
	if plan.Trigger != nil && plan.Trigger.Kind != crashwalk.TriggerCrashAt && plan.Trigger.Kind != crashwalk.TriggerCorrupt {
		return res, errors.New("harness: unknown recovery trigger")
	}
	if plan.Trigger != nil {
		if _, exists := byGen[1]; exists {
			return res, errors.New("harness: a recovery trigger owns the first generation")
		}
		if plan.Trigger.Kind == crashwalk.TriggerCorrupt && s.Damage == nil {
			return res, errors.New("harness: the scenario has no offline damage callback")
		}
	}
	t := &walkTest{TB: b.t}
	defer t.finish()
	options := s.Options
	if options.Disk != nil {
		options.Disk = options.Disk.Clone()
	}
	h := NewStopped(t, options)
	stopCancel := context.AfterFunc(ctx, h.cancel)
	defer stopCancel()
	res.Reached = make([]bool, len(plan.Targets))
	triggered := false
	var generation *walkGeneration
	defer func() {
		if generation != nil {
			generation.close()
		}
	}()
	start := func() (bool, error) {
		gen := len(res.Hits) + 1
		generation = newWalkGeneration(gen, s.Options.Observer)
		if i, exists := byGen[gen]; exists {
			generation.target = plan.Targets[i]
		}
		if gen == 1 && plan.Trigger != nil && plan.Trigger.Kind == crashwalk.TriggerCrashAt {
			generation.target = crashwalk.Target{Gen: 1, Point: plan.Trigger.CrashAt}
		}
		crashed, startErr := h.tryStart(ctx, StartOptions{CrashAt: generation.startupCrash}, generation)
		if !crashed {
			generation.serving()
		}
		return crashed, startErr
	}
	finishGeneration := func() {
		generation.close()
		res.Hits = append(res.Hits, generation.recording())
		if i, exists := byGen[generation.gen]; exists {
			res.Reached[i] = generation.reached()
		}
		if generation.gen == 1 && plan.Trigger != nil && plan.Trigger.Kind == crashwalk.TriggerCrashAt {
			triggered = generation.reached()
		}
	}
	crashed, err := start()
	if err != nil {
		return res, err
	}
	if !crashed {
		// Setup cannot be interrupted by a main target. Its writes remain in the
		// trace for coverage, marked outside the scenario's crash window.
		for _, name := range s.Script.Setup.Direct {
			if _, err := h.Direct(name); err != nil {
				return res, err
			}
		}
		if err := h.Quiesce(); err != nil {
			return res, err
		}
		generation.openWindow()
		for _, step := range s.Script.Steps {
			if err := h.BeginStep(step.Name); err != nil {
				return res, err
			}
			crashed, err = generation.work(ctx, h, func() error {
				if err := step.Do(h); err != nil {
					return err
				}
				return h.Quiesce()
			})
			if err != nil {
				return res, fmt.Errorf("step %q: %w", step.Name, err)
			}
			if !crashed && generation.boundary(step.Name) {
				h.Kill(ProcessCrash.At("after step " + step.Name))
				crashed = true
			}
			if crashed {
				break
			}
		}
	}
	if !crashed && plan.Trigger != nil && plan.Trigger.Kind == crashwalk.TriggerCorrupt {
		generation.close()
		h.Stop()
		if err := s.Damage(h, *plan.Trigger); err != nil {
			return res, err
		}
		triggered, crashed = true, true
	}
	for {
		if crashed {
			finishGeneration()
			crashed, err = start()
			if err != nil {
				return res, err
			}
			if crashed {
				continue
			}
			generation.openWindow()
		}
		crashed, err = generation.work(ctx, h, func() error {
			if err := h.Quiesce(); err != nil {
				return err
			}
			if s.Drain != nil {
				if err := s.Drain(h); err != nil {
					return err
				}
			}
			return h.Quiesce()
		})
		if err != nil {
			return res, fmt.Errorf("drain: %w", err)
		}
		if !crashed {
			break
		}
	}
	finishGeneration()
	if plan.Trigger != nil && !triggered {
		return res, fmt.Errorf("harness: recovery trigger %v was not reached", plan.Trigger.CrashAt)
	}
	log, err := h.Ledger()
	if err != nil {
		return res, err
	}
	if err := scenario.Check(log, s.Script.Expect); err != nil {
		res.Violations = append(res.Violations, err.Error())
	}
	if s.Check != nil {
		if err := s.Check(h); err != nil {
			res.Violations = append(res.Violations, err.Error())
		}
	}
	return res, nil
}

// A generation owns its observer before its writer exists. Closing its window
// never redirects an old callback to a newer process's trace.
type walkGeneration struct {
	mu                              sync.Mutex
	gen                             int
	observer                        txn.Observer
	target                          crashwalk.Target
	hits                            []crashwalk.Hit
	setup, closed, startup, claimed bool
	hold                            chan struct{}
	release                         chan struct{}
}

func newWalkGeneration(gen int, observer txn.Observer) *walkGeneration {
	return &walkGeneration{gen: gen, observer: observer, setup: gen == 1, startup: true, hold: make(chan struct{}), release: make(chan struct{})}
}

func (g *walkGeneration) Reached(o fsx.Observation, kind txn.Kind) {
	g.mu.Lock()
	hold := false
	if !g.closed {
		g.hits = append(g.hits, crashwalk.NewHit(g.gen, o, g.setup))
		if !g.setup && g.target.Gen == g.gen && g.target.AfterStep == "" && g.target.Point == o.Point {
			g.claimed, g.closed = true, true
			if !g.startup {
				close(g.hold)
				hold = true
			}
		}
	}
	g.mu.Unlock()
	if g.observer != nil {
		g.observer.Reached(o, kind)
	}
	if hold {
		<-g.release
	}
}

func (g *walkGeneration) startupCrash(p fsx.Point, _ txn.Kind) (CrashMode, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return ProcessCrash.At(p.String()), g.claimed && g.target.Point == p
}
func (g *walkGeneration) serving()      { g.mu.Lock(); g.startup = false; g.mu.Unlock() }
func (g *walkGeneration) openWindow()   { g.mu.Lock(); g.setup = false; g.mu.Unlock() }
func (g *walkGeneration) close()        { g.mu.Lock(); g.closed = true; g.mu.Unlock() }
func (g *walkGeneration) reached() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.claimed }
func (g *walkGeneration) recording() []crashwalk.Hit {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]crashwalk.Hit(nil), g.hits...)
}
func (g *walkGeneration) boundary(name string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.target.Gen != g.gen || g.target.AfterStep != name {
		return false
	}
	g.claimed, g.closed = true, true
	return true
}

// work keeps Kill outside the program callback. Only the selected callback is
// held; the cutoff takes its snapshot before that callback is released to unwind
// through the ordinary filesystem errors. The work goroutine is always joined.
func (g *walkGeneration) work(ctx context.Context, h *H, do func() error) (bool, error) {
	if err := ctx.Err(); err != nil {
		return g.finishWork(h, nil, true, err)
	}
	done := make(chan error, 1)
	go func() { done <- do() }()
	select {
	case <-g.hold:
		return g.finishWork(h, done, false, nil)
	case err := <-done:
		return g.finishWork(h, done, true, err)
	}
}

// finishWork owns both exits: a target can already be held when done wins the
// select. completed says the work's result has already been received.
func (g *walkGeneration) finishWork(h *H, done <-chan error, completed bool, err error) (bool, error) {
	// A script error can arrive while another program goroutine is held. Fence
	// further claims before inspecting the current one, so either that callback
	// owns a cutoff or it never waits. Never release it onto a live disk.
	g.mu.Lock()
	if err != nil {
		g.closed = true
	}
	held := false
	select {
	case <-g.hold:
		held = true
	default:
	}
	g.mu.Unlock()
	if held {
		h.Kill(ProcessCrash.At(g.target.Point.String()))
		close(g.release)
	}
	if !completed {
		<-done
	}
	return held, err
}
