package preauth

// RelayOp names what a request on the relay protocol asks for. It is read only
// for BuiltinCircuitHop.
type RelayOp uint8

const (
	// RelayNone is the zero value: no relay operation, the stream itself. It is
	// the only value for every protocol other than BuiltinCircuitHop.
	RelayNone RelayOp = iota
	// RelayReserve: a node asks the local relay to hold a slot so that others can
	// reach it through the relay.
	RelayReserve
	// RelayConnect: a peer asks the local relay to open a circuit to a node that
	// holds a reservation.
	RelayConnect
)

func (o RelayOp) String() string {
	switch o {
	case RelayNone:
		return "none"
	case RelayReserve:
		return "reserve"
	case RelayConnect:
		return "connect"
	}
	return "unknown"
}

// Use is one thing a peer asks for on a connection: a stream of a protocol or,
// on the relay protocol, one relay operation.
type Use struct {
	Protocol ProtocolID
	// Relay is RelayNone except for a relay operation on BuiltinCircuitHop.
	Relay RelayOp
}

// AccessReason says why Allowed answered as it did.
type AccessReason uint8

const (
	// AccessInvalid is the zero value; no answer carries it.
	AccessInvalid AccessReason = iota

	// Reasons of allowed uses.

	// AccessBuiltin: a libp2p protocol open in every state.
	AccessBuiltin
	// AccessExchange: the certificate exchange is open in every state.
	AccessExchange
	// AccessPairing: a node that is not a member yet, or a member, asks to
	// establish trust.
	AccessPairing
	// AccessApplication: a member uses a cluster protocol.
	AccessApplication
	// AccessManagement: an authorized person uses the management protocol.
	AccessManagement
	// AccessReachabilityQuery: a person whose device certificate was accepted,
	// listed or not, asks for a node's reachability record.
	AccessReachabilityQuery
	// AccessRelayReservation: a member reserves a slot on the local relay.
	AccessRelayReservation
	// AccessRelayConnect: a peer whose exchange completed asks for a circuit;
	// whether the relay lets it through is the relay's allow list and rate
	// limit, which this package does not hold.
	AccessRelayConnect

	// Reasons of refused uses.

	// RefusedInvalid: the state is unset or invalid, the protocol is empty, or
	// the relay operation does not fit the protocol. Nothing is allowed on an
	// input that cannot be judged.
	RefusedInvalid
	// RefusedExchangeRequired: the connection is unauthenticated and the use
	// needs a completed certificate exchange.
	RefusedExchangeRequired
	// RefusedMemberOnly: a cluster protocol or a relay reservation, and the peer
	// is not a member.
	RefusedMemberOnly
	// RefusedPersonOnly: the management protocol and the peer is not an authorized
	// person, or the reachability query and the peer is not a person.
	RefusedPersonOnly
	// RefusedNodeOnly: the pairing protocol is between nodes; a person's device
	// is not one, listed or not.
	RefusedNodeOnly
)

func (r AccessReason) String() string {
	switch r {
	case AccessBuiltin:
		return "builtin"
	case AccessExchange:
		return "exchange"
	case AccessPairing:
		return "pairing"
	case AccessApplication:
		return "application"
	case AccessManagement:
		return "management"
	case AccessReachabilityQuery:
		return "reachability-query"
	case AccessRelayReservation:
		return "relay-reservation"
	case AccessRelayConnect:
		return "relay-connect"
	case RefusedInvalid:
		return "refused-invalid"
	case RefusedExchangeRequired:
		return "refused-exchange-required"
	case RefusedMemberOnly:
		return "refused-member-only"
	case RefusedPersonOnly:
		return "refused-person-only"
	case RefusedNodeOnly:
		return "refused-node-only"
	}
	return "invalid"
}

// Access is the answer of Allowed.
type Access struct {
	Verdict Verdict
	Reason  AccessReason
}

func allow(r AccessReason) Access  { return Access{VerdictAllow, r} }
func refuse(r AccessReason) Access { return Access{VerdictRefuse, r} }

// Allowed decides whether a peer in state s may make use u. It is the one place
// that says what each state permits; the network layer asks it for every
// stream a peer opens and for every relay operation a peer requests, and acts on
// the answer. A refusal is shown to the peer as a stream that was negotiated and
// then reset, because the protocol library offers no earlier point.
//
// The table, with "x" for allowed:
//
//	                  unauth.  non-member  non-member  member  authorized
//	                           node        person              person
//	built-in libp2p      x        x           x          x       x
//	relay stream (hop)   x        x           x          x       x
//	relay reserve                                        x
//	relay connect                 x           x          x       x
//	certificate exchange x        x           x          x       x
//	pairing                       x                      x
//	reachability query                        x                  x
//	management                                                   x
//	any other protocol                                   x
//
// The reachability query is open to every person whose device certificate was
// accepted, because the person's right to the answer is not their standing on
// this node's list but the administrator's reachability proof they present,
// which the query handler checks (admin.CheckReachabilityProof); no node may
// use it.
//
// An input that cannot be judged (an unset state, an empty protocol, a relay
// operation on another protocol) is refused.
func Allowed(s State, u Use) Access {
	class := ClassOf(u.Protocol)
	if !s.Valid() || class == ClassUnset || (class != ClassRelayHop && u.Relay != RelayNone) {
		return refuse(RefusedInvalid)
	}
	switch class {
	case ClassBuiltin:
		return allow(AccessBuiltin)
	case ClassExchange:
		return allow(AccessExchange)
	case ClassRelayHop:
		return allowedRelay(s, u.Relay)
	case ClassPairing:
		switch s {
		case StateNonMemberNode, StateMember:
			return allow(AccessPairing)
		case StateNonMemberPerson, StateAuthorizedPerson:
			return refuse(RefusedNodeOnly)
		}
		return refuse(RefusedExchangeRequired)
	case ClassReachabilityQuery:
		switch s {
		case StateNonMemberPerson, StateAuthorizedPerson:
			return allow(AccessReachabilityQuery)
		case StateUnauthenticated:
			return refuse(RefusedExchangeRequired)
		}
		return refuse(RefusedPersonOnly)
	case ClassManagement:
		switch s {
		case StateAuthorizedPerson:
			return allow(AccessManagement)
		case StateUnauthenticated:
			return refuse(RefusedExchangeRequired)
		}
		return refuse(RefusedPersonOnly)
	}
	// ClassApplication.
	switch s {
	case StateMember:
		return allow(AccessApplication)
	case StateUnauthenticated:
		return refuse(RefusedExchangeRequired)
	}
	return refuse(RefusedMemberOnly)
}

func allowedRelay(s State, op RelayOp) Access {
	switch op {
	case RelayNone:
		return allow(AccessBuiltin)
	case RelayReserve:
		switch s {
		case StateMember:
			return allow(AccessRelayReservation)
		case StateUnauthenticated:
			return refuse(RefusedExchangeRequired)
		}
		return refuse(RefusedMemberOnly)
	case RelayConnect:
		if s == StateUnauthenticated {
			return refuse(RefusedExchangeRequired)
		}
		return allow(AccessRelayConnect)
	}
	return refuse(RefusedInvalid)
}
