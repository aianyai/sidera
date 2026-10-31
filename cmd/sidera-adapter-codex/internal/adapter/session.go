package adapter

import (
	"sync"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// session is a thread this adapter holds as a session endpoint. Its fields are
// guarded by App.mu, except the mutexes and the wake channel.
type session struct {
	endpoint adapterproto.EndpointRef
	preset   string
	threadID string
	// access is what the thread's tools need to reach the daemon (SIDERA_TOKEN and
	// SIDERA_HOME). It lives in memory only: it is not written anywhere, not even
	// the checkpoint.
	access map[string]string

	status string // the thread's state in the server: an appserver.Thread* value
	lost   bool   // the thread's record is gone
	// creating is set while session.create is still working on the session: the
	// creation owns the thread and resumes it itself.
	creating bool

	turns map[string]*turnRec
	order []string // turn ids, oldest first, for pruning
	// activeTurn is the turn reported started and not yet ended.
	activeTurn string
	// persisted holds the attempts whose prompt was seen in the record.
	persisted *idSet
	prompts   map[adapterproto.AttemptID]*promptRec
	// pending is set while a turn start is being answered, so that the event of the
	// turn can be given the attempt that caused it.
	pending *pendingStart

	startMu sync.Mutex    // one turn start at a time
	wake    chan struct{} // signalled by every event of the thread; capacity one
}

// turnRec is what is known of a turn that was reported.
type turnRec struct {
	attempt string
	started bool
	ended   bool
}

// promptRec is the answer given to a prompt.deliver, kept so that a repeat of the
// attempt gets the same answer and so that the persisted notice is sent once.
type promptRec struct {
	text          string
	result        adapterproto.PromptDeliverResult
	turnID        string
	persistedSent bool
}

type pendingStart struct {
	resolved chan struct{} // closed when the answer to the turn start was processed
}

func newSession(ep adapterproto.EndpointRef, preset, threadID string, access map[string]string) *session {
	env := make(map[string]string, len(access))
	for k, v := range access {
		env[k] = v
	}
	return &session{
		endpoint: ep, preset: preset, threadID: threadID, access: env,
		status:    appserver.ThreadIdle,
		turns:     map[string]*turnRec{},
		persisted: newIDSet(256),
		prompts:   map[adapterproto.AttemptID]*promptRec{},
		wake:      make(chan struct{}, 1),
	}
}

// signal wakes whoever waits on the session; a signal that finds one pending is
// dropped, since a waiter re-reads the state after any wake.
func (s *session) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// turn returns the record of a turn, creating it. The caller holds App.mu.
func (s *session) turn(id string) *turnRec {
	if t, ok := s.turns[id]; ok {
		return t
	}
	t := &turnRec{}
	s.turns[id] = t
	s.order = append(s.order, id)
	// A session lives as long as its endpoint: only finished turns are dropped, the
	// oldest first, so the table stays bounded.
	for len(s.order) > 64 {
		old := s.order[0]
		if r := s.turns[old]; !r.ended {
			break
		}
		delete(s.turns, old)
		s.order = s.order[1:]
	}
	return t
}

// busy reports whether the thread runs a turn, by the server's word or the
// adapter's own books.
func (s *session) busy() bool { return s.status == appserver.ThreadActive || s.activeTurn != "" }

// idSet is a set of ids that forgets the oldest past its capacity.
type idSet struct {
	order []string
	set   map[string]struct{}
	cap   int
}

func newIDSet(capacity int) *idSet { return &idSet{set: map[string]struct{}{}, cap: capacity} }

func (s *idSet) add(id string) {
	if _, ok := s.set[id]; ok {
		return
	}
	s.set[id] = struct{}{}
	s.order = append(s.order, id)
	if len(s.order) > s.cap {
		delete(s.set, s.order[0])
		s.order = s.order[1:]
	}
}

func (s *idSet) has(id string) bool { _, ok := s.set[id]; return ok }
