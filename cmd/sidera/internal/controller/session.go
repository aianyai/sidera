package controller

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// DefaultInterval is how often a running session repeats its attachment. Each
// attachment is the controller's sign of life and re-sends the wake information
// to a node or adapter that restarted.
const DefaultInterval = time.Minute

// TokenSink takes the endpoint token for the Agent calls that follow.
type TokenSink interface {
	SetToken(adapterproto.Token)
}

// Config supplies what a Session needs. Manage, Agent, Getenv and Getwd are
// required; a zero Interval is DefaultInterval.
type Config struct {
	Manage   Manager
	Agent    TokenSink
	Getenv   func(string) string
	Getwd    func() (string, error)
	Interval time.Duration
	Log      *slog.Logger
}

// Session is a long-running process's attachment: made on first use, then
// repeated until the context ends.
type Session struct {
	cfg Config
	ctx context.Context

	mu       sync.Mutex
	attached bool
	closed   bool
	req      Request
	beat     sync.WaitGroup
}

// NewSession ties the heartbeat to ctx. Call Wait after ctx ends.
func NewSession(ctx context.Context, cfg Config) *Session {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	return &Session{cfg: cfg, ctx: ctx}
}

// Ensure attaches the session on the first call and returns the wake state then;
// later calls return nil at once. thread is the Codex thread id the caller has for
// this call, empty if none. A failed attachment is tried again by the next call.
// Ensure holds the token only in the Agent client.
func (s *Session) Ensure(ctx context.Context, thread string) (*Wake, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attached {
		return nil, nil
	}
	if s.closed {
		return nil, s.ctx.Err()
	}
	req, err := RequestFromEnv(s.cfg.Getenv, s.cfg.Getwd, thread)
	if errors.Is(err, ErrNotController) {
		err = SessionUnknown()
	}
	if err != nil {
		return nil, err
	}
	att, err := Attach(ctx, s.cfg.Manage, req)
	if err != nil {
		return nil, err
	}
	s.cfg.Agent.SetToken(att.Token)
	s.attached, s.req = true, req
	s.beat.Add(1)
	go s.heartbeat(att.Token)
	return &att.Wake, nil
}

// Wait stops new attachments and returns after the heartbeat has ended, which
// needs the context given to NewSession to have ended.
func (s *Session) Wait() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.beat.Wait()
}

// heartbeat repeats the attachment with the request of the first. When the node
// answers with another token the endpoint was replaced, after the old one closed,
// and the Agent client takes the new token.
func (s *Session) heartbeat(token adapterproto.Token) {
	defer s.beat.Done()
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
		att, err := Attach(s.ctx, s.cfg.Manage, s.req)
		if err != nil {
			if s.ctx.Err() == nil && s.cfg.Log != nil {
				s.cfg.Log.Warn("controller heartbeat failed", "error", err)
			}
			continue
		}
		if att.Token != token {
			token = att.Token
			s.cfg.Agent.SetToken(token)
		}
	}
}
