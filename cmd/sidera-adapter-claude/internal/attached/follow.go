package attached

import (
	"slices"
	"strconv"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// IdleGrace is how long a session must have shown no turn running before a
// turn that the transcript did not end is ended on the registry's word alone.
// It is longer than the delay between the registry going idle and the runtime
// writing the end of the turn (a third of a second), so the transcript wins
// whenever it can.
const IdleGrace = 2 * time.Second

// EventKind says which notification to send.
type EventKind int

// The notifications following a session gives rise to.
const (
	// EventPersisted: prompt.persisted for Event.Attempt.
	EventPersisted EventKind = iota + 1
	// EventStarted: turn.started for Event.Turn, naming Event.Attempt when it has one.
	EventStarted
	// EventEnded: turn.ended for Event.Turn with Event.Final.
	EventEnded
)

// Event is one notification to send.
type Event struct {
	Kind    EventKind
	Turn    string
	Attempt string
	Final   adapterproto.TurnFinal // for EventEnded
}

// Observation is what the caller saw of the session in one look. The markers
// of the transcript are read before the registry the look acts on: the transcript
// is the slower of the two, so a registry read afterwards is never older than what
// the markers show.
type Observation struct {
	Status   Status
	StatusAt int64
	Now      time.Time
	// Markers are the new lines of the transcript that matter, in file order.
	Markers []record.Marker
}

type openTurn struct {
	ref     string
	attempt string
	// activeAt is the latest change of status seen while the turn ran and the
	// status said a turn runs.
	activeAt int64
}

// Follower is the turns of one session someone else runs, as the adapter
// reports them: it turns what the registry and the transcript show into the
// notifications of the protocol.
//
// A turn the person (or a task of the session) starts is told from the registry:
// it begins when the status shows a turn running, and has no attempt. The
// transcript tells it too, from the user record that begins it, for a turn that
// the registry does not show as a new one: a session that stays busy from one
// turn to the next keeps its status and the time of it. A
// turn a prompt of ours started begins when the prompt's user record is in the
// transcript and carries the attempt, which is the proof of delivery the daemon
// takes. A prompt that is written to a session in the middle of a turn is taken
// into that turn at its next tool boundary and starts none: it is reported
// persisted and the turn goes on without it.
//
// A turn ends when the transcript says it did, and, if the transcript never
// does, when the registry has shown no turn running for IdleGrace, so that a
// session never stays busy for the daemon because of a record that was not
// written.
type Follower struct {
	open     *openTurn
	consumed int64
	idle     time.Time
	expected map[string]bool
}

// Expect says that a prompt was written to the session and its user record,
// carrying the attempt as its uuid, is to be looked for.
func (f *Follower) Expect(attempt string) {
	if f.expected == nil {
		f.expected = map[string]bool{}
	}
	f.expected[attempt] = true
}

// Forget drops an expected prompt that turned out not to have been written.
func (f *Follower) Forget(attempt string) { delete(f.expected, attempt) }

// Expected lists the attempts whose user records are still to be found.
func (f *Follower) Expected() []string {
	out := make([]string, 0, len(f.expected))
	for a := range f.expected {
		out = append(out, a)
	}
	slices.Sort(out)
	return out
}

// Open reports whether a turn is running as far as the follower has told, and
// its reference.
func (f *Follower) Open() (ref string, ok bool) {
	if f.open == nil {
		return "", false
	}
	return f.open.ref, true
}

// Adopt makes a turn that the status shows running the follower's turn without
// telling of its start, for a caller that has told it another way (the state
// answered in adapter.start).
func (f *Follower) Adopt(statusAt int64) {
	f.open = &openTurn{ref: personTurn(statusAt), activeAt: statusAt}
}

// personTurn names a turn that has no attempt by the time of the status that
// showed it, and recordTurn one that the transcript showed by the uuid of the user
// record that began it.
func personTurn(statusAt int64) string { return "person-" + strconv.FormatInt(statusAt, 10) }

func recordTurn(uuid string) string { return "person-" + uuid }

// Step advances the follower by one look and returns the notifications to send,
// in order.
func (f *Follower) Step(o Observation) []Event {
	activeAt := int64(0)
	if o.Status.Active() {
		activeAt = o.StatusAt
	}
	events := f.apply(o.Markers, activeAt)

	if o.Status.Active() {
		f.idle = time.Time{}
		switch {
		case f.open != nil:
			f.open.activeAt = max(f.open.activeAt, o.StatusAt)
		case o.StatusAt > f.consumed:
			// A status that belongs to a turn the transcript already ended is not a new turn.
			f.open = &openTurn{ref: personTurn(o.StatusAt), activeAt: o.StatusAt}
			events = append(events, Event{Kind: EventStarted, Turn: f.open.ref})
		}
		return events
	}
	if f.open == nil {
		f.idle = time.Time{}
		return events
	}
	if f.idle.IsZero() {
		f.idle = o.Now
	} else if o.Now.Sub(f.idle) >= IdleGrace {
		// Without the runtime's own end of turn, the likeliest cause is that the person
		// stopped the turn.
		events = append(events, f.end(adapterproto.TurnInterrupted)...)
	}
	return events
}

// apply tells what the markers of the transcript show, in order. activeAt is the
// time of the registry's status when it shows a turn running, else zero.
func (f *Follower) apply(markers []record.Marker, activeAt int64) []Event {
	var events []Event
	for _, m := range markers {
		switch m.Kind {
		case record.MarkPrompt:
			if !f.expected[m.UUID] {
				continue
			}
			delete(f.expected, m.UUID)
			events = append(events, Event{Kind: EventPersisted, Attempt: m.UUID})
			if f.open == nil {
				f.open = &openTurn{ref: m.UUID, attempt: m.UUID}
				events = append(events, Event{Kind: EventStarted, Turn: m.UUID, Attempt: m.UUID})
			}
		case record.MarkTurnStart:
			// A message that arrives in a turn that runs is taken into it.
			if f.open == nil {
				f.open = &openTurn{ref: recordTurn(m.UUID), activeAt: activeAt}
				events = append(events, Event{Kind: EventStarted, Turn: f.open.ref})
			}
		case record.MarkTurnEnd:
			events = append(events, f.end(adapterproto.TurnCompleted)...)
		}
	}
	return events
}

// End ends the turn that is running, if any, for a caller that knows it is over
// by other means: the session was cleared or its process is gone.
func (f *Follower) End(final adapterproto.TurnFinal) []Event { return f.end(final) }

// Finish is End for a caller that still holds markers of the transcript that were
// not told: they are told first, so that a prompt or the end of a turn written
// just before the session went is not lost.
func (f *Follower) Finish(markers []record.Marker, final adapterproto.TurnFinal) []Event {
	return append(f.apply(markers, 0), f.end(final)...)
}

func (f *Follower) end(final adapterproto.TurnFinal) []Event {
	if f.open == nil {
		return nil
	}
	ev := Event{Kind: EventEnded, Turn: f.open.ref, Final: final}
	f.consumed = max(f.consumed, f.open.activeAt)
	f.open, f.idle = nil, time.Time{}
	return []Event{ev}
}
