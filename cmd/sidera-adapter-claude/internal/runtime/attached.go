package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sync"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/attached"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/reconcile"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// An attached session is a Claude Code session a person runs in a terminal and
// that asked to be attached. The adapter does not start it, interrupt it or
// close it: it reads the registry entry of its process and the session record
// to follow its turns, and delivers a prompt by writing a message to the
// session's inbox socket.

const (
	// maxRegistryBytes bounds a registry file read; a real one is about a kilobyte.
	maxRegistryBytes = 1 << 20
	// formErrorLimit is how many looks in a row may find the registry in a form
	// that is not read before the session is reported unavailable: a file read
	// while it is being written fails once, a file in another form keeps failing.
	formErrorLimit = 5
	// downEvery slows the looks at a session that cannot be reached to one in this many.
	downEvery = 25
	// sentMemory is how many delivered prompts a session remembers, for a repeat
	// of an attempt to be told from a new one.
	sentMemory = 128
)

// errNativeGone says the session is not running any more: its registry entry is
// gone, or its process is, or the number belongs to another process now.
var errNativeGone = errors.New("the native session is gone")

type sentPrompt struct {
	digest    string
	persisted bool
}

// attachedSession is one attached endpoint the adapter holds.
type attachedSession struct {
	endpoint  adapterproto.EndpointRef
	nativeRef adapterproto.NativeRef

	// mu guards everything below and orders the notifications of the session.
	mu sync.Mutex
	// wake is what was last given to deliver prompts by. It lives in memory only.
	wake attached.Wake
	// pid is the Claude Code process the registry entry is named by.
	pid int
	// sessionID is the session now running in the process; it changes at /clear.
	sessionID string
	// idx reads the transcript of sessionID, the file recordFile; nil when it
	// cannot be located.
	idx        *record.Index
	recordFile string
	follower   attached.Follower
	sent       map[string]*sentPrompt
	sentList   []string

	// down is why the registry or the process shows the session unreachable, and
	// sockDown why the inbox is; empty when they are fine.
	down, sockDown string
	formErrors     int
	polls          int
	unknownStatus  string
	cancel         context.CancelFunc
	// dropped is set once the endpoint is detached or its session replaced: nothing
	// of the session is looked at or reported after it.
	dropped bool
}

// attachedCheckpoint is what an attached session keeps with the daemon: the
// process its registry entry is named by, for a restart that comes without a
// wake. It holds nothing secret.
type attachedCheckpoint struct {
	V        int  `json:"v"`
	Attached bool `json:"attached"`
	PID      int  `json:"pid"`
}

func decodeAttachedCheckpoint(raw json.RawMessage) (attachedCheckpoint, error) {
	var c attachedCheckpoint
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("attached checkpoint: %w", err)
	}
	if c.V != checkpointVersion || !c.Attached || c.PID <= 0 {
		return c, errors.New("attached checkpoint: not usable")
	}
	return c, nil
}

func (a *App) attachedSession(endpoint adapterproto.EndpointRef) *attachedSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.attached[endpoint]
}

func wrongEndpointKind() error {
	return peer.NewError(adapterproto.CodeWrongEndpointKind, "the endpoint is an attached session: the person runs it")
}

// readRegistry reads the registry entry of a process. The error wraps
// errNativeGone for a process that has no entry, attached.ErrRegistryForm for an
// entry in a form that is not read, and is any other for what could not be
// looked at.
func (a *App) readRegistry(pid int) (attached.Registry, error) {
	if a.o.ClaudeDir == "" {
		return attached.Registry{}, errors.New("the configuration directory of Claude Code is not known")
	}
	rc, err := a.o.Files.Open(attached.RegistryFile(a.o.ClaudeDir, pid), 0)
	if errors.Is(err, fs.ErrNotExist) {
		return attached.Registry{}, fmt.Errorf("%w: no registry entry", errNativeGone)
	}
	if err != nil {
		return attached.Registry{}, fmt.Errorf("read the registry entry: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(rc, maxRegistryBytes))
	rc.Close()
	if err != nil {
		return attached.Registry{}, fmt.Errorf("read the registry entry: %w", err)
	}
	reg, err := attached.ParseRegistry(data)
	if err != nil {
		return attached.Registry{}, err
	}
	if reg.PID != pid {
		return attached.Registry{}, fmt.Errorf("%w: the entry names another process", attached.ErrRegistryForm)
	}
	return reg, nil
}

// checkProcess checks that the process a registry entry names runs and is the one
// that wrote the entry. The error wraps errNativeGone for a session that is not
// running, and is any other for what could not be looked at.
func (a *App) checkProcess(reg attached.Registry) error {
	info, found, err := a.o.Table.Lookup(reg.PID)
	if err != nil {
		return fmt.Errorf("look at the process: %w", err)
	}
	switch {
	case !found:
		return fmt.Errorf("%w: the process has ended", errNativeGone)
	case !reg.SameProcess(info.StartUnix, info.NativeStart):
		a.log.Warn("the registry entry of a session names a process start that is not the running process's: its number was reused, or the registry's form changed",
			"registry_start", reg.ProcStart, "process_start", info.NativeStart, "process_start_unix", info.StartUnix)
		return fmt.Errorf("%w: the process is not the one the entry was written by", errNativeGone)
	}
	return nil
}

// nativeSession reads the registry entry of a process and checks that the
// process runs. The error wraps errNativeGone for a session that is not there,
// attached.ErrRegistryForm for an entry in a form that is not read, and is any
// other for what could not be looked at.
func (a *App) nativeSession(pid int) (attached.Registry, error) {
	reg, err := a.readRegistry(pid)
	if err != nil {
		return attached.Registry{}, err
	}
	if err := a.checkProcess(reg); err != nil {
		return attached.Registry{}, err
	}
	return reg, nil
}

// errNoProcess says no process could be named for a session: the wake carries
// none and the registry does not show one.
var errNoProcess = errors.New("the registry does not name one process for the session")

// processOf names the Claude Code process of an attached session. The wake's
// process comes first, then known, the process the adapter already holds for the
// session. A wake without a process (the environment of Claude Code's MCP servers
// has none) is completed from the registry: the live process whose entry holds
// the session's id. The error wraps errNoProcess when the registry shows none or
// more than one, and is any other for what could not be looked at.
func (a *App) processOf(wake attached.Wake, known int, ref adapterproto.NativeRef) (int, error) {
	switch {
	case wake.PID != 0:
		return wake.PID, nil
	case known != 0:
		return known, nil
	}
	if a.o.ClaudeDir == "" {
		return 0, errors.New("the configuration directory of Claude Code is not known")
	}
	names, err := a.o.Files.ListDir(attached.RegistryDir(a.o.ClaudeDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("list the registry: %w", err)
	}
	var found []int
	unread := 0
	for _, name := range names {
		pid, ok := attached.RegistryPID(name)
		if !ok {
			continue
		}
		reg, err := a.readRegistry(pid)
		switch {
		case errors.Is(err, errNativeGone):
			continue
		case errors.Is(err, attached.ErrRegistryForm):
			unread++
			continue
		case err != nil:
			return 0, err
		case reg.SessionID != string(ref):
			continue
		}
		switch err := a.checkProcess(reg); {
		case errors.Is(err, errNativeGone):
		case err != nil:
			return 0, err
		default:
			found = append(found, pid)
		}
	}
	switch {
	case len(found) == 1:
		return found[0], nil
	case len(found) > 1:
		return 0, fmt.Errorf("%w: %d running processes hold the session id in their registry entries", errNoProcess, len(found))
	}
	if unread > 0 {
		a.log.Warn("registry entries in a form the adapter does not read were passed over while looking for a session", "count", unread)
	}
	return 0, fmt.Errorf("%w: no running process holds the session id in its registry entry", errNoProcess)
}

// AttachSession takes a session a person runs: it checks that the session is
// there, holds the wake and starts following its turns. It is idempotent: a
// repeat finds the session again and replaces the wake, so a wake that is
// missing from the repeat is dropped.
func (a *App) AttachSession(ctx context.Context, p adapterproto.SessionAttachParams) (adapterproto.SessionAttachResult, error) {
	if err := a.requireStarted(); err != nil {
		return adapterproto.SessionAttachResult{}, err
	}
	wake, err := attached.ParseWake(p.Wake)
	if err != nil {
		return attachFailed(adapterproto.AttachFailWakeUnavailable, err.Error()), nil
	}
	s := a.attachedSession(p.Endpoint)
	known := 0
	if s != nil {
		s.mu.Lock()
		known = s.pid
		s.mu.Unlock()
	}
	pid, err := a.processOf(wake, known, p.NativeRef)
	switch {
	case errors.Is(err, errNoProcess):
		return attachFailed(adapterproto.AttachFailWakeUnavailable,
			"the wake names no process ("+attached.WakePID+"), and "+err.Error()), nil
	case err != nil:
		a.log.Warn("the registry cannot be searched for a session", "endpoint", p.Endpoint, "error", err)
		return attachFailed(adapterproto.AttachFailNativeUnreachable, err.Error()), nil
	}

	reg, err := a.nativeSession(pid)
	switch {
	case errors.Is(err, errNativeGone):
		return adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachNotFound, Detail: err.Error()}, nil
	case errors.Is(err, attached.ErrRegistryForm):
		a.log.Error("the registry entry of a session is not in the form the adapter reads", "endpoint", p.Endpoint, "error", err)
		return attachFailed(adapterproto.AttachFailNativeError, err.Error()), nil
	case err != nil:
		a.log.Warn("the registry entry of a session cannot be looked at", "endpoint", p.Endpoint, "error", err)
		return attachFailed(adapterproto.AttachFailNativeUnreachable, err.Error()), nil
	case !reg.Interactive:
		return adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachNotFound, Detail: "the process is not an interactive session"}, nil
	}

	created := false
	a.mu.Lock()
	if s = a.attached[p.Endpoint]; s == nil {
		s = &attachedSession{endpoint: p.Endpoint, nativeRef: p.NativeRef, sent: map[string]*sentPrompt{}}
		a.attached[p.Endpoint] = s
		created = true
	}
	a.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	// The registry was just read: a session that was reported unreachable is back.
	s.formErrors = 0
	a.setDown(s, "")
	pidChanged, newSocket := s.pid != pid, wake.Socket != "" && wake.Socket != s.wake.Socket
	s.wake, s.pid = wake, pid
	if created {
		s.sessionID = reg.SessionID
		a.openTranscript(s, reg, true)
		ctx, cancel := context.WithCancel(a.life)
		s.cancel = cancel
		go a.monitorAttached(ctx, s)
	}
	if created || pidChanged {
		if err := a.putCheckpoint(ctx, adapterproto.ScopeSession, string(s.endpoint), attachedCheckpoint{V: checkpointVersion, Attached: true, PID: pid}); err != nil {
			a.log.Warn("the checkpoint of an attached session was not stored", "endpoint", s.endpoint, "error", err)
		}
	}
	if newSocket {
		s.sockDown = ""
		a.publishNative(s)
	}

	if detail := wake.Problem(hostOS); detail != "" {
		if wake.Socket == "" && !reg.Messaging {
			detail = "this Claude Code does not list support for messages between sessions: it is too old"
		}
		a.log.Warn("an attached session cannot be sent prompts", "endpoint", p.Endpoint, "detail", detail)
		return attachFailed(adapterproto.AttachFailWakeUnavailable, detail), nil
	}
	return adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachAttached}, nil
}

func attachFailed(reason adapterproto.SessionAttachFailure, detail string) adapterproto.SessionAttachResult {
	return adapterproto.SessionAttachResult{Outcome: adapterproto.SessionAttachFailed, Reason: reason, Detail: clip(detail)}
}

// SessionDetached stops following the session and drops its wake. The session
// is the person's and goes on.
func (a *App) SessionDetached(_ context.Context, p adapterproto.SessionDetachedParams) {
	a.mu.Lock()
	s := a.attached[p.Endpoint]
	delete(a.attached, p.Endpoint)
	a.mu.Unlock()
	if s != nil {
		a.dropAttached(s)
	}
}

// dropAttached ends the following of a session whose endpoint is gone and takes
// its condition back. It is all done under the session's lock: a look or a
// delivery that was in progress finishes first, and none that comes after reports
// anything, so the condition of a dropped endpoint cannot come back.
func (a *App) dropAttached(s *attachedSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.retireLocked(s)
	a.setNativeUnavailable(s.endpoint, "")
}

// retireLocked stops the following of a session and drops its wake. The caller
// holds s.mu.
func (a *App) retireLocked(s *attachedSession) {
	s.dropped = true
	if s.cancel != nil {
		s.cancel()
	}
	s.wake = attached.Wake{}
}

// resumeAttached takes an attached session back at adapter.start and fills
// state. The wake may be missing, as the daemon holds it in memory only; the
// session is then followed all the same and gets its wake from the next
// session.attach.
func (a *App) resumeAttached(at adapterproto.SessionAttach, state *adapterproto.SessionState) {
	wake, err := attached.ParseWake(at.Wake)
	if err != nil {
		a.log.Warn("the wake of an attached session is not usable", "endpoint", at.Endpoint, "error", err)
		wake = attached.Wake{}
	}
	known := 0
	if at.Checkpoint != nil {
		cp, err := decodeAttachedCheckpoint(at.Checkpoint)
		if err != nil {
			a.log.Warn("the checkpoint of an attached session is not usable", "endpoint", at.Endpoint, "error", err)
		}
		known = cp.PID
	}
	pid, err := a.processOf(wake, known, at.NativeRef)
	if err != nil {
		a.log.Warn("an attached session cannot be found again: neither its wake, its checkpoint nor the registry name its process", "endpoint", at.Endpoint, "error", err)
		return
	}
	reg, err := a.nativeSession(pid)
	switch {
	case errors.Is(err, errNativeGone):
		state.State = adapterproto.AttachLost
		return
	case err != nil:
		a.log.Warn("the registry entry of an attached session cannot be used", "endpoint", at.Endpoint, "error", err)
		return
	case !reg.Interactive:
		state.State = adapterproto.AttachLost
		return
	}

	s := &attachedSession{
		endpoint: at.Endpoint, nativeRef: at.NativeRef, wake: wake, pid: pid, sessionID: reg.SessionID,
		sent: map[string]*sentPrompt{},
	}
	a.openTranscript(s, reg, true)
	if reg.Status.Active() {
		s.follower.Adopt(reg.StatusAt)
		state.TurnActive = true
	}
	state.State = adapterproto.AttachAttached

	mctx, cancel := context.WithCancel(a.life)
	s.cancel = cancel
	a.mu.Lock()
	old := a.attached[at.Endpoint]
	a.attached[at.Endpoint] = s
	a.mu.Unlock()
	if old != nil {
		// The new session speaks for the endpoint; the old one must not.
		old.mu.Lock()
		a.retireLocked(old)
		old.mu.Unlock()
	}
	go a.monitorAttached(mctx, s)
}

// dropUnlistedAttached stops following the attached sessions that adapter.start
// no longer lists: their endpoints are closed, and the notice that said so may
// have been missed.
func (a *App) dropUnlistedAttached(listed []adapterproto.SessionAttach) {
	keep := map[adapterproto.EndpointRef]bool{}
	for _, l := range listed {
		keep[l.Endpoint] = bool(l.Attached)
	}
	a.mu.Lock()
	var dropped []*attachedSession
	for e, s := range a.attached {
		if !keep[e] {
			dropped = append(dropped, s)
			delete(a.attached, e)
		}
	}
	a.mu.Unlock()
	for _, s := range dropped {
		a.dropAttached(s)
	}
}

// openTranscript locates the transcript of the session now running and starts
// reading it. A session joined in progress (catchUp) has its history read once
// and passed over: only what is written after it is followed. The caller holds
// s.mu.
func (a *App) openTranscript(s *attachedSession, reg attached.Registry, catchUp bool) {
	s.idx = nil
	cwd, err := a.o.Files.RealDir(reg.Cwd)
	if err != nil {
		cwd = reg.Cwd
	}
	dir, err := record.Dir(filepath.Join(a.o.ClaudeDir, "projects"), cwd)
	if err != nil {
		a.log.Warn("the transcript of an attached session cannot be located: its turns are followed from the registry alone", "endpoint", s.endpoint, "error", err)
		return
	}
	ix := record.NewIndex(s.sessionID, s.follower.Expected()...)
	s.idx = ix
	s.recordFile = record.File(dir, s.sessionID)
	if catchUp {
		if _, err := a.scan(s.recordFile, ix); err != nil {
			a.log.Warn("the transcript of an attached session cannot be read", "endpoint", s.endpoint, "error", err)
		}
	}
	ix.Track()
	ix.Markers()
}

// monitorAttached follows the session until the endpoint is detached or the
// adapter ends.
func (a *App) monitorAttached(ctx context.Context, s *attachedSession) {
	ticker := time.NewTicker(a.o.PollInterval)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		if ctx.Err() == nil && !s.dropped {
			a.attachedTick(ctx, s)
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// attachedTick looks once at the session and sends what that calls for. The
// caller holds s.mu.
//
// The registry is read when the look begins, for what the process and the session
// id say, and read again after the transcript, for the status the turns are told
// by: a status read after the transcript is never older than the markers, while one
// read before it could show a turn that the markers have already ended.
func (a *App) attachedTick(ctx context.Context, s *attachedSession) {
	s.polls++
	if s.down != "" && s.polls%downEvery != 0 {
		return
	}
	reg, err := a.nativeSession(s.pid)
	switch {
	case errors.Is(err, errNativeGone):
		// A turn that was running ended with the session.
		a.finishAttached(ctx, s, adapterproto.TurnInterrupted)
		s.formErrors = 0
		a.setDown(s, "the session is gone: "+err.Error())
		return
	case errors.Is(err, attached.ErrRegistryForm):
		if s.formErrors++; s.formErrors == formErrorLimit {
			a.log.Error("the registry entry of an attached session is not in the form the adapter reads", "endpoint", s.endpoint, "error", err)
			a.setDown(s, err.Error())
		}
		return
	case err != nil:
		a.log.Warn("the registry entry of an attached session cannot be looked at", "endpoint", s.endpoint, "error", err)
		return
	}
	s.formErrors = 0
	a.setDown(s, "")

	if reg.SessionID != s.sessionID {
		// /clear: the process goes on with a new session and a new record file. What
		// the old file still shows is told before its turn ends with it.
		a.finishAttached(ctx, s, adapterproto.TurnInterrupted)
		s.sessionID = reg.SessionID
		a.openTranscript(s, reg, false)
	}

	var markers []record.Marker
	if s.idx != nil {
		if _, err := a.scan(s.recordFile, s.idx); err != nil {
			a.log.Warn("the transcript of an attached session cannot be read", "endpoint", s.endpoint, "error", err)
		}
		markers = s.idx.Markers()
	}
	// A registry that cannot be read again, or that now names another session (a
	// /clear that came during the look, which the next look takes up), leaves the
	// status of the first reading to go with the markers.
	if fresh, err := a.nativeSession(s.pid); err == nil && fresh.SessionID == s.sessionID {
		reg = fresh
	}
	if reg.Status == attached.StatusUnknown && s.unknownStatus != reg.StatusName {
		s.unknownStatus = reg.StatusName
		a.log.Warn("the registry entry of an attached session has a status the adapter does not know: it counts as no turn running", "endpoint", s.endpoint, "status", reg.StatusName)
	}
	events := s.follower.Step(attached.Observation{Status: reg.Status, StatusAt: reg.StatusAt, Now: a.o.Now(), Markers: markers})
	for _, ev := range events {
		if ev.Kind == attached.EventEnded && ev.Final == adapterproto.TurnInterrupted {
			a.log.Warn("a turn of an attached session ended without the runtime's own record of it: it is reported interrupted", "endpoint", s.endpoint)
		}
	}
	a.emitAttached(ctx, s, events)
}

// finishAttached ends the turn that is running with the session, which was cleared
// or is gone, after reading what its transcript still holds: a prompt or the end of
// a turn written just before is not lost. The caller holds s.mu.
func (a *App) finishAttached(ctx context.Context, s *attachedSession, final adapterproto.TurnFinal) {
	var markers []record.Marker
	if s.idx != nil {
		if _, err := a.scan(s.recordFile, s.idx); err != nil {
			a.log.Warn("the transcript of an attached session cannot be read", "endpoint", s.endpoint, "error", err)
		}
		markers = s.idx.Markers()
	}
	a.emitAttached(ctx, s, s.follower.Finish(markers, final))
}

// emitAttached sends the notifications of a step, in order. One that cannot be
// sent is logged and not repeated: the daemon learns a turn's state again from
// adapter.start and from reconciliation.
func (a *App) emitAttached(ctx context.Context, s *attachedSession, events []attached.Event) {
	d := a.conn()
	for _, ev := range events {
		var err error
		switch ev.Kind {
		case attached.EventPersisted:
			if p := s.sent[ev.Attempt]; p != nil {
				p.persisted = true
			}
			err = d.PromptPersisted(ctx, adapterproto.PromptPersistedParams{Endpoint: s.endpoint, Attempt: adapterproto.AttemptID(ev.Attempt)})
		case attached.EventStarted:
			err = d.TurnStarted(ctx, adapterproto.TurnStartedParams{
				Endpoint: s.endpoint, Turn: adapterproto.TurnRef(ev.Turn), Attempt: adapterproto.AttemptID(ev.Attempt), AtMS: a.nowMS(),
			})
		case attached.EventEnded:
			err = d.TurnEnded(ctx, adapterproto.TurnEndedParams{Endpoint: s.endpoint, Turn: adapterproto.TurnRef(ev.Turn), Final: ev.Final, AtMS: a.nowMS()})
		}
		if err != nil {
			a.log.Warn("a turn notification of an attached session was not sent", "endpoint", s.endpoint, "event", ev.Kind, "error", err)
		}
	}
}

// setDown records why the registry or the process shows the session
// unreachable, empty for reachable, and reports a change. The caller holds s.mu.
func (a *App) setDown(s *attachedSession, detail string) {
	if s.down == detail {
		return
	}
	s.down = detail
	a.publishNative(s)
}

// publishNative reports the session's unavailability: the registry's, else the
// inbox's. The caller holds s.mu.
func (a *App) publishNative(s *attachedSession) {
	if s.dropped {
		return
	}
	detail := s.down
	if detail == "" {
		detail = s.sockDown
	}
	a.setNativeUnavailable(s.endpoint, detail)
}

// deliverAttached puts a prompt into an attached session by writing it to the
// session's inbox socket, with the attempt as the uuid of the message. The
// session takes it at once when idle, and at its next tool boundary when busy;
// either way the prompt is delivered when its user record is in the transcript,
// which the monitor reports as prompt.persisted. The adapter never answers
// anything for the person's session: it only writes the message.
func (a *App) deliverAttached(ctx context.Context, s *attachedSession, p adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	attempt, sum := string(p.Attempt), digest(p.Text)
	if prev := s.sent[attempt]; prev != nil {
		if prev.digest != sum {
			return adapterproto.PromptDeliverResult{}, peer.NewError(adapterproto.CodeAttemptConflict, "the attempt was made with another text")
		}
		if prev.persisted {
			return adapterproto.PromptDeliverResult{Stage: adapterproto.PromptPersisted}, nil
		}
		return adapterproto.PromptDeliverResult{Stage: adapterproto.PromptAccepted}, nil
	}

	if _, err := a.nativeSession(s.pid); err != nil {
		a.log.Warn("a prompt cannot be delivered to an attached session", "endpoint", s.endpoint, "error", err)
		if errors.Is(err, errNativeGone) || errors.Is(err, attached.ErrRegistryForm) {
			a.setDown(s, err.Error())
		}
		return refused(adapterproto.RefusalNativeUnavailable)
	}
	// The registry was just read: a session that was reported unreachable is back,
	// and the next look need not wait for its slow turn to find out.
	s.formErrors = 0
	a.setDown(s, "")
	if problem := s.wake.Problem(hostOS); problem != "" {
		s.sockDown = problem + ": the next attachment brings it"
		a.publishNative(s)
		return refused(adapterproto.RefusalNativeUnavailable)
	}
	lines, err := s.wake.Lines(attempt, p.Text)
	if err != nil {
		return adapterproto.PromptDeliverResult{}, fmt.Errorf("form the message: %w", err)
	}

	// A checkpoint that was never stored is how a reconciliation proves that nothing was written.
	if err := a.putCheckpoint(ctx, adapterproto.ScopeAttempt, attempt, dispatchCheckpoint{V: checkpointVersion, Stage: "dispatching", Session: s.sessionID}); err != nil {
		return adapterproto.PromptDeliverResult{}, err
	}
	// The record can be written within milliseconds of the write: look for it first.
	s.follower.Expect(attempt)
	if s.idx != nil {
		s.idx.Watch(attempt)
	}
	err = a.o.Inbox.Write(ctx, s.wake.Socket, lines)
	switch {
	case errors.Is(err, ErrInboxUnreachable):
		s.follower.Forget(attempt)
		a.log.Warn("the inbox of an attached session cannot be connected", "endpoint", s.endpoint, "error", err)
		s.sockDown = "the session's inbox socket cannot be connected"
		a.publishNative(s)
		return refused(adapterproto.RefusalNativeUnavailable)
	case err != nil:
		// A connection was made: the message may have arrived, and the record decides.
		s.sent[attempt] = &sentPrompt{digest: sum}
		a.remember(s, attempt)
		return adapterproto.PromptDeliverResult{}, fmt.Errorf("a prompt was written to the inbox of an attached session and the outcome is unknown: %w", err)
	}
	s.sockDown = ""
	a.publishNative(s)
	s.sent[attempt] = &sentPrompt{digest: sum}
	a.remember(s, attempt)
	return adapterproto.PromptDeliverResult{Stage: adapterproto.PromptAccepted}, nil
}

// remember keeps the attempt in the bounded memory of delivered prompts, and
// forgets the oldest past the bound together with what follows its record: a
// prompt whose record never came is not waited for for ever. The caller holds s.mu.
func (a *App) remember(s *attachedSession, attempt string) {
	s.sentList = append(s.sentList, attempt)
	if len(s.sentList) > sentMemory {
		oldest := s.sentList[0]
		delete(s.sent, oldest)
		s.follower.Forget(oldest)
		if s.idx != nil {
			s.idx.Unwatch(oldest)
		}
		s.sentList = s.sentList[1:]
	}
}

// reconcileAttached says whether a prompt attempt reached the session's
// transcript, by the rules of package reconcile. Of the turn it started it can
// say that it ended (the runtime's own record of the end followed the prompt),
// runs (the registry shows a turn and none ended) or was interrupted (neither).
// Only the transcript of the session now running is read: a prompt written
// just before /clear is in the one before, and is then judged absent.
func (a *App) reconcileAttached(s *attachedSession, p adapterproto.ReconcileParams) (adapterproto.ReconcileResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	attempt := string(p.Attempt)
	in := reconcile.Input{Dispatched: p.Checkpoint != nil || s.sent[attempt] != nil}

	// The registry is read before the transcript: a session seen idle has written
	// everything it will until the next turn.
	reg, err := a.nativeSession(s.pid)
	active := err == nil && reg.Status.Active()
	in.SessionBusy = active || (err != nil && !errors.Is(err, errNativeGone))

	var markers []record.Marker
	ix := record.NewIndex(s.sessionID, attempt)
	ix.Track()
	if s.idx != nil {
		exists, err := a.scan(s.recordFile, ix)
		in.RecordReadable = exists && err == nil
		if err != nil {
			a.log.Warn("the transcript of an attached session cannot be read", "endpoint", s.endpoint, "error", err)
		}
		markers = ix.Markers()
	}
	in.RecordRecognized = ix.Recognized()
	in.InRecord = in.RecordReadable && ix.UserSeen(attempt)
	if in.InRecord {
		seen, ended := false, false
		for _, m := range markers {
			switch {
			case m.Kind == record.MarkPrompt && m.UUID == attempt:
				seen = true
			case m.Kind == record.MarkTurnEnd && seen:
				ended = true
			}
		}
		switch {
		case ended:
			in.Outcome = record.Succeeded
		case active:
			in.TurnAlive = true
		default:
			in.Interrupted = true
		}
	}

	v := reconcile.Decide(in)
	res := adapterproto.ReconcileResult{Verdict: v.Verdict, Grounds: adapterproto.Grounds{Code: v.Grounds}}
	if v.Verdict == adapterproto.VerdictFound {
		res.Turn = &adapterproto.TurnReport{Ref: adapterproto.TurnRef(attempt), State: v.Turn}
	}
	return res, nil
}
