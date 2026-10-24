package preauth

import "testing"

// The expected answers are written out cell by cell, never derived from the
// classification the code uses, so a wrong cell in the code fails here.
type cell = Access

var (
	okBuiltin  = cell{VerdictAllow, AccessBuiltin}
	okExchange = cell{VerdictAllow, AccessExchange}
	okPairing  = cell{VerdictAllow, AccessPairing}
	okApp      = cell{VerdictAllow, AccessApplication}
	okManage   = cell{VerdictAllow, AccessManagement}
	okQuery    = cell{VerdictAllow, AccessReachabilityQuery}
	okReserve  = cell{VerdictAllow, AccessRelayReservation}
	okConnect  = cell{VerdictAllow, AccessRelayConnect}
	noExchange = cell{VerdictRefuse, RefusedExchangeRequired}
	noMember   = cell{VerdictRefuse, RefusedMemberOnly}
	noPerson   = cell{VerdictRefuse, RefusedPersonOnly}
	noNode     = cell{VerdictRefuse, RefusedNodeOnly}
	noInvalid  = cell{VerdictRefuse, RefusedInvalid}
)

func TestAllowedMatrix(t *testing.T) {
	rows := []struct {
		rule string
		use  Use
		// expected answers in the order: unauthenticated, non-member node,
		// non-member person, member, authorized person
		want [5]cell
	}{
		{"identify stays open in every state", Use{Protocol: "/ipfs/id/1.0.0"}, [5]cell{okBuiltin, okBuiltin, okBuiltin, okBuiltin, okBuiltin}},
		{"identify push stays open in every state", Use{Protocol: "/ipfs/id/push/1.0.0"}, [5]cell{okBuiltin, okBuiltin, okBuiltin, okBuiltin, okBuiltin}},
		{"ping stays open in every state", Use{Protocol: "/ipfs/ping/1.0.0"}, [5]cell{okBuiltin, okBuiltin, okBuiltin, okBuiltin, okBuiltin}},
		{"circuit stop stays open in every state", Use{Protocol: "/libp2p/circuit/relay/0.2.0/stop"}, [5]cell{okBuiltin, okBuiltin, okBuiltin, okBuiltin, okBuiltin}},
		{"DCUtR stays open in every state", Use{Protocol: "/libp2p/dcutr"}, [5]cell{okBuiltin, okBuiltin, okBuiltin, okBuiltin, okBuiltin}},
		{"the relay stream itself stays open in every state", Use{Protocol: "/libp2p/circuit/relay/0.2.0/hop"}, [5]cell{okBuiltin, okBuiltin, okBuiltin, okBuiltin, okBuiltin}},
		{"a relay reservation needs a completed exchange and a member", Use{Protocol: "/libp2p/circuit/relay/0.2.0/hop", Relay: RelayReserve}, [5]cell{noExchange, noMember, noMember, okReserve, noMember}},
		{"a relay circuit request needs a completed exchange", Use{Protocol: "/libp2p/circuit/relay/0.2.0/hop", Relay: RelayConnect}, [5]cell{noExchange, okConnect, okConnect, okConnect, okConnect}},
		{"the certificate exchange is open in every state", Use{Protocol: "/sidera/cert-exchange/1.0.0"}, [5]cell{okExchange, okExchange, okExchange, okExchange, okExchange}},
		{"pairing is for nodes that completed the exchange, never for a person", Use{Protocol: "/sidera/pairing/1.0.0"}, [5]cell{noExchange, okPairing, noNode, okPairing, noNode}},
		{"the reachability query is for persons that completed the exchange, never for a node", Use{Protocol: "/sidera/reachability-query/1.0.0"}, [5]cell{noExchange, noPerson, okQuery, noPerson, okQuery}},
		{"management is for authorized persons", Use{Protocol: "/sidera/manage/1.0.0"}, [5]cell{noExchange, noPerson, noPerson, noPerson, okManage}},
		{"a cluster protocol is for members", Use{Protocol: "/sidera/deliver/1.0.0"}, [5]cell{noExchange, noMember, noMember, okApp, noMember}},
		{"the reachability records members exchange are a cluster protocol", Use{Protocol: "/sidera/reachability/1.0.0"}, [5]cell{noExchange, noMember, noMember, okApp, noMember}},
		{"an unknown protocol is for members", Use{Protocol: "/example/unknown/1.0.0"}, [5]cell{noExchange, noMember, noMember, okApp, noMember}},
		{"another version of pairing is not pairing", Use{Protocol: "/sidera/pairing/2.0.0"}, [5]cell{noExchange, noMember, noMember, okApp, noMember}},
		{"another version of the reachability query is not the query", Use{Protocol: "/sidera/reachability-query/2.0.0"}, [5]cell{noExchange, noMember, noMember, okApp, noMember}},
		{"another version of a built-in is not the built-in", Use{Protocol: "/ipfs/ping/2.0.0"}, [5]cell{noExchange, noMember, noMember, okApp, noMember}},
		{"an empty protocol cannot be judged", Use{}, [5]cell{noInvalid, noInvalid, noInvalid, noInvalid, noInvalid}},
		{"a relay operation on another protocol cannot be judged", Use{Protocol: "/sidera/pairing/1.0.0", Relay: RelayReserve}, [5]cell{noInvalid, noInvalid, noInvalid, noInvalid, noInvalid}},
		{"an unknown relay operation cannot be judged", Use{Protocol: "/libp2p/circuit/relay/0.2.0/hop", Relay: RelayOp(9)}, [5]cell{noInvalid, noInvalid, noInvalid, noInvalid, noInvalid}},
	}
	states := [5]State{StateUnauthenticated, StateNonMemberNode, StateNonMemberPerson, StateMember, StateAuthorizedPerson}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			for i, s := range states {
				if got := Allowed(s, r.use); got != r.want[i] {
					t.Errorf("state %v: got %v/%v, want %v/%v", s, got.Verdict, got.Reason, r.want[i].Verdict, r.want[i].Reason)
				}
			}
		})
	}
}

func TestAllowedRefusesAnInvalidState(t *testing.T) {
	uses := []Use{
		{Protocol: "/ipfs/id/1.0.0"},
		{Protocol: "/sidera/cert-exchange/1.0.0"},
		{Protocol: "/sidera/pairing/1.0.0"},
		{Protocol: "/sidera/reachability-query/1.0.0"},
		{Protocol: "/sidera/deliver/1.0.0"},
		{Protocol: "/libp2p/circuit/relay/0.2.0/hop", Relay: RelayConnect},
	}
	for _, s := range []State{StateUnset, State(6), State(255)} {
		for _, use := range uses {
			if got := Allowed(s, use); got != noInvalid {
				t.Errorf("state %d, %v: got %v/%v, want a refusal as invalid", s, use, got.Verdict, got.Reason)
			}
		}
	}
}

// Business protocols before the exchange are refused, in particular the ones
// that carry messages and control signals and the one that establishes trust.
func TestBusinessProtocolBeforeTheExchangeIsRefused(t *testing.T) {
	for _, id := range []ProtocolID{"/sidera/deliver/1.0.0", "/sidera/control/1.0.0", "/sidera/trust-sync/1.0.0", ProtocolPairing, ProtocolManagement, ProtocolReachabilityQuery} {
		if got := Allowed(StateUnauthenticated, Use{Protocol: id}); got != noExchange {
			t.Errorf("%s: got %v/%v, want refused, exchange required", id, got.Verdict, got.Reason)
		}
	}
}

// A node that is not a member and completed the exchange may only pair: every
// protocol the matrix allows it is a built-in, the exchange, pairing, or a relay
// circuit.
func TestNonMemberNodeMayOnlyPair(t *testing.T) {
	probes := map[ProtocolID]Verdict{
		ProtocolPairing:                  VerdictAllow,
		ProtocolCertExchange:             VerdictAllow,
		BuiltinPing:                      VerdictAllow,
		ProtocolManagement:               VerdictRefuse,
		ProtocolReachabilityQuery:        VerdictRefuse,
		"/sidera/deliver/1.0.0":          VerdictRefuse,
		"/sidera/control/1.0.0":          VerdictRefuse,
		"/sidera/reachability/1.0.0":     VerdictRefuse,
		"/sidera/revocation-sync/1.0.0":  VerdictRefuse,
		"/sidera/trust-graph-sync/1.0.0": VerdictRefuse,
	}
	for id, want := range probes {
		if got := Allowed(StateNonMemberNode, Use{Protocol: id}).Verdict; got != want {
			t.Errorf("%s: got %v, want %v", id, got, want)
		}
	}
}

// A person with a valid certificate who is not on the list may only ask for a
// node's reachability record (the handler then checks the proof): pairing is
// between nodes, and everything else is for members or authorized persons.
func TestNonMemberPersonMayOnlyQueryReachability(t *testing.T) {
	probes := map[ProtocolID]Verdict{
		ProtocolReachabilityQuery:        VerdictAllow,
		ProtocolCertExchange:             VerdictAllow,
		BuiltinPing:                      VerdictAllow,
		ProtocolPairing:                  VerdictRefuse,
		ProtocolManagement:               VerdictRefuse,
		"/sidera/deliver/1.0.0":          VerdictRefuse,
		"/sidera/control/1.0.0":          VerdictRefuse,
		"/sidera/reachability/1.0.0":     VerdictRefuse,
		"/sidera/revocation-sync/1.0.0":  VerdictRefuse,
		"/sidera/trust-graph-sync/1.0.0": VerdictRefuse,
	}
	for id, want := range probes {
		if got := Allowed(StateNonMemberPerson, Use{Protocol: id}).Verdict; got != want {
			t.Errorf("%s: got %v, want %v", id, got, want)
		}
	}
}

// Relay reservations after the exchange only: the order "reserve, then
// exchange" is refused, the order "exchange, then reserve" works for a member.
func TestRelayReservationBeforeTheExchangeIsRefused(t *testing.T) {
	use := Use{Protocol: BuiltinCircuitHop, Relay: RelayReserve}
	if got := Allowed(StateUnauthenticated, use); got != noExchange {
		t.Errorf("before the exchange: got %v/%v", got.Verdict, got.Reason)
	}
	if got := Allowed(StateMember, use); got != okReserve {
		t.Errorf("after the exchange, as a member: got %v/%v", got.Verdict, got.Reason)
	}
}

func TestClassOf(t *testing.T) {
	for _, row := range []struct {
		id   ProtocolID
		want Class
	}{
		{"", ClassUnset},
		{BuiltinIdentify, ClassBuiltin},
		{BuiltinIdentifyPush, ClassBuiltin},
		{BuiltinPing, ClassBuiltin},
		{BuiltinCircuitStop, ClassBuiltin},
		{BuiltinDCUtR, ClassBuiltin},
		{BuiltinCircuitHop, ClassRelayHop},
		{ProtocolCertExchange, ClassExchange},
		{ProtocolPairing, ClassPairing},
		{ProtocolManagement, ClassManagement},
		{ProtocolReachabilityQuery, ClassReachabilityQuery},
		{"/sidera/anything/1.0.0", ClassApplication},
	} {
		if got := ClassOf(row.id); got != row.want {
			t.Errorf("%q: got %v, want %v", row.id, got, row.want)
		}
	}
}

// The identifiers are written out here, not taken from the constants, so a
// change to one of them (a wire-visible name) fails this test.
func TestProtocolIdentifiers(t *testing.T) {
	for _, row := range []struct {
		got  ProtocolID
		want string
	}{
		{ProtocolCertExchange, "/sidera/cert-exchange/1.0.0"},
		{ProtocolPairing, "/sidera/pairing/1.0.0"},
		{ProtocolManagement, "/sidera/manage/1.0.0"},
		{ProtocolReachabilityQuery, "/sidera/reachability-query/1.0.0"},
		{BuiltinIdentify, "/ipfs/id/1.0.0"},
		{BuiltinIdentifyPush, "/ipfs/id/push/1.0.0"},
		{BuiltinPing, "/ipfs/ping/1.0.0"},
		{BuiltinCircuitStop, "/libp2p/circuit/relay/0.2.0/stop"},
		{BuiltinCircuitHop, "/libp2p/circuit/relay/0.2.0/hop"},
		{BuiltinDCUtR, "/libp2p/dcutr"},
	} {
		if string(row.got) != row.want {
			t.Errorf("got %q, want %q", row.got, row.want)
		}
	}
}
