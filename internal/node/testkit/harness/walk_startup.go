package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/txn"
)

// StartupOperation selects the real daemon entry point of a startup walk.
type StartupOperation int

const (
	StartupInit StartupOperation = iota + 1
	StartupStart
)

// StartupWalkScenario observes actual Init or Start, independently of the engine
// fixture. Each generation has one writer. Start is prepared by an unobserved
// actual Init; Init starts from an empty disk. Both finish at the complete step.
type StartupWalkScenario struct {
	Label     string
	Operation StartupOperation
	// StartAfter advances the fixed clock after Start's unobserved Init and
	// before its first observed generation. It must be non-negative and is only
	// valid for StartupStart; zero leaves the freshly initialized clock unchanged.
	StartAfter time.Duration
	// AdvanceAfterStart advances the shared clock once after each successful
	// Start has quiesced, then waits for the resulting work before stopping.
	// It must be non-negative and is only valid for StartupStart; zero keeps
	// the startup-only run.
	AdvanceAfterStart time.Duration
	PointBudget       int
	RecoveryCases     []crashwalk.RecoveryCase
	// Damage changes only the stopped disk, outside every observation window.
	Damage func(fsx.FS, crashwalk.Trigger) error
	// Check reads the completed disk after every process resource was reclaimed.
	Check func(fsx.Reader) error
}

func (s *StartupWalkScenario) Name() string                       { return s.Label }
func (s *StartupWalkScenario) Steps() []string                    { return []string{"complete"} }
func (s *StartupWalkScenario) MaxPoints() int                     { return s.PointBudget }
func (s *StartupWalkScenario) Recovery() []crashwalk.RecoveryCase { return s.RecoveryCases }
func (s *StartupWalkScenario) validate() error {
	if s.Label == "" || s.PointBudget <= 0 {
		return errors.New("harness: a startup walk requires a name and positive point budget")
	}
	if s.Operation != StartupInit && s.Operation != StartupStart {
		return errors.New("harness: unknown startup operation")
	}
	if s.StartAfter < 0 || (s.Operation != StartupStart && s.StartAfter != 0) {
		return errors.New("harness: StartAfter requires StartupStart and a non-negative duration")
	}
	if s.AdvanceAfterStart < 0 || (s.Operation != StartupStart && s.AdvanceAfterStart != 0) {
		return errors.New("harness: AdvanceAfterStart requires StartupStart and a non-negative duration")
	}
	return nil
}

func runStartup(ctx context.Context, t *walkTest, s *StartupWalkScenario, r *walkRun) error {
	checkNoLeaks(t, append(append([]string(nil), ownFrames...), "internal/node/daemon."), leakWait)
	home, err := startupHome(t)
	if err != nil {
		return err
	}
	disk := fsxtest.NewMem()
	clk := clocktest.NewFake(DefaultStart)
	if s.Operation == StartupStart {
		if _, err := daemon.Init(ctx, daemon.Config{Home: home, FS: disk, Clock: clk, Rand: &startupRandom{r: RandReader(keySeed)}}); err != nil {
			return err
		}
		clk.Advance(s.StartAfter)
	}
	// Init's never-overwrite promise is judged against bytes present before each
	// attempt, independently of the writer that will try to finish the home.
	protected := map[string][]byte{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.Operation == StartupInit {
			if err := rememberInitFiles(disk, protected); err != nil {
				return err
			}
		}
		g := r.begin(nil)
		g.openWindow()
		startCtx, cancel := context.WithCancel(ctx)
		a := &startupAttempt{generation: g, disk: disk, cut: newCutoff(disk), cancel: cancel}
		cfg := daemon.Config{Home: home, FS: a.cut, Clock: clk, Rand: &startupRandom{r: RandReader(keySeed + uint64(g.gen))}, Observer: a}
		if s.Operation == StartupInit {
			done, e := keys.Initialized(disk)
			err = e
			if err == nil && !done {
				_, err = daemon.Init(startCtx, cfg)
			}
		} else {
			var d *daemon.Daemon
			d, err = daemon.Start(startCtx, cfg)
			if err == nil {
				err = finishStartup(startCtx, d, clk, s.AdvanceAfterStart)
			}
		}
		cancel()
		snapshot := a.snapshot()
		crashed := snapshot != nil
		if err != nil && !crashed {
			return err
		}
		if errors.Is(err, daemon.ErrEngineNotStopped) {
			return err
		}
		if crashed {
			disk = snapshot
		}
		if !crashed && g.boundary("complete") {
			a.crash()
			disk = a.snapshot()
			crashed = true
		}
		r.finishGeneration()
		if s.Operation == StartupInit {
			if err := checkInitFiles(disk, protected); err != nil {
				r.result.Violations = append(r.result.Violations, err.Error())
				return nil
			}
		}
		if crashed {
			continue
		}
		if g.gen == 1 && r.plan.Trigger != nil && r.plan.Trigger.Kind == crashwalk.TriggerCorrupt {
			// Start returned only after RequestStop/Wait, and Init released its lock.
			if err := s.Damage(disk, *r.plan.Trigger); err != nil {
				return err
			}
			r.triggered = true
			continue
		}
		break
	}
	if err := r.checkTrigger(); err != nil {
		return err
	}
	if done, err := keys.Initialized(disk); err != nil || !done {
		r.result.Violations = append(r.result.Violations, fmt.Sprintf("harness: startup did not complete init: initialized=%v, error=%v", done, err))
	}
	if missing, err := daemon.LayoutWork(disk); err != nil || len(missing) != 0 {
		r.result.Violations = append(r.result.Violations, fmt.Sprintf("harness: startup left missing layout %v: %v", missing, err))
	}
	if s.Check != nil {
		if err := s.Check(disk); err != nil {
			r.result.Violations = append(r.result.Violations, err.Error())
		}
	}
	return nil
}

// finishStartup owns a serving generation through its last quiescence and stop,
// including when the caller is canceled or a selected point cuts off its disk.
func finishStartup(ctx context.Context, d *daemon.Daemon, clk *clocktest.Fake, advance time.Duration) (err error) {
	defer func() {
		d.RequestStop("startup walk complete")
		err = errors.Join(err, d.Wait())
	}()
	if _, err = d.Engine().Quiesce(ctx, engine.Expect{}); err != nil {
		return err
	}
	if advance != 0 {
		if err = ctx.Err(); err != nil {
			return err
		}
		clk.Advance(advance)
		_, err = d.Engine().Quiesce(ctx, engine.Expect{})
		if err == nil {
			err = ctx.Err()
		}
	}
	return err
}

// startupAttempt keeps selection in the shared recorder. The callback only
// fences disk access, snapshots, and cancels startup waits; its caller owns all
// process cleanup. A commit can select a crash even when the entry returns nil.
type startupAttempt struct {
	generation *walkGeneration
	disk       *fsxtest.Mem
	cut        *cutoff
	cancel     context.CancelFunc
	mu         sync.Mutex
	saved      *fsxtest.Mem
}

func (a *startupAttempt) Reached(o fsx.Observation, kind txn.Kind) {
	a.generation.Reached(o, kind)
	if a.generation.reached() {
		a.crash()
	}
}
func (a *startupAttempt) crash() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.saved != nil {
		return
	}
	a.cut.cutOff()
	a.saved = a.disk.Clone()
	a.cancel()
}
func (a *startupAttempt) snapshot() *fsxtest.Mem {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.saved
}

// startupHome names a short private child from testing's unique allocation for
// Unix socket limits. Only that child is removed; daemon data stays in Mem.
func startupHome(t *walkTest) (string, error) {
	unique := sha256.Sum256([]byte(t.TempDir()))
	name := fmt.Sprintf("w%x", unique[:5])
	base := os.TempDir()
	home := filepath.Join(base, name)
	parent, err := fsx.OpenOS(base, RandReader(keySeed))
	if err != nil {
		return "", err
	}
	if err := fsx.MakeRoot(home); err != nil {
		return "", err
	}
	t.Cleanup(func() {
		if err := parent.RemoveAll(setupTag, name); err != nil {
			t.Errorf("harness: removing startup home: %v", err)
		}
	})
	real, err := fsx.OpenOS(home, RandReader(keySeed))
	if err != nil {
		return "", err
	}
	for _, dir := range []string{"run", "manage"} {
		if err := fsx.MakeDir(real, setupTag, dir); err != nil {
			return "", err
		}
	}
	return home, nil
}

type startupRandom struct {
	mu sync.Mutex
	r  io.Reader
}

func (r *startupRandom) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.r.Read(p)
}

// These are the files init must reuse verbatim. Certificates and their serial
// may be reissued after an interrupted init and therefore are not in this list.
var initPreservedFiles = []string{
	keys.IdentityKeyPath, keys.NetworkKeyPath, keys.SelfRevocationPath, keys.ClockPath,
	"config/node.json", "config/policy.json", "config/presets.json", "config/instances.json",
}

func rememberInitFiles(r fsx.Reader, protected map[string][]byte) error {
	for _, path := range initPreservedFiles {
		if _, ok := protected[path]; ok {
			continue
		}
		data, err := r.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		protected[path] = data
	}
	return nil
}
func checkInitFiles(r fsx.Reader, protected map[string][]byte) error {
	for _, path := range initPreservedFiles {
		want, ok := protected[path]
		if !ok {
			continue
		}
		got, err := r.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			return fmt.Errorf("harness: init changed existing %s", path)
		}
	}
	return nil
}
