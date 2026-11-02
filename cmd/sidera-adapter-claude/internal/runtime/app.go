// Package runtime is the Claude adapter's side that meets the world: it answers
// the daemon's requests by reading session files, watching the process table,
// starting `claude -p` processes and running the probes, and reports the turns
// of the sessions it holds. The decisions are in the pure packages beside it;
// what it does to the world goes through the interfaces of world.go and package
// proc, so its flows run against fakes.
package runtime

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/create"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime/proc"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Options are what the adapter is built from. Zero fields take the operating
// system's implementation, which is what the executable uses; tests replace them.
type Options struct {
	// Environ is the adapter's environment, which turn processes inherit after
	// the strip of claudeargs. Default os.Environ().
	Environ []string
	// LookPath finds an executable on PATH. Default exec.LookPath.
	LookPath func(file string) (string, error)

	Table     proc.Table
	Launcher  proc.Launcher
	Signaller proc.Signaller
	Files     Files
	Runner    Runner
	// Inbox writes to the inbox sockets of attached sessions.
	Inbox Inbox

	// ClaudeDir is the runtime's configuration directory, where it keeps the
	// registry of running sessions and the session records. Default: the
	// CLAUDE_CONFIG_DIR of Environ, else ~/.claude.
	ClaudeDir string

	// Now is the clock. Rand is where uuids come from.
	Now  func() time.Time
	Rand io.Reader

	// Create holds the bounds of the creation flow.
	Create create.Params
	// PollInterval is how often a session's process and record are looked at.
	PollInterval time.Duration
	// CloseWait is how long a close waits for an interrupted turn to end.
	CloseWait time.Duration

	Logger *slog.Logger
}

// DefaultPollInterval and DefaultCloseWait are the provisional values: a prompt
// reaches the record within a couple of seconds of the start, and an interrupt
// ends a turn within seconds.
const (
	DefaultPollInterval = 200 * time.Millisecond
	DefaultCloseWait    = 10 * time.Second
)

// App is the Claude adapter: an adapterkit.Application for a runtime.
type App struct {
	adapterkit.Base
	o   Options
	log *slog.Logger

	// life ends with Shutdown; the monitors of the sessions run under it.
	life context.Context
	stop context.CancelFunc

	mu        sync.Mutex
	daemon    Daemon
	instance  adapterproto.InstanceInfo
	started   bool
	claude    string // the Claude executable; "" when none was found
	sidera    string // the sidera executable; "" when none was found
	mcpConfig string // the path of the MCP configuration file
	presets   map[string]adapterproto.ConfigValues
	sessions  map[adapterproto.EndpointRef]*session
	attempts  map[adapterproto.AttemptID]*attempt
	attached  map[adapterproto.EndpointRef]*attachedSession

	avail availability
}

// New builds the adapter.
func New(o Options) *App {
	if o.Environ == nil {
		o.Environ = os.Environ()
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Table == nil {
		o.Table = proc.NewTable()
	}
	if o.Launcher == nil {
		o.Launcher = proc.NewLauncher(o.Table)
	}
	if o.Signaller == nil {
		o.Signaller = proc.NewSignaller()
	}
	if o.Files == nil {
		o.Files = osFiles{}
	}
	if o.Runner == nil {
		o.Runner = osRunner{}
	}
	if o.Inbox == nil {
		o.Inbox = osInbox{}
	}
	if o.ClaudeDir == "" {
		o.ClaudeDir = claudeDir(o.Environ)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Rand == nil {
		o.Rand = rand.Reader
	}
	if o.Create == (create.Params{}) {
		o.Create = create.DefaultParams()
	}
	if o.PollInterval <= 0 {
		o.PollInterval = DefaultPollInterval
	}
	if o.CloseWait <= 0 {
		o.CloseWait = DefaultCloseWait
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	life, stop := context.WithCancel(context.Background())
	return &App{
		o: o, log: o.Logger, life: life, stop: stop,
		presets:  map[string]adapterproto.ConfigValues{},
		sessions: map[adapterproto.EndpointRef]*session{},
		attempts: map[adapterproto.AttemptID]*attempt{},
		attached: map[adapterproto.EndpointRef]*attachedSession{},
	}
}

// Initialize keeps the connection and the instance, and makes the directory the
// per-turn files go in.
func (a *App) Initialize(_ context.Context, conn *adapterkit.Conn, instance adapterproto.InstanceInfo) error {
	if err := os.MkdirAll(a.turnsDir(instance), 0o700); err != nil {
		return fmt.Errorf("make the turn directory: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.daemon, a.instance = conn, instance
	return nil
}

// claudeDir is the runtime's configuration directory as a process with this
// environment finds it; "" when neither CLAUDE_CONFIG_DIR nor the home
// directory is known.
func claudeDir(environ []string) string {
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, "CLAUDE_CONFIG_DIR="); ok && v != "" {
			return v
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// Shutdown stops the monitors. The turn processes are detached and run on.
func (a *App) Shutdown(context.Context) { a.stop() }

func (a *App) turnsDir(instance adapterproto.InstanceInfo) string {
	return filepath.Join(instance.StateDir, "turns")
}

// errNotStarted is the answer to a request that needs the configuration the
// daemon sends in adapter.start, when it did not send it yet. The outcome of a
// request that answers with it is unknown to the daemon, which asks again.
var errNotStarted = errors.New("adapter.start has not been received")

func (a *App) requireStarted() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.started {
		return errNotStarted
	}
	return nil
}

func (a *App) nowMS() int64 { return a.o.Now().UnixMilli() }

// conn returns the connection to the daemon, which Initialize set.
func (a *App) conn() Daemon {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.daemon
}
