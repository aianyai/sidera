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
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/claudeargs"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/create"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime/proc"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/turn"
	"github.com/aianyai/sidera/sdk/go/adapterkit"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
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
	// InterruptWait is how long an interrupt waits for the turn it stopped to end
	// before it answers that the stop was requested.
	InterruptWait time.Duration

	Logger *slog.Logger
}

// The defaults are provisional values: a prompt reaches the record within a
// couple of seconds of the start, and an interrupt ends a turn within seconds.
const (
	DefaultPollInterval  = 200 * time.Millisecond
	DefaultCloseWait     = 10 * time.Second
	DefaultInterruptWait = 10 * time.Second
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
		o.Launcher = proc.NewLauncher()
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
	if o.InterruptWait <= 0 {
		o.InterruptWait = DefaultInterruptWait
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
	if dir := claudeargs.ConfigDir(environ); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// Shutdown stops the monitors. A turn process is detached and runs on through a
// restart or a stop, so that a turn survives the daemon or the adapter being
// replaced; when the instance is retired (disabled or removed) nothing follows to
// take it over, and the turns this adapter knows are interrupted first. The error
// names what could not be interrupted, and the adapter ends all the same.
func (a *App) Shutdown(ctx context.Context, p adapterproto.ShutdownParams) error {
	var err error
	if p.Effective() == adapterproto.ShutdownRetire {
		err = a.interruptRunningTurns(ctx)
	}
	a.stop()
	return err
}

// interruptRunningTurns interrupts, as Ctrl-C does, the running turn of every session
// the adapter holds, and waits up to InterruptWait for the processes to end. A process
// is signalled only while it is the one recorded: the same pid with another start key
// is another program. Only the recorded turn process is known by identity; a turn
// process the adapter has not seen is not looked for. Attached sessions are the
// person's and are never touched.
func (a *App) interruptRunningTurns(ctx context.Context) error {
	a.mu.Lock()
	sessions := slices.Collect(maps.Values(a.sessions))
	a.mu.Unlock()

	var signalled []turn.Process
	var failures []string
	for _, s := range sessions {
		s.mu.Lock()
		t := s.turn
		running := t != nil && !t.Done()
		var p turn.Process
		if running {
			p = t.Process
		}
		s.mu.Unlock()
		if !running {
			continue
		}
		// An interrupt or a close of the session may be on its way to the same
		// process; interruptProcess is called only by the holder of the lock.
		s.interruptMu.Lock()
		alive, err := a.alive(p)
		if err == nil && alive {
			if err = a.interruptProcess(ctx, s, p); err == nil {
				signalled = append(signalled, p)
			}
		}
		s.interruptMu.Unlock()
		if err != nil && !errors.Is(err, proc.ErrNotRunning) {
			a.log.Warn("a turn cannot be interrupted at retire", "endpoint", s.endpoint, "error", err)
			failures = append(failures, fmt.Sprintf("session %s: %v", s.endpoint, err))
		}
	}
	gone, err := a.waitEnded(ctx, signalled, a.o.InterruptWait)
	switch {
	case err != nil:
		failures = append(failures, "the processes cannot be looked at: "+err.Error())
	case !gone:
		failures = append(failures, "a turn process did not end after the interrupt")
	}
	if len(failures) > 0 {
		return peer.NewError(peer.CodeInternalError, "turns were left running: "+strings.Join(failures, "; "))
	}
	return nil
}

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
