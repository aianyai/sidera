package pairing

import (
	"crypto/sha256"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/frame"
	"github.com/aianyai/sidera/internal/ident"
)

// Request is who asks and under which operation identifier. The operation kind
// is not the caller's to name: each command below is authorized as the one
// operation its meaning requires.
type Request struct {
	Actor admin.Actor
	ID    admin.OperationID
}

// Disposition says what became of a management request.
type Disposition int

const (
	// DispositionRefused: not authorized, malformed, or the operation identifier
	// was used before for a different request. Nothing happened; Reason says why.
	DispositionRefused Disposition = iota + 1
	// DispositionReplayed: the same request was answered before. Nothing
	// happened; the caller returns the stored result.
	DispositionReplayed
	// DispositionExecuted: the request ran; Result says how it ended. The
	// caller stores Result under Key with Fingerprint, together with the state
	// the call returned.
	DispositionExecuted
)

// Result is how an executed command ended.
type Result int

const (
	// ResultOK: done.
	ResultOK Result = iota + 1
	// ResultNotFound: there is no such request, or no such expectation.
	ResultNotFound
	// ResultWrongState: the request has ended, was confirmed already or is being
	// withdrawn.
	ResultWrongState
	// ResultExpired: the request ran out.
	ResultExpired
	// ResultWrongCode: the code entered is not the request's. It is not a
	// failed verification and counts for nothing.
	ResultWrongCode
	// ResultAmbiguousCode: another request that awaits its owner shows the same
	// code. Confirm neither by code; reject one of them.
	ResultAmbiguousCode
	// ResultPaused: pairing with nodes that are not expected is paused;
	// RetryAfter says for how long.
	ResultPaused
	// ResultNotAccepted: the other node is retired, locally revoked, the node
	// itself, or already trusted directly.
	ResultNotAccepted
	// ResultWrongPeer: the connection reaches another node than the one named.
	ResultWrongPeer
	// ResultInProgress: this node is already pairing with that node.
	ResultInProgress
	// ResultInvalid: an argument is unusable.
	ResultInvalid
	// ResultFull: the expectation list is at its cap.
	ResultFull
)

func (r Result) String() string {
	names := map[Result]string{
		ResultOK: "ok", ResultNotFound: "not_found", ResultWrongState: "wrong_state", ResultExpired: "expired",
		ResultWrongCode: "wrong_code", ResultAmbiguousCode: "ambiguous_code", ResultPaused: "paused",
		ResultNotAccepted: "not_accepted", ResultWrongPeer: "wrong_peer", ResultInProgress: "in_progress",
		ResultInvalid: "invalid", ResultFull: "full",
	}
	if n, ok := names[r]; ok {
		return n
	}
	return "invalid"
}

// Outcome is the answer to a management request.
type Outcome struct {
	Disposition Disposition
	// Reason is the administration package's reason for a refusal or a replay.
	Reason admin.Reason
	// Key and Fingerprint identify an executed request for the stored result.
	Key         admin.RequestKey
	Fingerprint admin.Fingerprint
	// Result and RetryAfter are meaningful when Disposition is
	// DispositionExecuted.
	Result     Result
	RetryAfter time.Duration
}

// Request kinds in the fingerprint.
const (
	kindConfirm byte = iota + 1
	kindReject
	kindExpect
	kindForget
	kindInitiate
)

// fingerprint digests what a request means: its kind and arguments. A resend
// of the same operation identifier with different arguments is a conflict, not
// a replay.
func fingerprint(kind byte, args ...[]byte) admin.Fingerprint {
	return admin.Fingerprint(sha256.Sum256(frame.Frame(requestDomain, append([][]byte{{kind}}, args...)...)))
}

// admit runs the order the administration package defines: authorize, validate,
// look for a stored result, and only then let the command execute.
func (s State) admit(w World, req Request, kind admin.OpKind, fp admin.Fingerprint, now time.Time) (Outcome, bool) {
	op := admin.Operation{Kind: kind}
	if v := admin.Authorize(w.List, req.Actor, op, now); !v.Allowed {
		return Outcome{Disposition: DispositionRefused, Reason: v.Reason}, false
	}
	if reason := admin.ValidateRequest(admin.Request{ID: req.ID, Op: op}); reason != admin.ReasonOK {
		return Outcome{Disposition: DispositionRefused, Reason: reason}, false
	}
	key := admin.KeyFor(req.Actor, req.ID)
	var stored *admin.StoredRequest
	if w.Stored != nil {
		stored = w.Stored(key)
	}
	switch rv := admin.CheckReplay(stored, fp); rv.Action {
	case admin.ReplayReturnStored:
		return Outcome{Disposition: DispositionReplayed, Reason: rv.Reason, Key: key, Fingerprint: fp}, false
	case admin.ReplayConflict:
		return Outcome{Disposition: DispositionRefused, Reason: rv.Reason, Key: key, Fingerprint: fp}, false
	}
	return Outcome{Disposition: DispositionExecuted, Key: key, Fingerprint: fp}, true
}

func executed(out Outcome, r Result) Outcome {
	out.Result = r
	return out
}

func u32be(v uint32) []byte { return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }

// confirmable is what the confirmation gate reads of a request, whichever side
// holds it.
type confirmable struct {
	id        ID
	code      Code
	peer      ident.NodeID
	waiting   bool // still within its time and unfinished
	expired   bool // ran out unfinished
	confirmed bool
}

// confirmGate decides whether a code entered by an owner confirms the request.
// A wrong code is not a failed verification; only an explicit report of
// differing codes is.
func (s State) confirmGate(w World, c confirmable, code Code, now time.Time) (Result, time.Duration) {
	switch {
	case c.expired:
		return ResultExpired, 0
	case !c.waiting || c.confirmed:
		return ResultWrongState, 0
	case w.peerFaultOf(c.peer, now, false) != peerFine:
		return ResultNotAccepted, 0
	}
	if paused, retry := s.limits.Paused(w.Params, now); paused {
		return ResultPaused, retry
	}
	switch {
	case code != c.code:
		return ResultWrongCode, 0
	case s.codeCollides(c.id, c.code, now):
		return ResultAmbiguousCode, 0
	}
	return ResultOK, 0
}

// Confirm confirms the request id with the code the owner read from the other
// node's screen. The node checks the code itself against the request the
// identifier names, so the confirmation lands on the request the owner was
// shown and on no other. It is authorized as confirming trust. On the target
// the confirmation completes the credential if the initiator's signature is
// already in; on the initiator it signs, and the signature leaves with the next
// result exchange.
func (s State) Confirm(w World, req Request, id ID, code Code, now time.Time) (State, Outcome, []Action, error) {
	if err := w.validate(); err != nil {
		return s, Outcome{}, nil, err
	}
	now = wholeNow(now)
	out, ok := s.admit(w, req, admin.OpConfirmTrust, fingerprint(kindConfirm, id[:], u32be(uint32(code))), now)
	if !ok {
		return s, out, nil, nil
	}
	author, err := AuthorOf(req.Actor)
	if err != nil {
		return s, executed(out, ResultInvalid), nil, nil
	}
	conf := &Confirmation{Method: ConfirmedByCode, By: author, Operation: req.ID, At: now}

	if i := s.targetIndex(id); i >= 0 {
		rec := s.targets[i]
		gate := confirmable{id: id, code: rec.Transcript.code, peer: rec.Transcript.terms.Initiator.node,
			waiting: rec.Waiting(now), expired: rec.ExpiredAt(now) || rec.Status == TargetExpired, confirmed: rec.Confirmation != nil}
		if r, retry := s.confirmGate(w, gate, code, now); r != ResultOK {
			out.RetryAfter = retry
			return s, executed(out, r), nil, nil
		}
		rec.Confirmation = conf
		actions := []Action{PersistTarget{rec}}
		if rec.readyToComplete() {
			done, doneActions, err := s.completeTarget(w, rec, now)
			if err != nil {
				return s, Outcome{}, nil, err
			}
			rec, actions = done, append(actions, doneActions...)
		}
		return s.withTarget(rec), executed(out, ResultOK), actions, nil
	}

	if i := s.initiatorIndex(id); i >= 0 {
		rec := s.initiators[i]
		active := rec.Status == InitiatorActive
		gate := confirmable{id: id, code: rec.Transcript.code, peer: rec.Transcript.terms.Target.node,
			waiting: active && now.Before(rec.ExpiresAt), expired: active && !now.Before(rec.ExpiresAt) || rec.Status == InitiatorExpired, confirmed: rec.Confirmation != nil}
		if r, retry := s.confirmGate(w, gate, code, now); r != ResultOK {
			out.RetryAfter = retry
			return s, executed(out, r), nil, nil
		}
		signature, err := signInitiator(w, rec.Transcript)
		if err != nil {
			return s, Outcome{}, nil, err
		}
		rec.Confirmation, rec.Signature = conf, &signature
		next := s.withInitiator(rec).withPlan(fetchPlan{id: id, nextAt: now})
		return next, executed(out, ResultOK), []Action{PersistInitiator{rec}}, nil
	}
	return s, executed(out, ResultNotFound), nil, nil
}

// Reject ends the request id because its owner declined it, or because the
// owner compared the codes and they differ. Mismatch is the one thing that
// counts as a failed verification: it is recorded on the node, whichever side
// of the pairing it was. It is authorized as rejecting a trust request.
//
// On the initiator the end is a withdrawal: the target is told with the next
// result exchange, and a signature that may have left is revoked at once.
func (s State) Reject(w World, req Request, id ID, reason WithdrawReason, now time.Time) (State, Outcome, []Action, error) {
	if err := w.validate(); err != nil {
		return s, Outcome{}, nil, err
	}
	now = wholeNow(now)
	out, ok := s.admit(w, req, admin.OpRejectTrustRequest, fingerprint(kindReject, id[:], u32be(uint32(reason))), now)
	if !ok {
		return s, out, nil, nil
	}
	if !reason.valid() {
		return s, executed(out, ResultInvalid), nil, nil
	}

	failing := func(st State) (State, []Action) {
		if reason != WithdrawMismatch {
			return st, nil
		}
		l := st.limits.RecordFailure(w.Params, now)
		return st.withLimits(l), []Action{PersistLimits{l}}
	}

	if i := s.targetIndex(id); i >= 0 {
		rec := s.targets[i]
		switch {
		case rec.ExpiredAt(now) || rec.Status == TargetExpired:
			return s, executed(out, ResultExpired), nil, nil
		case rec.Status != TargetPending:
			return s, executed(out, ResultWrongState), nil, nil
		}
		rec.Status, rec.Rejection, rec.EndedAt = TargetRejected, reason, now
		next, actions := failing(s)
		actions = append(actions, PersistTarget{rec}, Notify{Notice{Kind: NoticeEnded, ID: id, Remote: rec.Transcript.terms.Initiator.node}})
		return next.withTarget(rec), executed(out, ResultOK), actions, nil
	}

	if i := s.initiatorIndex(id); i >= 0 {
		rec := s.initiators[i]
		if rec.Status != InitiatorActive {
			return s, executed(out, ResultWrongState), nil, nil
		}
		next, actions := failing(s)
		next, withdrawal, err := next.withdraw(w, rec, reason, now)
		if err != nil {
			return s, Outcome{}, nil, err
		}
		return next, executed(out, ResultOK), append(actions, withdrawal...), nil
	}
	return s, executed(out, ResultNotFound), nil, nil
}

// Expect enters an expectation: the owner means to pair with this node. It is
// authorized as confirming trust, because it stands for a confirmation made in
// advance. Entering the same node again replaces the earlier entry.
func (s State) Expect(w World, req Request, node ident.NodeID, now time.Time) (State, Outcome, []Action, error) {
	if err := w.validate(); err != nil {
		return s, Outcome{}, nil, err
	}
	now = wholeNow(now)
	out, ok := s.admit(w, req, admin.OpConfirmTrust, fingerprint(kindExpect, nodeBytes(node)), now)
	if !ok {
		return s, out, nil, nil
	}
	author, err := AuthorOf(req.Actor)
	if err != nil || node.IsZero() || node == w.Trust.Local {
		return s, executed(out, ResultInvalid), nil, nil
	}
	live := slices.DeleteFunc(slices.Clone(s.expectations), func(e Expectation) bool {
		return e.Node == node || e.ExpiredAt(w.Params, now) || !w.authorMayConfirm(e.Author, now)
	})
	if len(live) >= w.Params.ExpectationCap {
		return s, executed(out, ResultFull), nil, nil
	}
	live = append(live, Expectation{Node: node, Author: author, Operation: req.ID, CreatedAt: now})
	return s.withExpectations(live), executed(out, ResultOK), []Action{PersistExpectations{slices.Clone(live)}}, nil
}

// Forget deletes the expectation for a node. It is authorized as rejecting a
// trust request, which needs less than entering one: withdrawing a standing
// consent only ever narrows who can be paired with.
func (s State) Forget(w World, req Request, node ident.NodeID, now time.Time) (State, Outcome, []Action, error) {
	if err := w.validate(); err != nil {
		return s, Outcome{}, nil, err
	}
	now = wholeNow(now)
	out, ok := s.admit(w, req, admin.OpRejectTrustRequest, fingerprint(kindForget, nodeBytes(node)), now)
	if !ok {
		return s, out, nil, nil
	}
	if !slices.ContainsFunc(s.expectations, func(e Expectation) bool { return e.Node == node }) {
		return s, executed(out, ResultNotFound), nil, nil
	}
	rest := slices.DeleteFunc(slices.Clone(s.expectations), func(e Expectation) bool { return e.Node == node })
	return s.withExpectations(rest), executed(out, ResultOK), []Action{PersistExpectations{slices.Clone(rest)}}, nil
}

func nodeBytes(n ident.NodeID) []byte { return n.Bytes() }

// Initiation is what an owner asks for when starting a pairing.
type Initiation struct {
	// Target is the node the owner means to pair with, when the owner knows
	// it: the connection must then reach exactly this node, authenticated. The
	// zero value means the owner only knows an address and will compare the
	// codes to find out who is there.
	Target ident.NodeID
	// NotAfter is when the credential should expire; zero for never.
	NotAfter time.Time
	// Addresses are dial hints for the target kept for the result exchanges.
	// They are not part of what the request means.
	Addresses []string
}

// Initiate starts the online segment on a stream to the target, once the
// connection to it has completed the certificate exchange. It is authorized as
// confirming trust: the initiating node's owner must be allowed to let a new
// node in. It never starts by itself and is never retried by this package.
func (s State) Initiate(w World, c Conn, sid SessionID, req Request, in Initiation, rand io.Reader, now time.Time) (State, Outcome, []Action, error) {
	if err := w.validate(); err != nil {
		return s, Outcome{}, nil, err
	}
	if err := c.validate(); err != nil {
		return s, Outcome{}, nil, err
	}
	if s.sessionIndex(sid) >= 0 {
		return s, Outcome{}, nil, fmt.Errorf("%w: session %d is already open", ErrWorld, sid)
	}
	now = wholeNow(now)
	var notAfter int64
	if !in.NotAfter.IsZero() {
		notAfter = in.NotAfter.Unix()
	}
	out, ok := s.admit(w, req, admin.OpConfirmTrust, fingerprint(kindInitiate, nodeBytes(in.Target), i64be(notAfter)), now)
	if !ok {
		return s, out, nil, nil
	}
	remote := c.Remote.node
	switch {
	case !in.Target.IsZero() && in.Target != remote:
		return s, executed(out, ResultWrongPeer), nil, nil
	case !in.NotAfter.IsZero() && (!wholeSecond(in.NotAfter) || !in.NotAfter.After(now)),
		len(in.Addresses) > MaxAddresses,
		slices.ContainsFunc(in.Addresses, func(a string) bool { return a == "" || len(a) > MaxAddressLen }):
		return s, executed(out, ResultInvalid), nil, nil
	case w.peerFaultOf(remote, now, true) != peerFine || remote == c.Local.node:
		return s, executed(out, ResultNotAccepted), nil, nil
	}
	expectation, expected := s.liveExpectation(w, remote, now)
	if !expected {
		if paused, retry := s.limits.Paused(w.Params, now); paused {
			out.RetryAfter = retry
			return s, executed(out, ResultPaused), nil, nil
		}
	}
	for _, r := range s.initiators {
		if r.Status == InitiatorActive && r.Transcript.terms.Target.node == remote {
			return s, executed(out, ResultInProgress), nil, nil
		}
	}
	for _, x := range s.sessions {
		if (x.phase == phaseAwaitReveal || x.phase == phaseAwaitAccept) && x.conn.Remote.node == remote {
			return s, executed(out, ResultInProgress), nil, nil
		}
	}

	nA, err := NewNonce(rand)
	if err != nil {
		return s, Outcome{}, nil, err
	}
	terms := Terms{Protocol: c.Protocol, Initiator: c.Local, Target: c.Remote, CreatedAt: now, NotAfter: in.NotAfter}
	commitment, err := terms.Commit(nA)
	if err != nil {
		return s, Outcome{}, nil, err
	}
	m1, err := CommitMsg{Commitment: commitment, CreatedAt: now, NotAfter: in.NotAfter}.Encode()
	if err != nil {
		return s, Outcome{}, nil, err
	}
	sess := onlineSession{
		id: sid, phase: phaseAwaitReveal, conn: c, deadline: now.Add(w.Params.OnlineTimeout),
		terms: terms, commit: commitment, nA: nA, addresses: slices.Clone(in.Addresses),
	}
	if expected {
		sess.expected = expectation.Author
	}
	return s.withSession(sess), executed(out, ResultOK), []Action{Send{sid, m1}}, nil
}
