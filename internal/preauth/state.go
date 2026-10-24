package preauth

// State is what the local node knows about the peer on one connection.
type State uint8

const (
	// StateUnset is the zero value. It is never a valid state: every function
	// that takes a State refuses it.
	StateUnset State = iota
	// StateUnauthenticated: the secure handshake is done, so the peer's network
	// key is known, but no certificate exchange has been accepted on this
	// connection. Also the state of a connection whose exchange was refused; the
	// network layer closes such a connection.
	StateUnauthenticated
	// StateNonMember: the exchange completed with a valid certificate, but the
	// peer is not a member of the local cluster and not an authorized person. It
	// may only ask to establish trust.
	StateNonMember
	// StateMember: the peer is a node the local node trusts directly or by
	// inheritance, or whose trust is suspended (see Decide).
	StateMember
	// StateAuthorizedPerson: the peer is a person's device with a valid device
	// certificate and a standing on the local authorized-persons list.
	StateAuthorizedPerson
)

func (s State) String() string {
	switch s {
	case StateUnauthenticated:
		return "unauthenticated"
	case StateNonMember:
		return "non-member"
	case StateMember:
		return "member"
	case StateAuthorizedPerson:
		return "authorized-person"
	}
	return "unset"
}

// Valid reports whether s is one of the four states.
func (s State) Valid() bool { return s >= StateUnauthenticated && s <= StateAuthorizedPerson }

// Verdict is the allow-or-refuse half of a decision; the reason is a typed value
// next to it. The zero value is neither.
type Verdict uint8

const (
	// VerdictUnset is the zero value; no decision returns it for a valid input.
	VerdictUnset Verdict = iota
	// VerdictAllow lets the thing happen.
	VerdictAllow
	// VerdictRefuse stops it.
	VerdictRefuse
)

func (v Verdict) String() string {
	switch v {
	case VerdictAllow:
		return "allow"
	case VerdictRefuse:
		return "refuse"
	}
	return "unset"
}
