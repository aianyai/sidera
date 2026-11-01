package reconcile

import (
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestJudge(t *testing.T) {
	record := []Turn{
		{Ref: "u3", State: adapterproto.TurnStateInterrupted},
		{Ref: "u2", State: adapterproto.TurnStateCompleted, Attempts: []string{"a2"}},
		{Ref: "u1", State: adapterproto.TurnStateCompleted, Attempts: []string{"a1"}},
	}
	// A turn that is still in progress, as the record shows it while the server has
	// not yet reported the thread active.
	running := append([]Turn{{Ref: "u4", State: adapterproto.TurnStateRunning, Attempts: []string{"a4"}}}, record...)
	for _, tc := range []struct {
		name    string
		in      Input
		verdict adapterproto.ReconcileVerdict
		grounds adapterproto.GroundsCode
		turn    string
		state   adapterproto.TurnState
	}{
		{"found in a finished turn", Input{Attempt: "a2", Turns: record, Complete: true},
			adapterproto.VerdictFound, adapterproto.GroundsAttemptInRecord, "u2", adapterproto.TurnStateCompleted},
		{"found in a running turn", Input{Attempt: "a4", Turns: running},
			adapterproto.VerdictFound, adapterproto.GroundsAttemptInRecord, "u4", adapterproto.TurnStateRunning},
		{"absent from a whole record of an idle thread", Input{Attempt: "zz", Turns: record, Complete: true},
			adapterproto.VerdictProvablyAbsent, adapterproto.GroundsRecordCompleteWithoutAttempt, "", ""},
		{"absent from an empty record", Input{Attempt: "zz", Complete: true},
			adapterproto.VerdictProvablyAbsent, adapterproto.GroundsRecordCompleteWithoutAttempt, "", ""},
		{"a turn is running in the thread", Input{Attempt: "zz", Turns: record, Complete: true, ThreadActive: true},
			adapterproto.VerdictIndeterminate, adapterproto.GroundsTurnRunning, "", ""},
		{"the adapter's own turn has not ended", Input{Attempt: "zz", Turns: record, Complete: true, TurnRunning: true},
			adapterproto.VerdictIndeterminate, adapterproto.GroundsTurnRunning, "", ""},
		{"a turn of the record is in progress", Input{Attempt: "zz", Turns: running, Complete: true},
			adapterproto.VerdictIndeterminate, adapterproto.GroundsTurnRunning, "", ""},
		{"the record was not read to its end", Input{Attempt: "zz", Turns: record},
			adapterproto.VerdictIndeterminate, adapterproto.GroundsRecordUnreadable, "", ""},
		{"the server cannot be asked", Input{Attempt: "a2", Unreachable: true},
			adapterproto.VerdictIndeterminate, adapterproto.GroundsNativeUnreachable, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Judge(tc.in)
			if err := got.Validate(); err != nil {
				t.Fatalf("not a valid result: %v", err)
			}
			if got.Verdict != tc.verdict || got.Grounds.Code != tc.grounds {
				t.Fatalf("got %s/%s, want %s/%s", got.Verdict, got.Grounds.Code, tc.verdict, tc.grounds)
			}
			if tc.turn != "" && (got.Turn == nil || string(got.Turn.Ref) != tc.turn || got.Turn.State != tc.state) {
				t.Fatalf("turn = %+v, want %s %s", got.Turn, tc.turn, tc.state)
			}
		})
	}
}
