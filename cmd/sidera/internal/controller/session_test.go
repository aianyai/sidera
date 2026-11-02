package controller_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
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

func TestHeartbeatRepeatsTheAttachmentAndStopsWithItsContext(t *testing.T) {
	m := &fakeManager{tokens: []string{tokenA}}
	agent := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	s := controller.NewSession(ctx, controller.Config{Manage: m, Agent: agent, Getenv: claudeEnv(), Getwd: cwd("/w/p"), Interval: 2 * time.Millisecond})
	wake, err := s.Ensure(ctx, "")
	if err != nil || wake == nil || wake.State != "ready" || agent.get() != tokenA {
		t.Fatalf("first use: %+v %v token %q", wake, err, agent.get())
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
	for _, r := range m.requests {
		if r != m.requests[0] {
			t.Fatalf("the heartbeat changed its request: %s", r)
		}
	}
}

func TestHeartbeatTakesTheTokenOfAReplacedEndpoint(t *testing.T) {
	m := &fakeManager{tokens: []string{tokenA, tokenA, tokenB}}
	agent := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	s := controller.NewSession(ctx, controller.Config{Manage: m, Agent: agent, Getenv: claudeEnv(), Getwd: cwd("/w/p"), Interval: 2 * time.Millisecond})
	if _, err := s.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new token", func() bool { return agent.get() == tokenB })
	cancel()
	s.Wait()
}

func TestCodexFirstCallWithoutAThreadIsAClearError(t *testing.T) {
	m := &fakeManager{tokens: []string{tokenA}}
	ctx, cancel := context.WithCancel(context.Background())
	s := controller.NewSession(ctx, controller.Config{Manage: m, Agent: &sink{}, Getenv: env(), Getwd: cwd("/w")})
	_, err := s.Ensure(ctx, "")
	var wire *apiwire.Error
	if !errors.As(err, &wire) || wire.Code != "token_missing" || !strings.Contains(wire.Message, "session id is unknown") {
		t.Fatalf("got %v", err)
	}
	if m.count() != 0 {
		t.Fatal("attached without knowing the session")
	}
	// The next call may know the thread and then attaches.
	if wake, err := s.Ensure(ctx, "thread-1"); err != nil || wake == nil {
		t.Fatalf("with a thread: %+v %v", wake, err)
	}
	cancel()
	s.Wait()
}

func TestSecretsStayOutOfLogsAndTheProcessEnvironment(t *testing.T) {
	logs := &lockedBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m := &fakeManager{tokens: []string{tokenA}}
	agent := &sink{}
	ctx, cancel := context.WithCancel(context.Background())
	s := controller.NewSession(ctx, controller.Config{Manage: m, Agent: agent, Getenv: claudeEnv(), Getwd: cwd("/w/p"), Interval: time.Millisecond, Log: log})
	if _, err := s.Ensure(ctx, ""); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.fail = errors.New("management interface unreachable")
	m.mu.Unlock()
	eventually(t, "a failed heartbeat", func() bool { return strings.Contains(logs.String(), "controller heartbeat failed") })
	cancel()
	s.Wait()
	for _, secret := range []string{tokenA, "wake-secret-value"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("a secret reached the log: %s", logs.String())
		}
		for _, kv := range os.Environ() {
			if strings.Contains(kv, secret) {
				t.Errorf("a secret reached the process environment")
			}
		}
	}
}
