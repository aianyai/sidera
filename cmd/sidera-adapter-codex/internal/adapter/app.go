// Package adapter implements the adapter protocol for Codex: it keeps a private
// App Server, maps each protocol method to the server's methods and reports the
// server's events as the turns of the sessions it holds.
//
// Every session is a thread of the server, and the thread's id is the session's
// native reference. The thread carries, as its own configuration, the credentials
// the agent's tools need; the server keeps none of it, so it is sent again every
// time the thread is loaded.
package adapter

import (
	"context"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/codexcfg"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/create"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/server"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// ClientVersion is the version the adapter reports to the App Server as its own.
const ClientVersion = "0.1.0"

// ServerManager finds or starts the App Server. *server.Server is the real one.
type ServerManager interface {
	Connect(ctx context.Context) (appserver.Endpoint, error)
}

// Options holds what a test replaces. The zero value is the real thing.
type Options struct {
	// NewServer makes the manager of the App Server from its configuration.
	NewServer func(server.Config) ServerManager
	// CodexVersion returns the version of the Codex program.
	CodexVersion func(ctx context.Context, program string) (string, error)
	// LookPath finds a program, as exec.LookPath does.
	LookPath func(name string) (string, error)
	// MCPProbe runs the MCP front end's probe; see runMCPProbe.
	MCPProbe func(ctx context.Context, program string, env []string) error
	// Create holds the numbers of session creation.
	Create create.Params
	// MCPWait is how long a creation waits for the channel's MCP server to start.
	MCPWait time.Duration
	// PollEvery is how often a creation looks into the thread's record while it
	// waits for the first prompt.
	PollEvery time.Duration
	// ReconnectMin and ReconnectMax bound the wait between attempts to reach the
	// App Server again.
	ReconnectMin, ReconnectMax time.Duration
	// Environ is the environment of the adapter; nil means the process's.
	Environ []string
	Logger  *slog.Logger
}

func (o *Options) defaults() {
	if o.NewServer == nil {
		o.NewServer = func(c server.Config) ServerManager { return server.New(c) }
	}
	if o.CodexVersion == nil {
		o.CodexVersion = server.CLIVersion
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.MCPProbe == nil {
		o.MCPProbe = runMCPProbe
	}
	if o.Create == (create.Params{}) {
		o.Create = create.DefaultParams()
	}
	if o.MCPWait == 0 {
		o.MCPWait = 20 * time.Second
	}
	if o.PollEvery == 0 {
		o.PollEvery = 2 * time.Second
	}
	if o.ReconnectMin == 0 {
		o.ReconnectMin = time.Second
	}
	if o.ReconnectMax == 0 {
		o.ReconnectMax = 30 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
}

// App is the Codex adapter.
type App struct {
	adapterkit.Base
	opts Options
	log  *slog.Logger

	conn  *adapterkit.Conn
	inst  adapterproto.InstanceInfo
	bgCtx context.Context // ends at Shutdown
	stop  context.CancelFunc

	// connMu serializes dialling, so that two callers never open two connections.
	connMu sync.Mutex

	mu          sync.Mutex // guards everything below
	started     bool
	runtimeEP   adapterproto.EndpointRef
	instance    codexcfg.Instance
	codexPath   string
	sideraPath  string
	missing     []string // programs that could not be found, for the availability
	codexVer    string
	srv         ServerManager
	presets     map[string]presetEntry
	sessions    map[adapterproto.EndpointRef]*session
	byThread    map[string]*session
	attempts    map[adapterproto.AttemptID]*createAttempt
	mcp         map[string]string // thread id to the state of the channel's MCP server
	client      *appserver.Client // nil while there is no connection
	serverVer   string
	signedIn    bool
	reconnect   bool // a reconnect loop is running
	lastAvail   string
	availSince  int64
	shutdownNow bool
}

type presetEntry struct {
	preset codexcfg.Preset
	// configured is false for a preset that has no section for this adapter, which
	// reaches the adapter as an empty set of values.
	configured bool
	problems   []string
}

// New returns the adapter.
func New(opts Options) *App {
	opts.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	return &App{
		opts: opts, log: opts.Logger, bgCtx: ctx, stop: cancel,
		presets:  map[string]presetEntry{},
		sessions: map[adapterproto.EndpointRef]*session{},
		byThread: map[string]*session{},
		attempts: map[adapterproto.AttemptID]*createAttempt{},
		mcp:      map[string]string{},
		signedIn: true,
	}
}

// Initialize keeps the connection to the daemon and the instance's directories.
func (a *App) Initialize(_ context.Context, conn *adapterkit.Conn, inst adapterproto.InstanceInfo) error {
	a.conn, a.inst = conn, inst
	return nil
}

// Shutdown ends the adapter's own work. The App Server is left running: it is
// meant to outlive the adapter, so that turns go on while the adapter restarts.
func (a *App) Shutdown(context.Context) {
	a.mu.Lock()
	a.shutdownNow = true
	client := a.client
	a.mu.Unlock()
	a.stop()
	if client != nil {
		_ = client.Close()
	}
}

func (a *App) shuttingDown() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.shutdownNow
}

func unknownEndpoint(ref adapterproto.EndpointRef) error {
	return peer.NewError(adapterproto.CodeUnknownEndpoint, "the adapter holds no session endpoint "+string(ref))
}

// sessionOf returns the session an endpoint names, or nil.
func (a *App) sessionOf(ref adapterproto.EndpointRef) *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[ref]
}

func (a *App) sessionByThread(threadID string) *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.byThread[threadID]
}

// threadConfig is the thread-level configuration of a session.
func (a *App) threadConfig(s *session, p codexcfg.Preset) map[string]any {
	a.mu.Lock()
	sidera := a.sideraPath
	a.mu.Unlock()
	return p.ThreadConfig(codexcfg.Access{Program: sidera, Env: s.access})
}

// presetEntryOf returns what the adapter knows of a preset by name, usable or not.
func (a *App) presetEntryOf(name string) (presetEntry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.presets[name]
	return e, ok
}

// presetOf returns a usable preset by name.
func (a *App) presetOf(name string) (codexcfg.Preset, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.presets[name]
	if !ok || len(e.problems) > 0 {
		return codexcfg.Preset{}, false
	}
	return e.preset, true
}
