package preauth

// ProtocolID is a libp2p protocol identifier. It is a plain string here: this
// package never opens a stream, it only classifies identifiers.
type ProtocolID string

// Sidera protocols whose handling differs by connection state. They are defined
// here and nowhere else; the pairing and network packages refer to them.
const (
	// ProtocolCertExchange carries the certificate exchange of every connection:
	// a node's network-key certificate or a person's device certificate. It is
	// open in every state, because it is how a connection leaves the
	// unauthenticated state.
	ProtocolCertExchange ProtocolID = "/sidera/cert-exchange/1.0.0"
	// ProtocolPairing carries the request to establish trust. It is open to
	// nodes that are not members yet, and to members.
	ProtocolPairing ProtocolID = "/sidera/pairing/1.0.0"
	// ProtocolManagement carries the requests of an authorized person's tool. It
	// is open to authorized persons only; what each request may do is the
	// administration rules' decision, not this package's.
	ProtocolManagement ProtocolID = "/sidera/manage/1.0.0"
	// ProtocolReachabilityQuery carries a person's request for a node's
	// reachability record, made with an administrator's reachability proof. It
	// is open to persons whose device certificate was accepted, whether or not
	// they are on the local list, and to no node. The query handler checks the
	// proof; this package does not.
	ProtocolReachabilityQuery ProtocolID = "/sidera/reachability-query/1.0.0"
)

// The libp2p protocols that stay open in every state. The library registers
// them itself, outside the gate the application sees, and a peer needs them
// before any certificate exists: identify tells it the addresses to dial back,
// ping measures the link, the circuit protocols and DCUtR are how two nodes
// behind NATs reach each other and punch a direct path. They are listed here as
// names, copied from the library, so the access table and its tests state them
// without importing it.
const (
	BuiltinIdentify     ProtocolID = "/ipfs/id/1.0.0"
	BuiltinIdentifyPush ProtocolID = "/ipfs/id/push/1.0.0"
	BuiltinPing         ProtocolID = "/ipfs/ping/1.0.0"
	// BuiltinCircuitStop is the protocol over which a relay hands a relayed
	// connection to its target.
	BuiltinCircuitStop ProtocolID = "/libp2p/circuit/relay/0.2.0/stop"
	// BuiltinCircuitHop is the protocol a relay serves. The stream is open in
	// every state; what a request on it may do is decided per relay operation
	// (RelayOp).
	BuiltinCircuitHop ProtocolID = "/libp2p/circuit/relay/0.2.0/hop"
	BuiltinDCUtR      ProtocolID = "/libp2p/dcutr"
)

// Class says how a protocol is treated. Only the classes with a rule of their
// own are named; every other protocol, which means every protocol Sidera defines
// for its cluster (messages, control signals, trust and revocation
// synchronization, the reachability records members exchange) and anything
// unknown, is ClassApplication, which only members may use. A new cluster
// protocol therefore needs no entry here to be safe, and a protocol that must be
// open to someone else needs one.
type Class uint8

const (
	// ClassUnset is the zero value: the protocol identifier is empty.
	ClassUnset Class = iota
	// ClassBuiltin: a libp2p protocol open in every state.
	ClassBuiltin
	// ClassRelayHop: the relay service; open as a stream in every state, with
	// each relay operation decided on its own.
	ClassRelayHop
	// ClassExchange: the certificate exchange.
	ClassExchange
	// ClassPairing: the request to establish trust.
	ClassPairing
	// ClassManagement: the authorized person's management requests.
	ClassManagement
	// ClassReachabilityQuery: a person's query for a node's reachability record.
	ClassReachabilityQuery
	// ClassApplication: everything else; members only.
	ClassApplication
)

func (c Class) String() string {
	switch c {
	case ClassBuiltin:
		return "builtin"
	case ClassRelayHop:
		return "relay-hop"
	case ClassExchange:
		return "exchange"
	case ClassPairing:
		return "pairing"
	case ClassManagement:
		return "management"
	case ClassReachabilityQuery:
		return "reachability-query"
	case ClassApplication:
		return "application"
	}
	return "unset"
}

// ClassOf returns the class of a protocol. The match is exact: a protocol with
// another version suffix is not the listed one and falls to ClassApplication.
func ClassOf(id ProtocolID) Class {
	switch id {
	case "":
		return ClassUnset
	case BuiltinIdentify, BuiltinIdentifyPush, BuiltinPing, BuiltinCircuitStop, BuiltinDCUtR:
		return ClassBuiltin
	case BuiltinCircuitHop:
		return ClassRelayHop
	case ProtocolCertExchange:
		return ClassExchange
	case ProtocolPairing:
		return ClassPairing
	case ProtocolManagement:
		return ClassManagement
	case ProtocolReachabilityQuery:
		return ClassReachabilityQuery
	}
	return ClassApplication
}
