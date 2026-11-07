// Package adapter is the Hermes adapter program: it connects the Hermes Kanban
// board to Sidera's direct endpoints. It owns the endpoints, the loop that receives
// messages, the loop that polls the board and the probe; what is done with a message
// or a card is the flows' (see Flows).
package adapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/config"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/kanban"
	"github.com/aianyai/sidera/cmd/sidera-adapter-hermes/internal/state"
	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

const (
	// AdapterID is the adapter's id in its manifest and in initialize.
	AdapterID = "hermes"
	// ProgramName is the program of the package, without an extension.
	ProgramName = "sidera-adapter-hermes"
)

// missingCapability is why the adapter does nothing on a daemon that cannot give a
// direct endpoint the directory and its own declaration. Without them the adapter
// would show every conversation endpoint in the directory and could not tell a
// sender where Hermes is.
const missingCapability = "this daemon cannot give a direct endpoint the directory and its own declaration; update the node"

// Serve runs the adapter over the streams of the daemon until it closes them.
func Serve(ctx context.Context, r io.Reader, w io.WriteCloser, log *slog.Logger, flows Flows) error {
	return serveApp(ctx, r, w, log, New(log, flows))
}

func serveApp(ctx context.Context, r io.Reader, w io.WriteCloser, log *slog.Logger, app *App) error {
	defer app.stop()
	return adapterkit.Serve(ctx, r, w, adapterkit.Config{
		Adapter: adapterproto.AdapterInfo{ID: AdapterID, Version: release.Version()},
		// An instance holds the instance endpoint, the orphan and up to
		// config.MaxMaxEndpoints-2 conversations at most.
		Capabilities: adapterproto.Capabilities{Direct: &adapterproto.DirectCapabilities{MaxEndpoints: config.MaxMaxEndpoints}},
		App:          app,
		Logger:       log,
	})
}

// App is the adapter's Application.
type App struct {
	adapterkit.Base
	log   *slog.Logger
	flows Flows
	tm    timing
	now   func() time.Time

	// ctx ends when the adapter stops; the loops and their calls are bound to it.
	ctx    context.Context
	cancel context.CancelFunc
	loops  sync.WaitGroup

	conn     *adapterkit.Conn
	stateDir string

	mu       sync.Mutex
	started  bool
	env      *Env
	receiver *receiver
}

// New returns the adapter. A nil flows is LogOnly.
func New(log *slog.Logger, flows Flows) *App {
	if flows == nil {
		flows = LogOnly
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &App{log: log, flows: flows, tm: defaultTiming, now: time.Now, ctx: ctx, cancel: cancel}
}

// Initialize keeps the connection and the state directory.
func (a *App) Initialize(_ context.Context, conn *adapterkit.Conn, instance adapterproto.InstanceInfo) error {
	a.conn, a.stateDir = conn, instance.StateDir
	return nil
}

func (a *App) hasDaemonCapability() bool { return a.conn.DaemonCapabilities().DirectEndpointAccess }

// Start reads the configuration, opens the state, attaches the endpoints the
// daemon lists and starts the loops. It answers at once: the daemon serves
// endpoint requests only after every adapter has answered start, so everything that
// calls the daemon runs after the answer. On a daemon without the capability
// direct_endpoint_access it attaches nothing and starts nothing; Probe says why.
func (a *App) Start(ctx context.Context, p adapterproto.StartParams) (adapterproto.StartResult, error) {
	if !a.hasDaemonCapability() {
		a.log.Error(missingCapability)
		return a.Base.Start(ctx, p)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return a.Base.Start(ctx, p)
	}
	cfg, err := config.Decode(p.Config)
	if err != nil {
		return adapterproto.StartResult{}, peer.NewError(peer.CodeInvalidParams, err.Error())
	}
	store, err := state.Open(a.stateDir)
	if err != nil {
		return adapterproto.StartResult{}, fmt.Errorf("opening the state: %w", err)
	}
	if err := kanban.RemoveLeftoverAttachFiles(filepath.Join(a.stateDir, attachDir)); err != nil {
		a.log.Warn("removing attachment files an earlier run left", "error", err)
	}
	board, err := kanban.New(kanban.Config{Executable: cfg.HermesPath, Home: cfg.HermesHome, KanbanHome: cfg.KanbanHome, Board: cfg.Board})
	if err != nil {
		return adapterproto.StartResult{}, err
	}
	eps := newEndpoints(a.conn, store, a.log, a.now, a.tm, cfg.MaxEndpoints)
	if err := eps.adopt(p.DirectEndpoints); err != nil {
		return adapterproto.StartResult{}, fmt.Errorf("recording the endpoints the daemon lists: %w", err)
	}
	gate := &gate{board: board, wakeProfiles: cfg.WakeProfiles(), homeProfile: cfg.Home.Profile, homePlatform: cfg.Home.Platform, report: a.conn.Availability, log: a.log, now: a.now, verifyEvery: a.tm.verifyEvery}
	a.env = &Env{Config: cfg, Board: board, State: store, Log: a.log, Now: a.now, StateDir: a.stateDir, conn: a.conn, endpoints: eps}
	handlers := a.flows(a.env)
	a.receiver = newReceiver(a.conn, eps, store, gate, handlers.Messages, a.log, a.now, a.tm)
	poll := &poller{board: board, gate: gate, handler: handlers.Cards, log: a.log, interval: cfg.PollInterval, tm: a.tm}
	a.started = true

	a.goLoop(func() { eps.setup(a.ctx, instanceName(cfg)) })
	a.goLoop(func() { eps.runIdle(a.ctx, cfg.EndpointIdle) })
	a.goLoop(func() { a.receiver.run(a.ctx) })
	a.goLoop(func() { poll.run(a.ctx) })
	if handlers.Timers != nil {
		timers := &timerLoop{handler: handlers.Timers, gate: gate, log: a.log, tm: a.tm}
		a.goLoop(func() { timers.run(a.ctx) })
	}
	return a.Base.Start(ctx, p)
}

func (a *App) goLoop(fn func()) {
	a.loops.Add(1)
	go func() {
		defer a.loops.Done()
		fn()
	}()
}

// instanceName is what the instance endpoint is called in the directory. The name
// only tells the endpoints of one node apart; the node describes the host.
func instanceName(cfg config.Config) string {
	if cfg.DisplayName != "" {
		return cfg.DisplayName
	}
	return "Hermes"
}

// EndpointPending wakes the receive loop for the endpoint.
func (a *App) EndpointPending(_ context.Context, p adapterproto.PendingParams) {
	a.mu.Lock()
	r := a.receiver
	a.mu.Unlock()
	if r != nil {
		r.notify(p.Endpoint)
	}
}

// Probe judges what the adapter depends on: the daemon's capability, the Hermes
// command line with its board, the Kanban notifier that wakes conversations and the
// toolsets of the agent that delegates.
func (a *App) Probe(ctx context.Context, p adapterproto.ProbeParams) (adapterproto.ProbeResult, error) {
	res, err := a.Base.Probe(ctx, p)
	if err != nil {
		return res, err
	}
	if !a.hasDaemonCapability() {
		res.Dependencies = append(res.Dependencies, adapterproto.Dependency{
			Name: "daemon", State: adapterproto.DependencyMissing, Detail: detail(missingCapability),
		})
		return res, nil
	}
	res.Dependencies = append(res.Dependencies, adapterproto.Dependency{Name: "daemon", State: adapterproto.DependencyOK})
	a.mu.Lock()
	env := a.env
	a.mu.Unlock()
	if env == nil {
		return res, errors.New("the adapter has not started, so there is no Hermes to probe")
	}
	hermes := adapterproto.Dependency{Name: "hermes", State: adapterproto.DependencyOK}
	if err := env.Board.Probe(ctx); err != nil {
		hermes.State, hermes.Detail = adapterproto.DependencyMissing, detail(err.Error())
	}
	res.Dependencies = append(res.Dependencies, hermes)
	if hermes.State != adapterproto.DependencyOK {
		return res, nil
	}
	// Receiving and writing cards work without the notifier, but nothing wakes the
	// conversation of a card that is completed, so the owner is told here as well.
	notifier := adapterproto.Dependency{Name: "hermes-notifier", State: adapterproto.DependencyOK}
	off, err := env.Board.NotifierDisabled(ctx, env.Config.WakeProfiles())
	switch {
	case len(off) > 0:
		// A profile known to be off is said even when another could not be read.
		notifier.State, notifier.Detail = adapterproto.DependencyMissing, notifierOffDetail(off)
		if err != nil {
			a.log.Warn("could not read whether the Kanban notifier is on in every profile", "error", err)
		}
	case err != nil:
		notifier.State = adapterproto.DependencyMissing
		notifier.Detail = detail(fmt.Sprintf("could not read whether the Kanban notifier is on: %v", err))
	}
	res.Dependencies = append(res.Dependencies, notifier)

	// An agent without the kanban or skills toolset on the home platform cannot
	// delegate over Sidera, or does not know how to: it creates a plain card instead.
	toolsets := adapterproto.Dependency{Name: "hermes-toolsets", State: adapterproto.DependencyOK}
	home := env.Config.Home
	reading, err := env.Board.Toolsets(ctx, home.Profile, home.Platform)
	switch {
	case err != nil:
		toolsets.State = adapterproto.DependencyMissing
		toolsets.Detail = detail(fmt.Sprintf("could not read which toolsets the agent of profile %s on platform %s has: %v",
			home.Profile, home.Platform, err))
	case len(reading.Missing) > 0:
		toolsets.State, toolsets.Detail = adapterproto.DependencyMissing, toolsetsMissingDetail(home.Profile, home.Platform, reading.Missing)
	}
	res.Dependencies = append(res.Dependencies, toolsets)
	return res, nil
}

// Shutdown stops the loops and waits for them. The adapter starts no process that
// outlives it (a Hermes command it runs ends with the call, or is killed with its
// context), so the intent says nothing the adapter must act on, and every intent is
// served the same way.
func (a *App) Shutdown(ctx context.Context, _ adapterproto.ShutdownParams) error {
	a.cancel()
	done := make(chan struct{})
	go func() { a.loops.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

func (a *App) stop() { a.cancel() }
