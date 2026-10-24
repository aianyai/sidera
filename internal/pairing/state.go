package pairing

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
	"github.com/aianyai/sidera/internal/trust"
)

// ErrWorld reports a World or Conn the state machines cannot use.
var ErrWorld = errors.New("invalid world")

// SessionID names one stream. The connection layer chooses it, unique among
// the open streams of this node, and uses it to match frames and actions.
type SessionID uint64

// Conn holds the facts of the connection a stream runs on, both parties as
// this connection shows them: the remote end as authenticated, the local end as
// this connection was made. Protocol is the protocol identifier the stream
// negotiated, taken from the stream.
type Conn struct {
	Local, Remote Party
	Protocol      string
}

func (c Conn) validate() error {
	if c.Local.IsZero() || c.Remote.IsZero() || c.Protocol == "" {
		return fmt.Errorf("%w: connection facts are unset", ErrWorld)
	}
	return nil
}

// World is what the state machines read of the rest of the node. The caller
// builds it from current state for every call; nothing in it is kept.
type World struct {
	Params Params
	// Identity signs for this node: the credential, and its revocation. Its key
	// must be the identity key of the local party of the connection.
	Identity record.Signer
	// Revocations answers whether a node is retired. A nil set panics, as the
	// package that owns it defines.
	Revocations *revocation.Set
	// Trust is the trust view: whom the node trusts directly and whom its owner
	// has revoked locally.
	Trust trust.View
	// List is the authorized-persons list.
	List admin.List
	// Actors returns the current standing of an author, as an actor the list
	// can judge, for the authors of expectations. Nil, or a result with an
	// unknown kind, means the author cannot act now: only the local owner needs
	// no lookup.
	Actors func(Author) admin.Actor
	// Stored returns the stored request filed under a management request key,
	// or nil. Nil Stored files nothing.
	Stored func(admin.RequestKey) *admin.StoredRequest
}

func (w World) validate() error {
	if err := w.Params.Validate(); err != nil {
		return err
	}
	if w.Identity == nil || w.Identity.Public().IsZero() {
		return fmt.Errorf("%w: no identity signer", ErrWorld)
	}
	if w.Revocations == nil {
		return fmt.Errorf("%w: no revocation set", ErrWorld)
	}
	return nil
}

// signsFor reports whether the world's identity key is the identity key of p.
func (w World) signsFor(p Party) error {
	if w.Identity.Public() != p.identity {
		return fmt.Errorf("%w: identity signer is not the local party's key", ErrWorld)
	}
	return nil
}

// authorStanding is the actor an author acts as now.
func (w World) authorStanding(a Author) admin.Actor {
	if a.IsLocalOwner() {
		return admin.LocalOwner()
	}
	if w.Actors == nil {
		return admin.Actor{}
	}
	return w.Actors(a)
}

// authorMayConfirm reports whether the author still has the right to confirm
// trust at now.
func (w World) authorMayConfirm(a Author, now time.Time) bool {
	return admin.Authorize(w.List, w.authorStanding(a), admin.Operation{Kind: admin.OpConfirmTrust}, now).Allowed
}

// peerFault says why a pairing with a node cannot go on, or peerFine. The
// causes are not told apart to the peer.
type peerFault int

const (
	peerFine peerFault = iota
	peerSelf
	peerRetired
	peerLocallyRevoked
	peerTrusted
)

// peerFaultOf judges a node as a pairing partner. direct adds the test for a
// direct credential that is already usable, which stops a new pairing from
// starting but not one that is under way from finishing.
func (w World) peerFaultOf(peer ident.NodeID, now time.Time, direct bool) peerFault {
	switch {
	case peer == w.Trust.Local:
		return peerSelf
	case w.Revocations.NodeRetired(peer):
		return peerRetired
	}
	if _, revoked := w.Trust.LocallyRevoked[peer]; revoked {
		return peerLocallyRevoked
	}
	if direct && trust.Evaluate(w.Trust, peer, now).Outcome == trust.AllowedDirect {
		return peerTrusted
	}
	return peerFine
}

// NoticeKind says what a Notify action is about.
type NoticeKind int

const (
	// NoticeNotStarted: the online segment of this node's request ended before
	// it opened its nonce, so no attempt was used. Refusal and RetryAfter say
	// why when the target refused.
	NoticeNotStarted NoticeKind = iota + 1
	// NoticeAttemptUsed: the online segment ended after this node opened its
	// nonce and before a request was saved. The target may have counted a
	// guess; the owner must start again by hand, and the pairing is never
	// retried automatically.
	NoticeAttemptUsed
	// NoticeCodeReady: the initiator saved the request; its owner can now read
	// the code from the record.
	NoticeCodeReady
	// NoticePending: the target saved a request for its owner.
	NoticePending
	// NoticeCompleted: the credential is stored.
	NoticeCompleted
	// NoticeEnded: a request ended without a credential (see ID's record).
	NoticeEnded
	// NoticeRevoked: this node revoked the credential it had signed, because
	// the pairing ended without the complete credential.
	NoticeRevoked
)

// Notice is something a human should be told.
type Notice struct {
	Kind       NoticeKind
	ID         ID
	Remote     ident.NodeID
	Refusal    RefusalReason
	RetryAfter time.Duration
}

// Action is something the caller does for a state machine. A call returns its
// actions in order, and the caller performs them in that order and completes
// each persist before it performs the next action: a frame follows the
// persist that made it safe to send.
type Action interface{ action() }

type (
	// Send writes one frame to a stream.
	Send struct {
		Session SessionID
		Frame   []byte
	}
	// Close ends a stream.
	Close struct{ Session SessionID }
	// FetchRequest asks the network layer to open a stream to the target of a
	// pairing, on any connection it can make (the addresses are hints), send
	// Frame and read one answer, within FetchTimeout. It reports the answer to
	// OnFetchResult with the authenticated party of that connection, or the
	// failure to OnFetchFailed.
	FetchRequest struct {
		ID        ID
		Frame     []byte
		Addresses []string
	}
	// PersistLimits stores the counters. They are stored before the reveal they
	// count leaves the node.
	PersistLimits struct{ Limits Limits }
	// PersistExpectations stores the whole list of expectations.
	PersistExpectations struct{ Expectations []Expectation }
	// PersistTarget stores a target record, replacing the one with its ID.
	PersistTarget struct{ Record TargetRecord }
	// DeleteTarget removes the target record with this ID.
	DeleteTarget struct{ ID ID }
	// PersistInitiator stores an initiator record, replacing the one with its ID.
	PersistInitiator struct{ Record InitiatorRecord }
	// DeleteInitiator removes the initiator record with this ID.
	DeleteInitiator struct{ ID ID }
	// StoreCredential stores a complete credential, which then belongs in the
	// trust view's credentials. It is idempotent: storing the same credential
	// again changes nothing. It comes before the persist that marks the pairing
	// complete, so a crash between the two is repaired by doing both again.
	StoreCredential struct {
		Credential record.Verified[credential.Credential]
		Record     record.Record
	}
	// StoreRevocation stores a revocation of a credential this node signed and
	// publishes it with the other revocations. It is idempotent.
	StoreRevocation struct{ Record record.Record }
	// Notify tells a human something.
	Notify struct{ Notice Notice }
)

func (Send) action()                {}
func (Close) action()               {}
func (FetchRequest) action()        {}
func (PersistLimits) action()       {}
func (PersistExpectations) action() {}
func (PersistTarget) action()       {}
func (DeleteTarget) action()        {}
func (PersistInitiator) action()    {}
func (DeleteInitiator) action()     {}
func (StoreCredential) action()     {}
func (StoreRevocation) action()     {}
func (Notify) action()              {}

// phase of an online session.
type phase int

const (
	phaseFirst       phase = iota // target: stream open, no frame yet
	phaseAwaitOpen                // target: revealed nB, waiting for M3
	phaseAwaitReveal              // initiator: sent M1, waiting for M2
	phaseAwaitAccept              // initiator: sent M3, waiting for M4
)

// onlineSession is the volatile state of one stream of the online segment, on
// either side. It is never persisted: a crash drops it, which is the same as
// the stream ending.
type onlineSession struct {
	id       SessionID
	phase    phase
	conn     Conn
	deadline time.Time
	terms    Terms
	commit   Commitment
	nB, nA   Nonce
	// transcript is set on the initiator once M2 opened it.
	transcript Transcript
	// expected is the author of the expectation that applies to this session,
	// zero if none.
	expected Author
	// reserved is set on the target when the session holds one of the slots
	// for unexpected requests.
	reserved  bool
	addresses []string
}

// fetchPlan is the volatile schedule of the fetch exchanges of one initiator
// record. A restart forgets it: the exchange starts again at once.
type fetchPlan struct {
	id       ID
	attempts int
	nextAt   time.Time
	// inflightUntil is when an exchange in flight counts as failed.
	inflightUntil time.Time
}

// State is everything the pairing protocol keeps on one node: the counters,
// the expectations, the requests it holds as a target and the ones it started,
// and the volatile bookkeeping of open streams and fetch schedules. It is a
// value; every method returns the next State and the actions to perform, and
// the receiver is never changed.
type State struct {
	limits       Limits
	expectations []Expectation
	targets      []TargetRecord
	initiators   []InitiatorRecord

	sessions []onlineSession
	plans    []fetchPlan
}

// Snapshot is the persisted part of a State.
type Snapshot struct {
	Limits       Limits
	Expectations []Expectation
	Targets      []TargetRecord
	Initiators   []InitiatorRecord
}

// Restore builds a State from persisted records, checking each and that no
// identifier repeats. Volatile bookkeeping starts empty, so call Recover next.
func Restore(snap Snapshot) (State, error) {
	seen := map[ID]bool{}
	for i, r := range snap.Targets {
		if err := r.Validate(); err != nil {
			return State{}, fmt.Errorf("target record %d: %w", i, err)
		}
		if seen[r.ID()] {
			return State{}, fmt.Errorf("%w: duplicate pairing id", ErrRecord)
		}
		seen[r.ID()] = true
	}
	seen = map[ID]bool{}
	for i, r := range snap.Initiators {
		if err := r.Validate(); err != nil {
			return State{}, fmt.Errorf("initiator record %d: %w", i, err)
		}
		if seen[r.ID()] {
			return State{}, fmt.Errorf("%w: duplicate pairing id", ErrRecord)
		}
		seen[r.ID()] = true
	}
	nodes := map[ident.NodeID]bool{}
	for i, e := range snap.Expectations {
		if err := e.validate(); err != nil {
			return State{}, fmt.Errorf("expectation %d: %w", i, err)
		}
		if nodes[e.Node] {
			return State{}, fmt.Errorf("%w: node expected twice", ErrExpectation)
		}
		nodes[e.Node] = true
	}
	return State{
		limits:       snap.Limits,
		expectations: slices.Clone(snap.Expectations),
		targets:      slices.Clone(snap.Targets),
		initiators:   slices.Clone(snap.Initiators),
	}, nil
}

// Snapshot returns the persisted part of the state.
func (s State) Snapshot() Snapshot {
	return Snapshot{
		Limits:       s.limits,
		Expectations: slices.Clone(s.expectations),
		Targets:      slices.Clone(s.targets),
		Initiators:   slices.Clone(s.initiators),
	}
}

// Limits returns the counters.
func (s State) Limits() Limits { return s.limits }

// Expectations returns the expectations, live or not.
func (s State) Expectations() []Expectation { return slices.Clone(s.expectations) }

// Targets returns the requests held as a target.
func (s State) Targets() []TargetRecord { return slices.Clone(s.targets) }

// Initiators returns the requests started here.
func (s State) Initiators() []InitiatorRecord { return slices.Clone(s.initiators) }

// Target returns the target record with the identifier.
func (s State) Target(id ID) (TargetRecord, bool) {
	if i := s.targetIndex(id); i >= 0 {
		return s.targets[i], true
	}
	return TargetRecord{}, false
}

// Initiator returns the initiator record with the identifier.
func (s State) Initiator(id ID) (InitiatorRecord, bool) {
	if i := s.initiatorIndex(id); i >= 0 {
		return s.initiators[i], true
	}
	return InitiatorRecord{}, false
}

// Withdrawn reports whether a credential with this digest is one this node
// signed and then revoked because the pairing did not end with the complete
// credential. A credential that arrives by synchronisation with such a digest
// is refused whatever its signatures say. The record is kept for
// ResultRetention after the pairing ended; the revocation itself is kept for
// good by the revocation set.
func (s State) Withdrawn(d record.Digest) bool {
	for _, r := range s.initiators {
		if !r.needsRevocation() {
			continue
		}
		if got, err := r.Transcript.credential.Digest(); err == nil && got == d {
			return true
		}
	}
	return false
}

func (s State) targetIndex(id ID) int {
	return slices.IndexFunc(s.targets, func(r TargetRecord) bool { return r.ID() == id })
}

func (s State) initiatorIndex(id ID) int {
	return slices.IndexFunc(s.initiators, func(r InitiatorRecord) bool { return r.ID() == id })
}

func (s State) sessionIndex(id SessionID) int {
	return slices.IndexFunc(s.sessions, func(x onlineSession) bool { return x.id == id })
}

func (s State) planIndex(id ID) int {
	return slices.IndexFunc(s.plans, func(p fetchPlan) bool { return p.id == id })
}

// withTarget returns s with r replacing the record of the same identifier, or
// added.
func (s State) withTarget(r TargetRecord) State {
	out := s
	out.targets = slices.Clone(s.targets)
	if i := s.targetIndex(r.ID()); i >= 0 {
		out.targets[i] = r
	} else {
		out.targets = append(out.targets, r)
	}
	return out
}

func (s State) withoutTarget(id ID) State {
	out := s
	out.targets = slices.DeleteFunc(slices.Clone(s.targets), func(r TargetRecord) bool { return r.ID() == id })
	return out
}

func (s State) withInitiator(r InitiatorRecord) State {
	out := s
	out.initiators = slices.Clone(s.initiators)
	if i := s.initiatorIndex(r.ID()); i >= 0 {
		out.initiators[i] = r
	} else {
		out.initiators = append(out.initiators, r)
	}
	return out
}

func (s State) withoutInitiator(id ID) State {
	out := s
	out.initiators = slices.DeleteFunc(slices.Clone(s.initiators), func(r InitiatorRecord) bool { return r.ID() == id })
	out.plans = slices.DeleteFunc(slices.Clone(s.plans), func(p fetchPlan) bool { return p.id == id })
	return out
}

func (s State) withSession(x onlineSession) State {
	out := s
	out.sessions = slices.Clone(s.sessions)
	if i := s.sessionIndex(x.id); i >= 0 {
		out.sessions[i] = x
	} else {
		out.sessions = append(out.sessions, x)
	}
	return out
}

func (s State) withoutSession(id SessionID) State {
	out := s
	out.sessions = slices.DeleteFunc(slices.Clone(s.sessions), func(x onlineSession) bool { return x.id == id })
	return out
}

func (s State) withPlan(p fetchPlan) State {
	out := s
	out.plans = slices.Clone(s.plans)
	if i := s.planIndex(p.id); i >= 0 {
		out.plans[i] = p
	} else {
		out.plans = append(out.plans, p)
	}
	return out
}

func (s State) withLimits(l Limits) State {
	s.limits = l
	return s
}

func (s State) withExpectations(e []Expectation) State {
	s.expectations = e
	return s
}

// wholeNow rounds the caller's clock to the seconds every stored time has.
func wholeNow(now time.Time) time.Time { return time.Unix(now.Unix(), 0).UTC() }

// liveExpectation returns the expectation for a node that stands now: not
// expired, and its author still allowed to confirm trust. It does not remove
// the expectation; using it up is a separate step.
func (s State) liveExpectation(w World, node ident.NodeID, now time.Time) (Expectation, bool) {
	for _, e := range s.expectations {
		if e.Node != node {
			continue
		}
		if e.ExpiredAt(w.Params, now) || !w.authorMayConfirm(e.Author, now) {
			return Expectation{}, false
		}
		return e, true
	}
	return Expectation{}, false
}

// consumeExpectation removes the expectation for a node: it is single use.
func (s State) consumeExpectation(node ident.NodeID) State {
	return s.withExpectations(slices.DeleteFunc(slices.Clone(s.expectations), func(e Expectation) bool { return e.Node == node }))
}

// unexpectedWaiting counts the requests that hold a slot of the cap for
// unexpected nodes: target requests still waiting for their owner, and online
// sessions that reserved a slot, leaving out those of one initiator node, whose
// next request would replace them.
func (s State) unexpectedWaiting(now time.Time, except ident.NodeID) int {
	n := 0
	for _, r := range s.targets {
		if r.Waiting(now) && r.Expected.IsZero() && r.Transcript.terms.Initiator.node != except {
			n++
		}
	}
	for _, x := range s.sessions {
		if x.reserved && x.conn.Remote.node != except {
			n++
		}
	}
	return n
}

// codeCollides reports whether another request that still awaits its owner's
// confirmation shows the same code. A request that was expected is confirmed
// from the start and never awaits. Confirming either
// would be ambiguous to the human who compares one code with one screen.
func (s State) codeCollides(id ID, code Code, now time.Time) bool {
	for _, r := range s.targets {
		if r.ID() != id && r.Waiting(now) && r.Confirmation == nil && r.Transcript.code == code {
			return true
		}
	}
	for _, r := range s.initiators {
		if r.ID() != id && r.Status == InitiatorActive && now.Before(r.ExpiresAt) && r.Confirmation == nil && r.Transcript.code == code {
			return true
		}
	}
	return false
}
