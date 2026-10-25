package adapterhost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"sort"
	"sync"

	"github.com/aianyai/sidera/internal/node/clock"
)

// Config configures New.
type Config struct {
	// Launcher starts the adapter processes. It is required.
	Launcher Launcher
	// Params are the tunables; a zero field takes its default.
	Params Params
	// Clock times everything the host waits for: pings, the restart backoff, the
	// waits for an adapter to end, the handshake limits. nil is clock.System(): in
	// the daemon these are real-time waits; a test passes a fake and moves it.
	Clock clock.Clock
	// Log receives what the host did and why; nil discards it. It never receives a
	// message body, a token or a key.
	Log *slog.Logger
}

// Errors of Start.
var (
	errStarted = errors.New("adapterhost: the host was started already")
	errEnded   = errors.New("adapterhost: the host was ended")
)

type phase uint8

const (
	phaseNew phase = iota
	phaseRunning
	phaseEnded
)

// Host supervises the adapter instances of the node. Create it with New, start it
// once with Start and end it with Shutdown or Abort. Every method is safe for
// concurrent use.
type Host struct {
	launcher Launcher
	params   Params
	clk      clock.Clock
	log      *slog.Logger
	version  string

	mu        sync.Mutex
	phase     phase
	env       Env
	endpoints EndpointCalls // nil: the engine does not answer endpoint requests
	observed  ObservedCalls // nil: the engine does not answer recv.observed
	ctx       context.Context
	cancel    context.CancelFunc
	sups      map[string]*supervisor // by instance; the newest supervisor of each
	live      sync.WaitGroup         // every supervisor that has not finished
	drained   chan struct{}          // closed once the host ended and every supervisor finished
}

// New returns a host that is not running yet. The launcher is required.
func New(c Config) (*Host, error) {
	if c.Launcher == nil {
		return nil, errors.New("adapterhost: no launcher")
	}
	if err := c.Params.validate(); err != nil {
		return nil, err
	}
	h := &Host{
		launcher: c.Launcher,
		params:   c.Params.withDefaults(),
		clk:      c.Clock,
		log:      c.Log,
		version:  daemonVersion(),
		sups:     map[string]*supervisor{},
	}
	if h.clk == nil {
		h.clk = clock.System()
	}
	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}
	return h, nil
}

// daemonVersion is the text initialize carries as the version of the daemon: the
// version of the module the binary was built from, which is "(devel)" for a
// build from a working tree.
func daemonVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "unknown"
}

// LockPath is the file the adapter of an instance locks before it answers
// initialize: adapter-<instance>.lock in the data directory. It is the one
// definition; the host sends it in initialize and in Instance.
func LockPath(home, instance string) string {
	return filepath.Join(home, "adapter-"+instance+".lock")
}

func (e Env) validate() error {
	switch {
	case e.Home == "" || !filepath.IsAbs(e.Home):
		return fmt.Errorf("adapterhost: Env.Home %q is not an absolute path", e.Home)
	case e.Source == nil:
		return errors.New("adapterhost: Env.Source is nil")
	case e.Logs == nil:
		return errors.New("adapterhost: Env.Logs is nil")
	case e.Engine == nil:
		return errors.New("adapterhost: Env.Engine is nil")
	}
	return nil
}

// Start starts the supervision of every enabled instance and returns when each
// has answered adapter.start, or has failed its first attempt (judged offline and
// reported), or Params.StartWait has passed on the host's clock (the ones still
// not started are then reported as GoneStartFailed and keep trying). It fails only
// for a bad Env, for a host that was started or ended before, and when ctx ends
// first; the host then keeps running and is ended with Shutdown or Abort. ctx
// bounds this call alone: the supervision does not end with it.
func (h *Host) Start(ctx context.Context, env Env) error {
	if err := env.validate(); err != nil {
		return err
	}
	h.mu.Lock()
	switch h.phase {
	case phaseRunning:
		h.mu.Unlock()
		return errStarted
	case phaseEnded:
		h.mu.Unlock()
		return errEnded
	}
	h.phase = phaseRunning
	h.env = env
	h.endpoints, _ = env.Engine.(EndpointCalls)
	h.observed, _ = env.Engine.(ObservedCalls)
	h.ctx, h.cancel = context.WithCancel(context.WithoutCancel(ctx))
	var ids []string
	for id, in := range env.Source.Instances().Items {
		if in.Enabled {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	started := make([]<-chan struct{}, 0, len(ids))
	for _, id := range ids {
		started = append(started, h.superviseLocked(id, nil, true).started)
	}
	h.mu.Unlock()

	for _, ch := range started {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// superviseLocked starts the supervisor of an instance. prev is the supervisor
// of the same instance that is still ending, if there is one. h.mu is held and
// the host is running.
func (h *Host) superviseLocked(id string, prev <-chan struct{}, startWait bool) *supervisor {
	s := newSupervisor(h, id, prev, startWait)
	h.sups[id] = s
	h.live.Add(1)
	go s.run()
	return s
}

// forget removes a finished supervisor from the host.
func (h *Host) forget(s *supervisor) {
	h.mu.Lock()
	if h.sups[s.id] == s {
		delete(h.sups, s.id)
	}
	h.mu.Unlock()
	h.live.Done()
}

// EnsureRunning starts the supervision of an enabled instance. It is idempotent
// and does nothing before Start and after the host ended.
func (h *Host) EnsureRunning(instance string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.phase != phaseRunning {
		return
	}
	var prev <-chan struct{}
	if s := h.sups[instance]; s != nil {
		if !s.isStopping() {
			return
		}
		// The adapter that is being stopped holds the instance until it has
		// ended: the new supervisor waits for it before it launches.
		prev = s.done
	}
	h.superviseLocked(instance, prev, false)
}

// Stop ends the supervision of an instance gracefully: the adapter is sent
// shutdown, given Params.ShutdownWait to end and killed if it does not. It is
// idempotent, returns at once and reports nothing to the engine. From the moment
// it returns, calls to the instance return ErrNotConnected and the adapter's
// requests are answered as the host shutting down.
func (h *Host) Stop(instance string) {
	h.mu.Lock()
	s := h.sups[instance]
	h.mu.Unlock()
	if s != nil {
		s.requestStop()
	}
}

// Counts returns what the host counts of the connection of an instance. It is all
// zero for an instance that is not supervised and after the host ended.
func (h *Host) Counts(instance string) ConnCounts {
	h.mu.Lock()
	var s *supervisor
	if h.phase == phaseRunning {
		s = h.sups[instance]
	}
	h.mu.Unlock()
	if s == nil {
		return ConnCounts{}
	}
	return s.counts()
}

// Shutdown ends the host gracefully: every adapter in service is sent shutdown
// (limited by Params.HandshakeTimeout), its stream is closed, and it is given
// Params.ShutdownWait to end before it is killed; the supervisors stop and
// Shutdown returns when every goroutine of the host has ended. The host reports
// nothing to the engine. When ctx ends first, the rest is ended as Abort ends it
// and Shutdown returns the error of ctx without waiting. It is idempotent; after
// it, calls return ErrNotConnected and Start fails.
func (h *Host) Shutdown(ctx context.Context) error {
	for _, s := range h.beginEnd() {
		s.requestStop()
	}
	select {
	case <-h.drained:
		return nil
	case <-ctx.Done():
		h.cancelAll()
		return ctx.Err()
	}
}

// Abort ends the host at once, as a crash or a fatal failure requires: every
// process is killed, nothing is sent to an adapter and nothing is reported to
// the engine. It returns when every goroutine of the host has ended. It is
// idempotent; after it, calls return ErrNotConnected and Start fails.
func (h *Host) Abort() {
	h.beginEnd()
	h.cancelAll()
	<-h.drained
}

// beginEnd marks the host ended and returns the supervisors that were running.
func (h *Host) beginEnd() []*supervisor {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.phase == phaseEnded {
		return nil
	}
	h.phase = phaseEnded
	sups := make([]*supervisor, 0, len(h.sups))
	for _, s := range h.sups {
		sups = append(sups, s)
	}
	h.drained = make(chan struct{})
	go func() {
		h.live.Wait()
		close(h.drained)
	}()
	return sups
}

func (h *Host) cancelAll() {
	h.mu.Lock()
	cancel := h.cancel
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
