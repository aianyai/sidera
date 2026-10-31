package turn

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func ended(f adapterproto.TurnFinal) Event { return Event{Kind: TurnEnded, Final: f} }

var (
	persisted = Event{Kind: PromptPersisted}
	started   = Event{Kind: TurnStarted}
)

func TestStep(t *testing.T) {
	proc := Process{PID: 7, StartUnix: 100, StartKey: "100.5"}
	tests := []struct {
		name   string
		in     State
		obs    Observation
		want   State
		events []Event
	}{
		{"the process runs and the prompt is not on disk yet: nothing is reported",
			State{Attempt: "a", Process: proc}, Observation{Alive: true},
			State{Attempt: "a", Process: proc}, nil},
		{"the prompt reaches the record: the turn starts",
			State{Attempt: "a", Process: proc}, Observation{Alive: true, PromptSeen: true},
			State{Attempt: "a", Process: proc, StartedReported: true}, []Event{started}},
		{"a prompt answered as accepted is also announced as persisted, before the start",
			State{Attempt: "a", Process: proc, PersistNotice: true}, Observation{Alive: true, PromptSeen: true},
			State{Attempt: "a", Process: proc, PersistNotice: true, StartedReported: true}, []Event{persisted, started}},
		{"a started turn that is still running reports nothing more",
			State{Attempt: "a", Process: proc, StartedReported: true}, Observation{Alive: true, PromptSeen: true},
			State{Attempt: "a", Process: proc, StartedReported: true}, nil},
		{"the process ends after a success: completed",
			State{Attempt: "a", Process: proc, StartedReported: true}, Observation{PromptSeen: true, Outcome: record.Succeeded},
			State{Attempt: "a", Process: proc, StartedReported: true, EndedReported: true}, []Event{ended(adapterproto.TurnCompleted)}},
		{"the process ends after an error result: failed",
			State{Attempt: "a", Process: proc, StartedReported: true}, Observation{PromptSeen: true, Outcome: record.Errored},
			State{Attempt: "a", Process: proc, StartedReported: true, EndedReported: true}, []Event{ended(adapterproto.TurnFailed)}},
		{"the process ends without any result: failed",
			State{Attempt: "a", Process: proc, StartedReported: true}, Observation{PromptSeen: true, Outcome: record.NoResult},
			State{Attempt: "a", Process: proc, StartedReported: true, EndedReported: true}, []Event{ended(adapterproto.TurnFailed)}},
		{"an interrupted process ends as interrupted, whatever its output says",
			State{Attempt: "a", Process: proc, StartedReported: true, Interrupted: true}, Observation{PromptSeen: true, Outcome: record.Succeeded},
			State{Attempt: "a", Process: proc, StartedReported: true, Interrupted: true, EndedReported: true}, []Event{ended(adapterproto.TurnInterrupted)}},
		{"the process ended between two looks: start and end are both reported, in order",
			State{Attempt: "a", Process: proc}, Observation{PromptSeen: true, Outcome: record.Succeeded},
			State{Attempt: "a", Process: proc, StartedReported: true, EndedReported: true}, []Event{started, ended(adapterproto.TurnCompleted)}},
		{"the process ends before the prompt reached the record: nothing is reported, ever",
			State{Attempt: "a", Process: proc}, Observation{},
			State{Attempt: "a", Process: proc, Abandoned: true}, nil},
		{"an ended turn stays ended",
			State{Attempt: "a", Process: proc, StartedReported: true, EndedReported: true}, Observation{PromptSeen: true},
			State{Attempt: "a", Process: proc, StartedReported: true, EndedReported: true}, nil},
		{"an abandoned turn stays abandoned even if the prompt shows up later",
			State{Attempt: "a", Process: proc, Abandoned: true}, Observation{PromptSeen: true},
			State{Attempt: "a", Process: proc, Abandoned: true}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, events := tc.in.Step(tc.obs)
			if got != tc.want {
				t.Errorf("state\n got %+v\nwant %+v", got, tc.want)
			}
			if !reflect.DeepEqual(events, tc.events) {
				t.Errorf("events\n got %v\nwant %v", events, tc.events)
			}
		})
	}
}

// A restarted adapter takes the turn up from its persisted state. Each row
// starts from a state as a previous adapter left it, through the JSON it was
// stored as.
func TestRestartAdoption(t *testing.T) {
	proc := Process{PID: 7, StartUnix: 100, StartKey: "100.5"}
	tests := []struct {
		name   string
		left   State
		obs    Observation
		events []Event
		done   bool
	}{
		{"it died before it saw the prompt reach the record, and the prompt is there: the start is reported late",
			State{Attempt: "a", Process: proc}, Observation{Alive: true, PromptSeen: true}, []Event{started}, false},
		{"the start was reported and the process still runs: adopted without a word",
			State{Attempt: "a", Process: proc, StartedReported: true}, Observation{Alive: true, PromptSeen: true}, nil, false},
		{"the start was reported and the process ended while nobody watched: the end is reported",
			State{Attempt: "a", Process: proc, StartedReported: true}, Observation{PromptSeen: true, Outcome: record.Succeeded},
			[]Event{ended(adapterproto.TurnCompleted)}, true},
		{"neither was reported and the process ended with the prompt on disk: both are reported",
			State{Attempt: "a", Process: proc, PersistNotice: true}, Observation{PromptSeen: true, Outcome: record.Errored},
			[]Event{persisted, started, ended(adapterproto.TurnFailed)}, true},
		{"the end was already reported: nothing is reported again",
			State{Attempt: "a", Process: proc, StartedReported: true, EndedReported: true}, Observation{PromptSeen: true}, nil, true},
		{"the process is gone and the prompt never reached the record",
			State{Attempt: "a", Process: proc}, Observation{}, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.left)
			if err != nil {
				t.Fatal(err)
			}
			var adopted State
			if err := json.Unmarshal(raw, &adopted); err != nil {
				t.Fatal(err)
			}
			if adopted != tc.left {
				t.Fatalf("the state did not survive its checkpoint: %+v", adopted)
			}
			got, events := adopted.Step(tc.obs)
			if !reflect.DeepEqual(events, tc.events) || got.Done() != tc.done {
				t.Fatalf("events %v done %v; want %v %v", events, got.Done(), tc.events, tc.done)
			}
			// Stepping again with the same look says nothing new.
			if _, again := got.Step(tc.obs); got.Done() && again != nil {
				t.Fatalf("a finished turn reported again: %v", again)
			}
		})
	}
}
