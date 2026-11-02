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
	// TurnRunning says the adapter's own books hold a turn of the thread that has
	// not ended. The server may hold a turn's events back while the thread's tools
	// start, so the thread can look idle to the server while the turn runs.
	TurnRunning bool
	// Queued says the prompt may still wait in the thread's queue of a session
	// that someone else runs, which takes it when its turn is over.
	Queued bool
	// Unreachable says the server could not be asked; the other fields are then
	// empty.
	Unreachable bool
}

// Judge answers prompt.reconcile.
//
// An attempt in the record is found whatever else holds. Absence is only
// concluded from a whole record of a thread that is not running a turn, by the
// server's word, the adapter's books or a turn of the record that is still in
// progress, or a queue that may hold the prompt: a turn still running may be about to
// record the prompt, and a record that was not read
// to its end, or whose format was not recognized, may hold it (the caller reports
// both as not complete). A server that was killed while a prompt was being taken
// in leaves a record that ends in an interrupted turn without the prompt, which is
// the absent case.
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
	case in.ThreadActive || in.TurnRunning || in.Queued || runningIn(in.Turns):
		return indeterminate(adapterproto.GroundsTurnRunning)
	case !in.Complete:
		return indeterminate(adapterproto.GroundsRecordUnreadable)
	}
	return adapterproto.ReconcileResult{
		Verdict: adapterproto.VerdictProvablyAbsent,
		Grounds: adapterproto.Grounds{Code: adapterproto.GroundsRecordCompleteWithoutAttempt},
	}
}

// runningIn reports whether a turn of the record is still in progress.
func runningIn(turns []Turn) bool {
	for _, t := range turns {
		if t.State == adapterproto.TurnStateRunning {
			return true
		}
	}
	return false
}

func indeterminate(code adapterproto.GroundsCode) adapterproto.ReconcileResult {
	return adapterproto.ReconcileResult{Verdict: adapterproto.VerdictIndeterminate, Grounds: adapterproto.Grounds{Code: code}}
}
