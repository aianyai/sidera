package preauth

import (
	"bytes"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// Direction is the direction the secure handshake reported for a connection.
type Direction uint8

const (
	// DirectionUnset is the zero value; it is refused.
	DirectionUnset Direction = iota
	// DirectionInbound: the remote side dialed us.
	DirectionInbound
	// DirectionOutbound: we dialed the remote side.
	DirectionOutbound
)

// ConnKey identifies a connection for accounting: the remote peer's network key
// and the remote address. It has no direction, because the direction the
// handshake reports and the direction the connection later reports can differ
// (a hole-punched connection is inbound at the handshake and outbound after),
// and a counter keyed with it would never release the entry.
//
// The key is not a connection identifier, and none is available: the connection
// gater, where the cap is enforced, sees a connection before the library has
// given it one, so (peer, address) is the finest key that the gater and the
// later connection events can both obtain. The model therefore accounts for at
// most one inbound connection per key at a time (see Secured), which is what
// lets every later event, named by the key alone, find the one connection it is
// about.
type ConnKey struct {
	// Peer is the network public key the secure handshake authenticated. The
	// network layer takes it from the peer identifier, which embeds it.
	Peer ident.PublicKey
	// Addr is the remote address in any stable text form; only equality is used.
	Addr string
}

func (k ConnKey) valid() bool { return !k.Peer.IsZero() && k.Addr != "" }

// less orders keys for deterministic output.
func (k ConnKey) less(o ConnKey) bool {
	if c := bytes.Compare(k.Peer.Bytes(), o.Peer.Bytes()); c != 0 {
		return c < 0
	}
	return k.Addr < o.Addr
}

// AdmissionReason says why Secured answered as it did.
type AdmissionReason uint8

const (
	// AdmissionInvalid is the zero value; no answer carries it.
	AdmissionInvalid AdmissionReason = iota
	// AdmitCounted: an inbound connection of a peer not known as a member; it
	// counts against UnauthenticatedCap.
	AdmitCounted
	// AdmitKnownMember: an inbound connection of a peer known as a member; it is
	// tracked for its deadline but does not count against the cap, so a flood
	// cannot keep members out.
	AdmitKnownMember
	// AdmitOutbound: an outbound connection is not accounted.
	AdmitOutbound
	// RefuseDuplicateKey: an inbound connection with this key is already on the
	// books, whether its peer is known as a member or not. Nothing changes: the
	// entry stays the first connection's, and the network layer closes the new
	// one.
	RefuseDuplicateKey
	// RefuseCapReached: UnauthenticatedCap connections are already
	// unauthenticated.
	RefuseCapReached
	// RefuseInvalidInput: an unset direction, key or time.
	RefuseInvalidInput
)

func (r AdmissionReason) String() string {
	switch r {
	case AdmitCounted:
		return "admit-counted"
	case AdmitKnownMember:
		return "admit-known-member"
	case AdmitOutbound:
		return "admit-outbound"
	case RefuseDuplicateKey:
		return "refuse-duplicate-key"
	case RefuseCapReached:
		return "refuse-cap-reached"
	case RefuseInvalidInput:
		return "refuse-invalid-input"
	}
	return "invalid"
}

// Admission is the answer of Secured.
type Admission struct {
	Verdict Verdict
	Reason  AdmissionReason
}

// PlacementReason says why Completed answered as it did.
type PlacementReason uint8

const (
	// PlacementInvalid is the zero value; no answer carries it.
	PlacementInvalid PlacementReason = iota
	// PlaceReleased: the connection became a member or an authorized person; it
	// no longer counts and the peer is known as a member from now on.
	PlaceReleased
	// PlaceNonMember: the connection became a non-member (node or person) and
	// holds one of the NonMemberConcurrency places until it ends or its lifetime
	// runs out.
	PlaceNonMember
	// PlaceAlreadyNonMember: the connection already is a non-member; nothing
	// changes.
	PlaceAlreadyNonMember
	// PlaceUntracked: the key is not tracked (an outbound connection, one that
	// was swept or has ended); nothing changes except what is known about the
	// peer.
	PlaceUntracked
	// RefuseNonMemberFull: NonMemberConcurrency non-members are already held. The
	// entry is dropped and the network layer closes the connection, without
	// reporting it as disconnected.
	RefuseNonMemberFull
	// RefuseNotCompleted: the state is unauthenticated, which a completed
	// exchange never is; nothing changes.
	RefuseNotCompleted
	// RefusePlacementInput: an unset state, key or time.
	RefusePlacementInput
)

func (r PlacementReason) String() string {
	switch r {
	case PlaceReleased:
		return "place-released"
	case PlaceNonMember:
		return "place-non-member"
	case PlaceAlreadyNonMember:
		return "place-already-non-member"
	case PlaceUntracked:
		return "place-untracked"
	case RefuseNonMemberFull:
		return "refuse-non-member-full"
	case RefuseNotCompleted:
		return "refuse-not-completed"
	case RefusePlacementInput:
		return "refuse-placement-input"
	}
	return "invalid"
}

// Placement is the answer of Completed. VerdictRefuse means the network layer
// closes the connection.
type Placement struct {
	Verdict Verdict
	Reason  PlacementReason
}

// Counts are the sizes of the tracked sets.
type Counts struct {
	// Unauthenticated counts inbound connections in the unauthenticated state
	// whose peer is not known as a member: the number UnauthenticatedCap bounds.
	Unauthenticated int
	// Exempt counts inbound unauthenticated connections of known members.
	Exempt int
	// NonMembers counts inbound connections in the non-member state: the number
	// NonMemberConcurrency bounds.
	NonMembers int
}

type phase uint8

const (
	phaseUnauthenticated phase = iota
	phaseNonMember
)

type entry struct {
	phase       phase
	exempt      bool
	securedAt   time.Time
	connectedAt time.Time // zero until Connected
}

// Accounting is the connection-accounting model: pure state transitions that
// the network layer drives with the events it sees and whose answers it carries
// out. It holds no timer and starts no goroutine; time arrives as an argument,
// and the question "which connections must be closed now" is answered by
// Overdue, so a missed timer costs a delay and not a leak.
//
// Only inbound connections are accounted. The direction passed to Secured is the
// one the handshake reported; every later event names the connection by its
// ConnKey alone. An Accounting is not safe for concurrent use: the caller
// serializes the calls.
type Accounting struct {
	p       Params
	entries map[ConnKey]*entry
	// learned holds the peers whose exchange completed as a member or an
	// authorized person; held holds the members of the certificates the node has.
	learned map[ident.PublicKey]struct{}
	held    map[ident.PublicKey]struct{}
	counts  Counts
}

// NewAccounting returns an empty model, or the error of Params.Validate.
func NewAccounting(p Params) (*Accounting, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &Accounting{p: p, entries: map[ConnKey]*entry{}, learned: map[ident.PublicKey]struct{}{}, held: map[ident.PublicKey]struct{}{}}, nil
}

// Counts returns the sizes of the tracked sets.
func (a *Accounting) Counts() Counts { return a.counts }

// SetHeld replaces the set of peers known as members from the certificates the
// node holds (see HeldMembers). The caller calls it at start and whenever a
// held certificate, a revocation or the trust view changes. Peers learned from
// completed exchanges are kept apart and survive it. An authorized person's new
// device is in neither until its first exchange completed.
func (a *Accounting) SetHeld(peers []ident.PublicKey) {
	a.held = make(map[ident.PublicKey]struct{}, len(peers))
	for _, p := range peers {
		if !p.IsZero() {
			a.held[p] = struct{}{}
		}
	}
}

// Known reports whether peer is known as a member, by a completed exchange or
// by a held certificate.
func (a *Accounting) Known(peer ident.PublicKey) bool {
	if _, ok := a.learned[peer]; ok {
		return true
	}
	_, ok := a.held[peer]
	return ok
}

// Secured is called when the secure handshake of a connection is done, the point
// where a connection gater can still refuse it. An inbound connection of a peer
// that is not known as a member is refused when UnauthenticatedCap are already
// unauthenticated; one of a known member is always let through and does not
// count. An outbound connection is let through and not accounted.
//
// An inbound connection whose key is already on the books is refused, known
// member or not (RefuseDuplicateKey). The gater is asked once per connection, so
// a second call for a tracked key is a second connection with the same peer and
// address, not the same one told twice. Admitting it would let it share the
// first connection's entry, and once either connection ended, the other would
// outlive the books: no cap place, no ExchangeDeadline, no NonMemberLifetime.
// A legitimate peer does not hold two simultaneous inbound connections from one
// address, and a refused retry succeeds as soon as the first connection has
// completed its exchange or ended.
//
// A refused connection is closed by the caller and produces no later event for
// its key: the entry belongs to the first connection, and reporting the refused
// one as disconnected would release it.
//
// For a known member the refusal comes before the exemption, so the exempt path
// only ever creates an entry for a key that has none, and Counts.Exempt cannot
// count an entry twice or lose one. Every entry therefore has exactly one
// connection behind it and leaves with it: by Disconnected, by Sweep while
// the connection never came up, or by Completed once it is a member or an
// authorized person. No counter other than the entry itself exists, so there is
// nothing to leak.
func (a *Accounting) Secured(key ConnKey, dir Direction, now time.Time) Admission {
	if !key.valid() || now.IsZero() || (dir != DirectionInbound && dir != DirectionOutbound) {
		return Admission{VerdictRefuse, RefuseInvalidInput}
	}
	if dir == DirectionOutbound {
		return Admission{VerdictAllow, AdmitOutbound}
	}
	if _, ok := a.entries[key]; ok {
		return Admission{VerdictRefuse, RefuseDuplicateKey}
	}
	if a.Known(key.Peer) {
		a.entries[key] = &entry{phase: phaseUnauthenticated, exempt: true, securedAt: now}
		a.counts.Exempt++
		return Admission{VerdictAllow, AdmitKnownMember}
	}
	if a.counts.Unauthenticated >= a.p.UnauthenticatedCap {
		return Admission{VerdictRefuse, RefuseCapReached}
	}
	a.entries[key] = &entry{phase: phaseUnauthenticated, securedAt: now}
	a.counts.Unauthenticated++
	return Admission{VerdictAllow, AdmitCounted}
}

// Connected is called when the connection is established, which starts the clock
// of the exchange deadline and of the non-member lifetime. It reports whether
// the key is tracked, which for the one connection the books hold for the key
// means that this connection is the accounted one: the caller reports every
// established connection and remembers the answer, because Disconnected may be
// reported for the accounted connection only. An outbound connection is never
// tracked. An inbound one that is not means its entry was swept before it came
// up, which is a configuration error in SweepInterval, and no deadline binds it:
// the caller closes it.
func (a *Accounting) Connected(key ConnKey, now time.Time) bool {
	e, ok := a.entries[key]
	if !ok || now.IsZero() {
		return false
	}
	if e.connectedAt.IsZero() {
		e.connectedAt = now
	}
	return true
}

// Completed is called when the exchange on a connection was decided, with the
// state Decide returned, and again when a decision changes (pairing made a
// non-member a member). A member or an authorized person is released from the
// accounting and its peer is learned as known; a non-member takes a place among
// NonMemberConcurrency, or is refused when none is free; a non-member also stops
// being known as a member, by either source, until the next SetHeld lists it
// again. A non-member node and a non-member person are the same to the
// accounting: the limits are about how many strangers may hold a connection,
// whatever they present.
func (a *Accounting) Completed(key ConnKey, s State, now time.Time) Placement {
	if !key.valid() || now.IsZero() || !s.Valid() {
		return Placement{VerdictRefuse, RefusePlacementInput}
	}
	if s == StateUnauthenticated {
		return Placement{VerdictRefuse, RefuseNotCompleted}
	}
	e, tracked := a.entries[key]
	switch s {
	case StateMember, StateAuthorizedPerson:
		a.learned[key.Peer] = struct{}{}
		if !tracked {
			return Placement{VerdictAllow, PlaceUntracked}
		}
		a.remove(key, e)
		return Placement{VerdictAllow, PlaceReleased}
	}
	// A non-member node or person.
	delete(a.learned, key.Peer)
	delete(a.held, key.Peer)
	switch {
	case !tracked:
		return Placement{VerdictAllow, PlaceUntracked}
	case e.phase == phaseNonMember:
		return Placement{VerdictAllow, PlaceAlreadyNonMember}
	case a.counts.NonMembers >= a.p.NonMemberConcurrency:
		a.remove(key, e)
		return Placement{VerdictRefuse, RefuseNonMemberFull}
	}
	a.release(e)
	e.phase, e.exempt = phaseNonMember, false
	a.counts.NonMembers++
	return Placement{VerdictAllow, PlaceNonMember}
}

// Disconnected is called when the accounted connection of the key ends. It
// releases whatever the connection held. The key names the entry, not the
// connection, so the caller reports only the connection for which Connected
// answered true and which Completed has not released since: a released
// connection that ends while a newer connection of the same key is on the books
// must not take the newer connection's entry with it.
func (a *Accounting) Disconnected(key ConnKey) {
	if e, ok := a.entries[key]; ok {
		a.remove(key, e)
	}
}

// Sweep forgets the entries that were let through the handshake and were not
// seen connected within SweepInterval, and returns their keys in a fixed order.
// They belong to connections rejected after the handshake, which produce no
// other callback.
func (a *Accounting) Sweep(now time.Time) []ConnKey {
	var out []ConnKey
	for k, e := range a.entries {
		if e.connectedAt.IsZero() && !now.Before(e.securedAt.Add(a.p.SweepInterval)) {
			out = append(out, k)
		}
	}
	sortKeys(out)
	for _, k := range out {
		a.remove(k, a.entries[k])
	}
	return out
}

// Overdue returns, in a fixed order, the connections the network layer must
// close now: unauthenticated ones that did not complete the exchange within
// ExchangeDeadline of being connected, and non-members older than
// NonMemberLifetime. It changes nothing; the closing produces the Disconnected
// that releases them.
func (a *Accounting) Overdue(now time.Time) []ConnKey {
	var out []ConnKey
	for k, e := range a.entries {
		if e.connectedAt.IsZero() {
			continue
		}
		limit := ExchangeDeadline
		if e.phase == phaseNonMember {
			limit = a.p.NonMemberLifetime
		}
		if !now.Before(e.connectedAt.Add(limit)) {
			out = append(out, k)
		}
	}
	sortKeys(out)
	return out
}

func sortKeys(keys []ConnKey) {
	slices.SortFunc(keys, func(x, y ConnKey) int {
		switch {
		case x.less(y):
			return -1
		case y.less(x):
			return 1
		}
		return 0
	})
}

// release takes e's place out of the counts, keeping the entry.
func (a *Accounting) release(e *entry) {
	switch {
	case e.phase == phaseNonMember:
		a.counts.NonMembers--
	case e.exempt:
		a.counts.Exempt--
	default:
		a.counts.Unauthenticated--
	}
}

func (a *Accounting) remove(key ConnKey, e *entry) {
	a.release(e)
	delete(a.entries, key)
}
