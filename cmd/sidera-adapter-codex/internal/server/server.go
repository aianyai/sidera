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
	"encoding/base64"
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
)

// File names inside the state directory.
const (
	socketName    = "app-server.sock"
	logName       = "app-server.log"
	stateFileName = "app-server.json"
	tokenFileName = "ws-token"
)

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
	// what lets a restarted adapter find the server it left running.
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

	// The platform and the calls that reach outside the process, as fields so that
	// a test can stand in for them.
	goos         string
	probe        func(context.Context, appserver.Endpoint) error
	processStart func(pid int) (startedNS int64, running bool)
	retryWaits   []time.Duration
}

type ownedProcess struct {
	cmd    *exec.Cmd
	exited chan struct{}
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
	// StartedNS is when the process started, in nanoseconds since the Unix epoch,
	// where the platform can tell (Windows); zero otherwise. With the pid it
	// identifies the process.
	StartedNS int64 `json:"started_ns,omitempty"`
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
	if s.goos != "windows" {
		var err error
		if ep, err = s.unixEndpoint(); err != nil {
			return ep, err
		}
		args = append(args, "--listen", "unix://"+ep.Address)
	} else {
		var err error
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

	cmd, err := s.spawn(args, logFile)
	if err != nil {
		return ep, fmt.Errorf("%w: %v", ErrStartFailed, err)
	}
	owned := &ownedProcess{cmd: cmd, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(owned.exited)
	}()
	s.mu.Lock()
	s.owned = owned
	s.mu.Unlock()
	s.log.Info("started the App Server", "pid", cmd.Process.Pid, "listen", ep.Network)
	s.writeState(cmd.Process.Pid, ep)

	deadline := time.NewTimer(s.cfg.StartTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-owned.exited:
			return ep, fmt.Errorf("%w: it exited (%v) before it was reachable; an earlier App Server may hold the socket, see %s",
				ErrStartFailed, cmd.ProcessState, logName)
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

// spawn starts the server detached. Where the job the adapter runs in forbids
// breakaway (Windows), it starts without it and says so: the server then ends with
// the job.
func (s *Server) spawn(args []string, logFile *os.File) (*exec.Cmd, error) {
	cmd, err := s.spawnOnce(args, logFile, true)
	if err != nil && breakawayRefused(err) {
		s.log.Warn("the job the adapter runs in forbids breakaway: the App Server's life ends with the job")
		cmd, err = s.spawnOnce(args, logFile, false)
	}
	return cmd, err
}

func (s *Server) spawnOnce(args []string, logFile *os.File, breakaway bool) (*exec.Cmd, error) {
	cmd := exec.Command(s.cfg.Program, args...)
	cmd.Env = Environ(s.cfg.Environ)
	cmd.Dir = s.cfg.StateDir
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = detachAttr(breakaway)
	return cmd, cmd.Start()
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

func (s *Server) writeState(pid int, ep appserver.Endpoint) {
	started, _ := s.processStart(pid)
	raw, _ := json.Marshal(state{PID: pid, Address: ep.Address, StartedNS: started})
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
	pid := o.cmd.Process.Pid
	_ = signalGroup(o.cmd.Process, false)
	select {
	case <-o.exited:
	case <-time.After(termWait):
		_ = signalGroup(o.cmd.Process, true)
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
		_ = signalGroup(o.cmd.Process, true)
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
	return groupGone(o.cmd.Process.Pid)
}
