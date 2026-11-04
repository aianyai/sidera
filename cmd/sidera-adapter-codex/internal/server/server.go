// Package server runs the App Server the adapter talks to: it finds one that is
// already up or starts one that outlives the adapter, and ends only a server it can
// show, by the pid and start key in the state file, to be the one it started.
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
	"github.com/aianyai/sidera/sdk/go/adapterproto"
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
	// terminateGrace is how long a server asked to end may take before it is
	// killed.
	terminateGrace = 3 * time.Second
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

// ErrNotProven is wrapped by the error of a Terminate that left a server running
// because the state file cannot show that the process it names is the one that was
// started: an earlier adapter wrote the file with a start time that does not match
// the process, or none. Such a server is restarted by hand.
var ErrNotProven = errors.New("server: the state file does not prove which process is the App Server")

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

	mu sync.Mutex
	// exposure says why the server Connect last returned may let a sandboxed
	// command reach the state directory; empty when it cannot.
	exposure string

	// The platform and the calls that reach outside the process, as fields so that
	// a test can stand in for them.
	goos         string
	probe        func(context.Context, appserver.Endpoint) error
	running      func(process.Identity) (bool, error)
	lookup       func(pid int) (process.Process, bool, error)
	terminate    func(context.Context, process.Identity, time.Duration) error
	userCacheDir func() (string, error)
	retryWaits   []time.Duration
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
		running:      process.Running,
		lookup:       process.Lookup,
		terminate:    process.Terminate,
		userCacheDir: os.UserCacheDir,
		retryWaits:   defaultRetryWaits,
	}
}

// Exposure says why the server Connect last returned may let a sandboxed command
// reach the adapter's files, or is empty. Only a server an earlier adapter left
// running can: it was started in the state directory, before the server had a
// working directory of its own (see workDir). A server whose start the adapter
// cannot show is not ended by it, so this is for someone to restart it.
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
	if rec, err := s.recorded(); err == nil {
		s.migrate(rec)
	}
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
// could be anyone's by now, so the start recorded with it must match. A state file
// that records no start at all can only be checked for a running process. A
// process table that does not answer counts as not running, for the reason given
// at Terminate's opposite: starting a second server is visible and recoverable.
func (s *Server) recordedServer() (state, bool) {
	rec, err := s.recorded()
	if err != nil {
		return rec.state, false
	}
	return rec.state, rec.verdict == recordedOurs ||
		(rec.verdict == recordedUnproven && rec.state.StartKey == "" && rec.state.StartedNS == 0)
}

// verdict is what the state file and the process table together say about the
// recorded server.
type verdict int

const (
	// recordedGone: there is no record, or the process it names is not running, or
	// the pid is held by another process.
	recordedGone verdict = iota
	// recordedOurs: the record names the process that runs.
	recordedOurs
	// recordedUnproven: a process runs under the recorded pid but the record, which
	// an earlier adapter wrote with a start time and no start key, does not show it
	// is the one started.
	recordedUnproven
)

// record is the state file read against the process table.
type record struct {
	state   state
	verdict verdict
	// id names the process when the verdict is recordedOurs.
	id process.Identity
	// legacy is set when the record had no start key and the process was proven by
	// its start time alone, so the key is still to be written (see migrate).
	legacy bool
}

// recorded judges the state file against the process table. An error means the
// table did not answer, and never that the server is gone.
//
// A record with a start key is judged by the key. An older record has only the
// start time, which the SDK says never to use as identity (on Linux it moves with
// the clock): it names the same process only when it equals the start time the
// table reports exactly. On macOS and Windows both come from the same value of the
// kernel, so it always does; on Linux it does unless the clock was set after boot.
// When it does not, the record is unproven rather than guessed at: the process may
// be the server or another one that holds the pid.
func (s *Server) recorded() (record, error) {
	st, err := s.loadState()
	if err != nil || st.PID <= 0 {
		return record{state: st}, nil
	}
	if st.StartKey != "" {
		id := process.Identity{PID: st.PID, StartKey: st.StartKey}
		running, err := s.running(id)
		switch {
		case err != nil:
			return record{state: st}, err
		case running:
			return record{state: st, verdict: recordedOurs, id: id}, nil
		}
		return record{state: st}, nil
	}
	p, found, err := s.lookup(st.PID)
	switch {
	case err != nil:
		return record{state: st}, err
	case !found:
		return record{state: st}, nil
	case st.StartedNS != 0 && st.StartedNS == p.StartTime.UnixNano():
		return record{state: st, verdict: recordedOurs, id: p.Identity, legacy: true}, nil
	}
	return record{state: st, verdict: recordedUnproven}, nil
}

// migrate writes the start key of a record that was proven by its start time, so
// that the record is judged by the key from now on. It replaces the file whole, so
// a crash leaves the old record or the new.
func (s *Server) migrate(rec record) {
	if !rec.legacy {
		return
	}
	rec.state.StartKey = rec.id.StartKey
	if err := s.storeState(rec.state); err != nil {
		s.log.Warn("cannot add the start key to the state file", "error", err)
		return
	}
	s.log.Info("recognised the App Server an earlier adapter started", "pid", rec.id.PID, "start_key", rec.id.StartKey)
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
	// StartKey is the SDK's start key of the process; with the pid it identifies the
	// process. Empty when it could not be read, and in a file an earlier adapter
	// wrote.
	StartKey string `json:"start_key,omitempty"`
	// Container is where the process was put so that it outlives the adapter.
	Container adapterproto.Container `json:"container,omitzero"`
	// StartedNS is when the process started, in nanoseconds since the Unix epoch;
	// zero when it could not be read. It is written for the log and for the earlier
	// adapter that reads it, and is used for nothing but to recognise a file that has
	// no StartKey (see recorded).
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
		// The server runs; only its identity could not be read, so the state file
		// records none and a check of the recorded server tests the pid alone.
		s.log.Warn("started the App Server but could not identify it", "error", err)
	}
	exited := make(chan struct{}) // closed once the server has ended and been reaped
	var exitErr error             // how it ended; read only after exited is closed
	go func() {
		exitErr = <-started.Done
		close(exited)
	}()
	s.mu.Lock()
	s.exposure = ""
	s.mu.Unlock()
	s.log.Info("started the App Server", "pid", started.PID, "listen", ep.Network, "dir", workDir)
	if !started.Detachment.OutlivesDaemon {
		s.log.Warn("the App Server's life ends with the adapter and the daemon")
	}
	if started.Detachment.Caveat != "" {
		s.log.Info("how the App Server is detached", "detail", started.Detachment.Caveat)
	}
	previous, readErr := os.ReadFile(filepath.Join(s.cfg.StateDir, stateFileName))
	s.writeState(started, ep, workDir)

	deadline := time.NewTimer(s.cfg.StartTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-exited:
			s.restoreState(previous, readErr == nil)
			return ep, fmt.Errorf("%w: it exited (%s) before it was reachable; an earlier App Server may hold the socket, see %s",
				ErrStartFailed, exitStatus(exitErr), logName)
		case <-ctx.Done():
			return ep, ctx.Err()
		case <-deadline.C:
			s.endUnreachable(started)
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
	dir, err := s.workDirPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot make the directory to run the App Server in: %v", err)
	}
	return dir, nil
}

// workDirPath is where workDir puts the working directory.
func (s *Server) workDirPath() (string, error) {
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
	return filepath.Join(cache, workDirParent, filepath.Base(stateDir)+"-"+hex.EncodeToString(sum[:4])), nil
}

// RemoveWorkDir deletes the working directory the server runs in. It is the
// last thing an instance that is disabled or removed leaves outside its state
// directory, and the caller ends the server first: a directory taken from under
// a server that runs is not what the server was started in. A directory that
// is not there is not an error.
func (s *Server) RemoveWorkDir() error {
	dir, err := s.workDirPath()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("server: delete the App Server's working directory: %w", err)
	}
	return nil
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
	if err := s.storeState(state{
		PID: started.PID, Address: ep.Address, StartKey: started.StartKey, Container: started.Container,
		StartedNS: startedNS, WorkDir: workDir,
	}); err != nil {
		s.log.Warn("cannot write the state file", "error", err)
	}
}

// restoreState puts back the state file as it was before a start wrote its own,
// for a server that ended before it was reachable. Such a server lost the socket
// to an earlier one, still running, and stop, retire and replace find a server
// only through this file: left naming the dead process, it would hide the earlier
// one. With no earlier file there is nothing to put back and the new one goes.
func (s *Server) restoreState(previous []byte, existed bool) {
	path := filepath.Join(s.cfg.StateDir, stateFileName)
	if !existed {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.log.Warn("cannot remove the state file of the App Server that ended", "error", err)
		}
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, previous, 0o600); err != nil {
		s.log.Warn("cannot put back the state file", "error", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		s.log.Warn("cannot put back the state file", "error", err)
	}
}

// storeState replaces the state file whole.
func (s *Server) storeState(st state) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	path := filepath.Join(s.cfg.StateDir, stateFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
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

// Terminate ends the server the state file names, if the file and the process
// table show that the process is the one that was started, whichever adapter
// started it: the process group gets SIGTERM and, after a few seconds, SIGKILL (on
// Windows the process is ended and what it started is not reached). It returns once
// the server is gone. Nothing is done, and nil returned, when there is no record or
// the recorded process has ended.
//
// Where Connect takes a process table that does not answer for "not running", so as
// to start a server rather than wait for one that may not be there, Terminate takes
// it for "not to be touched": a wrong guess here ends someone else's process. So a
// table that does not answer is the error, and so is a record that cannot be shown
// to name the process (ErrNotProven), and in both the server is left running.
func (s *Server) Terminate(ctx context.Context) error {
	rec, err := s.recorded()
	if err != nil {
		s.log.Warn("the App Server was left running: the process table did not answer", "pid", rec.state.PID, "error", err)
		return fmt.Errorf("server: look up the App Server (pid %d): %w", rec.state.PID, err)
	}
	switch rec.verdict {
	case recordedGone:
		return nil
	case recordedUnproven:
		s.log.Warn("the App Server was left running: the state file does not show that the process is the one started", "pid", rec.state.PID)
		return fmt.Errorf("%w (pid %d); restart it by hand", ErrNotProven, rec.state.PID)
	}
	s.migrate(rec)
	return s.end(ctx, rec.id)
}

// end ends the server id names, politely first and then by force, and returns once
// it is gone; one that has ended already is not an error.
func (s *Server) end(ctx context.Context, id process.Identity) error {
	s.log.Info("ending the App Server", "pid", id.PID, "start_key", id.StartKey)
	if err := s.terminate(ctx, id, terminateGrace); err != nil && !errors.Is(err, process.ErrNotRunning) {
		return fmt.Errorf("server: end the App Server (pid %d): %w", id.PID, err)
	}
	return nil
}

// endUnreachable ends a server this call started that never answered. It is ended
// by the identity the start returned, not by the state file: the file may not have
// been written, and then it names no server or an earlier, gone one. When the
// identity could not be read, the process the start returned is killed.
func (s *Server) endUnreachable(started process.Started) {
	if started.StartKey == "" {
		_ = started.Kill()
		return
	}
	if err := s.end(context.Background(), started.Identity); err != nil {
		s.log.Warn("cannot end the App Server that did not answer", "error", err)
	}
}
