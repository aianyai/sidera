package controller_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera/internal/controller"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

type sink struct{ token atomic.Value }

func (s *sink) SetToken(t adapterproto.Token) { s.token.Store(string(t)) }
func (s *sink) get() string {
	v, _ := s.token.Load().(string)
	return v
}

// lockedBuffer is a log destination the test can read while the heartbeat writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for " + what)
		}
		time.Sleep(time.Millisecond)
	}
}

func claudeEnv() func(string) string {
	return env("CLAUDE_CODE_SESSION_ID", "sess-1", "CLAUDE_CODE_MESSAGING_TOKEN", "wake-secret-value")
}

const (
	wakeSecret = "wake-secret-value"
	find       = `,"create":false`
)

// session starts a Session on a fake node; the caller cancels and waits.
func session(t *testing.T, m *fakeManager, env func(string) string, interval time.Duration, log *slog.Logger) (*controller.Session, *sink, context.Context, context.CancelFunc) {
	t.Helper()
	agent := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	s := controller.NewSession(ctx, controller.Config{Manage: m, Agent: agent, Getenv: env, Getwd: cwd("/w/p"), Interval: interval, Log: log})
	return s, agent, ctx, cancel
}

// The heartbeat only finds the endpoint the first attachment made: it repeats the
// request with create false, so it can never make an endpoint, and it ends with its
// context.
func TestHeartbeatFindsTheEndpointAndStopsWithItsContext(t *testing.T) {
	m := &fakeManager{}
	s, agent, ctx, cancel := session(t, m, claudeEnv(), 2*time.Millisecond, nil)
	arrival, err := s.Ensure(ctx, "")
	if err != nil || arrival == nil || arrival.Wake.State != "ready" || arrival.Endpoint != "n/e1" || arrival.Replaced != "" || agent.get() != tokenA {
		t.Fatalf("first use: %+v %v token %q", arrival, err, agent.get())
	}
	if again, err := s.Ensure(ctx, ""); again != nil || err != nil {
		t.Fatalf("second use: %+v %v", again, err)
	}
	eventually(t, "three attachments", func() bool { return m.count() >= 3 })
	cancel()
	s.Wait()
	stopped := m.count()
	time.Sleep(20 * time.Millisecond)
	if m.count() != stopped {
		t.Fatal("the heartbeat continued after its context ended")
	}
	requests := m.sent()
	for _, r := range requests[1:] {
		if want := strings.TrimSuffix(requests[0], "}") + find + "}"; r != want {
			t.Fatalf("a heartbeat sent %s; want the first request with create false: %s", r, want)
		}
	}
}

// The heartbeat takes the token of the endpoint it finds when another process of the
// session replaced the one it held.
func TestHeartbeatTakesTheTokenOfAReplacedEndpoint(t *testing.T) {
	m := &fakeManager{}
	s, agent, ctx, cancel := session(t, m, claudeEnv(), 2*time.Millisecond, nil)
	if _, err := s.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	m.replaceEndpoint()
	eventually(t, "the new token", func() bool { return agent.get() == tokenB })
	cancel()
	s.Wait()
}

// An endpoint the owner closed is not made again by the heartbeat: it learns there is
// none and stops, and the session's next use attaches it as a new endpoint and tells
// the model which address was closed and which is the new one.
func TestAClosedEndpointIsReplacedByTheNextUseNotByTheHeartbeat(t *testing.T) {
	m := &fakeManager{}
	s, agent, ctx, cancel := session(t, m, claudeEnv(), 2*time.Millisecond, nil)
	if _, err := s.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	m.closeEndpoint()
	eventually(t, "the heartbeat to learn it", func() bool { return m.count() >= 2 })
	time.Sleep(20 * time.Millisecond)
	if n := m.count(); n != 2 {
		t.Fatalf("%d requests after the close, want the first attachment and the one beat that found nothing: %q", n, m.sent())
	}

	arrival, err := s.Ensure(ctx, "")
	if err != nil || arrival == nil || arrival.Endpoint != "n/e2" || arrival.Replaced != "n/e1" || agent.get() != tokenB {
		t.Fatalf("next use: %+v %v token %q", arrival, err, agent.get())
	}
	if notice := arrival.Notice(); !strings.Contains(notice, "n/e1") || !strings.Contains(notice, "closed") || !strings.Contains(notice, "n/e2") {
		t.Errorf("the notice does not name both addresses and say the old one is closed: %s", notice)
	}
	if again, err := s.Ensure(ctx, ""); again != nil || err != nil {
		t.Fatalf("the use after: %+v %v", again, err)
	}
	cancel()
	s.Wait()
}

// A call the node refuses for its token is the other way to learn the endpoint is
// gone; a failure that says nothing about the token leaves the session attached.
func TestACallRefusedForItsTokenMakesTheSessionAttachAgain(t *testing.T) {
	m := &fakeManager{}
	s, _, ctx, cancel := session(t, m, claudeEnv(), time.Hour, nil)
	if _, err := s.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	s.Rejected(apiwire.New(apiwire.CategoryUnavailable, "daemon_not_running", "the node is not running"))
	if again, err := s.Ensure(ctx, ""); again != nil || err != nil || m.count() != 1 {
		t.Fatalf("after a failure that is not about the token: %+v %v, %d requests", again, err, m.count())
	}
	m.closeEndpoint()
	s.Rejected(apiwire.New(apiwire.CategoryUnauthenticated, "token_unknown", "the token is not known"))
	arrival, err := s.Ensure(ctx, "")
	if err != nil || arrival == nil || arrival.Replaced != "n/e1" || arrival.Endpoint != "n/e2" {
		t.Fatalf("after the token was refused: %+v %v", arrival, err)
	}
	cancel()
	s.Wait()
}

// A session resumed after its controller was away has an endpoint already: Resume
// finds it without making one, starts the heartbeat, and the model is told once, at
// the first use. A session with none is left alone until that use attaches it.
func TestResumeFindsTheEndpointAndCreatesNone(t *testing.T) {
	t.Run("an endpoint exists", func(t *testing.T) {
		m := &fakeManager{opened: 1, open: true}
		s, agent, ctx, cancel := session(t, m, claudeEnv(), 2*time.Millisecond, nil)
		if err := s.Resume(ctx); err != nil || agent.get() != tokenA {
			t.Fatalf("resume: %v, token %q", err, agent.get())
		}
		eventually(t, "a heartbeat", func() bool { return m.count() >= 2 })
		for _, r := range m.sent() {
			if !strings.HasSuffix(r, find+"}") {
				t.Fatalf("a request that could create an endpoint: %s", r)
			}
		}
		arrival, err := s.Ensure(ctx, "")
		if err != nil || arrival == nil || arrival.Endpoint != "n/e1" || arrival.Replaced != "" {
			t.Fatalf("first use: %+v %v", arrival, err)
		}
		if again, _ := s.Ensure(ctx, ""); again != nil {
			t.Fatalf("a later use tells the model again: %+v", again)
		}
		cancel()
		s.Wait()
	})
	t.Run("no endpoint", func(t *testing.T) {
		m := &fakeManager{}
		s, agent, ctx, cancel := session(t, m, claudeEnv(), time.Hour, nil)
		if err := s.Resume(ctx); err != nil || agent.get() != "" || m.count() != 1 {
			t.Fatalf("resume: %v, token %q, %d requests", err, agent.get(), m.count())
		}
		if arrival, err := s.Ensure(ctx, ""); err != nil || arrival == nil || arrival.Endpoint != "n/e1" {
			t.Fatalf("first use: %+v %v", arrival, err)
		}
		if got := m.sent(); len(got) != 2 || !strings.HasSuffix(got[0], find+"}") || strings.HasSuffix(got[1], find+"}") {
			t.Fatalf("requests %q; want the lookup and then the attachment of the first use", got)
		}
		cancel()
		s.Wait()
	})
}

func TestCodexFirstCallWithoutAThreadIsAClearError(t *testing.T) {
	m := &fakeManager{}
	s, _, ctx, cancel := session(t, m, env(), time.Hour, nil)
	_, err := s.Ensure(ctx, "")
	var wire *apiwire.Error
	if !errors.As(err, &wire) || wire.Code != "token_missing" || !strings.Contains(wire.Message, "session id is unknown") {
		t.Fatalf("got %v", err)
	}
	if m.count() != 0 {
		t.Fatal("attached without knowing the session")
	}
	// The next call may know the thread and then attaches.
	if arrival, err := s.Ensure(ctx, "thread-1"); err != nil || arrival == nil {
		t.Fatalf("with a thread: %+v %v", arrival, err)
	}
	cancel()
	s.Wait()
}

// A later call that names another Codex thread than the attached one is logged once,
// and the attachment is not switched.
func TestAnotherCodexThreadIsLoggedOnceAndTheAttachmentStays(t *testing.T) {
	logs := &lockedBuffer{}
	m := &fakeManager{}
	s, _, ctx, cancel := session(t, m, env(), time.Hour, slog.New(slog.NewTextHandler(logs, nil)))
	if _, err := s.Ensure(ctx, "thread-1"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := s.Ensure(ctx, "thread-1"); err != nil {
			t.Fatal(err)
		}
	}
	if logs.String() != "" {
		t.Fatalf("the attached thread was logged: %s", logs.String())
	}
	for range 3 {
		if again, err := s.Ensure(ctx, "thread-2"); again != nil || err != nil {
			t.Fatalf("another thread: %+v %v", again, err)
		}
	}
	if n := strings.Count(logs.String(), "another Codex thread"); n != 1 || !strings.Contains(logs.String(), "thread-2") || m.count() != 1 {
		t.Fatalf("logged %d times, %d requests: %s", n, m.count(), logs.String())
	}
	cancel()
	s.Wait()
}

// A refusal that holds until a person changes something is logged once for each code,
// and the heartbeat goes on trying; a failure that may pass is logged every time.
func TestHeartbeatLogsAPermanentRefusalOnce(t *testing.T) {
	logs := &lockedBuffer{}
	m := &fakeManager{}
	s, _, ctx, cancel := session(t, m, claudeEnv(), time.Millisecond, slog.New(slog.NewTextHandler(logs, nil)))
	if _, err := s.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	m.setFail(apiwire.New(apiwire.CategoryRefused, "attach_unsupported", "the adapter does not take attached sessions"))
	eventually(t, "five refused beats", func() bool { return m.count() >= 6 })
	m.setFail(apiwire.New(apiwire.CategoryUnavailable, "daemon_not_running", "the node is not running"))
	n := m.count()
	eventually(t, "three failed beats", func() bool { return m.count() >= n+3 })
	cancel()
	s.Wait()
	if got := strings.Count(logs.String(), "attach_unsupported"); got != 1 {
		t.Errorf("the permanent refusal was logged %d times: %s", got, logs.String())
	}
	if got := strings.Count(logs.String(), "daemon_not_running"); got < 2 {
		t.Errorf("the failure that may pass was logged %d times: %s", got, logs.String())
	}
}

// A wake value never reaches what a person, a model or a log is shown, even when an
// error on the way carries the request that held it.
func TestAnErrorThatCarriesTheRequestShowsNoWakeValue(t *testing.T) {
	logs := &lockedBuffer{}
	m := &fakeManager{}
	m.setFail(nil)
	m.failWith = func(body string) error {
		return errors.New("management interface unreachable while sending " + body)
	}
	s, _, ctx, cancel := session(t, m, claudeEnv(), time.Millisecond, slog.New(slog.NewTextHandler(logs, nil)))
	_, err := s.Ensure(ctx, "")
	if err == nil || !strings.Contains(err.Error(), "management interface unreachable") || strings.Contains(err.Error(), wakeSecret) {
		t.Fatalf("the error shown to the model: %v", err)
	}

	// The same for a node's own error, as the beat logs it.
	m.mu.Lock()
	m.failWith = nil
	m.mu.Unlock()
	if _, err := s.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.failWith = func(body string) error {
		return apiwire.New(apiwire.CategoryInternal, "attach_failed", "could not attach "+body)
	}
	m.mu.Unlock()
	eventually(t, "a failed beat in the log", func() bool { return strings.Contains(logs.String(), "controller heartbeat failed") })
	cancel()
	s.Wait()
	if strings.Contains(logs.String(), wakeSecret) {
		t.Errorf("a wake value reached the log: %s", logs.String())
	}
}
