// Package reconcile judges whether a prompt attempt reached the session, from what
// the thread's record and the adapter's own memory show. The judgement is a pure
// function; reading the record is the caller's.
package reconcile

import "github.com/aianyai/sidera/sdk/go/adapterproto"

// Turn is one turn of the record: its reference, how it stands and the attempts of
// the prompts it holds.
type Turn struct {
	Ref      string
	State    adapterproto.TurnState
	Attempts []string
}

// Input is everything the judgement rests on.
type Input struct {
	Attempt string
	// Turns is the record, newest first.
	Turns []Turn
	// Complete says the whole record was read, to its first turn.
	Complete bool
	// ThreadActive says the server reports a turn running in the thread.
	ThreadActive bool
	// TurnRunning says the adapter knows a turn it started for this attempt that
	// has not ended.
	TurnRunning bool
	// Unreachable says the server could not be asked; the other fields are then
	// empty.
	Unreachable bool
	// VersionUntested says the Codex version is outside the range the adapter was
	// tested with, so a record that does not show the attempt proves little.
	VersionUntested bool
}

// Judge answers prompt.reconcile.
//
// An attempt in the record is found whatever else holds. Absence is only
// concluded from a whole record of a thread that is not running a turn, on a
// version the record's format is known for: a turn still running may be about to
// record the prompt, and a record that was not read to its end, or that a newer
// version may write differently, may hold it. A server that was killed while a
// prompt was being taken in leaves a record that ends in an interrupted turn
// without the prompt, which is the absent case.
func Judge(in Input) adapterproto.ReconcileResult {
	if in.Unreachable {
		return indeterminate(adapterproto.GroundsNativeUnreachable)
	}
	for _, t := range in.Turns {
		for _, a := range t.Attempts {
			if a == in.Attempt {
				return adapterproto.ReconcileResult{
					Verdict: adapterproto.VerdictFound,
					Turn:    &adapterproto.TurnReport{Ref: adapterproto.TurnRef(t.Ref), State: t.State},
					Grounds: adapterproto.Grounds{Code: adapterproto.GroundsAttemptInRecord},
				}
			}
		}
	}
	switch {
	case in.VersionUntested:
		return indeterminate(adapterproto.GroundsRuntimeVersionUntested)
	case in.ThreadActive || in.TurnRunning:
		return indeterminate(adapterproto.GroundsTurnRunning)
	case !in.Complete:
		return indeterminate(adapterproto.GroundsRecordUnreadable)
	}
	return adapterproto.ReconcileResult{
		Verdict: adapterproto.VerdictProvablyAbsent,
		Grounds: adapterproto.Grounds{Code: adapterproto.GroundsRecordCompleteWithoutAttempt},
	}
}

func indeterminate(code adapterproto.GroundsCode) adapterproto.ReconcileResult {
	return adapterproto.ReconcileResult{Verdict: adapterproto.VerdictIndeterminate, Grounds: adapterproto.Grounds{Code: code}}
}
