// Package server runs the App Server the adapter talks to: it finds one that is
// already up or starts one that outlives the adapter, and ends only a server this
// process started.
//
// The server is Codex's `app-server` run privately, not the daemon Codex manages
// for its own clients: that one is shared with the owner's other Codex programs,
// its updates interrupt running work and its version is not ours to fix.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/sdk/go/adapterkit/process"
)

// File names inside the state directory.
const (
	socketName    = "app-server.sock"
	logName       = "app-server.log"
	stateFileName = "app-server.json"
	tokenFileName = "ws-token"
)

// workDirParent is the directory, under the user's cache directory, that holds
// the working directory of each instance's App Server.
const workDirParent = "sidera-adapter-codex"

// maxSocketPath is the longest socket path every supported Unix accepts (macOS
// holds 104 bytes with the terminator).
const maxSocketPath = 100

// Timing of a start and of the end of a server.
const (
	defaultStartTimeout = 15 * time.Second
	termWait            = 3 * time.Second
	killWait            = 2 * time.Second
	// probeTimeout bounds one probe of a server that is known to be running.
	probeTimeout = 2 * time.Second
)

// defaultRetryWaits is the pause before each further probe of a server that is
// recorded as running but did not answer: a few attempts over a few seconds,
// enough for a server that is busy or still starting, short enough that a server
// which is wedged is reported while the caller's reconnect loop still has its turn.
var defaultRetryWaits = []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}

// ErrStartFailed is wrapped by errors from a start that did not end in a
// reachable server.
var ErrStartFailed = errors.New("server: the App Server did not start")

// Config says how to reach or start the server.
type Config struct {
	// Program is the Codex executable, a name looked up in PATH or a path.
	Program string
	// StateDir holds the socket, the log, the state file and, on Windows, the
	// token file. The same directory is used by every run of the adapter, which is
	// what lets a restarted adapter find the server it left running. It is never
	// the server's working directory (see workDir).
	StateDir string
	// Environ is the environment of the adapter, from which the server's is made;
	// nil means the process environment.
	Environ []string
	// StartTimeout is how long a started server may take to become reachable;
	// zero means fifteen seconds.
	StartTimeout time.Duration
	Logger       *slog.Logger
}

// Server finds or starts the App Server and remembers the one it started.
type Server struct {
	cfg Config
	log *slog.Logger

	mu    sync.Mutex
	owned *ownedProcess // the server this process started, if any
	// exposure says why the server Connect last returned may let a sandboxed
	// command reach the state directory; empty when it cannot.
	exposure string

	// The platform and the calls that reach outside the process, as fields so that
	// a test can stand in for them.
	goos         string
	probe        func(context.Context, appserver.Endpoint) error
	processStart func(pid int) (startedNS int64, running bool)
	userCacheDir func() (string, error)
	retryWaits   []time.Duration
}

// ownedProcess is the server this process started.
type ownedProcess struct {
	started process.Started
	exited  chan struct{} // closed once the server has ended and been reaped
	// exitErr is how it ended (nil for exit status 0); read only after exited is
	// closed.
	exitErr error
}

// processStart reports whether the process with the pid is running and when it
// started, in nanoseconds since the Unix epoch. A pid that names no process, or one
// the platform cannot be asked about, is not running as far as the adapter can
// tell: a wrong "not running" starts a second server, which shows and can be ended,
// where the same mistake about a session's writer would corrupt the session.
func processStart(pid int) (int64, bool) {
	p, found, err := process.Lookup(pid)
	if err != nil || !found {
		return 0, false
	}
	return p.StartTime.UnixNano(), true
}

// New returns a Server for the configuration.
func New(cfg Config) *Server {
	if cfg.StartTimeout == 0 {
		cfg.StartTimeout = defaultStartTimeout
	}
	if cfg.Environ == nil {
		cfg.Environ = os.Environ()
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{
		cfg:          cfg,
		log:          log,
		goos:         runtime.GOOS,
		probe:        appserver.Probe,
		processStart: processStart,
		userCacheDir: os.UserCacheDir,
		retryWaits:   defaultRetryWaits,
	}
}

// Started reports whether this process started the running server and still
// holds it, which is the only server Stop ends.
func (s *Server) Started() bool {
	o := s.ownedProcess()
	if o == nil {
		return false
	}
	select {
	case <-o.exited:
		return false
	default:
		return true
	}
}

func (s *Server) ownedProcess() *ownedProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.owned
}

// Exposure says why the server Connect last returned may let a sandboxed command
// reach the adapter's files, or is empty. Only a server an earlier adapter left
// running can: it was started in the state directory, before the server had a
// working directory of its own (see workDir). The adapter does not end a server
// it did not start, so this is for someone to restart it.
func (s *Server) Exposure() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exposure
}

// noteFound records whether a server that was found, not started, runs in a
// working directory of its own. Every start records its working directory in
// the state file; a server whose record has none was started in the state
// directory.
func (s *Server) noteFound() {
	exposure := ""
	if st, err := s.loadState(); err != nil || st.WorkDir == "" {
		exposure = "it was started by an earlier adapter in the state directory, where a sandboxed command can be given write access to the adapter's files: restart it"
		s.log.Warn("the App Server that answers runs in the state directory: restart it")
	}
	s.mu.Lock()
	s.exposure = exposure
	s.mu.Unlock()
}

// Connect returns an endpoint a server answers at: the one that is already
// listening, or the one it starts. Finding one that is up is the normal case after
// the adapter restarts, and the reason the server is started detached.
//
// On Windows nothing but this check keeps a second server from being started
// beside one that is running: a server recorded as running that fails to answer
// is waited for, never replaced, because the new server would take over the
// address and the token of the one that still holds threads. On Unix Codex takes a
// lock per socket path, so a second server for the same path cannot run and a
// failed probe may go straight to a start.
func (s *Server) Connect(ctx context.Context) (appserver.Endpoint, error) {
	ep, err := s.existing()
	if err == nil && s.probe(ctx, ep) == nil {
		s.noteFound()
		return ep, nil
	}
	if err == nil && s.goos == "windows" {
		if st, running := s.recordedServer(); running {
			return s.awaitRecorded(ctx, ep, st)
		}
	}
	return s.start(ctx)
}

// awaitRecorded probes a server that is running but did not answer again, with a
// short backoff. When it still does not answer the attempt fails, naming it; the
// caller's reconnect loop tries again later. A server that ends meanwhile is
// replaced.
func (s *Server) awaitRecorded(ctx context.Context, ep appserver.Endpoint, st state) (appserver.Endpoint, error) {
	for _, wait := range s.retryWaits {
		select {
		case <-ctx.Done():
			return ep, ctx.Err()
		case <-time.After(wait):
		}
		if _, running := s.recordedServer(); !running {
			return s.start(ctx)
		}
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		err := s.probe(pctx, ep)
		cancel()
		if err == nil {
			s.noteFound()
			return ep, nil
		}
	}
	return ep, fmt.Errorf("the App Server (pid %d) recorded in %s is running but does not answer at %s", st.PID, stateFileName, st.Address)
}

// recordedServer reads the state file and reports the server it names and
// whether that process is running and is the one that was started: a pid alone
// could be anyone's by now, so the process's start time recorded with it must
// match. A state file without a start time can only be checked for a running
// process.
func (s *Server) recordedServer() (state, bool) {
	st, err := s.loadState()
	if err != nil || st.PID <= 0 {
		return st, false
	}
	started, running := s.processStart(st.PID)
	return st, running && (st.StartedNS == 0 || st.StartedNS == started)
}

// existing is the endpoint a server left running would be at.
func (s *Server) existing() (appserver.Endpoint, error) {
	if s.goos != "windows" {
		return s.unixEndpoint()
	}
	return s.readState()
}

func (s *Server) unixEndpoint() (appserver.Endpoint, error) {
	path := filepath.Join(s.cfg.StateDir, socketName)
	if len(path) > maxSocketPath {
		return appserver.Endpoint{}, fmt.Errorf("the state directory path is too long for a Unix socket (%d bytes, at most %d): %s", len(path), maxSocketPath, path)
	}
	return appserver.Endpoint{Network: "unix", Address: path}, nil
}

// state is the file that tells a restarted adapter where the server listens when
// the address is not a fixed path.
type state struct {
	PID     int    `json:"pid"`
	Address string `json:"address"`
	// StartedNS is when the process started, in nanoseconds since the Unix epoch;
	// zero when it could not be read. With the pid it identifies the process. Only
	// Windows reads it back: on Unix the lock per socket path already keeps a second
	// server from running, and the start time there is derived from the boot time.
	StartedNS int64 `json:"started_ns,omitempty"`
	// WorkDir is the working directory the server was started in. A server an
	// earlier adapter started has none recorded: it ran in the state directory.
	WorkDir string `json:"work_dir,omitempty"`
}

func (s *Server) loadState() (state, error) {
	raw, err := os.ReadFile(filepath.Join(s.cfg.StateDir, stateFileName))
	if err != nil {
		return state{}, err
	}
	var st state
	if err := json.Unmarshal(raw, &st); err != nil || st.Address == "" {
		return state{}, errors.New("unreadable state file")
	}
	return st, nil
}

func (s *Server) readState() (appserver.Endpoint, error) {
	st, err := s.loadState()
	if err != nil {
		return appserver.Endpoint{}, err
	}
	token, err := os.ReadFile(filepath.Join(s.cfg.StateDir, tokenFileName))
	if err != nil {
		return appserver.Endpoint{}, err
	}
	return appserver.Endpoint{Network: "tcp", Address: st.Address, Token: string(token)}, nil
}

func (s *Server) start(ctx context.Context) (appserver.Endpoint, error) {
	args := []string{"--no-daemon", "app-server"}
	var ep appserver.Endpoint
	workDir, err := s.workDir()
	if err != nil {
		return ep, fmt.Errorf("%w: %v", ErrStartFailed, err)
	}
	if s.goos != "windows" {
		if ep, err = s.unixEndpoint(); err != nil {
			return ep, err
		}
		args = append(args, "--listen", "unix://"+ep.Address)
	} else {
		if ep, err = s.newTCPEndpoint(); err != nil {
			return ep, err
		}
		args = append(args, "--listen", "ws://"+ep.Address, "--ws-auth", "capability-token",
			"--ws-token-file", filepath.Join(s.cfg.StateDir, tokenFileName))
	}

	logFile, err := s.openLog()
	if err != nil {
		return ep, err
	}
	defer logFile.Close()

	program, err := s.program()
	if err != nil {
		return ep, fmt.Errorf("%w: %v", ErrStartFailed, err)
	}
	started, err := process.Start(process.Spec{
		Argv:   append([]string{program}, args...),
		Env:    Environ(s.cfg.Environ),
		Dir:    workDir,
		Stdout: logFile,
		Stderr: logFile,
	})
	if err != nil {
		if !errors.Is(err, process.ErrStartedUnidentified) {
			return ep, fmt.Errorf("%w: %v", ErrStartFailed, err)
		}
		// The server runs; only its start time could not be read, so the state file
		// records none and a check of the recorded server tests the pid alone.
		s.log.Warn("started the App Server but could not identify it", "error", err)
	}
	owned := &ownedProcess{started: started, exited: make(chan struct{})}
	go func() {
		owned.exitErr = <-started.Done
		close(owned.exited)
	}()
	s.mu.Lock()
	s.owned, s.exposure = owned, ""
	s.mu.Unlock()
	s.log.Info("started the App Server", "pid", started.PID, "listen", ep.Network, "dir", workDir)
	if !started.Detachment.OutlivesDaemon {
		s.log.Warn("the App Server's life ends with the adapter and the daemon")
	}
	if started.Detachment.Caveat != "" {
		s.log.Info("how the App Server is detached", "detail", started.Detachment.Caveat)
	}
	s.writeState(started, ep, workDir)

	deadline := time.NewTimer(s.cfg.StartTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-owned.exited:
			return ep, fmt.Errorf("%w: it exited (%s) before it was reachable; an earlier App Server may hold the socket, see %s",
				ErrStartFailed, exitStatus(owned.exitErr), logName)
		case <-ctx.Done():
			return ep, ctx.Err()
		case <-deadline.C:
			_ = s.Stop()
			return ep, fmt.Errorf("%w: not reachable after %v", ErrStartFailed, s.cfg.StartTimeout)
		case <-tick.C:
			pctx, cancel := context.WithTimeout(ctx, time.Second)
			err := s.probe(pctx, ep)
			cancel()
			if err == nil {
				return ep, nil
			}
		}
	}
}

// program is the absolute path of the Codex executable. process.Start runs its
// first argument as written, in the server's working directory, so a name that is
// looked up in PATH, or a path relative to the adapter's own directory, has to be
// resolved here, as the adapter's configuration allows both.
func (s *Server) program() (string, error) {
	path, err := exec.LookPath(s.cfg.Program)
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

// workDir is the working directory the server is started in: an empty directory
// of the adapter's own under the user's cache directory, one per state
// directory, so outside SIDERA_HOME.
//
// Codex derives a sandbox's writable roots from a working directory, and the
// server's own is the one it falls back to: `command/exec` takes it whatever cwd
// the request names, as does a thread started without one, and on Windows the
// elevated sandbox grants its accounts write access to it. The state directory
// holds the WebSocket token, with which a sandboxed command could reach the
// server and start a thread without a sandbox, so the server must never run
// there. An empty directory gives a sandbox nothing it can use; `/` or the home
// directory would give it everything. The adapter's threads and commands all
// name their own cwd, the preset's.
func (s *Server) workDir() (string, error) {
	cache, err := s.userCacheDir()
	if err != nil {
		return "", fmt.Errorf("no directory to run the App Server in: %v", err)
	}
	stateDir, err := filepath.Abs(s.cfg.StateDir)
	if err != nil {
		return "", err
	}
	// The instance's name says whose directory it is; the hash of the whole path
	// keeps the instances of two Sidera homes apart.
	sum := sha256.Sum256([]byte(stateDir))
	dir := filepath.Join(cache, workDirParent, filepath.Base(stateDir)+"-"+hex.EncodeToString(sum[:4]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot make the directory to run the App Server in: %v", err)
	}
	return dir, nil
}

// exitStatus says how a process ended, from the error of waiting for it.
func exitStatus(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

// openLog opens the server's log for appending, after moving the previous one
// aside, so that a log holds one run and does not grow without bound.
func (s *Server) openLog() (*os.File, error) {
	path := filepath.Join(s.cfg.StateDir, logName)
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		_ = os.Rename(path, path+".1")
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
}

func (s *Server) writeState(started process.Started, ep appserver.Endpoint, workDir string) {
	var startedNS int64 // zero when the start could not be read
	if !started.StartTime.IsZero() {
		startedNS = started.StartTime.UnixNano()
	}
	raw, _ := json.Marshal(state{PID: started.PID, Address: ep.Address, StartedNS: startedNS, WorkDir: workDir})
	if err := os.WriteFile(filepath.Join(s.cfg.StateDir, stateFileName), raw, 0o600); err != nil {
		s.log.Warn("cannot write the state file", "error", err)
	}
}

// newTCPEndpoint picks a free loopback port and makes the token the listener will
// ask for. The port is found by binding and releasing it, which a second process
// could take in between; the start then fails loudly and the next try picks again.
//
// The token file is written here, before the server exists, because the server
// reads it as it starts. It replaces the token of an earlier server, so it is
// reached only when Connect found no running server recorded: a running server's
// token is never overwritten.
func (s *Server) newTCPEndpoint() (appserver.Endpoint, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return appserver.Endpoint{}, err
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return appserver.Endpoint{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	if err := os.WriteFile(filepath.Join(s.cfg.StateDir, tokenFileName), []byte(token), 0o600); err != nil {
		return appserver.Endpoint{}, err
	}
	return appserver.Endpoint{Network: "tcp", Address: addr, Token: token}, nil
}

// Stop ends the server this process started: the whole process group, politely,
// then by force. A server another process started is never touched: a pid in a
// file could be anyone's now, and a wrong guess here would end someone else's
// process. It
// returns once the server's process group is gone.
func (s *Server) Stop() error {
	o := s.ownedProcess()
	if o == nil {
		return nil
	}
	select {
	case <-o.exited:
		return nil
	default:
	}
	pid := o.started.PID
	_ = signalGroup(o.started, false)
	select {
	case <-o.exited:
	case <-time.After(termWait):
		_ = signalGroup(o.started, true)
		select {
		case <-o.exited:
		case <-time.After(killWait):
			return fmt.Errorf("the App Server (pid %d) did not end", pid)
		}
	}
	// The leader is reaped; members of its group may still be dying.
	for i := 0; i < 20 && !groupGone(pid); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if !groupGone(pid) {
		_ = signalGroup(o.started, true)
	}
	return nil
}

// GroupGone reports whether the process group of the server that was started is
// empty; it is true when none was started.
func (s *Server) GroupGone() bool {
	o := s.ownedProcess()
	if o == nil {
		return true
	}
	return groupGone(o.started.PID)
}
