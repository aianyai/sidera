// Package turn is the state machine of the events one turn process gives rise
// to. A Sidera turn is the life of one turn process: it starts when the prompt
// it was given is first seen in the session record, and ends when the process
// is gone. The package is pure: it takes what was observed and returns what to
// report, so that the same function runs for a process the adapter just started,
// one it found again after a restart and one it never saw start.
package turn

import (
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Process is a turn process as the adapter recorded it. A PID alone is reused by
// the system; with the start it names one process.
type Process struct {
	PID       int    `json:"pid"`
	StartUnix int64  `json:"start_unix"`
	StartKey  string `json:"start_key"`
	// ContainerKind and ContainerName say where the process runs, as the
	// adapterproto.Container the launch reported; empty for a process the adapter
	// did not start and so cannot say it for.
	ContainerKind string `json:"container_kind,omitempty"`
	ContainerName string `json:"container_name,omitempty"`
}

// Same reports whether p and q are one process: the same PID and the same start
// key. StartUnix and the container are left out. On Linux the start in seconds
// derives from the boot time, which moves when the clock is set, so two readings
// of one process may differ; the start key is the identity.
func (p Process) Same(q Process) bool { return p.PID == q.PID && p.StartKey == q.StartKey }

// State is where one turn is: the persisted record of what was reported about
// it. It is stored in the session's checkpoint, and a restarted adapter picks
// the turn up from it.
type State struct {
	// Attempt is the prompt attempt the process was given; it is also the uuid of
	// the user message in the session record.
	Attempt string  `json:"attempt"`
	Process Process `json:"process"`
	// Digest is the SHA-256 of the prompt text, in hex, so that the same attempt
	// repeated with another text is told from a retry.
	Digest string `json:"digest"`
	// PersistNotice: send prompt.persisted when the prompt is first seen in the
	// record. It is set for a prompt the adapter answered as accepted, which the
	// daemon waits to hear persisted; the first prompt of a session is answered
	// by the creation itself.
	PersistNotice bool `json:"persist_notice"`
	// StartedReported: turn.started was sent.
	StartedReported bool `json:"started_reported"`
	// EndedReported: turn.ended was sent.
	EndedReported bool `json:"ended_reported"`
	// Interrupted: the adapter interrupted the process, so its end is an
	// interrupted turn whatever its output says.
	Interrupted bool `json:"interrupted"`
	// Abandoned: the process ended before the prompt reached the record. Nothing
	// was reported and nothing will be; the daemon finds out by reconciling.
	Abandoned bool `json:"abandoned"`
}

// Done reports whether nothing is left to report: the turn ended and was
// reported, or never began.
func (s State) Done() bool { return s.EndedReported || s.Abandoned }

// Observation is what the caller saw of the turn process in one look. The
// process is looked at before the record is read: a process seen as gone has
// written everything it ever will, so a record read afterwards is complete.
type Observation struct {
	// Alive: the recorded process is running (same PID, same start).
	Alive bool
	// PromptSeen: a user record with the attempt's uuid is in the session record.
	PromptSeen bool
	// Outcome is the last result of the process's output stream; read only
	// when the process is not alive, and meaningful only then.
	Outcome record.Outcome
}

// EventKind says which notification to send.
type EventKind int

// The notifications a turn gives rise to, in the order they are sent.
const (
	// PromptPersisted: prompt.persisted for the attempt.
	PromptPersisted EventKind = iota + 1
	// TurnStarted: turn.started for the attempt.
	TurnStarted
	// TurnEnded: turn.ended with Event.Final.
	TurnEnded
)

// Event is one notification to send.
type Event struct {
	Kind  EventKind
	Final adapterproto.TurnFinal // set for TurnEnded
}

// Step advances the turn by what was observed and returns the notifications to
// send, in order. The caller sends them, persists the new state and looks again
// unless the state is Done.
//
// A turn begins when its prompt is first seen in the record, not when the
// process is started: turn.started carries the attempt, which proves the prompt
// was delivered, and saying so before it is on disk would be a false proof. A
// process that ends before that reports nothing. A turn whose process is gone is
// ended once, with the final that its last result says; a restarted adapter that
// finds a turn begun but not ended and its process gone reports the end that was
// lost, and may report a turn.started again that was lost with it, which the
// daemon tolerates.
func (s State) Step(o Observation) (State, []Event) {
	if s.Done() {
		return s, nil
	}
	var events []Event
	if !s.StartedReported && o.PromptSeen {
		if s.PersistNotice {
			events = append(events, Event{Kind: PromptPersisted})
		}
		events = append(events, Event{Kind: TurnStarted})
		s.StartedReported = true
	}
	if !o.Alive {
		if s.StartedReported {
			events = append(events, Event{Kind: TurnEnded, Final: s.final(o.Outcome)})
			s.EndedReported = true
		} else {
			s.Abandoned = true
		}
	}
	return s, events
}

func (s State) final(o record.Outcome) adapterproto.TurnFinal {
	switch {
	case s.Interrupted:
		return adapterproto.TurnInterrupted
	case o == record.Succeeded:
		return adapterproto.TurnCompleted
	}
	return adapterproto.TurnFailed
}
