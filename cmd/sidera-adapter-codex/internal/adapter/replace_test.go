package adapter_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/adapter"
	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/server"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// installed is the version of the Codex program, which a test changes the way an
// upgrade does.
type installed struct {
	mu sync.Mutex
	v  string
}

func (i *installed) get() string  { i.mu.Lock(); defer i.mu.Unlock(); return i.v }
func (i *installed) set(v string) { i.mu.Lock(); i.v = v; i.mu.Unlock() }

// upgradeHarness starts the adapter with one session whose first turn is over, and
// a version check every few milliseconds. Ending the server (the manager's
// Terminate) drops the connection and lets the next one see the version installed
// by then, as a server started afresh would.
func upgradeHarness(t *testing.T, terminate func(*installed, *fakeCodex) error) (*harness, *fakeCodex, *installed) {
	t.Helper()
	codex := newFakeCodex(t)
	ver := &installed{v: "0.164.0"}
	h := newHarnessWith(t, codex, func(o *adapter.Options) {
		o.CodexVersion = func(context.Context, string) (string, error) { return ver.get(), nil }
		o.UpgradeCheckEvery = 25 * time.Millisecond
	})
	h.mgr.terminate = func() error {
		if terminate != nil {
			if err := terminate(ver, codex); err != nil {
				return err
			}
		}
		codex.mu.Lock()
		codex.serving = ver.get()
		codex.mu.Unlock()
		codex.srv.DropConnections()
		return nil
	}
	h.start()
	return h, codex, ver
}

func (h *harness) appServerDependency() adapterproto.Dependency {
	h.t.Helper()
	var res adapterproto.ProbeResult
	h.mustCall(adapterproto.MethodAdapterProbe, adapterproto.ProbeParams{ToolProbes: []adapterproto.ToolProbeRequest{}}, &res)
	for _, d := range res.Dependencies {
		if d.Name == "app-server" {
			return d
		}
	}
	h.t.Fatalf("no app-server dependency in %+v", res.Dependencies)
	return adapterproto.Dependency{}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// After Codex is upgraded the server it left running is replaced, but only when no
// session runs a turn: the turn is the session's work and waits. Once the server
// is replaced the sessions are resumed in the new one, and the same version is not
// replaced again.
func TestServerIsReplacedWhenIdleAfterAnUpgrade(t *testing.T) {
	h, codex, ver := upgradeHarness(t, nil)
	h.create(attempt1, sessionEP, "dev")
	first := decode[adapterproto.TurnStartedParams](h.waitNote(adapterproto.MethodTurnStarted, nil))
	resumes := len(codex.callsOf("thread/resume"))

	ver.set("0.165.0")
	time.Sleep(300 * time.Millisecond) // a dozen checks
	if n := h.mgr.terminations(); n != 0 {
		t.Fatalf("the server was ended %d times while a turn ran", n)
	}
	if d := h.appServerDependency(); d.State != adapterproto.DependencyWrongVersion || !strings.Contains(d.Detail, "replaced") {
		t.Fatalf("app-server dependency while the replacement waits = %+v", d)
	}

	codex.end("th-1", string(first.Turn), "completed")
	waitFor(t, "the server to be ended", func() bool { return h.mgr.terminations() == 1 })
	waitFor(t, "the session to be resumed in the new connection", func() bool {
		return len(codex.callsOf("thread/resume")) > resumes
	})
	time.Sleep(300 * time.Millisecond)
	if n := h.mgr.terminations(); n != 1 {
		t.Fatalf("the server was ended %d times, want once", n)
	}
	waitFor(t, "the dependency to be in order", func() bool { return h.appServerDependency().State == adapterproto.DependencyOK })
}

// A server whose start cannot be shown is never ended, and the refusal is not
// repeated at every check; the probe says to restart it by hand.
func TestServerThatIsNotProvenOursIsLeftForSomeoneToRestart(t *testing.T) {
	h, _, ver := upgradeHarness(t, func(*installed, *fakeCodex) error { return server.ErrNotProven })
	ver.set("0.165.0")
	waitFor(t, "the attempt to end the server", func() bool { return h.mgr.terminations() == 1 })
	time.Sleep(300 * time.Millisecond)
	if n := h.mgr.terminations(); n != 1 {
		t.Fatalf("the server was asked to end %d times, want once", n)
	}
	if d := h.appServerDependency(); d.State != adapterproto.DependencyWrongVersion || !strings.Contains(d.Detail, "restart it") {
		t.Fatalf("app-server dependency = %+v, want a manual restart", d)
	}
}

// Another failure to end the server is tried again at the next check.
func TestFailureToEndTheServerIsRetried(t *testing.T) {
	var mu sync.Mutex
	failures := 1
	h, _, ver := upgradeHarness(t, func(*installed, *fakeCodex) error {
		mu.Lock()
		defer mu.Unlock()
		if failures > 0 {
			failures--
			return errors.New("the process table did not answer")
		}
		return nil
	})
	ver.set("0.165.0")
	waitFor(t, "the second attempt", func() bool { return h.mgr.terminations() >= 2 })
}

// While the server is being replaced a prompt is refused as the runtime being
// unavailable, which the daemon retries, and nothing is sent over a connection
// that is about to end.
func TestPromptDuringTheReplacementIsRefusedAsUnavailable(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	h, codex, ver := upgradeHarness(t, func(*installed, *fakeCodex) error {
		close(entered)
		<-release
		return nil
	})
	h.create(attempt1, sessionEP, "dev")
	first := decode[adapterproto.TurnStartedParams](h.waitNote(adapterproto.MethodTurnStarted, nil))
	codex.end("th-1", string(first.Turn), "completed")
	h.waitNote(adapterproto.MethodTurnEnded, nil)
	starts := len(codex.callsOf("turn/start"))

	ver.set("0.165.0")
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the server was not replaced")
	}
	var res adapterproto.PromptDeliverResult
	h.mustCall(adapterproto.MethodPromptDeliver, adapterproto.PromptDeliverParams{Endpoint: sessionEP, Attempt: attempt2, Text: "next"}, &res)
	if res.Stage != adapterproto.PromptRefused || res.Reason != adapterproto.RefusalNativeUnavailable {
		t.Fatalf("a prompt during the replacement: %+v", res)
	}
	close(release)
	if got := len(codex.callsOf("turn/start")); got != starts {
		t.Fatalf("%d turns were started during the replacement", got-starts)
	}
}

// Sessions that adapter.start did not wait for are not known to be idle when it
// answers, so the check made then finds the server busy; when their loading ends
// the server is replaced at once, not at the next periodic check.
func TestServerIsReplacedWhenTheBackgroundRestoreOfIdleSessionsEnds(t *testing.T) {
	codex := newFakeCodex(t)
	codex.resumeDelay = 150 * time.Millisecond
	h := newHarnessWith(t, codex, func(o *adapter.Options) {
		o.CodexVersion = func(context.Context, string) (string, error) { return "0.165.0", nil }
		o.StartWait = 100 * time.Millisecond
		o.UpgradeCheckEvery = time.Hour
	})
	access := &adapterproto.ToolAccess{Env: map[string]string{"SIDERA_TOKEN": token, "SIDERA_HOME": dataDir}}
	codex.seed("th-1")
	codex.seed("th-2")
	res := h.start(
		adapterproto.SessionAttach{Endpoint: adapterproto.EndpointRef(strings.Repeat("1", 32)), Preset: "dev", NativeRef: "th-1", ToolAccess: access},
		adapterproto.SessionAttach{Endpoint: adapterproto.EndpointRef(strings.Repeat("2", 32)), Preset: "dev", NativeRef: "th-2", ToolAccess: access},
	)
	if res.Sessions[len(res.Sessions)-1].State != adapterproto.AttachUnreachable {
		t.Fatalf("start = %+v: the sessions were loaded before the answer, so the restore did not run in the background", res)
	}
	waitFor(t, "the server to be ended after the restore", func() bool { return h.mgr.terminations() == 1 })
}
