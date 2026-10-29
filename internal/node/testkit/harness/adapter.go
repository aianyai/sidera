package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/adapterhost"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/logfile"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent/engineagent"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// AdapterOptions give the fixture an adapter host, and with it a fake adapter for
// the instance Instance that runs in the test process: the node admits the
// instance the way the daemon does, through the host, and not by calling the
// engine's entry itself.
type AdapterOptions struct {
	// Host are the tunables of the host; a zero field takes the host's default.
	// The host runs on the fixture's test clock, so Advance moves its pings, its
	// backoff and its waits for a process to end.
	Host adapterhost.Params
	// Runtime gives the adapter a native runtime to drive, as a chat adapter that is
	// also a front end of agents has: it declares a runtime beside its direct
	// endpoints, so the node makes the runtime endpoint of the instance, and its
	// sessions are those of one fake runtime in the test process, which outlives
	// every process of the adapter, as a native process outlives the adapter that
	// started it. H.Runtime is its controller.
	Runtime bool
	// ExtraInstances are further adapter instances the node has besides Instance,
	// each installed, enabled and run the way Instance is: its own package, its
	// own fake adapter processes and controller, and, with Runtime, its own fake
	// runtime and runtime endpoint. They are for a test of what a node does when
	// one adapter is away while another serves. Each name must be a valid adapter
	// identifier, different from Instance and from the others. The accessors that
	// take an instance (Adapter, AdapterLaunches, RuntimeOf, RuntimeAddrOf,
	// DirectOn) reach them by name; the ones that take none (Direct, Runtime,
	// RuntimeAddr) stay on Instance. Options.Disk is used as it is, so a disk
	// handed in must already have these instances installed and enabled.
	ExtraInstances []string
}

// adapterProgram is the program of the installed package; no launcher runs it.
const adapterProgram = "bin/sidera-adapter-chat"

// AdapterManifest is what the fake adapter of the fixture declares: the adapter
// Instance at version 1.0.0, with direct endpoints only (a runtime needs a fake
// runtime and a session creation, which come later), at most maxDirect of them.
// The manifest the fixture installs and the capabilities the fake answers
// initialize with both come from it, so they cannot disagree.
func AdapterManifest(maxDirect int) fakeadapter.ManifestOptions {
	return AdapterManifestOf(Instance, maxDirect)
}

// AdapterManifestOf is AdapterManifest for the adapter of another name: what a
// further instance of the fixture (AdapterOptions.ExtraInstances) declares.
func AdapterManifestOf(instance string, maxDirect int) fakeadapter.ManifestOptions {
	return fakeadapter.ManifestOptions{
		ID: instance, Version: "1.0.0",
		Direct: true, MaxDirectEndpoints: maxDirect,
		Program: adapterProgram,
	}
}

// RuntimeAdapterManifest is AdapterManifest for an adapter that also declares a
// runtime: the command line and MCP front ends, interrupt and acknowledging on
// behalf, with the runtime and the tested range the fake adapter declares by
// default.
func RuntimeAdapterManifest(maxDirect int) fakeadapter.ManifestOptions {
	return RuntimeAdapterManifestOf(Instance, maxDirect)
}

// RuntimeAdapterManifestOf is RuntimeAdapterManifest for the adapter of another name.
func RuntimeAdapterManifestOf(instance string, maxDirect int) fakeadapter.ManifestOptions {
	o := AdapterManifestOf(instance, maxDirect)
	d := fakeadapter.DefaultManifestOptions()
	o.Runtime = true
	o.Frontends, o.Interrupt, o.AckOnBehalf = d.Frontends, d.Interrupt, d.AckOnBehalf
	o.RecvWaitS, o.Budgets = d.RecvWaitS, d.Budgets
	o.RuntimeName, o.RuntimeVersion, o.Tested, o.AckMarkers = d.RuntimeName, d.RuntimeVersion, d.Tested, d.AckMarkers
	return o
}

// LauncherConfig configures an AdapterLauncher.
type LauncherConfig struct {
	// T receives the failures of the launcher itself.
	T testing.TB
	// Ctx bounds every adapter the launcher starts.
	Ctx context.Context
	// Now is the shared test clock.
	Now func() time.Time
	// Ledgers is where the adapters write: one writer, adapter/<instance>, for
	// every process of an instance.
	Ledgers *ledger.Memory
	// Manifest is what each adapter declares.
	Manifest fakeadapter.ManifestOptions
	// Runtime is the native runtime the adapters drive; required when Manifest
	// declares a runtime, and shared by every process of the instance.
	Runtime fakeadapter.RuntimeLauncher
	// Instances, which may be nil, holds what an instance that is not like the
	// others declares and drives, by instance: an instance named here takes this
	// pair in place of Manifest and Runtime.
	Instances map[string]LauncherInstance
	// Arm, which may be nil, is called with the knobs of each adapter before it
	// starts, with the number of the launch of the instance (1 is the first): a
	// knob armed here is in place before the adapter answers anything.
	Arm func(instance string, n int, k *fakeadapter.Knobs)
	// Order, which may be nil, is the order the instances of a first boot are taken on
	// in: a launch of an instance waits until the latest process of the one before it
	// has been sent adapter.probe or ended. The engine probes after it has admitted the
	// instance and drawn what the admission needs from its random source. Without an
	// order the host starts the instances at once and the admissions reach the engine
	// in whichever order the processes answer, so each instance gets other identifiers
	// and other tokens from run to run of one seed.
	Order []string
}

// LauncherInstance is what the adapters of one instance declare and drive.
type LauncherInstance struct {
	Manifest fakeadapter.ManifestOptions
	// Runtime is the native runtime the instance's adapters drive; required when
	// Manifest declares a runtime.
	Runtime fakeadapter.RuntimeLauncher
}

// AdapterLauncher is an adapterhost.Launcher whose processes are fake adapters in
// the test process, over pipes. It keeps every launch, so a test can read what the
// daemon wrote to each one and turn its knobs, and it is the one implementation
// that the fixture and a test of the daemon share.
type AdapterLauncher struct {
	c LauncherConfig

	mu       sync.Mutex
	writers  map[string]*ledger.Writer
	launches map[string][]*AdapterLaunch
}

var _ adapterhost.Launcher = (*AdapterLauncher)(nil)

// NewAdapterLauncher returns a launcher that has started nothing.
func NewAdapterLauncher(c LauncherConfig) *AdapterLauncher {
	return &AdapterLauncher{c: c, writers: map[string]*ledger.Writer{}, launches: map[string][]*AdapterLaunch{}}
}

// Launch starts a fake adapter for the instance. Every launch has knobs of its own;
// the ledger writer of the instance is shared by all of them, because opening the
// same stream twice would fork its sequence.
func (l *AdapterLauncher) Launch(ctx context.Context, inst adapterhost.Instance) (adapterhost.Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.awaitPredecessor(ctx, inst.ID)
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.writers[inst.ID]
	if !ok {
		var err error
		if w, err = l.c.Ledgers.Open("adapter/"+inst.ID, 0, l.c.Now); err != nil {
			return nil, fmt.Errorf("harness: ledger writer of the adapter %s: %w", inst.ID, err)
		}
		l.writers[inst.ID] = w
	}
	n := len(l.launches[inst.ID]) + 1
	knobs := fakeadapter.NewKnobs()
	if l.c.Arm != nil {
		l.c.Arm(inst.ID, n, knobs)
	}
	launch := &AdapterLaunch{
		t: l.c.T, knobs: knobs, ctl: knobs.Controller(),
		endReq: make(chan struct{}), done: make(chan struct{}),
	}
	own := LauncherInstance{Manifest: l.c.Manifest, Runtime: l.c.Runtime}
	if o, ok := l.c.Instances[inst.ID]; ok {
		own = o
	}
	launch.proc = fakeadapter.StartPipe(l.c.Ctx, fakeadapter.Config{
		InstanceID: inst.ID, Now: l.c.Now, Ledger: w, Manifest: own.Manifest, Knobs: knobs, Runtime: own.Runtime,
	})
	l.launches[inst.ID] = append(l.launches[inst.ID], launch)
	go launch.watch()
	return &launchedProcess{launch}, nil
}

// adapterProbeWait bounds probe waits on the real clock. Predecessor ordering
// proceeds after this limit if an instance was deliberately kept away; startup
// instead fails rather than releasing a script before the probe's random draws.
const adapterProbeWait = 10 * time.Second

// awaitPredecessor holds the launch of an instance until the latest process of the one
// before it in Order has been probed or ended. After a restart the latest process of the
// predecessor is the one of the run before, which was probed long ago, so the restart
// of a node starts its instances at once.
func (l *AdapterLauncher) awaitPredecessor(ctx context.Context, id string) {
	i := slices.Index(l.c.Order, id)
	if i <= 0 {
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, adapterProbeWait)
	defer cancel()
	_ = l.awaitProbe(waitCtx, l.c.Order[i-1])
}

// probeState reports whether the latest launch has received its probe or ended,
// and counts launches so quiet detects a replacement between its two looks.
// Probe tokens are drawn before adapter.probe is written; until then an adapter
// can still consume the engine's seeded random stream ahead of the next script call.
func (l *AdapterLauncher) probeState(instance string) (bool, uint64) {
	ls := l.Launches(instance)
	if len(ls) == 0 {
		return false, 0
	}
	last := ls[len(ls)-1]
	return last.Ended() || slices.Contains(last.Methods(), adapterproto.MethodAdapterProbe), uint64(len(ls))
}

func (l *AdapterLauncher) awaitProbe(ctx context.Context, instance string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if settled, _ := l.probeState(instance); settled {
			return nil
		}
		poll := clock.System().NewTimer(quietPoll)
		select {
		case <-poll.C():
		case <-ctx.Done():
			poll.Stop()
			return ctx.Err()
		}
	}
}

// Launches are the adapter processes started for the instance, in the order they
// were started.
func (l *AdapterLauncher) Launches(instance string) []*AdapterLaunch {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*AdapterLaunch(nil), l.launches[instance]...)
}

func (l *AdapterLauncher) all() []*AdapterLaunch {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*AdapterLaunch
	for _, ls := range l.launches {
		out = append(out, ls...)
	}
	return out
}

// cutAll cuts the pipes of every process, as the death of the daemon does; a
// process that was told to survive is not ended by it.
func (l *AdapterLauncher) cutAll() {
	for _, launch := range l.all() {
		launch.cut()
	}
}

// endSurvivors really ends the processes that were told to survive: the host that
// would wait for them is about to be stopped, and none may outlive the test.
func (l *AdapterLauncher) endSurvivors() {
	for _, launch := range l.all() {
		if launch.survive.Load() {
			launch.End()
		}
	}
}

// Close really ends every process that is still running. It is for the end of a
// test, after the host that supervised them was stopped.
func (l *AdapterLauncher) Close() {
	for _, launch := range l.all() {
		launch.End()
	}
}

// endWait is how long, on the real clock, a fake adapter is given to end after it
// was cut.
const endWait = 10 * time.Second

// AdapterLaunch is one adapter process the host started.
type AdapterLaunch struct {
	t     testing.TB
	knobs *fakeadapter.Knobs
	ctl   fakeadapter.Controller
	proc  *fakeadapter.PipeProcess

	survive atomic.Bool
	endOnce sync.Once
	endReq  chan struct{} // closed by End
	done    chan struct{} // closed when the process has really ended

	streamOnce sync.Once
	in         io.Reader
	out        io.WriteCloser

	mu      sync.Mutex
	methods []string
}

// Controller turns the knobs of this process.
func (l *AdapterLaunch) Controller() fakeadapter.Controller { return l.ctl }

// Methods are the methods of the requests and notifications the daemon wrote to
// this process, in order. What it wrote in answer to the process's own requests has
// no method and is not among them.
func (l *AdapterLaunch) Methods() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.methods...)
}

// Survive makes this process one the host cannot end: from now on neither Kill nor
// closing its stream ends it, and Wait does not return until End. It is a hung
// process that holds its instance lock.
func (l *AdapterLaunch) Survive() { l.survive.Store(true) }

// End really ends the process, whatever Survive said, and returns when it has
// ended and released the instance lock.
func (l *AdapterLaunch) End() {
	l.endOnce.Do(func() { close(l.endReq) })
	_ = l.proc.Kill()
	l.waitEnded()
}

// Ended reports whether the process has ended.
func (l *AdapterLaunch) Ended() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// cut cuts the pipes of the process, which ends it unless it survives, and waits
// for it to end.
func (l *AdapterLaunch) cut() {
	if l.survive.Load() {
		return
	}
	_ = l.proc.Kill()
	l.waitEnded()
}

func (l *AdapterLaunch) waitEnded() {
	timer := clock.System().NewTimer(endWait)
	defer timer.Stop()
	select {
	case <-l.done:
	case <-timer.C():
		l.t.Errorf("harness: a fake adapter did not end within %v", endWait)
	}
}

// watch closes done when the process has ended. One that was told to survive ends
// only when End says so, even if its connection is over.
func (l *AdapterLaunch) watch() {
	_ = l.proc.Wait()
	if l.survive.Load() {
		<-l.endReq
	}
	close(l.done)
}

func (l *AdapterLaunch) wrote(line []byte) {
	var m struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(line, &m) != nil || m.Method == "" {
		return
	}
	l.mu.Lock()
	l.methods = append(l.methods, m.Method)
	l.mu.Unlock()
}

// launchedProcess is the adapterhost.Process of a launch: the fake's own, except
// for what the launch was told about surviving, and with what the daemon writes
// noted.
type launchedProcess struct{ l *AdapterLaunch }

func (p *launchedProcess) Stream() (io.Reader, io.WriteCloser) {
	l := p.l
	l.streamOnce.Do(func() {
		r, w := l.proc.Stream()
		hostIn, hostW := io.Pipe()
		go l.relay(r, hostW)
		l.in, l.out = &hostReader{l: l, pr: hostIn, inner: r}, &tappedStream{l: l, w: w}
	})
	return l.in, l.out
}

// errCannotEnd is what Kill says of a process that was told to survive: the host
// is told that the process cannot be ended, as an operating system tells a process
// it may not signal.
var errCannotEnd = errors.New("harness: the process cannot be ended")

func (p *launchedProcess) PID() int { return p.l.proc.PID() }

func (p *launchedProcess) Kill() error {
	if p.l.survive.Load() {
		return errCannotEnd
	}
	return p.l.proc.Kill()
}

func (p *launchedProcess) Wait() error {
	<-p.l.done
	return p.l.proc.Wait()
}

// relay hands what the adapter writes to the host. It stands between the two so
// that a process which survives is not touched by the host closing its end: the
// host's reader is closed (its reader goroutine must be interruptible), and the
// relay then goes on taking what the adapter writes and drops it, so the adapter's
// writes neither fail nor block and the process lives on, as a hung one does.
func (l *AdapterLaunch) relay(from io.Reader, to *io.PipeWriter) {
	buf := make([]byte, 32<<10)
	forwarding := true
	for {
		n, err := from.Read(buf)
		if n > 0 && forwarding {
			if _, werr := to.Write(buf[:n]); werr != nil {
				if !l.survive.Load() {
					return
				}
				forwarding = false
			}
		}
		if err != nil {
			_ = to.CloseWithError(err) // io.EOF reads as the end of the stream
			return
		}
	}
}

// hostReader is what the host reads the adapter's output from. Closing it closes
// the adapter's end too, unless the process survives.
type hostReader struct {
	l     *AdapterLaunch
	pr    *io.PipeReader
	inner io.Reader
}

func (r *hostReader) Read(b []byte) (int, error) { return r.pr.Read(b) }

func (r *hostReader) Close() error {
	err := r.pr.Close()
	if c, ok := r.inner.(io.Closer); ok && !r.l.survive.Load() {
		_ = c.Close()
	}
	return err
}

// tappedStream notes the method of what the daemon writes. The peer writes one
// line per Write, so a Write is read as one message.
type tappedStream struct {
	l *AdapterLaunch
	w io.WriteCloser
}

func (s *tappedStream) Write(b []byte) (int, error) {
	s.l.wrote(b)
	return s.w.Write(b)
}

// Close closes the stream, which ends a process that is not told to survive.
func (s *tappedStream) Close() error {
	if s.l.survive.Load() {
		return nil
	}
	return s.w.Close()
}

// unlessAbandoned runs a call made to a process and returns its result, unless the
// process was abandoned by a crash before the call was made or returned: nobody
// could have read what such a call answers, so the result is that the process
// ended, whatever the old process made of it. A client of a dead process sees a
// connection that ended, not a success.
func unlessAbandoned[T any](abandoned *atomic.Bool, call func() (T, error)) (T, error) {
	var zero T
	if abandoned.Load() {
		return zero, fmt.Errorf("%w (the call was made after it ended)", engineagent.ErrProcessEnded)
	}
	v, err := call()
	if abandoned.Load() {
		return zero, fmt.Errorf("%w (the answer was %v)", engineagent.ErrProcessEnded, err)
	}
	return v, err
}

// abandonable wraps the adapter host the engine of a process calls: a call made
// after the process was abandoned, or one that returns after it was, has the result
// engineagent.ErrProcessEnded and is never a confirmation, so that a stale engine
// neither acts on an adapter nor is believed. The counts are not a call that can be
// confirmed and pass through.
func abandonable(inner engine.Adapters, abandoned *atomic.Bool) engine.Adapters {
	return &abandonableAdapters{inner: inner, abandoned: abandoned}
}

type abandonableAdapters struct {
	inner     engine.Adapters
	abandoned *atomic.Bool
}

func (a *abandonableAdapters) EnsureRunning(instance string) {
	if !a.abandoned.Load() {
		a.inner.EnsureRunning(instance)
	}
}

func (a *abandonableAdapters) Stop(instance string) {
	if !a.abandoned.Load() {
		a.inner.Stop(instance)
	}
}

// Stopping is a read and passes through, as the counts do.
func (a *abandonableAdapters) Stopping(instance string) bool { return a.inner.Stopping(instance) }

func (a *abandonableAdapters) NotifyPending(instance string, p adapterproto.PendingParams) {
	if !a.abandoned.Load() {
		a.inner.NotifyPending(instance, p)
	}
}

func (a *abandonableAdapters) CreateSession(ctx context.Context, instance string, p adapterproto.SessionCreateParams) (adapterproto.SessionCreateResult, error) {
	return unlessAbandoned(a.abandoned, func() (adapterproto.SessionCreateResult, error) { return a.inner.CreateSession(ctx, instance, p) })
}

func (a *abandonableAdapters) DeliverPrompt(ctx context.Context, instance string, p adapterproto.PromptDeliverParams) (adapterproto.PromptDeliverResult, error) {
	return unlessAbandoned(a.abandoned, func() (adapterproto.PromptDeliverResult, error) { return a.inner.DeliverPrompt(ctx, instance, p) })
}

func (a *abandonableAdapters) Reconcile(ctx context.Context, instance string, p adapterproto.ReconcileParams) (adapterproto.ReconcileResult, error) {
	return unlessAbandoned(a.abandoned, func() (adapterproto.ReconcileResult, error) { return a.inner.Reconcile(ctx, instance, p) })
}

func (a *abandonableAdapters) Interrupt(ctx context.Context, instance string, p adapterproto.InterruptParams) (adapterproto.InterruptResult, error) {
	return unlessAbandoned(a.abandoned, func() (adapterproto.InterruptResult, error) { return a.inner.Interrupt(ctx, instance, p) })
}

func (a *abandonableAdapters) Close(ctx context.Context, instance string, p adapterproto.CloseParams) (adapterproto.CloseResult, error) {
	return unlessAbandoned(a.abandoned, func() (adapterproto.CloseResult, error) { return a.inner.Close(ctx, instance, p) })
}

func (a *abandonableAdapters) Counts(instance string) adapterhost.ConnCounts {
	return a.inner.Counts(instance)
}

// newLogs is the handle the host opens the logs of the adapters on: a file system
// in memory with the directory they live in.
func newLogs(t testing.TB) *logfile.Handle {
	t.Helper()
	mem := fsxtest.NewMem()
	mustMkdir(t, mem, "logs")
	return logfile.New(mem, logfile.Budget{Total: func() int64 { return 1 << 20 }})
}
