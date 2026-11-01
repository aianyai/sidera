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
	// RecordRecognized: every user line of the file carries a uuid and a session
	// id, the form the lookup of the attempt relies on. A record in another form
	// can hide the attempt, so it cannot show it absent.
	RecordRecognized bool
	// InRecord: a user record with the attempt's uuid is in the file.
	InRecord bool
	// SessionBusy: a turn process of the session is alive.
	SessionBusy bool

	// The remaining fields describe the turn the attempt started.

	// TurnAlive: the process that carried the attempt is alive.
	TurnAlive bool
	// Interrupted: the adapter interrupted the process; matters only when InRecord.
	Interrupted bool
	// Outcome is the last result of the attempt's output stream, read when the
	// process is not alive. A successful result means the process worked on the
	// prompt, so the prompt belongs in the record.
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
// an attempt that is in neither case is the absence concluded from the record,
// which needs a record that was read in full and in the form the lookup reads, and
// no running process that may still write it. A turn that succeeded on a prompt
// the record does not show tells the same as a form not read: the lookup misses
// what is there.
func Decide(in Input) Verdict {
	switch {
	case in.RecordReadable && in.InRecord:
		return Verdict{Verdict: adapterproto.VerdictFound, Grounds: adapterproto.GroundsAttemptInRecord, Turn: turnState(in)}
	case !in.Dispatched:
		return Verdict{Verdict: adapterproto.VerdictProvablyAbsent, Grounds: adapterproto.GroundsNeverDispatched}
	case !in.RecordReadable:
		return Verdict{Verdict: adapterproto.VerdictIndeterminate, Grounds: adapterproto.GroundsRecordUnreadable}
	case !in.RecordRecognized, in.Outcome == record.Succeeded:
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
