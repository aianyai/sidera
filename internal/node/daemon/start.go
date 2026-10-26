package daemon

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/node/txn"
)

// Start brings the node up through the fifteen steps of the start, in their order,
// and returns when it serves. Each step logs when it begins and when it ends, so
// the order can be read from logs/daemon.log. A step that fails stops the start,
// and what had begun is shut down in the order of the shutdown, so nothing is left
// running and the lock is free for the next start.
//
// The context is for the start only: when Start has returned the node lives until
// RequestStop, a fatal failure of the disk, or the end of Run. When it ends during
// the start, the start stops before the next step and what had begun is shut down,
// and the error wraps the error of the context.
func Start(ctx context.Context, cfg Config) (*Daemon, error) {
	cfg = cfg.withDefaults()
	home, err := resolveHome(cfg.Home)
	if err != nil {
		return nil, err
	}
	d := &Daemon{
		cfg: cfg, home: home, stopCh: make(chan struct{}), done: make(chan struct{}),
		defer_: newDeferredHandler(), served: make(chan error, 1),
	}
	d.log = slog.New(d.defer_)

	// The steps, as a table: the number the log says, the name, and what it does.
	// Steps 11 to 14 are the engine's recovery of deliveries, which logs its own.
	for _, s := range []struct {
		n    int
		name string
		run  func(context.Context) error
	}{
		{1, "lock", d.stepLock},
		{2, "config", d.stepConfig},
		{3, "keys", d.stepKeys},
		{4, "clock", d.stepClock},
		{5, "certificate", d.stepCertificate},
		{6, "scan", d.stepScan},
		{7, "trust", d.stepTrust},
		{8, "manage", d.stepManage},
		{9, "agent-probe", d.stepAgentProbe},
		{10, "adapters", d.stepAdapters},
		{0, "", d.stepRecoverDelivery},
		{15, "serve", d.stepServe},
	} {
		if err := ctx.Err(); err != nil {
			return nil, d.abort(fmt.Errorf("the start was interrupted before step %d: %w", s.n, err))
		}
		if s.n == 0 {
			if err := s.run(ctx); err != nil {
				return nil, d.abort(err)
			}
			continue
		}
		d.log.Info(fmt.Sprintf("startup step %d begin", s.n), "name", s.name)
		if err := s.run(ctx); err != nil {
			err = fmt.Errorf("startup step %d (%s): %w", s.n, s.name, err)
			d.log.Error(fmt.Sprintf("startup step %d failed", s.n), "name", s.name, "error", err.Error())
			return nil, d.abort(err)
		}
		d.log.Info(fmt.Sprintf("startup step %d end", s.n), "name", s.name)
	}
	go d.supervise()
	return d, nil
}

// abort shuts down what the start had begun, in the order of the shutdown, and
// returns the error that stopped the start. A start that stopped because of a fatal
// failure of the disk, in whatever step it met it, ends as a fatal failure does:
// memory and disk may disagree, so the process exits with ExitFatal and the next
// start scans the disk again. The failure is known by the error (a fatal storage
// error, or the engine's refusal that says so) or by the engine having died, which
// a hook that only saw a refusal of a dead engine cannot say.
func (d *Daemon) abort(cause error) error {
	fatal := txn.IsFatal(cause) || storageFatal(cause) || d.engineFailed()
	d.shutdown(fatal)
	if fatal || d.engineFailed() {
		return &FatalError{Err: cause}
	}
	return cause
}

// storageFatal reports whether err carries the engine's refusal for a fatal
// storage error, which the engine gives a command that came after the failure too,
// when the error itself is no longer there.
func storageFatal(err error) bool {
	r, ok := engine.ReasonOf(err)
	return ok && r == engine.ReasonStorageFatal
}

// engineFailed reports whether the engine ended in a failure.
func (d *Daemon) engineFailed() bool { return d.engine != nil && d.engine.Err() != nil }

// env is what the hooks are given.
func (d *Daemon) env() *Env {
	return &Env{
		Home: d.home, Clock: d.cfg.Clock, Rand: d.cfg.Rand, Log: d.log,
		Keys: d.keys, Config: d.cfgd, Engine: d.engine, Lock: d.lock, Logs: d.logs, RequestStop: d.RequestStop,
	}
}

// Step 1: the data directory is looked at before anything is written, and the lock
// is taken. The permissions come first and refuse without narrowing; a data
// directory that init did not finish is not started; and only then is the lock
// taken, so that a refused start leaves no lock file and a second daemon is told
// that one is running.
func (d *Daemon) stepLock(context.Context) error {
	fsys, err := openHome(d.cfg, d.home)
	if err != nil {
		return err
	}
	d.fsys = fsys
	if err := checkPermissions(fsys, d.home); err != nil {
		return err
	}
	if done, err := keys.Initialized(fsys); err != nil {
		return err
	} else if !done {
		return fmt.Errorf("%w: %s has no keys/init.rec", ErrNotInitialized, d.home)
	}
	d.lock, err = takeLock(fsys, d.home)
	return err
}

// Step 2: the transaction layer recovers (the settle, then the removal of every
// temporary file), the layout the owner removed is made again, the log opens, and
// the configuration is loaded strictly: a file that is not valid stops the start
// and is named. The recovery comes first because what follows reads existence and
// acts on it durably.
func (d *Daemon) stepConfig(context.Context) error {
	tab, err := Table()
	if err != nil {
		return err
	}
	d.w = txn.New(d.fsys, tab, d.cfg.Observer)
	if err := d.w.Recover(); err != nil {
		return fmt.Errorf("recovering the data directory: %w", err)
	}
	if err := RepairLayout(d.w); err != nil {
		return fmt.Errorf("making the layout again: %w", err)
	}
	if err := d.openLog(); err != nil {
		return fmt.Errorf("opening the log: %w", err)
	}
	if d.cfgd, err = config.Load(d.w); err != nil {
		return err
	}
	d.loaded.Store(d.cfgd)
	return nil
}

// Step 3: the keys, the revocations the owner kept and the serial the node has
// seen are loaded, and a node whose own identity was retired refuses to run. No
// signature is made here.
func (d *Daemon) stepKeys(context.Context) error {
	k, err := keys.Load(d.w)
	if err != nil {
		return err
	}
	d.keys = k
	return k.CheckNotRetired(d.cfgd.RevocationSet())
}

// Step 4: the wall clock is judged before anything is signed. A clock found
// behind the mark that was saved, by more than the tolerance every node accepts,
// is not trusted: the pause is made durable at once, together with the mark that
// was, which is never lowered, and the owner clears it.
func (d *Daemon) stepClock(context.Context) error {
	st, err := keys.ReadClock(d.w.Reader())
	if err != nil {
		return err
	}
	hw := clock.NewHighWater(st.HighWater)
	paused := st.Paused()
	d.pause.since = st.RollbackPausedSince
	now := d.cfg.Clock.Now()
	if v, _ := hw.Observe(now); v == clock.WallRolledBack {
		d.log.Warn("the wall clock was set back; new work that depends on a deadline is refused until the owner clears it",
			"now", now, "high_water", hw.Value())
		if !paused {
			paused, d.pause.since = true, now
			if err := d.keys.SaveClock(keys.ClockState{HighWater: hw.Value(), RollbackPausedSince: now}); err != nil {
				return fmt.Errorf("making the pause durable: %w", err)
			}
		}
	}
	d.clockHW, d.clockPaused = hw, paused
	return nil
}

// Step 5: the certificate is renewed when it has ten days or less left, and never
// while the clock is in doubt. It comes after step 4 because nothing is signed on
// a clock that was not judged. This is the one goroutine the node has yet, so the
// renewal does not meet the loop; the hourly check that follows runs in the loop.
func (d *Daemon) stepCertificate(context.Context) error {
	// Step 3 refused a node whose identity is retired, so none is at this point.
	return d.renewCertificate(d.cfg.Clock.Now(), engine.SignRefusalOf(false, d.clockPaused))
}

// The adapter host is what the engine calls to reach an adapter: this is where the
// two layers meet, and neither imports the other.
var _ engine.Adapters = (*adapterhost.Host)(nil)

// openPackage opens the directory the owner names as an adapter package, for the
// engine to read: a reader rooted at it. The package is on the machine's own file
// system, whatever file system the node's data directory is on, and the engine is
// given the reading half only.
func (d *Daemon) openPackage(dir string) (fsx.Reader, error) {
	return fsx.OpenOS(dir, d.cfg.Rand)
}

// Step 6: the scan of state/ (the engine's first recovery entry) runs before the
// loop exists: damaged files are quarantined, the rules of recovery run until none
// has work, and the engine builds its view. The periodic tasks are registered
// here, before the loop starts, and the clock state is the one the loop also runs
// at once when it finds the clock set back. The loopback is set here as well.
func (d *Daemon) stepScan(ctx context.Context) error {
	deps := engine.Deps{
		Node: d.keys.Node(), Clock: d.cfg.Clock, HighWater: d.clockHW, RollbackPaused: d.clockPaused, RollbackPausedSince: d.pause.since,
		Writer: d.w, Store: store.New(d.w), Config: d.cfgd, Keys: d.keys, Rand: d.cfg.Rand, Home: d.home, Log: d.log,
		OpenPackage: d.openPackage,
	}
	if d.cfg.AdapterHost != nil { // never a nil host inside the interface: the engine tests it for nil
		deps.Adapters = d.cfg.AdapterHost
	}
	e, rep, err := engine.Open(ctx, deps, d.cfg.Engine)
	if err != nil {
		return err
	}
	d.engine = e
	// The one transport of a node of one machine is the loopback, and it is made
	// here with no knobs: they are faults for tests, and the product has none to
	// arm. Without a transport every send and every session creation would fail
	// inside the engine. It is set before the loop starts, as the engine requires.
	if err := e.SetTransport(transport.NewLoopback(d.keys.Node(), e, nil)); err != nil {
		return err
	}
	if err := e.AddClockState(clockStateTask, ClockStateEvery, d.clockTask); err != nil {
		return err
	}
	if err := e.AddPeriodic(certificateTask, CertificateCheckEvery, d.certificateTask); err != nil {
		return err
	}
	if err := e.AddArchiveSweep(ArchiveSweepEvery); err != nil {
		return err
	}
	for _, t := range d.cfg.Attach.Periodic {
		if err := e.AddPeriodic(t.Name, t.Every, t.Run); err != nil {
			return err
		}
	}
	d.log.Info("state scanned", "passes", rep.Passes, "endpoints", rep.Endpoints, "unfinished", rep.Unfinished)
	return nil
}

// Step 7: the place of the synchronisation of revocations and of the trust graph,
// and of the gate on vouchers. A node of one machine has none of it.
func (d *Daemon) stepTrust(ctx context.Context) error { return run(ctx, d.cfg.Attach.TrustSync, d) }

// Step 8: the loop starts, in the phase of recovery, and the management interface
// opens. It answers reads, and refuses what changes state as unavailable until the
// start is over: the owner can see that the node is starting, and cannot change
// what the start has not finished building on.
func (d *Daemon) stepManage(context.Context) error {
	d.engine.Start()
	addr, err := ipc.Locate(d.home, ipc.KindManage)
	if err != nil {
		return err
	}
	l, err := manageapi.Listen(addr, d.lock, ipc.Options{Logger: d.log})
	if err != nil {
		return err
	}
	srv, err := manageapi.New(manageapi.Config{
		Service: d.engine, Node: d.keys.Node(), Clock: d.cfg.Clock, Stop: d.RequestStop, Version: d.cfg.Version, Log: d.log,
	})
	if err != nil {
		l.Close()
		return err
	}
	d.manage = srv
	go func() { d.served <- srv.Serve(l) }()
	return nil
}

// Step 9: the Agent interface opens so that it takes only the one-use probe token.
func (d *Daemon) stepAgentProbe(ctx context.Context) error {
	d.agentBegan = true
	return run(ctx, d.cfg.Attach.AgentProbeOnly, d)
}

// Step 10: the adapter instances the owner enabled are started, and the step ends
// when each has said which turns run or has been judged offline. The host does that
// and answers the requests of the adapters through the engine, whose refusals
// apiwire words as the adapter protocol does; what else starts at this step is the
// hook's, which runs after the host.
func (d *Daemon) stepAdapters(ctx context.Context) error {
	d.adaptersBegan = true
	if h := d.cfg.AdapterHost; h != nil {
		env := d.env()
		err := h.Start(ctx, adapterhost.Env{
			Home: d.home, Source: env.Config, Logs: env.Logs, Engine: apiwire.AdapterEngine(d.engine),
		})
		if err != nil {
			return err
		}
	}
	return run(ctx, d.cfg.Attach.Adapters, d)
}

// Steps 11 to 14: the engine's second recovery entry: the creation intents and
// the prompt attempts are checked, every record is ticked, the feedback that fell
// due goes out, and the loopback goes on. They come after the adapters have
// reported, so that a lease is not released under a turn that is running. The
// engine logs the begin and the end of each.
func (d *Daemon) stepRecoverDelivery(ctx context.Context) error { return d.engine.RecoverDelivery(ctx) }

// Step 15: the engine serves, and the Agent interface switches to full service.
func (d *Daemon) stepServe(ctx context.Context) error {
	d.engine.Serve()
	return run(ctx, d.cfg.Attach.AgentServe, d)
}

// run calls a hook, if there is one.
func run(ctx context.Context, h func(context.Context, *Env) error, d *Daemon) error {
	if h == nil {
		return nil
	}
	return h(ctx, d.env())
}
