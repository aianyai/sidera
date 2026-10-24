package pairing

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/timing"
)

// TargetOpen registers a stream the target accepted, so its online segment is
// timed from the moment it opened. The first frame decides what the stream
// carries.
func (s State) TargetOpen(w World, sid SessionID, now time.Time) (State, []Action, error) {
	if err := w.validate(); err != nil {
		return s, nil, err
	}
	if s.sessionIndex(sid) >= 0 {
		return s, nil, fmt.Errorf("%w: session %d is already open", ErrWorld, sid)
	}
	now = wholeNow(now)
	return s.withSession(onlineSession{id: sid, phase: phaseFirst, deadline: now.Add(w.Params.OnlineTimeout)}), nil, nil
}

// TargetClosed forgets a stream that ended. A reservation it held is released.
func (s State) TargetClosed(sid SessionID) State { return s.withoutSession(sid) }

// refuse answers a request with a refusal and ends the stream.
func (s State) refuse(sid SessionID, reason RefusalReason, retry time.Duration) (State, []Action, error) {
	frame, err := RefusalMsg{Reason: reason, RetryAfter: retry}.Encode()
	if err != nil {
		return s, nil, err
	}
	return s.withoutSession(sid), []Action{Send{sid, frame}, Close{sid}}, nil
}

// abort ends a stream without an answer and leaves nothing behind.
func (s State) abort(sid SessionID) (State, []Action, error) {
	return s.withoutSession(sid), []Action{Close{sid}}, nil
}

// TargetReceive handles one frame on a stream the target opened. The first
// frame of a stream is either a commitment, which starts the online segment,
// or a fetch, which is answered at once. c is the connection the stream runs
// on; rand supplies the nonce the target reveals.
//
// A protocol failure is not an error: it is a refusal or an abort in the
// returned actions. An error means the call could not be made (a bad World,
// failing randomness or signer) and leaves the state as it was; the caller
// closes the stream.
func (s State) TargetReceive(w World, c Conn, sid SessionID, frame []byte, rand io.Reader, now time.Time) (State, []Action, error) {
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
		return s.abort(sid)
	}
	switch sess.phase {
	case phaseFirst:
		if err := c.validate(); err != nil {
			return s, nil, err
		}
		sess.conn = c
		first, err := DecodeOpening(frame)
		switch {
		case err != nil:
			return s.refuse(sid, RefusalProtocolError, 0)
		case first.Fetch != nil:
			next, reply, actions, err := s.targetFetch(w, c.Remote, *first.Fetch, now)
			if err != nil {
				return s, nil, err
			}
			out, err := reply.Encode()
			if err != nil {
				return s, nil, err
			}
			next = next.withoutSession(sid)
			return next, append(actions, Send{sid, out}, Close{sid}), nil
		default:
			return s.targetCommit(w, sess, *first.Commit, rand, now)
		}
	case phaseAwaitOpen:
		return s.targetOpen(w, sess, frame, now)
	}
	return s.abort(sid)
}

// targetCommit handles M1: the checks that decide whether to reveal, then the
// reveal (M2). It is the only place the target's nonce is made, and it is made
// only once every check passed.
func (s State) targetCommit(w World, sess onlineSession, m CommitMsg, rand io.Reader, now time.Time) (State, []Action, error) {
	c := sess.conn
	remote := c.Remote.node

	if skew := m.CreatedAt.Sub(now); skew > timing.ClockTolerance || skew < -timing.ClockTolerance {
		return s.refuse(sess.id, RefusalClockSkew, 0)
	}
	if w.peerFaultOf(remote, now, true) != peerFine || remote == c.Local.node {
		return s.refuse(sess.id, RefusalNotAccepted, 0)
	}
	terms := Terms{Protocol: c.Protocol, Initiator: c.Remote, Target: c.Local, CreatedAt: m.CreatedAt, NotAfter: m.NotAfter}
	if err := terms.validate(); err != nil {
		return s.refuse(sess.id, RefusalProtocolError, 0)
	}

	expectation, expected := s.liveExpectation(w, remote, now)
	if !expected {
		if paused, retry := s.limits.Paused(w.Params, now); paused {
			return s.refuse(sess.id, RefusalPaused, retry)
		}
		if ok, retry := s.limits.RevealAllowed(w.Params, now); !ok {
			return s.refuse(sess.id, RefusalPaused, retry)
		}
		if s.unexpectedWaiting(now, remote) >= w.Params.PendingCap {
			return s.refuse(sess.id, RefusalBusy, 0)
		}
	}
	if s.holdsSignature(remote, now) {
		return s.refuse(sess.id, RefusalBusy, 0)
	}

	nB, err := NewNonce(rand)
	if err != nil {
		return s, nil, err
	}
	reveal, err := RevealMsg{NB: nB}.Encode()
	if err != nil {
		return s, nil, err
	}

	var actions []Action
	next := s
	if !expected {
		limits, err := s.limits.SpendReveal(w.Params, now)
		if err != nil {
			return s, nil, err
		}
		next = next.withLimits(limits)
		actions = append(actions, PersistLimits{limits})
	}
	// One online session per initiator node: a newer one replaces an older one
	// that is still open, so a node's own retry never counts against the cap
	// twice.
	for _, other := range s.sessions {
		if other.id != sess.id && other.phase == phaseAwaitOpen && other.conn.Remote.node == remote {
			next = next.withoutSession(other.id)
			actions = append(actions, Close{other.id})
		}
	}
	sess.phase = phaseAwaitOpen
	sess.terms = terms
	sess.commit = m.Commitment
	sess.nB = nB
	sess.reserved = !expected
	if expected {
		sess.expected = expectation.Author
	}
	next = next.withSession(sess)
	return next, append(actions, Send{sess.id, reveal}), nil
}

// holdsSignature reports whether the initiator node has a request here that
// already holds its signature and still waits: it is not replaced by a new one.
func (s State) holdsSignature(initiator ident.NodeID, now time.Time) bool {
	for _, r := range s.targets {
		if r.Waiting(now) && r.InitiatorSignature != nil && r.Transcript.terms.Initiator.node == initiator {
			return true
		}
	}
	return false
}

// targetOpen handles M3: the commitment is checked against the target's own
// view, the request is saved, and only then is it accepted (M4).
func (s State) targetOpen(w World, sess onlineSession, frame []byte, now time.Time) (State, []Action, error) {
	m, err := DecodeOpen(frame)
	if err != nil {
		return s.abort(sess.id)
	}
	tr, err := Open(sess.terms, sess.commit, sess.nB, m.NA)
	if err != nil {
		return s.abort(sess.id)
	}
	initiator := sess.conn.Remote.node

	rec := TargetRecord{
		Transcript:    tr,
		ReceivedAt:    now,
		ExpiresAt:     now.Add(w.Params.PendingTTL),
		HighestSerial: sess.conn.Remote.serial,
		Status:        TargetPending,
	}
	next := s.withoutSession(sess.id)
	var actions []Action
	if !sess.expected.IsZero() {
		e, ok := s.liveExpectation(w, initiator, now)
		if !ok || e.Author != sess.expected {
			return s.abort(sess.id)
		}
		rec.Expected = e.Author
		rec.Confirmation = &Confirmation{Method: ConfirmedByExpectation, By: e.Author, Operation: e.Operation, At: now}
		next = next.consumeExpectation(initiator)
		actions = append(actions, PersistExpectations{next.Expectations()})
	}
	if next.targetIndex(rec.ID()) >= 0 {
		return s.abort(sess.id)
	}
	// A request of the same initiator that does not yet hold its signature is
	// replaced; one that does is kept and this one is dropped.
	var replaced []ID
	for _, r := range s.targets {
		if r.Transcript.terms.Initiator.node != initiator || !r.Waiting(now) {
			continue
		}
		if r.InitiatorSignature != nil {
			return s.abort(sess.id)
		}
		replaced = append(replaced, r.ID())
	}
	accepted, err := AcceptedMsg{ID: rec.ID(), ExpiresAt: rec.ExpiresAt}.Encode()
	if err != nil {
		return s, nil, err
	}
	next = next.withTarget(rec)
	actions = append(actions, PersistTarget{rec})
	for _, id := range replaced {
		next = next.withoutTarget(id)
		actions = append(actions, DeleteTarget{id})
	}
	actions = append(actions, Notify{Notice{Kind: NoticePending, ID: rec.ID(), Remote: initiator}},
		Send{sess.id, accepted}, Close{sess.id})
	return next, actions, nil
}

// targetFetch answers an initiator that asks for the result. peer is the
// authenticated party of the connection; it is the initiator of the request
// only if its node equals the transcript's (which fixes the identity key),
// whatever connection or network key it comes from, and its certificate serial is not
// older than one already seen. Anyone else gets exactly the answer for a
// request that does not exist.
func (s State) targetFetch(w World, peer Party, f FetchMsg, now time.Time) (State, ResultMsg, []Action, error) {
	unknown := ResultMsg{ID: f.ID, Status: StatusUnknown}
	i := s.targetIndex(f.ID)
	if i < 0 {
		return s, unknown, nil, nil
	}
	rec := s.targets[i]
	// The peer is the initiator when its node is: a node identifier is the
	// digest of the identity key, which a Party always derives it from, so the
	// identity key needs no comparison of its own. Neither the connection nor the
	// network key matters, only that the certificate serial is not older than one
	// already seen.
	if peer.node != rec.Transcript.terms.Initiator.node || peer.serial < rec.HighestSerial {
		return s, unknown, nil, nil
	}

	var actions []Action
	changed := false
	if peer.serial > rec.HighestSerial {
		rec.HighestSerial = peer.serial
		changed = true
	}
	if rec.ExpiredAt(now) {
		rec.Status, rec.EndedAt = TargetExpired, rec.ExpiresAt
		changed = true
	}
	if rec.Status == TargetPending {
		switch {
		case f.Withdraw != WithdrawNone:
			rec.Status, rec.EndedAt = TargetCancelled, now
			changed = true
		case f.Signed != nil && rec.InitiatorSignature == nil && checkPartial(rec.Transcript, *f.Signed) == nil:
			rec.InitiatorSignature = f.Signed
			changed = true
		}
	}
	if changed {
		actions = append(actions, PersistTarget{rec})
	}
	if rec.readyToComplete() {
		done, doneActions, err := s.completeTarget(w, rec, now)
		if err != nil {
			return s, ResultMsg{}, nil, err
		}
		rec, actions = done, append(actions, doneActions...)
	}
	return s.withTarget(rec), targetResult(rec), actions, nil
}

// targetResult is the answer the target gives for a record.
func targetResult(rec TargetRecord) ResultMsg {
	id := rec.ID()
	switch rec.Status {
	case TargetComplete:
		c := rec.Completed.Credential
		return ResultMsg{ID: id, Status: StatusComplete, Credential: &c}
	case TargetRejected:
		return ResultMsg{ID: id, Status: StatusRejected, Rejection: rec.Rejection}
	case TargetCancelled:
		return ResultMsg{ID: id, Status: StatusCancelled}
	case TargetExpired:
		return ResultMsg{ID: id, Status: StatusExpired}
	}
	if rec.InitiatorSignature == nil {
		return ResultMsg{ID: id, Status: StatusAwaitingInitiatorSignature}
	}
	return ResultMsg{ID: id, Status: StatusAwaitingTarget}
}

// completeTarget finishes a request that is confirmed and holds the
// initiator's signature: the pair is judged once more (a node retired or
// locally revoked since the confirmation is not signed for), the initiator's
// record is checked again, and the target adds its own signature. The
// credential is stored before the record is marked complete, so a crash
// between the two is repaired by running this again, which yields the same
// bytes: the signature is deterministic and the digest does not cover it.
func (s State) completeTarget(w World, rec TargetRecord, now time.Time) (TargetRecord, []Action, error) {
	if !rec.readyToComplete() {
		return rec, nil, errors.New("pairing: completing a request that is not ready")
	}
	terms := rec.Transcript.terms
	if err := w.signsFor(terms.Target); err != nil {
		return rec, nil, err
	}
	if w.peerFaultOf(terms.Initiator.node, now, false) != peerFine {
		rec.Status, rec.Rejection, rec.EndedAt = TargetRejected, WithdrawDeclined, now
		return rec, []Action{PersistTarget{rec}, Notify{Notice{Kind: NoticeEnded, ID: rec.ID(), Remote: terms.Initiator.node}}}, nil
	}
	if err := checkPartial(rec.Transcript, *rec.InitiatorSignature); err != nil {
		return rec, nil, err
	}
	signed, err := record.CoSign(credential.Descriptor, *rec.InitiatorSignature, w.Identity)
	if err != nil {
		return rec, nil, fmt.Errorf("pairing: signing the credential: %w", err)
	}
	ver, err := checkComplete(rec.Transcript, signed)
	if err != nil {
		return rec, nil, err
	}
	rec.Status = TargetComplete
	rec.EndedAt = now
	rec.Completed = &Completion{At: now, Credential: signed}
	return rec, []Action{
		StoreCredential{Credential: ver, Record: signed},
		PersistTarget{rec},
		Notify{Notice{Kind: NoticeCompleted, ID: rec.ID(), Remote: terms.Initiator.node}},
	}, nil
}
