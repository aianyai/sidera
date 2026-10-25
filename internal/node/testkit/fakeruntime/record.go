package fakeruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// The events of a session record.
const (
	EventSession     = "session"
	EventPrompt      = "prompt"
	EventTurnStarted = "turn_started"
	EventToolCall    = "tool_call"
	EventToolResult  = "tool_result"
	EventTurnEnded   = "turn_ended"
	EventInterrupted = "interrupted"
	EventClosed      = "closed"
	EventKilled      = "killed"
)

// How a turn can end.
const (
	FinalCompleted   = "completed"
	FinalInterrupted = "interrupted"
	FinalFailed      = "failed"
)

// Event is one line of a session record, in the canonical order of its members.
// The record is what an adapter reads to reconcile a prompt attempt and to see
// what the model was shown, as a real adapter reads the file its native runtime
// keeps; it is a file of the runtime, not of the daemon, and so it holds the text
// the model saw. The ledger holds digests only.
type Event struct {
	Seq        int    `json:"seq"`
	AtMS       int64  `json:"at_ms"`
	Kind       string `json:"event"`
	Endpoint   string `json:"endpoint,omitempty"`
	Turn       string `json:"turn,omitempty"`
	Attempt    string `json:"attempt,omitempty"`
	Final      string `json:"final,omitempty"`
	Action     string `json:"action,omitempty"`
	Lease      string `json:"lease,omitempty"`
	Frontend   string `json:"frontend,omitempty"`
	TextDigest string `json:"text_digest,omitempty"`
	Text       string `json:"text,omitempty"`
	Error      string `json:"error,omitempty"`
}

type eventSpec struct{ required, optional []string }

var eventSpecs = map[string]eventSpec{
	EventSession:     {required: []string{"endpoint", "attempt"}},
	EventPrompt:      {required: []string{"attempt", "text_digest"}},
	EventTurnStarted: {required: []string{"turn"}, optional: []string{"attempt"}},
	EventToolCall:    {required: []string{"turn", "action"}},
	EventToolResult:  {required: []string{"turn", "action"}, optional: []string{"lease", "frontend", "text", "error"}},
	EventTurnEnded:   {required: []string{"turn", "final"}},
	EventInterrupted: {},
	EventClosed:      {},
	EventKilled:      {},
}

func (e Event) present() map[string]bool {
	out := map[string]bool{}
	for name, v := range map[string]string{
		"endpoint": e.Endpoint, "turn": e.Turn, "attempt": e.Attempt, "final": e.Final, "action": e.Action,
		"lease": e.Lease, "frontend": e.Frontend, "text_digest": e.TextDigest, "text": e.Text, "error": e.Error,
	} {
		if v != "" {
			out[name] = true
		}
	}
	return out
}

func (e Event) validate() error {
	if e.Seq < 1 {
		return fmt.Errorf("seq %d: sequence numbers start at 1", e.Seq)
	}
	if e.AtMS < 0 {
		return fmt.Errorf("at_ms %d is negative", e.AtMS)
	}
	spec, ok := eventSpecs[e.Kind]
	if !ok {
		return fmt.Errorf("unknown event %q", e.Kind)
	}
	for _, s := range []string{e.Endpoint, e.Turn, e.Attempt, e.Final, e.Action, e.Lease, e.Frontend, e.TextDigest, e.Text, e.Error} {
		if !utf8.ValidString(s) {
			return errors.New("text is not UTF-8")
		}
	}
	present := e.present()
	for _, name := range spec.required {
		if !present[name] {
			return fmt.Errorf("event %s requires %s", e.Kind, name)
		}
	}
	for name := range present {
		if !slices.Contains(spec.required, name) && !slices.Contains(spec.optional, name) {
			return fmt.Errorf("event %s does not take %s", e.Kind, name)
		}
	}
	if e.Kind == EventTurnEnded && !slices.Contains([]string{FinalCompleted, FinalInterrupted, FinalFailed}, e.Final) {
		return fmt.Errorf("final %q is not completed, interrupted or failed", e.Final)
	}
	return nil
}

// EncodeEvent validates e and returns its canonical line, without a newline.
func EncodeEvent(e Event) ([]byte, error) {
	if err := e.validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// DecodeEvent reads one line strictly; a line that is not the canonical line of
// its event is an error.
func DecodeEvent(line []byte) (Event, error) {
	var e Event
	if err := strictjson.Decode(line, &e); err != nil {
		return Event{}, err
	}
	canonical, err := EncodeEvent(e)
	if err != nil {
		return Event{}, err
	}
	if !bytes.Equal(canonical, line) {
		return Event{}, errors.New("not the canonical line of its event")
	}
	return e, nil
}

// ParseRecord reads a whole record: every line strict, sequence numbers from 1
// without a gap, every line ended by a newline.
func ParseRecord(data []byte) ([]Event, error) {
	var events []Event
	rest := data
	for n := 1; len(rest) > 0; n++ {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			return nil, fmt.Errorf("line %d has no newline: the record is cut", n)
		}
		e, err := DecodeEvent(rest[:i])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		if e.Seq != n {
			return nil, fmt.Errorf("line %d: seq %d, want %d", n, e.Seq, n)
		}
		events = append(events, e)
		rest = rest[i+1:]
	}
	return events, nil
}
