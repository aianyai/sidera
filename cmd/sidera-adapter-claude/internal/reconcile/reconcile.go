// Package reconcile decides what the adapter answers to prompt.reconcile: did
// the prompt attempt reach the session record. It is pure; the caller gathers
// the facts.
package reconcile

import (
	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Input is what the caller found out about one attempt.
type Input struct {
	// Dispatched: the attempt has a checkpoint. The adapter puts it before it
	// starts the turn process, so an attempt without one never started anything.
	Dispatched bool
	// RecordReadable: the session file was read in full. A file that is missing,
	// or that cannot be read, is not readable: nothing about the attempt follows
	// from it.
	RecordReadable bool
	// InRecord: a user record with the attempt's uuid is in the file.
	InRecord bool
	// SessionBusy: a turn process of the session is alive.
	SessionBusy bool

	// The remaining fields describe the turn the attempt started, and matter
	// only when InRecord.

	// TurnAlive: the process that carried the attempt is alive.
	TurnAlive bool
	// Interrupted: the adapter interrupted the process.
	Interrupted bool
	// Outcome is the last result of the attempt's output stream.
	Outcome record.Outcome
}

// Verdict is the answer: a verdict, the grounds it stands on and, for an
// attempt found in the record, the state of its turn.
type Verdict struct {
	Verdict adapterproto.ReconcileVerdict
	Grounds adapterproto.GroundsCode
	// Turn is set exactly for VerdictFound.
	Turn adapterproto.TurnState
}

// Decide applies the rules in the order of the evidence: the record decides
// first, because a prompt in it was delivered whatever else is true; then the
// checkpoint, which proves nothing was started when it is missing; and only for
// an attempt that is in neither case a running process, which may still write it,
// stands against calling it absent.
func Decide(in Input) Verdict {
	switch {
	case in.RecordReadable && in.InRecord:
		return Verdict{Verdict: adapterproto.VerdictFound, Grounds: adapterproto.GroundsAttemptInRecord, Turn: turnState(in)}
	case !in.Dispatched:
		return Verdict{Verdict: adapterproto.VerdictProvablyAbsent, Grounds: adapterproto.GroundsNeverDispatched}
	case !in.RecordReadable:
		return Verdict{Verdict: adapterproto.VerdictIndeterminate, Grounds: adapterproto.GroundsRecordUnreadable}
	case in.SessionBusy:
		return Verdict{Verdict: adapterproto.VerdictIndeterminate, Grounds: adapterproto.GroundsTurnRunning}
	}
	return Verdict{Verdict: adapterproto.VerdictProvablyAbsent, Grounds: adapterproto.GroundsRecordCompleteWithoutAttempt}
}

func turnState(in Input) adapterproto.TurnState {
	switch {
	case in.TurnAlive:
		return adapterproto.TurnStateRunning
	case in.Interrupted:
		return adapterproto.TurnStateInterrupted
	case in.Outcome == record.Succeeded:
		return adapterproto.TurnStateCompleted
	}
	return adapterproto.TurnStateFailed
}
