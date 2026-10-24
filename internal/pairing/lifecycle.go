package pairing

import (
	"io"
	"slices"
	"time"
)

// Tick advances everything that depends on time: online streams past their
// deadline end, requests that outlived their time to live expire, records past
// their retention are removed, expectations that ran out or lost their author
// are dropped, and the result exchanges of the initiator that are due are
// issued. The caller runs it on a timer and after every command and event, so a
// confirmation is followed by its exchange at once. rand supplies the jitter of
// the schedule.
func (s State) Tick(w World, rand io.Reader, now time.Time) (State, []Action, error) {
	if err := w.validate(); err != nil {
		return s, nil, err
	}
	now = wholeNow(now)
	next, actions := s, []Action(nil)

	for _, sess := range s.sessions {
		if now.Before(sess.deadline) {
			continue
		}
		if sess.phase == phaseAwaitReveal || sess.phase == phaseAwaitAccept {
			var acts []Action
			var err error
			if next, acts, err = next.endOnline(sess, RefusalMsg{}); err != nil {
				return s, nil, err
			}
			actions = append(actions, acts...)
			continue
		}
		next = next.withoutSession(sess.id)
		actions = append(actions, Close{sess.id})
	}

	live := slices.DeleteFunc(slices.Clone(next.expectations), func(e Expectation) bool {
		return e.ExpiredAt(w.Params, now) || !w.authorMayConfirm(e.Author, now)
	})
	if len(live) != len(next.expectations) {
		next = next.withExpectations(live)
		actions = append(actions, PersistExpectations{slices.Clone(live)})
	}

	for _, rec := range s.targets {
		switch {
		case rec.ExpiredAt(now):
			rec.Status, rec.EndedAt = TargetExpired, rec.ExpiresAt
			next = next.withTarget(rec)
			actions = append(actions, PersistTarget{rec}, Notify{Notice{Kind: NoticeEnded, ID: rec.ID(), Remote: rec.Transcript.terms.Initiator.node}})
		case rec.Terminal() && !now.Before(rec.EndedAt.Add(w.Params.ResultRetention)):
			next = next.withoutTarget(rec.ID())
			actions = append(actions, DeleteTarget{rec.ID()})
		}
	}

	for _, rec := range s.initiators {
		var acts []Action
		var err error
		if next, acts, err = next.tickInitiator(w, rec, now); err != nil {
			return s, nil, err
		}
		actions = append(actions, acts...)
	}
	ids := make([]ID, len(next.initiators))
	for i, rec := range next.initiators {
		ids[i] = rec.ID()
	}
	for _, id := range ids {
		rec, ok := next.Initiator(id)
		if !ok || !(rec.Status == InitiatorActive || rec.Status == InitiatorWithdrawing) || !next.planDue(rec.ID(), now) {
			continue
		}
		if rec.Status == InitiatorActive && rec.Signature == nil && !now.Before(rec.ExpiresAt) {
			continue
		}
		var acts []Action
		var err error
		if next, acts, err = next.poll(w, rec, now); err != nil {
			return s, nil, err
		}
		actions = append(actions, acts...)
	}
	return next, actions, nil
}

// tickInitiator applies the time rules to one initiator record: withdrawing a
// request whose peer has become a node this node would not pair with, ending an
// unconfirmed request that ran out, giving up on an answer that never came
// (revoking the signature if it may have left), and removing a record whose
// retention is over.
func (s State) tickInitiator(w World, rec InitiatorRecord, now time.Time) (State, []Action, error) {
	giveUpAt := rec.ExpiresAt.Add(w.Params.ResultRetention)
	switch {
	case rec.Status == InitiatorActive && w.peerUnfit(rec.Transcript.terms.Target.node, now):
		return s.withdraw(w, rec, WithdrawDeclined, now)
	case rec.Status == InitiatorActive && rec.Signature == nil && !now.Before(rec.ExpiresAt):
		return s.endInitiator(w, rec, InitiatorExpired, now)
	case rec.Status == InitiatorActive && rec.Signature != nil && !now.Before(giveUpAt):
		return s.endInitiator(w, rec, InitiatorGaveUp, now)
	case rec.Status == InitiatorWithdrawing && !now.Before(giveUpAt):
		rec.Status = InitiatorWithdrawn
		return s.withInitiator(rec), []Action{PersistInitiator{rec}}, nil
	case rec.Status.Terminal() && !now.Before(rec.EndedAt.Add(w.Params.ResultRetention)):
		return s.withoutInitiator(rec.ID()), []Action{DeleteInitiator{rec.ID()}}, nil
	}
	return s, nil, nil
}

// Recover brings a State restored from persisted records back to a consistent
// point after a restart. A request that was confirmed and signed by the
// initiator but not completed is completed (the credential is stored again,
// which changes nothing if it already was), and the revocation of a signature
// that may have left the node is stored again. The restart may come after the
// request would have expired: a request only holds a confirmation and a
// signature if both arrived while it still waited, so both decisions predate
// the expiry and completing repeats what was already decided. Streams and
// schedules did not survive: result exchanges start again at the next Tick.
func (s State) Recover(w World, now time.Time) (State, []Action, error) {
	if err := w.validate(); err != nil {
		return s, nil, err
	}
	now = wholeNow(now)
	next, actions := s, []Action(nil)
	for _, rec := range s.targets {
		if !rec.readyToComplete() {
			continue
		}
		done, acts, err := next.completeTarget(w, rec, now)
		if err != nil {
			return s, nil, err
		}
		next = next.withTarget(done)
		actions = append(actions, acts...)
	}
	for _, rec := range s.initiators {
		if !rec.needsRevocation() {
			continue
		}
		rev, err := revocationOf(w, rec)
		if err != nil {
			return s, nil, err
		}
		actions = append(actions, StoreRevocation{Revocation: rev.verified, Record: rev.record})
	}
	return next, actions, nil
}
