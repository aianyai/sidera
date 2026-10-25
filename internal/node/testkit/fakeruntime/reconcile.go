package fakeruntime

// reconcileFromRecord answers what the record says about an attempt, for a
// session whose process is gone: the record is complete and nothing will be
// added to it.
func reconcileFromRecord(events []Event, attempt string) Reconciliation {
	rec := Reconciliation{WriterStopped: true}
	turnOf := map[string]string{} // attempt -> turn
	state := map[string]string{}  // turn -> state
	var pending []string          // prompts that wait for a turn
	found := false
	for _, e := range events {
		switch e.Kind {
		case EventPrompt:
			if e.Attempt == attempt {
				found = true
			}
			pending = append(pending, e.Attempt)
		case EventTurnStarted:
			state[e.Turn] = "running"
			for _, a := range pending {
				turnOf[a] = e.Turn
			}
			pending = nil
		case EventTurnEnded:
			state[e.Turn] = e.Final
		}
	}
	// The writer has stopped: a turn that was running when it did will never end,
	// and the adapter that watched it die ended it as failed.
	for turn, st := range state {
		if st == "running" {
			state[turn] = FinalFailed
		}
	}
	if !found {
		return rec
	}
	rec.Found = true
	if turn, ok := turnOf[attempt]; ok {
		rec.Turn, rec.TurnState = turn, state[turn]
	}
	return rec
}
