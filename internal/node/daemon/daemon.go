package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/logfile"
	"github.com/aianyai/sidera/internal/node/manageapi"
	"github.com/aianyai/sidera/internal/node/txn"
)

// ExitFatal is the exit code of a daemon that ended because of a failure of the
// disk after which memory and disk may disagree (EX_IOERR): the next start scans
// the disk again.
const ExitFatal = 74

// The ways a start or an init is refused. Each is wrapped with what is wrong and
// where, so a caller matches with errors.Is and shows the text.
var (
	// ErrAlreadyRunning: another daemon, or an init, holds the instance lock of the
	// data directory.
	ErrAlreadyRunning = errors.New("daemon: a daemon is already running on this data directory")
	// ErrAlreadyInitialized: init finished on this data directory; it is not made
	// again and nothing is changed.
	ErrAlreadyInitialized = errors.New("daemon: this data directory is already initialized")
	// ErrNotInitialized: there is no data directory that init finished; run init.
	ErrNotInitialized = errors.New("daemon: this data directory is not initialized; run `sidera init`")
	// ErrPermissions: the data directory is not a directory only its owner can
	// reach. Nothing is narrowed: the owner decides what becomes of it.
	ErrPermissions = errors.New("daemon: the data directory is not private to its owner")
)

// FatalError is how a daemon that ended because of a fatal failure of the disk
// reports it.
type FatalError struct{ Err error }

func (e *FatalError) Error() string {
	return "daemon: stopped by a failure of the disk: " + e.Err.Error()
}
func (e *FatalError) Unwrap() error { return e.Err }

// ExitCode is the exit code of a process whose daemon ended with err: 0 for a
// normal shutdown, ExitFatal for a fatal failure of the disk, 1 for anything else.
func ExitCode(err error) int {
	var fatal *FatalError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &fatal):
		return ExitFatal
	}
	return 1
}

// Config is what a daemon or an init needs.
type Config struct {
	// Home is the data directory. It is required.
	Home string
	// Clock is the time source; nil is the real clock.
	Clock clock.Clock
	// Rand is the source of randomness of the whole node: keys, tokens, names of
	// temporary files. Nil is crypto/rand, which this package is the only runtime
	// package to read. It is read by several goroutines and must be safe for
	// concurrent use.
	Rand io.Reader
	// FS is the file system under the data directory; nil is the real one rooted
	// at Home. A test passes a file system in memory, with the faults it injects.
	// The sockets are on the real disk whatever FS is.
	FS fsx.FS
	// Observer is told of every crash point of the transaction layer before its
	// step runs. Nil is none. It is the seam of the crash tests; the daemon never
	// installs one of its own.
	Observer txn.Observer
	// Engine are the parameters of the engine; zero fields take its defaults.
	Engine engine.Params
	// Attach is what the features that come after the start steps of this package
	// attach to it.
	Attach Attachments
}

func (c Config) withDefaults() Config {
	if c.Clock == nil {
		c.Clock = clock.System()
	}
	if c.Rand == nil {
		c.Rand = rand.Reader
	}
	return c
}

// Env is what a hook of the start is given: the parts of the node that are up at
// its step.
type Env struct {
	Home   string
	Clock  clock.Clock
	Rand   io.Reader
	Log    *slog.Logger
	Writer *txn.Writer
	Config *config.Config
	Keys   *keys.Keys
	Engine *engine.Engine
	// Logs is the handle for the log of an adapter instance
	// (`adapter-<instance>.log`), the same as the daemon's own.
	Logs *logfile.Handle
	// RequestStop asks the daemon to shut down in the normal way; it returns at once.
	RequestStop func(reason string)
}

// Attachments are the places where the features that the start steps of this
// package leave to others attach. A nil hook does nothing.
type Attachments struct {
	// TrustSync is step 7: the synchronisation of revocations and the trust graph,
	// and the gate of vouchers, which a node of one machine has no use for.
	TrustSync func(context.Context, *Env) error
	// AgentProbeOnly is step 9: open the Agent interface so that it takes only the
	// one-use probe token and answers anything else as unavailable.
	AgentProbeOnly func(context.Context, *Env) error
	// Adapters is step 10: start the adapter instances the owner enabled. It
	// returns when each has answered adapter.start or been judged offline, because
	// the recovery of deliveries that follows releases a lease only when no turn is
	// running and has to be told which turns run first.
	Adapters func(context.Context, *Env) error
	// AgentServe is step 15: the Agent interface serves everything.
	AgentServe func(context.Context, *Env) error
	// StopInterfaces is shutdown step 2, beside the management interface: stop
	// taking requests of the Agent interface, within the context's deadline.
	StopInterfaces func(context.Context) error
	// StopAdapters is shutdown step 3: stop the adapters, within the deadline.
	// Native sessions are separate processes and are not affected.
	StopAdapters func(context.Context) error
	// Periodic are tasks the engine runs on a period, besides the daemon's own.
	Periodic []PeriodicTask
}

// Daemon is a node that is running.
type Daemon struct {
	cfg  Config
	home string
	log  *slog.Logger

	fsys   fsx.FS
	lock   *instanceLock
	w      *txn.Writer
	logs   *logfile.Handle
	logOut *logfile.Log
	defer_ *deferredHandler

	loaded atomic.Pointer[config.Config] // set once the configuration is loaded
	cfgd   *config.Config
	keys   *keys.Keys
	engine *engine.Engine
	manage *manageapi.Server
	served chan error // Serve of the management interface has returned

	pausedSince time.Time // for the clock task, which runs in the loop
	clockHW     clock.HighWater
	clockPaused bool

	// what the shutdown has to undo: the hooks that began
	agentBegan, adaptersBegan bool

	stopOnce   sync.Once
	stopCh     chan struct{}
	stopReason string
	done       chan struct{}
	err        error
}

// Done is closed when the daemon has shut down, whatever the reason.
func (d *Daemon) Done() <-chan struct{} { return d.done }

// Wait waits for the shutdown to end and returns nil after a normal one, and a
// *FatalError after a fatal failure of the disk.
func (d *Daemon) Wait() error {
	<-d.done
	return d.err
}

// Engine is the engine of the running node, for the hooks and the tests.
func (d *Daemon) Engine() *engine.Engine { return d.engine }

// Home is the data directory, absolute and clean.
func (d *Daemon) Home() string { return d.home }

// RequestStop asks for the shutdown in the normal way. It is the one entry for
// every cause (the management interface, a signal, the program) and may be called
// from any goroutine any number of times: the shutdown runs once. It returns at
// once.
func (d *Daemon) RequestStop(reason string) {
	d.stopOnce.Do(func() {
		d.stopReason = reason
		close(d.stopCh)
	})
}

// Run starts the daemon, waits until the context ends or the daemon stops itself,
// shuts it down in the normal way and returns how it ended. The context of the
// process is the place to cancel on a signal; Run also takes SIGINT and SIGTERM
// where the system has them.
func Run(ctx context.Context, cfg Config) error {
	d, err := Start(ctx, cfg)
	if err != nil {
		return err
	}
	sig, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-sig.Done():
		d.RequestStop("signal")
	case <-d.Done():
	}
	return d.Wait()
}

func resolveHome(home string) (string, error) {
	if home == "" {
		return "", errors.New("daemon: the data directory is not set")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("daemon: the data directory %q: %w", home, err)
	}
	return filepath.Clean(abs), nil
}
