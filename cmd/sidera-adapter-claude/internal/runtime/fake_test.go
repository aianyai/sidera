package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/record"
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/runtime/proc"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// world is the whole outside of the adapter, in memory: the process table, the
// launcher, the file system, the programs it runs and the daemon. Every part of
// the runtime reaches the world through an interface, so a flow runs against it
// as against the real one, without a process or a Claude.
type world struct {
	t  *testing.T
	mu sync.Mutex

	files map[string][]byte
	procs map[int]*fakeProc
	next  int

	launches  []proc.Launch
	signals   []int
	puts      []string // "scope/id" in order
	checkpts  map[string]json.RawMessage
	events    []string
	order     []string // checkpoint puts and events, in the order they happened
	failPutIf func(scope adapterproto.CheckpointScope, id string, n int) bool
	putCount  int

	// onLaunch runs after a process was started, with the world unlocked.
	onLaunch func(w *world, p *fakeProc, l proc.Launch)
	// unidentified makes Start report a process it did start as one whose
	// identity cannot be read.
	unidentified bool
	// afterOpen runs after a file was opened and its content taken, with the
	// world unlocked: what happens in the world just after a read.
	afterOpen func(w *world, path string)
	// onInterrupt runs when a process is interrupted.
	onInterrupt func(w *world, p *fakeProc)
	// interruptErr is what Interrupt answers; the signal is counted all the same.
	interruptErr error
	// atSignal runs in Interrupt, with the world unlocked, before it answers: what
	// the world holds at the moment a signal is sent.
	atSignal func(w *world)
	loggedIn bool
	// detachment is what Start and Host report.
	detachment proc.Detachment

	// inbox: what was written to inbox sockets, and what the next write does.
	inbox     []inboxWrite
	inboxErr  error
	lookupErr error
	// lists counts the listings of the process table.
	lists int
	notes []string // the notices about attached sessions, in order
	avail []adapterproto.AvailabilityParams
}

// inboxWrite is one write to an inbox socket.
type inboxWrite struct {
	socket string
	data   string
}

func (w *world) Write(_ context.Context, socket string, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inboxErr != nil {
		return w.inboxErr
	}
	w.inbox = append(w.inbox, inboxWrite{socket, string(data)})
	return nil
}

func (w *world) inboxWrites() []inboxWrite {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.inbox)
}

// The accessors below are the only way a test reads or changes what the world
// holds while the adapter's goroutines run: every field behind w.mu is touched
// under it, so a monitor that writes a checkpoint never races a test that reads it.

// locked runs f with the world locked, for a change of several fields that a
// reader must see together or not at all.
func (w *world) locked(f func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f()
}

// setInboxErr sets the error the next writes to an inbox socket return.
func (w *world) setInboxErr(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inboxErr = err
}

// setLookupErr sets the error a process lookup returns.
func (w *world) setLookupErr(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lookupErr = err
}

// setUnidentified sets whether Start reports the process it started as one whose
// identity cannot be read.
func (w *world) setUnidentified(v bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.unidentified = v
}

// setLoggedIn sets whether the Claude program reports a logged-in account.
func (w *world) setLoggedIn(v bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.loggedIn = v
}

// file is a copy of the content of a file, nil when there is none.
func (w *world) file(path string) []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.files[path])
}

// setFile replaces the content of a file.
func (w *world) setFile(path string, data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.files[path] = slices.Clone(data)
}

// removeFile deletes a file.
func (w *world) removeFile(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.files, path)
}

// checkpoint is the state stored for "scope/id", and whether any was.
func (w *world) checkpoint(key string) (json.RawMessage, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	state, ok := w.checkpts[key]
	return slices.Clone(state), ok
}

// checkpoints is a copy of every state stored, by "scope/id".
func (w *world) checkpoints() map[string]json.RawMessage {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.checkpts)
}

// putKeys lists the checkpoint puts, as "scope/id", in order.
func (w *world) putKeys() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.puts)
}

// orderList lists the checkpoint puts and the events in the order they happened.
func (w *world) orderList() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.order)
}

// launched lists the launches, in order.
func (w *world) launched() []proc.Launch {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.launches)
}

// signalled lists the processes that were interrupted, in order.
func (w *world) signalled() []int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.signals)
}

// procByPID is the process with the pid, nil when there is none.
func (w *world) procByPID(pid int) *fakeProc {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.procs[pid]
}

// isAlive reports whether the process with the pid runs.
func (w *world) isAlive(pid int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.procs[pid].alive
}

// setAlive sets whether the process with the pid runs.
func (w *world) setAlive(pid int, alive bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.procs[pid].alive = alive
}

type fakeProc struct {
	pid       int
	args      []string
	env       []string
	startUnix int64
	alive     bool
	launch    proc.Launch
}

func newWorld(t *testing.T) *world {
	return &world{t: t, files: map[string][]byte{}, procs: map[int]*fakeProc{}, next: 1000, checkpts: map[string]json.RawMessage{}, loggedIn: true, detachment: proc.Detachment{OutlivesDaemon: true}}
}

// --- proc.Table, proc.Launcher, proc.Signaller ---

func (w *world) info(p *fakeProc) proc.Info {
	return proc.Info{
		Identity:    proc.Identity{PID: p.pid, StartKey: fmt.Sprintf("%d.%06d", p.startUnix, p.pid)},
		StartUnix:   p.startUnix,
		NativeStart: nativeStart(p.startUnix, p.pid),
	}
}

// nativeStart is the native start the fake table reports for a process, in the
// form of the platform the tests run on (see procStart).
func nativeStart(startUnix int64, pid int) string {
	if goruntime.GOOS == "darwin" {
		return fmt.Sprintf("%d.%06d", startUnix, pid)
	}
	return strconv.FormatInt(startUnix*100+int64(pid), 10)
}

func (w *world) Lookup(pid int) (proc.Info, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lookupErr != nil {
		return proc.Info{}, false, w.lookupErr
	}
	if p, ok := w.procs[pid]; ok && p.alive {
		return w.info(p), true, nil
	}
	return proc.Info{}, false, nil
}

func (w *world) Running(id proc.Identity) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lookupErr != nil {
		return false, w.lookupErr
	}
	p, ok := w.procs[id.PID]
	return ok && p.alive && w.info(p).StartKey == id.StartKey, nil
}

func (w *world) List() ([]proc.Entry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lists++
	var s []proc.Entry
	for _, p := range w.procs {
		if p.alive {
			s = append(s, proc.Entry{Info: w.info(p), Args: p.args})
		}
	}
	return s, nil
}

func (w *world) Start(l proc.Launch) (proc.Started, error) {
	w.mu.Lock()
	w.next++
	p := &fakeProc{pid: w.next, args: slices.Clone(l.Argv), env: slices.Clone(l.Env), startUnix: time.Now().Unix(), alive: true, launch: l}
	w.procs[p.pid] = p
	w.launches = append(w.launches, l)
	hook := w.onLaunch
	info := w.info(p)
	unidentified, detachment := w.unidentified, w.detachment
	w.mu.Unlock()
	if hook != nil {
		go hook(w, p, l)
	}
	if unidentified {
		return proc.Started{}, fmt.Errorf("%w: process %d", proc.ErrStartedUnidentified, p.pid)
	}
	return proc.Started{Info: info, Container: adapterproto.Container{Kind: adapterproto.ContainerProcessGroup, Name: strconv.Itoa(p.pid)}, Detachment: detachment, Done: make(chan error)}, nil
}

func (w *world) Host() proc.Detachment {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.detachment
}

func (w *world) Interrupt(_ context.Context, id proc.Identity) error {
	pid := id.PID
	w.mu.Lock()
	w.signals = append(w.signals, pid)
	p, hook, err, at := w.procs[pid], w.onInterrupt, w.interruptErr, w.atSignal
	w.mu.Unlock()
	if at != nil {
		at(w)
	}
	if err != nil {
		return err
	}
	if hook != nil && p != nil {
		go hook(w, p)
	}
	return nil
}

// exit ends a process; its output stream is what it wrote.
func (w *world) exit(p *fakeProc, out string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p.alive = false
	w.files[p.launch.Stdout] = []byte(out)
}

func (w *world) aliveCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, p := range w.procs {
		if p.alive {
			n++
		}
	}
	return n
}

// --- Files ---

func (w *world) Open(path string, offset int64) (io.ReadCloser, error) {
	w.mu.Lock()
	b, ok := w.files[path]
	if !ok {
		w.mu.Unlock()
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
	}
	if int(offset) > len(b) {
		offset = int64(len(b))
	}
	content := slices.Clone(b[offset:])
	hook := w.afterOpen
	w.mu.Unlock()
	if hook != nil {
		hook(w, path)
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func (w *world) ListDir(path string) ([]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var names []string
	for p := range w.files {
		if filepath.Dir(p) == path {
			names = append(names, filepath.Base(p))
		}
	}
	if names == nil {
		return nil, &fs.PathError{Op: "readdir", Path: path, Err: fs.ErrNotExist}
	}
	return names, nil
}

func (w *world) WriteFile(path string, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.files[path] = slices.Clone(data)
	return nil
}

func (w *world) RealDir(path string) (string, error) {
	if strings.Contains(path, "missing") {
		return "", &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
	}
	return path, nil
}

// appendRecord adds lines to a session file.
func (w *world) appendRecord(path string, lines ...string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, l := range lines {
		w.files[path] = append(w.files[path], l+"\n"...)
	}
}

// --- Runner ---

func (w *world) Run(_ context.Context, c Command) (CommandResult, error) {
	switch {
	case slices.Equal(c.Argv[1:], []string{"--version"}):
		return CommandResult{Stdout: []byte("2.1.316 (Claude Code)\n")}, nil
	case slices.Equal(c.Argv[1:], []string{"auth", "status"}):
		if !w.loggedIn {
			return CommandResult{Stdout: []byte(`{"loggedIn":false}`), ExitCode: 1}, nil
		}
		out, _ := json.Marshal(map[string]any{"loggedIn": true, "authMethod": "claude.ai", "projectsDirectory": projectsDir})
		return CommandResult{Stdout: out}, nil
	case slices.Equal(c.Argv[1:], []string{"probe"}):
		if !slices.Contains(c.Env, "SIDERA_PROBE_TOKEN="+token) {
			return CommandResult{ExitCode: 2, Stderr: []byte("no probe token")}, nil
		}
		return CommandResult{}, nil
	}
	return CommandResult{}, fmt.Errorf("unexpected command %q", c.Argv)
}

// --- Daemon ---

func (w *world) CheckpointPut(_ context.Context, p adapterproto.CheckpointPutParams) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.putCount++
	if w.failPutIf != nil && w.failPutIf(p.Scope, p.ID, w.putCount) {
		return errors.New("the daemon is gone")
	}
	key := string(p.Scope) + "/" + p.ID
	w.order = append(w.order, "put "+key)
	w.puts = append(w.puts, key)
	w.checkpts[key] = p.State
	return nil
}

func (w *world) event(format string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e := fmt.Sprintf(format, args...)
	w.events = append(w.events, e)
	w.order = append(w.order, e)
}

func (w *world) note(format string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.notes = append(w.notes, fmt.Sprintf(format, args...))
}

func (w *world) PromptPersisted(_ context.Context, p adapterproto.PromptPersistedParams) error {
	w.note("persisted %s", p.Attempt)
	w.event("persisted %s", p.Attempt)
	return nil
}

func (w *world) TurnStarted(_ context.Context, p adapterproto.TurnStartedParams) error {
	if err := p.Validate(); err != nil {
		w.t.Errorf("an invalid turn.started: %v", err)
	}
	w.note("started %s attempt=%s", p.Turn, p.Attempt)
	w.event("started %s", p.Attempt)
	return nil
}

func (w *world) TurnEnded(_ context.Context, p adapterproto.TurnEndedParams) error {
	if err := p.Validate(); err != nil {
		w.t.Errorf("an invalid turn.ended: %v", err)
	}
	w.note("ended %s %s", p.Turn, p.Final)
	w.event("ended %s %s", p.Turn, p.Final)
	return nil
}

func (w *world) Availability(_ context.Context, p adapterproto.AvailabilityParams) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.avail = append(w.avail, p)
	return nil
}

func (w *world) eventList() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.events)
}

// waitEvents waits until the daemon was told the events, in order.
func (w *world) waitEvents(want ...string) {
	w.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := w.eventList(); slices.Equal(got, want) {
			return
		} else if time.Now().After(deadline) {
			w.t.Fatalf("events\n got %q\nwant %q", got, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// --- the adapter built on the world ---

// absPath is an absolute path of this platform.
func absPath(elem ...string) string {
	return filepath.Join(append([]string{filepath.VolumeName(os.TempDir()) + string(filepath.Separator)}, elem...)...)
}

// exeSuffix is the extension of a native executable: Windows uses only those.
var exeSuffix = func() string {
	if hostOS == "windows" {
		return ".exe"
	}
	return ""
}()

var (
	claudeHome  = absPath("claude")
	projectsDir = filepath.Join(claudeHome, "projects")
	workDir     = absPath("work", "dir")
	recordDir   = func() string { d, _ := record.Dir(projectsDir, workDir); return d }()
	stateDir    = absPath("state")
	siderPath   = absPath("bin", "sidera")
	claudePath  = absPath("bin", "claude"+exeSuffix)
)

// newApp builds the adapter on the world and takes it through initialize and
// start with one preset, "dev". A test may call Start again for a restart.
func newApp(t *testing.T, w *world, sessions ...adapterproto.SessionAttach) (*App, adapterproto.StartResult) {
	t.Helper()
	app := New(Options{
		Environ: []string{"PATH=/usr/bin", "ANTHROPIC_API_KEY=leak", "SIDERA_TOKEN=leak"},
		LookPath: func(file string) (string, error) {
			switch file {
			case "claude":
				return claudePath, nil
			case "sidera":
				return siderPath, nil
			}
			return "", errors.New("not found")
		},
		Table: w, Launcher: w, Signaller: w, Files: w, Runner: w, Inbox: w, ClaudeDir: claudeHome,
		PollInterval: 2 * time.Millisecond, CloseWait: 2 * time.Second,
	})
	t.Cleanup(app.stop)
	app.daemon, app.instance = w, adapterproto.InstanceInfo{ID: "claude", LockPath: filepath.Join(stateDir, "lock"), StateDir: stateDir}
	return app, startApp(t, app, sessions...)
}

func startApp(t *testing.T, app *App, sessions ...adapterproto.SessionAttach) adapterproto.StartResult {
	t.Helper()
	raw := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	res, err := app.Start(context.Background(), adapterproto.StartParams{
		Node: "n", RuntimeEndpoint: "runtime", Config: adapterproto.ConfigValues{},
		Presets: []adapterproto.PresetConfig{
			{Name: "dev", Config: adapterproto.ConfigValues{
				"project_dir": raw(workDir), "permission_mode": raw("dontAsk"), "background_wait_ms": raw(1000),
			}},
			{Name: "choosy", Config: adapterproto.ConfigValues{
				"project_dir": raw(workDir), "permission_mode": raw("dontAsk"), "model": raw("sonnet"), "effort": raw("high"),
				"allowed_models": raw(map[string][]string{"sonnet": {"low", "high"}, "opus": {"high", "max"}}),
			}},
			{Name: "lean", Config: adapterproto.ConfigValues{
				"project_dir": raw(workDir), "permission_mode": raw("dontAsk"),
				"allowed_models": raw(map[string][]string{"opus": {"high"}}),
			}},
			{Name: "gone", Config: adapterproto.ConfigValues{"project_dir": raw(workDir + "-missing"), "permission_mode": raw("plan")}},
			{Name: "bad", Config: adapterproto.ConfigValues{"project_dir": raw(workDir), "permission_mode": raw("yolo")}},
		},
		Sessions: sessions, DirectEndpoints: []adapterproto.DirectAttach{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// ids are valid attempt identifiers: UUID version 4.
const (
	attempt1 = "11111111-1111-4111-8111-111111111111"
	prompt1  = "22222222-2222-4222-8222-222222222222"
	attempt2 = "33333333-3333-4333-8333-333333333333"
	prompt2  = "44444444-4444-4444-8444-444444444444"
)

// toolHome is SIDERA_HOME in the tool access of the sessions of these tests.
const toolHome = "/home/sidera"

const token = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func createParams(attempt, prompt string) adapterproto.SessionCreateParams {
	return adapterproto.SessionCreateParams{
		Attempt: adapterproto.AttemptID(attempt), RuntimeEndpoint: "runtime", SessionEndpoint: "session-1", Preset: "dev",
		FirstPrompt: adapterproto.FirstPrompt{Attempt: adapterproto.AttemptID(prompt), Text: "hello"},
		ToolAccess:  adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": token, "SIDERA_HOME": toolHome}},
	}
}

// userLine is the record line Claude Code writes for a prompt.
func userLine(session, prompt string) string {
	return `{"type":"user","uuid":"` + prompt + `","promptSource":"sdk","sessionId":"` + session + `"}`
}

func queueLine(session string) string {
	return `{"type":"queue-operation","operation":"enqueue","sessionId":"` + session + `"}`
}

const (
	resultOK    = `{"type":"result","subtype":"success","is_error":false}` + "\n"
	resultError = `{"type":"result","subtype":"error_during_execution","is_error":true}` + "\n"
)

// sessionOf reads the session uuid from the arguments of a launch.
func sessionOf(l proc.Launch) string {
	for i, a := range l.Argv {
		if (a == "--session-id" || a == "--resume") && i+1 < len(l.Argv) {
			return l.Argv[i+1]
		}
	}
	return ""
}

// claudeWrites is the behaviour of a Claude that takes its prompt: it writes the
// session file and runs until the test ends it.
func claudeWrites(delay time.Duration) func(*world, *fakeProc, proc.Launch) {
	return func(w *world, p *fakeProc, l proc.Launch) {
		time.Sleep(delay)
		var in struct {
			UUID string `json:"uuid"`
		}
		w.mu.Lock()
		raw := w.files[l.Stdin]
		w.mu.Unlock()
		_ = json.Unmarshal(raw, &in)
		session := sessionOf(l)
		w.appendRecord(filepath.Join(recordDir, session+".jsonl"), queueLine(session), userLine(session, in.UUID))
	}
}

// waitRecord waits until the session file holds the user record of the prompt.
func (w *world) waitRecord(session, prompt string) {
	w.t.Helper()
	path := filepath.Join(recordDir, session+".jsonl")
	deadline := time.Now().Add(5 * time.Second)
	for {
		w.mu.Lock()
		ok := bytes.Contains(w.files[path], []byte(`"uuid":"`+prompt+`"`))
		w.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("the prompt %s never reached the session file", prompt)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (w *world) launchCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.launches)
}

func (w *world) lastProc() *fakeProc {
	w.mu.Lock()
	defer w.mu.Unlock()
	var last *fakeProc
	for _, p := range w.procs {
		if last == nil || p.pid > last.pid {
			last = p
		}
	}
	return last
}
