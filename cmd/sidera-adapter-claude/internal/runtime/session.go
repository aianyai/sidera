package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/claudeargs"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/record"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime/proc"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/turn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// session is an open session the adapter holds: a Claude session of the
// daemon's endpoint, the files it lives in and its last turn.
type session struct {
	endpoint  adapterproto.EndpointRef
	id        string // the Claude session uuid, the native reference
	preset    string
	cwd       string
	recordDir string
	// toolEnv is the channel access of the session. It lives in memory only:
	// the daemon sends it again with every request that needs it.
	toolEnv map[string]string

	// mu guards everything below and orders the notifications of the session.
	mu sync.Mutex
	// turn is the last turn, nil before the first.
	turn *turn.State
	// idx reads the session file for idxAttempt's prompt.
	idx        *record.Index
	idxAttempt string
	monitoring bool
	cancel     context.CancelFunc
}

func (s *session) file() string { return record.File(s.recordDir, s.id) }

func (a *App) session(endpoint adapterproto.EndpointRef) *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[endpoint]
}

// executables returns the programs a turn needs, as found when the daemon sent
// adapter.start.
func (a *App) executables() (claude, sidera, mcpConfig string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.claude, a.sidera, a.mcpConfig
}

// errDependencyMissing is a launch that cannot be made because a program the
// turn needs was not found.
type errDependencyMissing struct{ program string }

func (e errDependencyMissing) Error() string { return e.program + " was not found" }

// turnLaunch is one start of a turn process.
type turnLaunch struct {
	preset  resolvedPreset
	session string
	resume  bool
	attempt string // the prompt's uuid, also the name of the turn's files
	text    string
	toolEnv map[string]string
}

func (a *App) turnFile(session, attempt, kind string) string {
	a.mu.Lock()
	dir := a.instance.StateDir
	a.mu.Unlock()
	return filepath.Join(dir, "turns", session, attempt+"."+kind+".jsonl")
}

// launch writes the prompt to the turn's input file and starts the process.
func (a *App) launch(l turnLaunch) (proc.Started, error) {
	claude, sidera, mcpConfig := a.executables()
	switch {
	case claude == "":
		return proc.Started{}, errDependencyMissing{"claude"}
	case sidera == "":
		return proc.Started{}, errDependencyMissing{"sidera"}
	}
	line, err := claudeargs.UserLine(l.attempt, l.text)
	if err != nil {
		return proc.Started{}, err
	}
	argv, err := claudeargs.Turn{Claude: claude, SessionID: l.session, Resume: l.resume, MCPConfig: mcpConfig, Preset: l.preset.Preset}.Argv()
	if err != nil {
		return proc.Started{}, err
	}
	env := map[string]string{}
	for k, v := range l.toolEnv {
		env[k] = v
	}
	for k, v := range l.preset.Env() {
		env[k] = v
	}
	in := a.turnFile(l.session, l.attempt, "in")
	if err := a.o.Files.WriteFile(in, line); err != nil {
		return proc.Started{}, fmt.Errorf("write the prompt of the turn: %w", err)
	}
	return a.o.Launcher.Start(proc.Launch{
		Argv: argv, Env: claudeargs.Environ(a.o.Environ, env, filepath.Dir(sidera)), Dir: l.preset.Cwd,
		Stdin: in, Stdout: a.turnFile(l.session, l.attempt, "out"), Stderr: a.turnFile(l.session, l.attempt, "err"),
	})
}

// turnProcesses lists the turn processes of a session in a snapshot.
func (a *App) turnProcesses(snap proc.Snapshot, session string) []proc.Info {
	claude, _, _ := a.executables()
	var out []proc.Info
	for _, e := range snap {
		if e.Alive && claudeargs.IsTurnOf(e.Args, claude, session) {
			out = append(out, e.Info)
		}
	}
	return out
}

func processOf(i proc.Info) turn.Process {
	return turn.Process{PID: i.PID, StartUnix: i.StartUnix, StartKey: i.StartKey()}
}

// alive reports whether the process is still running: the PID held by a process
// that started when it did.
func (a *App) alive(p turn.Process) (bool, error) {
	if p.PID == 0 {
		// The process was never recorded and is gone: Decide only lets an attempt
		// become ready without a recorded process when none runs.
		return false, nil
	}
	info, err := a.o.Table.Lookup(p.PID)
	if err != nil {
		return false, err
	}
	return info.Alive && info.StartKey() == p.StartKey, nil
}

// identity describes a recorded process; nil for a turn whose process was never
// recorded.
func identity(p turn.Process) *adapterproto.ProcessIdentity {
	if p.PID == 0 {
		return nil
	}
	return &adapterproto.ProcessIdentity{
		PID: int64(p.PID), StartKey: p.StartKey, StartedAtMS: p.StartUnix * 1000,
		// A process the adapter started leads a process group of its own.
		Container: &adapterproto.Container{Kind: adapterproto.ContainerProcessGroup, Name: strconv.Itoa(p.PID)},
	}
}

// turnRef names a turn: the prompt attempt that caused it or, for a process the
// adapter did not start for a prompt, the process.
func turnRef(t turn.State) adapterproto.TurnRef {
	if t.Attempt != "" {
		return adapterproto.TurnRef(t.Attempt)
	}
	return adapterproto.TurnRef("process-" + strconv.Itoa(t.Process.PID) + "-" + t.Process.StartKey)
}

// adoptUnknown makes a turn of a process the adapter has no turn for: one
// started by an adapter that died before it recorded it. It is reported from the
// moment it is known, without a prompt.
func adoptUnknown(p turn.Process) *turn.State { return &turn.State{Process: p} }

// persistSession stores the session's checkpoint. The caller holds s.mu.
func (a *App) persistSession(ctx context.Context, s *session) error {
	return a.putCheckpoint(ctx, adapterproto.ScopeSession, string(s.endpoint), sessionCheckpoint{
		V: checkpointVersion, Session: s.id, Cwd: s.cwd, RecordDir: s.recordDir, Turn: s.turn,
	})
}

// scan reads the session file from the index's offset to its end. A file that
// is not there leaves the index as it is and is reported as false.
func (a *App) scan(path string, ix *record.Index) (exists bool, err error) {
	rc, err := a.o.Files.Open(path, ix.Offset())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer rc.Close()
	if _, err := io.Copy(ix, rc); err != nil {
		return true, err
	}
	return true, nil
}

// promptSeen reports whether the attempt's prompt is in the session file,
// reading only what was written since the last look. The caller holds s.mu.
func (a *App) promptSeen(s *session, attempt string) (bool, error) {
	if s.idx == nil || s.idxAttempt != attempt {
		s.idx, s.idxAttempt = record.NewIndex(s.id, attempt), attempt
	}
	if s.idx.UserSeen(attempt) {
		return true, nil
	}
	if _, err := a.scan(s.file(), s.idx); err != nil {
		return false, err
	}
	return s.idx.UserSeen(attempt), nil
}

// lastResult reads how the attempt's process ended from its output file. A
// missing or unreadable file is no result, which is a turn that failed.
func (a *App) lastResult(session, attempt string) record.Outcome {
	if attempt == "" {
		// A process the adapter did not start for a prompt has no output file of its own.
		return record.NoResult
	}
	rc, err := a.o.Files.Open(a.turnFile(session, attempt, "out"), 0)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			a.log.Warn("the output of a turn cannot be opened", "session", session, "attempt", attempt, "error", err)
		}
		return record.NoResult
	}
	defer rc.Close()
	out, err := record.LastResult(rc)
	if err != nil {
		a.log.Warn("the output of a turn cannot be read", "session", session, "attempt", attempt, "error", err)
		return record.NoResult
	}
	return out
}

// tick looks once at the session's last turn, sends what that calls for and
// persists the new state; it reports whether the turn is settled. The process is
// looked at before the record is read, so that a process seen as gone has written
// everything it will. The caller holds s.mu.
func (a *App) tick(ctx context.Context, s *session) (settled bool) {
	t := s.turn
	if t == nil || t.Done() {
		return true
	}
	alive, err := a.alive(t.Process)
	if err != nil {
		a.log.Warn("the process of a turn cannot be looked at", "endpoint", s.endpoint, "error", err)
		return false
	}
	// A process with no prompt of ours is reported as soon as it is known.
	seen := t.Attempt == ""
	if !seen {
		if seen, err = a.promptSeen(s, t.Attempt); err != nil {
			a.log.Warn("the session file cannot be read", "endpoint", s.endpoint, "error", err)
			return false
		}
	}
	obs := turn.Observation{Alive: alive, PromptSeen: seen}
	if !alive {
		obs.Outcome = a.lastResult(s.id, t.Attempt)
	}
	next, events := t.Step(obs)
	a.emit(ctx, s, next, events)
	s.turn = &next
	if err := a.persistSession(ctx, s); err != nil {
		a.log.Warn("the checkpoint of a session was not stored", "endpoint", s.endpoint, "error", err)
	}
	return next.Done()
}

// emit sends the notifications of a step, in order. A notification that cannot
// be sent is logged and not retried: the turn's state is persisted afterwards, and
// a restarted adapter sends again what a state shows may have been lost.
func (a *App) emit(ctx context.Context, s *session, t turn.State, events []turn.Event) {
	d := a.conn()
	for _, ev := range events {
		var err error
		switch ev.Kind {
		case turn.PromptPersisted:
			err = d.PromptPersisted(ctx, adapterproto.PromptPersistedParams{Endpoint: s.endpoint, Attempt: adapterproto.AttemptID(t.Attempt)})
		case turn.TurnStarted:
			err = d.TurnStarted(ctx, adapterproto.TurnStartedParams{
				Endpoint: s.endpoint, Turn: turnRef(t), Attempt: adapterproto.AttemptID(t.Attempt),
				Process: identity(t.Process), AtMS: a.nowMS(),
			})
		case turn.TurnEnded:
			err = d.TurnEnded(ctx, adapterproto.TurnEndedParams{Endpoint: s.endpoint, Turn: turnRef(t), Final: ev.Final, AtMS: a.nowMS()})
		}
		if err != nil {
			a.log.Warn("a turn notification was not sent", "endpoint", s.endpoint, "event", ev.Kind, "error", err)
		}
	}
}

// watch starts the goroutine that follows the session's last turn until it is
// settled, unless one runs. The caller holds s.mu.
func (a *App) watch(s *session) {
	if s.monitoring || s.turn == nil || s.turn.Done() {
		return
	}
	ctx, cancel := context.WithCancel(a.life)
	s.monitoring, s.cancel = true, cancel
	go a.monitor(ctx, s)
}

func (a *App) monitor(ctx context.Context, s *session) {
	ticker := time.NewTicker(a.o.PollInterval)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		settled := a.tick(ctx, s)
		if settled {
			s.monitoring = false
		}
		s.mu.Unlock()
		if settled {
			return
		}
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.monitoring = false
			s.mu.Unlock()
			return
		case <-ticker.C:
		}
	}
}

// forget drops a session from the table and stops its monitor.
func (a *App) forget(s *session) {
	a.mu.Lock()
	if a.sessions[s.endpoint] == s {
		delete(a.sessions, s.endpoint)
	}
	a.mu.Unlock()
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
}

// fileExists reports whether a file can be opened.
func (a *App) fileExists(path string) (bool, error) {
	rc, err := a.o.Files.Open(path, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, rc.Close()
}
