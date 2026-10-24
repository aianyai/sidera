package create

import (
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// TestCreateFailureOfEveryReason checks the mapping from a failure reason to the
// reason session.create reports against a table written out by hand, for every
// reason the package names and for values it does not.
func TestCreateFailureOfEveryReason(t *testing.T) {
	const none = adapterproto.CreateFailure("")
	table := []struct {
		reason Reason
		rule   string
		want   adapterproto.CreateFailure
	}{
		{ReasonNotStarted, "not a failure", none},
		{ReasonProcessEndedWithoutSession, "not a failure", none},
		{ReasonRecordWithoutFirstPrompt, "not a failure", none},
		{ReasonTurnRunning, "not a failure", none},
		{ReasonMultipleProcesses, "two writers: control is unknown", "control_unknown"},
		{ReasonProcessFoundByCommandLine, "not a failure", none},
		{ReasonSessionRecordHasID, "not a failure", none},
		{ReasonFirstPromptRecorded, "not a failure", none},
		{ReasonAlreadyReady, "not a failure", none},
		{ReasonStartTimedOut, "the stuck process still runs: control is unknown", "control_unknown"},
		{ReasonStartsExhausted, "every process ended and the runtime cannot start", "native_error"},
		{ReasonOutsideWriter, "a writer that is not ours: control is unknown", "control_unknown"},
		{ReasonAlreadyFailed, "not a failure reason of its own: the stored one is reported", none},
		{0, "unset", none},
		{-1, "negative", none},
		{ReasonAlreadyFailed + 1, "beyond the last reason", none},
		{1000, "far beyond", none},
	}
	listed := map[Reason]bool{}
	for _, row := range table {
		listed[row.reason] = true
		got, ok := row.reason.CreateFailure()
		if got != row.want || ok != (row.want != none) {
			t.Errorf("%v (%s): CreateFailure() = %q, %v; want %q", row.reason, row.rule, got, ok, row.want)
		}
		if ok && !got.Valid() {
			t.Errorf("%v maps to %q, which is no CreateFailure of the protocol", row.reason, got)
		}
		if row.reason.endsAttempt() != ok {
			t.Errorf("%v: endsAttempt() = %v but CreateFailure() ok = %v: two sources for one fact", row.reason, row.reason.endsAttempt(), ok)
		}
		if ok {
			res := adapterproto.SessionCreateResult{Outcome: adapterproto.CreateFailed, Reason: got}
			if err := res.Validate(); err != nil {
				t.Errorf("%v: the failed result with %q is invalid: %v", row.reason, got, err)
			}
		}
	}
	for reason := range reasonNames {
		if !listed[reason] {
			t.Errorf("reason %v has no row in the table", reason)
		}
	}
}
