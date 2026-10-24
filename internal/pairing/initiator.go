package pairing

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/timing"
)

// endOnline ends an initiator's online segment with a notice for its owner.
// What the owner is told depends on whether this node had opened its nonce: if
// it had, the target may have counted a guess whatever happened after, and the
// owner learns that an attempt was used.
func (s State) endOnline(sess onlineSession, refusal RefusalMsg) (State, []Action, error) {
	kind := NoticeNotStarted
	if sess.phase == phaseAwaitAccept {
		kind = NoticeAttemptUsed
	}
	notice := Notice{Kind: kind, Remote: sess.conn.Remote.node, Refusal: refusal.Reason, RetryAfter: refusal.RetryAfter}
	return s.withoutSession(sess.id), []Action{Notify{notice}, Close{sess.id}}, nil
}

// InitiatorClosed handles a stream of the online segment that ended without a
// final frame: the connection dropped or the target closed it.
func (s State) InitiatorClosed(sid SessionID) (State, []Action, error) {
	i := s.sessionIndex(sid)
	if i < 0 {
		return s, nil, nil
	}
	sess := s.sessions[i]
	kind := NoticeNotStarted
	if sess.phase == phaseAwaitAccept {
		kind = NoticeAttemptUsed
	}
	return s.withoutSession(sid), []Action{Notify{Notice{Kind: kind, Remote: sess.conn.Remote.node}}}, nil
}

// InitiatorReceive handles one frame of the target on a stream this node
// started. The sequence is strict: a valid reveal is read before the nonce is
// opened, and a frame out of order ends the segment. This node never starts the
// online segment again by itself; an owner who sees it end starts it by hand.
func (s State) InitiatorReceive(w World, sid SessionID, frame []byte, now time.Time) (State, []Action, error) {
	if err := w.validate(); err != nil {
		return s, nil, err
	}
	now = wholeNow(now)
	i := s.sessionIndex(sid)
	if i < 0 {
		return s, []Action{Close{sid}}, nil
	}
	sess := s.sessions[i]
	if !now.Before(sess.deadline) {
		return s.endOnline(sess, RefusalMsg{})
	}
	if r, err := DecodeRefusal(frame); err == nil {
		return s.endOnline(sess, r)
	}
	switch sess.phase {
	case phaseAwaitReveal:
		return s.initiatorReveal(sess, frame)
	case phaseAwaitAccept:
		return s.initiatorAccepted(w, sess, frame, now)
	}
	return s.endOnline(sess, RefusalMsg{})
}

// initiatorReveal handles M2 and answers with M3. The nonce is opened only
// after a valid reveal was read, and the commitment the target will check is
// the one this node made.
func (s State) initiatorReveal(sess onlineSession, frame []byte) (State, []Action, error) {
	m, err := DecodeReveal(frame)
	if err != nil {
		return s.endOnline(sess, RefusalMsg{})
	}
	tr, err := Open(sess.terms, sess.commit, m.NB, sess.nA)
	if err != nil {
		return s.endOnline(sess, RefusalMsg{})
	}
	open, err := OpenMsg{NA: sess.nA}.Encode()
	if err != nil {
		return s, nil, err
	}
	sess.phase = phaseAwaitAccept
	sess.transcript = tr
	sess.nA = Nonce{}
	return s.withSession(sess), []Action{Send{sess.id, open}}, nil
}

// initiatorAccepted handles M4: the identifier must be the one this node
// computed, and the request is saved before its code can be shown.
func (s State) initiatorAccepted(w World, sess onlineSession, frame []byte, now time.Time) (State, []Action, error) {
	m, err := DecodeAccepted(frame)
	if err != nil || m.ID != sess.transcript.id {
		return s.endOnline(sess, RefusalMsg{})
	}
	expires := m.ExpiresAt
	if limit := now.Add(w.Params.PendingTTL + timing.ClockTolerance); expires.After(limit) {
		expires = limit
	}
	if !expires.After(now) || s.initiatorIndex(m.ID) >= 0 {
		return s.endOnline(sess, RefusalMsg{})
	}
	rec := InitiatorRecord{
		Transcript: sess.transcript,
		ReceivedAt: now,
		ExpiresAt:  expires,
		Addresses:  sess.addresses,
		Status:     InitiatorActive,
	}
	next := s.withoutSession(sess.id)
	var actions []Action
	if !sess.expected.IsZero() {
		e, ok := s.liveExpectation(w, sess.conn.Remote.node, now)
		if !ok || e.Author != sess.expected {
			return s.endOnline(sess, RefusalMsg{})
		}
		rec.Expected = e.Author
		rec.Confirmation = &Confirmation{Method: ConfirmedByExpectation, By: e.Author, Operation: e.Operation, At: now}
		signature, err := signInitiator(w, rec.Transcript)
		if err != nil {
			return s, nil, err
		}
		rec.Signature = &signature
		next = next.consumeExpectation(sess.conn.Remote.node)
		actions = append(actions, PersistExpectations{next.Expectations()})
	}
	next = next.withInitiator(rec).withPlan(fetchPlan{id: rec.ID(), nextAt: now})
	actions = append(actions,
		PersistInitiator{rec},
		Notify{Notice{Kind: NoticeCodeReady, ID: rec.ID(), Remote: sess.conn.Remote.node}},
		Close{sess.id})
	return next, actions, nil
}

// signInitiator makes the credential record with this node's signature only,
// from the credential this node computed itself.
func signInitiator(w World, t Transcript) (record.Record, error) {
	if err := w.signsFor(t.terms.Initiator); err != nil {
		return record.Record{}, err
	}
	r, err := record.Seal(credential.Descriptor, t.credential, w.Identity)
	if err != nil {
		return record.Record{}, fmt.Errorf("pairing: signing the credential: %w", err)
	}
	return r, nil
}

// backoff is the delay before the next result exchange after n exchanges since
// the last reset: FetchBackoffMin doubling to FetchBackoffMax, with the upper
// half jittered so initiators that began together do not stay in step.
func backoff(p Params, n int, rand io.Reader) time.Duration {
	d := p.FetchBackoffMin
	for i := 1; i < n && d < p.FetchBackoffMax; i++ {
		d *= 2
	}
	d = min(d, p.FetchBackoffMax)
	var b [8]byte
	if _, err := io.ReadFull(rand, b[:]); err != nil {
		return d
	}
	frac := float64(binary.BigEndian.Uint64(b[:])>>11) / (1 << 53)
	jittered := d/2 + time.Duration(frac*float64(d/2))
	return max(jittered, p.FetchBackoffMin)
}

// poll starts an exchange for the record: it returns the next state and the
// actions to perform. A record that must
// release its signature is persisted as released before the exchange is
// issued.
func (s State) poll(w World, rec InitiatorRecord, now time.Time) (State, []Action, error) {
	var actions []Action
	f := FetchMsg{ID: rec.ID()}
	switch {
	case rec.Status == InitiatorWithdrawing:
		f.Withdraw = rec.Reason
	case rec.Signature != nil:
		f.Signed = rec.Signature
		if !rec.Released {
			rec.Released = true
			actions = append(actions, PersistInitiator{rec})
		}
	}
	frame, err := f.Encode()
	if err != nil {
		return s, nil, err
	}
	plan := fetchPlan{id: rec.ID()}
	if i := s.planIndex(rec.ID()); i >= 0 {
		plan = s.plans[i]
	}
	plan.attempts++
	plan.inflightUntil = now.Add(w.Params.FetchTimeout)
	plan.nextAt = plan.inflightUntil
	next := s.withInitiator(rec).withPlan(plan)
	return next, append(actions, FetchRequest{ID: rec.ID(), Frame: frame, Addresses: rec.Addresses}), nil
}

// planDue reports whether the record's next exchange is due at now.
func (s State) planDue(id ID, now time.Time) bool {
	i := s.planIndex(id)
	return i < 0 || !now.Before(s.plans[i].nextAt)
}

// OnFetchFailed handles an exchange that could not be made or answered in
// time: the next one waits, longer each time.
func (s State) OnFetchFailed(w World, id ID, rand io.Reader, now time.Time) (State, []Action, error) {
	if err := w.validate(); err != nil {
		return s, nil, err
	}
	now = wholeNow(now)
	i := s.planIndex(id)
	if i < 0 {
		return s, nil, nil
	}
	plan := s.plans[i]
	plan.inflightUntil = time.Time{}
	plan.nextAt = now.Add(backoff(w.Params, plan.attempts, rand))
	return s.withPlan(plan), nil, nil
}

// OnFetchResult handles the answer to a FetchRequest. responder is the
// authenticated party of the connection the answer came on: it must be the
// target of the request, by node and identity key, whatever network key it
// uses now. An answer from anyone else, malformed, or for another request is
// treated as a failed exchange.
func (s State) OnFetchResult(w World, id ID, responder Party, frame []byte, rand io.Reader, now time.Time) (State, []Action, error) {
	if err := w.validate(); err != nil {
		return s, nil, err
	}
	now = wholeNow(now)
	i := s.initiatorIndex(id)
	if i < 0 {
		return s, nil, nil
	}
	rec := s.initiators[i]
	target := rec.Transcript.terms.Target
	msg, err := DecodeResult(frame)
	if err != nil || msg.ID != id || responder.node != target.node || responder.identity != target.identity {
		return s.OnFetchFailed(w, id, rand, now)
	}
	if rec.Status != InitiatorActive && rec.Status != InitiatorWithdrawing {
		return s, nil, nil
	}

	plan := fetchPlan{id: id}
	if j := s.planIndex(id); j >= 0 {
		plan = s.plans[j]
	}
	plan.inflightUntil = time.Time{}
	plan.nextAt = now.Add(backoff(w.Params, plan.attempts, rand))
	if hint := min(msg.RetryAfter, w.Params.FetchBackoffMax); now.Add(hint).After(plan.nextAt) {
		plan.nextAt = now.Add(hint)
	}
	next := s.withPlan(plan)

	if rec.Status == InitiatorWithdrawing {
		// The target answered: it knows of the withdrawal, or has nothing left
		// to be told. Whatever it says, the signature is already revoked.
		rec.Status = InitiatorWithdrawn
		return next.withInitiator(rec), []Action{PersistInitiator{rec}}, nil
	}

	var end InitiatorStatus
	switch msg.Status {
	case StatusAwaitingInitiatorSignature, StatusAwaitingTarget:
		return next, nil, nil
	case StatusComplete:
		return next.initiatorComplete(w, rec, *msg.Credential, rand, now)
	case StatusRejected:
		rec.Reason = msg.Rejection
		end = InitiatorRejected
	case StatusExpired:
		end = InitiatorExpired
	case StatusCancelled:
		end = InitiatorCancelled
	case StatusUnknown:
		end = InitiatorUnknown
	}
	return next.endInitiator(w, rec, end, now)
}

// initiatorComplete handles the complete credential. It is stored only if it
// is exactly the credential this node computed, signed by exactly the two
// identity keys, and the peer is still one this node would pair with; if the
// peer was retired or locally revoked meanwhile, the pairing ends here and the
// signature is revoked.
func (s State) initiatorComplete(w World, rec InitiatorRecord, cred record.Record, rand io.Reader, now time.Time) (State, []Action, error) {
	ver, err := checkComplete(rec.Transcript, cred)
	if err != nil {
		// A target that returns something else than the credential has not
		// completed anything; ask again later.
		plan := s.plans[s.planIndex(rec.ID())]
		plan.nextAt = now.Add(backoff(w.Params, plan.attempts, rand))
		return s.withPlan(plan), nil, nil
	}
	if w.peerFaultOf(rec.Transcript.terms.Target.node, now, false) != peerFine {
		rec.Reason = WithdrawDeclined
		return s.endInitiator(w, rec, InitiatorRejected, now)
	}
	rec.Status, rec.EndedAt = InitiatorComplete, now
	rec.Released = true
	rec.Completed = &Completion{At: now, Credential: cred}
	return s.withInitiator(rec), []Action{
		StoreCredential{Credential: ver, Record: cred},
		PersistInitiator{rec},
		Notify{Notice{Kind: NoticeCompleted, ID: rec.ID(), Remote: rec.Transcript.terms.Target.node}},
	}, nil
}

// endInitiator ends an active request without a credential. If the signature
// may have left this node, its revocation is stored first, and the record is
// persisted as ended after; a crash between the two is repaired by Recover.
func (s State) endInitiator(w World, rec InitiatorRecord, status InitiatorStatus, now time.Time) (State, []Action, error) {
	rec.Status, rec.EndedAt = status, now
	var actions []Action
	if rec.needsRevocation() {
		rev, err := revocationOf(w, rec)
		if err != nil {
			return s, nil, err
		}
		actions = append(actions, StoreRevocation{rev})
	}
	actions = append(actions, PersistInitiator{rec})
	remote := rec.Transcript.terms.Target.node
	actions = append(actions, Notify{Notice{Kind: NoticeEnded, ID: rec.ID(), Remote: remote}})
	if rec.needsRevocation() {
		actions = append(actions, Notify{Notice{Kind: NoticeRevoked, ID: rec.ID(), Remote: remote}})
	}
	return s.withInitiator(rec), actions, nil
}

// revocationOf is the revocation of the credential the record's transcript
// computes, signed by this node's identity key. The credential is known
// locally, so the revocation does not wait for the target or depend on it.
func revocationOf(w World, rec InitiatorRecord) (record.Record, error) {
	if err := w.signsFor(rec.Transcript.terms.Initiator); err != nil {
		return record.Record{}, err
	}
	r, err := credential.IssueRevocation(rec.Transcript.credential, w.Identity)
	if err != nil {
		return record.Record{}, fmt.Errorf("pairing: revoking the credential: %w", err)
	}
	return r, nil
}
