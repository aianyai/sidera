package harness

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/txn"
)

// StartOptions controls one synchronous start of a node that is down.
type StartOptions struct {
	// Arm installs ordinary fault rules before transaction recovery. An unhit
	// rule still fails the test, as it does with StartWith.
	Arm func(*fsxtest.Faulty)
	// CrashAt selects the first startup point to crash at. It may run on any
	// program goroutine; the fixture serializes calls and owns the cutoff,
	// snapshot and cleanup. It must not block or call Kill itself. After this
	// attempt finishes, it is never called again.
	CrashAt func(fsx.Point, txn.Kind) (CrashMode, bool)
}

// TryStart runs one generation until it is serving or all resources acquired
// during startup have been reclaimed. crashed reports an accepted CrashAt
// decision; err reports an ordinary startup failure, cancellation, or failed
// cleanup, including cleanup after a crash. It is only valid while down.
// Cancellation is cooperative: an observer that holds a step must be released
// by its caller. A crash requires an in-memory disk.
func (h *H) TryStart(ctx context.Context, o StartOptions) (crashed bool, err error) {
	return h.tryStart(ctx, o, h.opts.Observer)
}

// tryStart binds the observer to the writer's lifetime. A walk gives each
// generation a window of its own, including callbacks made after its cutoff.
func (h *H) tryStart(ctx context.Context, o StartOptions, observer txn.Observer) (crashed bool, err error) {
	if h.current() != nil {
		return false, errors.New("harness: the node is already running")
	}
	if h.unreaped != nil {
		return false, errors.New("harness: the previous start has not been reaped")
	}
	if h.real != nil && o.CrashAt != nil {
		return false, errors.New("harness: a node on the real file system cannot be crashed: its disk cannot be cloned")
	}
	var base fsx.FS = h.real
	if base == nil {
		base = h.Disk()
	}
	startCtx, cancel := context.WithCancel(h.ctx)
	// The caller's context bounds startup, not the lifetime of a serving node.
	stopCancel := context.AfterFunc(ctx, cancel)
	defer stopCancel()
	p := &process{cancel: cancel, log: &LogCapture{}}
	a := &startAttempt{h: h, p: p, ctx: startCtx, caller: ctx, options: o, observer: observer, active: true}
	p.mounted = newMounted(h.t, h.tab, base, a, o.Arm)
	if mem, ok := base.(*fsxtest.Mem); ok {
		p.mounted.Mem = mem
	}
	h.gen++ // even a generation that dies in transaction recovery owns a number
	err = h.boot(startCtx, p, a.check)
	// Deactivate the selector under the same lock as its callbacks. In particular,
	// a commit can return nil after its observer cut off the generation.
	a.mu.Lock()
	a.active = false
	crashed = p.abandoned.Load()
	if err == nil {
		err = a.checkLocked()
	}
	crashErr := a.crashErr
	// The writer keeps its observer for the serving generation. Release the
	// startup-only closures and caller context when their window ends.
	a.options = StartOptions{}
	a.caller, a.ctx = nil, nil
	if err == nil {
		p.eng.Serve()
		h.mu.Lock()
		h.proc = p
		h.mu.Unlock()
	}
	a.mu.Unlock()
	if err == nil {
		return false, nil
	}
	cleanupErr := h.reapStart(p)
	if cleanupErr != nil {
		h.unreaped = p
	}
	if crashed {
		// The filesystem/context error is how production unwinds a deliberate
		// cutoff. Only errors in taking the crash or reclaiming resources escape.
		return true, errors.Join(crashErr, cleanupErr)
	}
	return false, errors.Join(err, cleanupErr)
}

// startAttempt serializes selection and fences publication against callbacks
// from both the caller's goroutine and the engine/host goroutines.
type startAttempt struct {
	mu       sync.Mutex
	h        *H
	p        *process
	ctx      context.Context
	caller   context.Context
	options  StartOptions
	observer txn.Observer
	active   bool
	crashErr error
}

func (a *startAttempt) Reached(observation fsx.Observation, kind txn.Kind) {
	p := observation.Point
	if obs := a.observer; obs != nil {
		obs.Reached(observation, kind)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active || a.p.abandoned.Load() || a.options.CrashAt == nil {
		return
	}
	if mode, hit := a.options.CrashAt(p, kind); hit {
		a.crashErr = a.h.cutCrash(a.p, mode)
		// Take the snapshot before waking the outer startup call: its cleanup
		// must not race the disk copy or wait inside this observer's goroutine.
		a.p.cancel()
	}
}

func (a *startAttempt) check() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.checkLocked()
}

func (a *startAttempt) checkLocked() error {
	if a.p.abandoned.Load() {
		return errCutOff
	}
	if err := a.caller.Err(); err != nil {
		return err
	}
	return a.ctx.Err()
}

// cutCrash is the one owner of the crash barrier for running and starting
// generations. No cleanup runs before the cutoff snapshot has been installed.
func (h *H) cutCrash(p *process, mode CrashMode) error {
	p.abandoned.Store(true)
	p.mounted.cutOff()
	var disk *fsxtest.Mem
	if mode.power {
		disk = p.mounted.Mem.CrashClone(rand.New(rand.NewPCG(mode.seed, 0x5ce)))
	} else {
		disk = p.mounted.Mem.Clone()
	}
	h.mu.Lock()
	h.disk = disk
	h.mu.Unlock()
	return h.fixture.Append(ledger.KindFixtureCrash, ledger.Attrs{CrashPoint: mode.describe()})
}

// reapStart releases only process resources, never rolls back disk effects.
// Every write from the unpublished generation fails before its host is aborted.
func (h *H) reapStart(p *process) error {
	p.abandoned.Store(true)
	p.mounted.cutOff()
	p.cancel()
	if p.host != nil {
		p.host.Abort()
		h.launcher.cutAll()
	}
	if p.eng != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopLimit)
		defer cancel()
		if err := p.eng.Stop(ctx); err != nil {
			return fmt.Errorf("harness: reaping the startup engine: %w", err)
		}
	}
	return nil
}
