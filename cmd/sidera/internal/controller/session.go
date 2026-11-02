package controller

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/aianyai/sidera/internal/node/apiwire"
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
// repeated until the context ends or the endpoint is closed.
type Session struct {
	cfg Config
	ctx context.Context

	mu       sync.Mutex
	attached bool
	closed   bool
	req      Request
	// endpoint is the address of the endpoint the session holds while it is attached.
	endpoint string
	// gone is the address of the endpoint the session lost, until the attachment that
	// replaces it has told the model.
	gone string
	// arrival is what Resume found that the first Ensure still has to hand out, so
	// that the model is told once as it is after an attachment made by Ensure.
	arrival *Arrival
	// endBeat ends the heartbeat of the current attachment.
	endBeat context.CancelFunc
	// threadNoted says another Codex thread than the attached one was logged already.
	threadNoted bool
	beat        sync.WaitGroup
}

// NewSession ties the heartbeat to ctx. Call Wait after ctx ends.
func NewSession(ctx context.Context, cfg Config) *Session {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	return &Session{cfg: cfg, ctx: ctx}
}

// Ensure attaches the session when it is not attached and returns what the model is
// to be told then; calls while it is attached return nil. thread is the Codex thread
// id the caller has for this call, empty if none. A failed attachment is tried again
// by the next call, and so is the attachment of a session whose endpoint was closed
// (see Rejected and the heartbeat): the new endpoint is a new one, and the answer
// names the one that was lost. Ensure holds the token only in the Agent client.
func (s *Session) Ensure(ctx context.Context, thread string) (*Arrival, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attached {
		s.noteThread(thread)
		arrival := s.arrival
		s.arrival = nil
		return arrival, nil
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
	arrival := &Arrival{Endpoint: att.Endpoint, Replaced: s.gone, Wake: att.Wake}
	s.gone = ""
	s.begin(req, att)
	return arrival, nil
}

// noteThread logs, once, a tool call that names another Codex thread than the one the
// session attached: the attachment stays as it is, and the log is how a person finds
// out that a server serves two threads. The caller holds mu.
func (s *Session) noteThread(thread string) {
	if thread == "" || s.req.Adapter != Codex || thread == s.req.NativeRef || s.threadNoted || s.cfg.Log == nil {
		return
	}
	s.threadNoted = true
	s.cfg.Log.Warn("a tool call names another Codex thread than the one this server attached; the attachment is kept",
		"attached", s.req.NativeRef, "call", thread)
}

// Rejected tells the session that a call made with its token failed with err. A
// failure of authentication means the node no longer knows the token: the endpoint was
// closed, by its owner or because nothing tended it. The session is then not attached,
// and the next Ensure attaches it again with a new endpoint. Any other failure says
// nothing about the endpoint.
func (s *Session) Rejected(err error) {
	var wire *apiwire.Error
	if !errors.As(err, &wire) || wire.Category != apiwire.CategoryUnauthenticated {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lose()
}

// lose makes the session not attached because its endpoint is gone, and ends the
// heartbeat of that endpoint. The caller holds mu.
func (s *Session) lose() {
	if !s.attached {
		return
	}
	s.attached, s.gone = false, s.endpoint
	s.endBeat()
}

// Resume looks for the endpoint the session already has, without making one: a
// session resumed after its controller was away has one while it is still open, and
// taking it up at once starts the heartbeat, so that the node can wake the session
// for what waited. A session with no endpoint, or one this process cannot name,
// stays as it was and is attached by Ensure on its first use. Resume does nothing
// when the session is attached already.
func (s *Session) Resume(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attached || s.closed {
		return nil
	}
	req, err := RequestFromEnv(s.cfg.Getenv, s.cfg.Getwd, "")
	if err != nil {
		return nil
	}
	find := req
	find.Create = new(bool)
	att, err := Attach(ctx, s.cfg.Manage, find)
	if err != nil {
		var wire *apiwire.Error
		if errors.As(err, &wire) && wire.Code == CodeSessionNotAttached {
			return nil
		}
		return err
	}
	s.begin(req, att)
	s.arrival = &Arrival{Endpoint: att.Endpoint, Wake: att.Wake}
	return nil
}

// begin takes up the endpoint of an attachment: its token for the Agent calls and
// the heartbeat that finds it again. The caller holds mu.
func (s *Session) begin(req Request, att Attachment) {
	s.cfg.Agent.SetToken(att.Token)
	beatCtx, end := context.WithCancel(s.ctx)
	s.attached, s.req, s.endpoint, s.endBeat = true, req, att.Endpoint, end
	s.beat.Add(1)
	go s.heartbeat(beatCtx, req, att)
}

// Wait stops new attachments and returns after the heartbeat has ended, which
// needs the context given to NewSession to have ended.
func (s *Session) Wait() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.beat.Wait()
}

// heartbeat asks for the endpoint of the attachment again, which proves to the node
// that the controller lives and sends it the wake information anew. It only finds:
// an endpoint the owner closed is not made again behind the owner's back, and when the
// node says there is none the session is not attached any more, so that its next use
// attaches it as a new one. When the node answers with another token the endpoint was
// replaced (a command run in the session made a new one after the old closed), and the
// Agent client takes the new token.
func (s *Session) heartbeat(ctx context.Context, req Request, att Attachment) {
	defer s.beat.Done()
	find := req
	find.Create = new(bool)
	token := att.Token
	refused := map[string]bool{}
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		got, err := Attach(ctx, s.cfg.Manage, find)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var wire *apiwire.Error
			if errors.As(err, &wire) && wire.Code == CodeSessionNotAttached {
				s.mu.Lock()
				if ctx.Err() == nil {
					s.lose()
				}
				s.mu.Unlock()
				return
			}
			s.logFailure(err, refused)
			continue
		}
		if got.Token != token {
			token = got.Token
			s.mu.Lock()
			s.endpoint = got.Endpoint
			s.mu.Unlock()
			s.cfg.Agent.SetToken(token)
		}
	}
}

// logFailure logs a failed heartbeat. A refusal that holds until a person changes
// something (no adapter to attach to, an adapter that does not take the session) is
// logged once for each code, not every beat; any other failure is logged each time.
func (s *Session) logFailure(err error, refused map[string]bool) {
	if s.cfg.Log == nil {
		return
	}
	var wire *apiwire.Error
	if errors.As(err, &wire) {
		switch wire.Category {
		case apiwire.CategoryRefused, apiwire.CategoryNotFound, apiwire.CategoryInvalidArgument:
			if refused[wire.Code] {
				return
			}
			refused[wire.Code] = true
		}
	}
	s.cfg.Log.Warn("controller heartbeat failed", "error", err)
}
