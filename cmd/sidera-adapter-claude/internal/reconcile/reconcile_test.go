package reconcile

import (
	"testing"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func TestDecide(t *testing.T) {
	const (
		found  = adapterproto.VerdictFound
		absent = adapterproto.VerdictProvablyAbsent
		maybe  = adapterproto.VerdictIndeterminate
	)
	tests := []struct {
		name    string
		in      Input
		verdict adapterproto.ReconcileVerdict
		grounds adapterproto.GroundsCode
		turn    adapterproto.TurnState
	}{
		{"in the record with its process alive: found, running",
			Input{Dispatched: true, RecordReadable: true, RecordRecognized: true, InRecord: true, TurnAlive: true, SessionBusy: true},
			found, adapterproto.GroundsAttemptInRecord, adapterproto.TurnStateRunning},
		{"in the record, process gone, last result a success: completed",
			Input{Dispatched: true, RecordReadable: true, RecordRecognized: true, InRecord: true, Outcome: record.Succeeded},
			found, adapterproto.GroundsAttemptInRecord, adapterproto.TurnStateCompleted},
		{"in the record, process gone, last result an error: failed",
			Input{Dispatched: true, RecordReadable: true, RecordRecognized: true, InRecord: true, Outcome: record.Errored},
			found, adapterproto.GroundsAttemptInRecord, adapterproto.TurnStateFailed},
		{"in the record, process gone, no result at all: failed",
			Input{Dispatched: true, RecordReadable: true, RecordRecognized: true, InRecord: true, Outcome: record.NoResult},
			found, adapterproto.GroundsAttemptInRecord, adapterproto.TurnStateFailed},
		{"in the record, process gone after an interrupt: interrupted",
			Input{Dispatched: true, RecordReadable: true, RecordRecognized: true, InRecord: true, Interrupted: true, Outcome: record.Errored},
			found, adapterproto.GroundsAttemptInRecord, adapterproto.TurnStateInterrupted},
		{"in the record, process gone after an interrupt that came too late: completed",
			Input{Dispatched: true, RecordReadable: true, RecordRecognized: true, InRecord: true, Interrupted: true, Outcome: record.Succeeded},
			found, adapterproto.GroundsAttemptInRecord, adapterproto.TurnStateCompleted},
		{"in the record while another turn runs: the attempt's own turn is over",
			Input{Dispatched: true, RecordReadable: true, RecordRecognized: true, InRecord: true, SessionBusy: true, Outcome: record.Succeeded},
			found, adapterproto.GroundsAttemptInRecord, adapterproto.TurnStateCompleted},
		{"in the record without a checkpoint is still found",
			Input{RecordReadable: true, RecordRecognized: true, InRecord: true, Outcome: record.Succeeded},
			found, adapterproto.GroundsAttemptInRecord, adapterproto.TurnStateCompleted},

		{"never dispatched: no checkpoint, not in the record",
			Input{RecordReadable: true, RecordRecognized: true}, absent, adapterproto.GroundsNeverDispatched, ""},
		{"never dispatched even when the record cannot be read",
			Input{}, absent, adapterproto.GroundsNeverDispatched, ""},
		{"never dispatched even when a turn of another attempt runs",
			Input{RecordReadable: true, RecordRecognized: true, SessionBusy: true}, absent, adapterproto.GroundsNeverDispatched, ""},

		{"dispatched, record unreadable: cannot tell",
			Input{Dispatched: true}, maybe, adapterproto.GroundsRecordUnreadable, ""},
		{"dispatched, a user line without a uuid or session id: the record is in a form not read, so absence is not known",
			Input{Dispatched: true, RecordReadable: true}, maybe, adapterproto.GroundsRecordUnreadable, ""},
		{"dispatched, its turn ended in a success yet its uuid is not in the record: the record is in a form not read",
			Input{Dispatched: true, RecordReadable: true, RecordRecognized: true, Outcome: record.Succeeded}, maybe, adapterproto.GroundsRecordUnreadable, ""},
		{"dispatched, its turn ended in an error without its uuid in the record: the process may have failed before the prompt was written",
			Input{Dispatched: true, RecordReadable: true, RecordRecognized: true, Outcome: record.Errored}, absent, adapterproto.GroundsRecordCompleteWithoutAttempt, ""},
		{"dispatched, a turn process runs: it may still write the prompt",
			Input{Dispatched: true, RecordReadable: true, RecordRecognized: true, SessionBusy: true}, maybe, adapterproto.GroundsTurnRunning, ""},
		{"dispatched, nothing runs, the whole record scanned: absent",
			Input{Dispatched: true, RecordReadable: true, RecordRecognized: true}, absent, adapterproto.GroundsRecordCompleteWithoutAttempt, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.in)
			if got.Verdict != tc.verdict || got.Grounds != tc.grounds || got.Turn != tc.turn {
				t.Fatalf("got %+v, want verdict %s grounds %s turn %q", got, tc.verdict, tc.grounds, tc.turn)
			}
			// The pair must be one the protocol accepts together.
			res := adapterproto.ReconcileResult{Verdict: got.Verdict, Grounds: adapterproto.Grounds{Code: got.Grounds}}
			if got.Turn != "" {
				res.Turn = &adapterproto.TurnReport{Ref: "t", State: got.Turn}
			}
			if err := res.Validate(); err != nil {
				t.Fatalf("not a valid result: %v", err)
			}
		})
	}
}
