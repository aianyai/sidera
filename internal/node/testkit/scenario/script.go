package scenario

import (
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
)

// Script is one scenario: what exists before the first step, the steps, the
// judgements the finished run is held to, and the crash plan.
type Script struct {
	Name   string
	Setup  Setup
	Steps  []Step
	Expect Invariants
	Crash  CrashPlan
}

// Setup is what a script needs before its first step. Endpoints of other kinds
// are not listed here: a session endpoint exists only through a session
// creation, which a step asks for.
type Setup struct {
	// Direct names the direct endpoints to set up, each once.
	Direct []string
}

// Step is one thing a script does to the system from outside: one call, one
// advance of the clock or one knob, so that a failure points at a step and a kill
// between steps means something.
type Step struct {
	Name string
	Do   func(e Env) error
}

// CrashPlan is what the walk needs to know of the script.
type CrashPlan struct {
	// MaxPoints is the most main crash points the script's window may have.
	MaxPoints int
	// Recovery lists the recovery writes the script is responsible for crashing.
	Recovery []crashwalk.RecoveryCase
}

// Validate says whether the script can run: a name, steps with unique names and
// something to do, direct endpoints set up once, and waivers that name a known
// judgement and give a reason.
func (s Script) Validate() error {
	if s.Name == "" {
		return errors.New("scenario: a script has a name")
	}
	seen := map[string]bool{}
	for i, st := range s.Steps {
		switch {
		case st.Name == "":
			return fmt.Errorf("scenario %q: step %d has no name", s.Name, i+1)
		case st.Do == nil:
			return fmt.Errorf("scenario %q: step %q has nothing to do", s.Name, st.Name)
		case seen[st.Name]:
			// An entry of the ledger names its step and nothing else, so a name used
			// twice would put the entries of the second run among those of the first.
			return fmt.Errorf("scenario %q: step name %q is used twice", s.Name, st.Name)
		}
		seen[st.Name] = true
	}
	direct := map[string]bool{}
	for _, d := range s.Setup.Direct {
		switch {
		case d == "":
			return fmt.Errorf("scenario %q: a direct endpoint in the setup has no name", s.Name)
		case direct[d]:
			return fmt.Errorf("scenario %q: direct endpoint %q is set up twice", s.Name, d)
		}
		direct[d] = true
	}
	if err := s.Expect.validate(); err != nil {
		return fmt.Errorf("scenario %q: %w", s.Name, err)
	}
	return nil
}

// Run sets the script up, runs its steps in order, waits for the system to go
// quiet after the setup and after every step, and then judges the ledger. It
// stops at the first failure and names the step. A script that cannot run is
// refused before the system is touched.
func Run(e Env, s Script) error {
	if err := s.Validate(); err != nil {
		return err
	}
	for _, name := range s.Setup.Direct {
		if _, err := e.Direct(name); err != nil {
			return fmt.Errorf("scenario %q: setting up direct endpoint %q: %w", s.Name, name, err)
		}
	}
	if err := e.Quiesce(); err != nil {
		return fmt.Errorf("scenario %q: after the setup: %w", s.Name, err)
	}
	for _, st := range s.Steps {
		if err := e.BeginStep(st.Name); err != nil {
			return fmt.Errorf("scenario %q: announcing step %q: %w", s.Name, st.Name, err)
		}
		if err := st.Do(e); err != nil {
			return fmt.Errorf("scenario %q: step %q: %w", s.Name, st.Name, err)
		}
		if err := e.Quiesce(); err != nil {
			return fmt.Errorf("scenario %q: after step %q: %w", s.Name, st.Name, err)
		}
	}
	log, err := e.Ledger()
	if err != nil {
		return fmt.Errorf("scenario %q: reading the ledger: %w", s.Name, err)
	}
	if err := Check(log, s.Expect); err != nil {
		return fmt.Errorf("scenario %q: %w", s.Name, err)
	}
	return nil
}

// Scenario describes the script to the crash walk. It is a method that returns
// the walk's view and not the walk's interface on Script itself, because the
// script's fields Name and Steps are what a script is written with.
func (s Script) Scenario() crashwalk.Scenario { return walkable{s} }

type walkable struct{ s Script }

func (w walkable) Name() string { return w.s.Name }
func (w walkable) Steps() []string {
	out := make([]string, len(w.s.Steps))
	for i, st := range w.s.Steps {
		out[i] = st.Name
	}
	return out
}
func (w walkable) MaxPoints() int                     { return w.s.Crash.MaxPoints }
func (w walkable) Recovery() []crashwalk.RecoveryCase { return w.s.Crash.Recovery }

// Advance is the step that moves the clock forward by d. It is one step of its
// own so that what the advance makes due is judged apart from what a call did.
func Advance(name string, d time.Duration) Step {
	return Step{Name: name, Do: func(e Env) error { e.Advance(d); return nil }}
}
