# Pre-authentication

What a peer may do from the moment the secure handshake ends until its
certificate exchange has been judged, how that judgement turns into a connection
state, and how many connections each state may hold. The topic covers decisions
and data only; opening streams, timers and the libp2p resource manager belong to
the network layer, which applies these answers.

## What

### Connection states

Each connection is in one of five states, and the state belongs to the
connection, not to the peer:

| State | Meaning |
| --- | --- |
| unauthenticated | The secure handshake is done, so the peer's network key is known. No certificate exchange has been accepted on this connection. |
| non-member node | The exchange completed with a valid network-key certificate, but the node is not a member. It may only ask to establish trust. |
| non-member person | The exchange completed with a valid, checked device certificate, but the person has no standing on the local list. It may only query a node's reachability record. |
| member | The peer is a node the local node trusts directly or by inheritance, or whose trust is suspended. |
| authorized person | The peer is a person's device with a valid device certificate and a standing on the local authorized-persons list. |

### What each state may use

One function, `Allowed(state, use)`, answers it. `use` is a protocol or, on the
relay protocol, one relay operation.

| Use | unauthenticated | non-member node | non-member person | member | authorized person |
| --- | :---: | :---: | :---: | :---: | :---: |
| libp2p built-ins: identify, identify push, ping, circuit stop, DCUtR | yes | yes | yes | yes | yes |
| relay stream (hop) | yes | yes | yes | yes | yes |
| relay reservation | no | no | no | yes | no |
| relay circuit request | no | yes | yes | yes | yes |
| certificate exchange | yes | yes | yes | yes | yes |
| pairing | no | yes | no | yes | no |
| reachability query | no | no | yes | no | yes |
| management | no | no | no | no | yes |
| every other protocol | no | no | no | yes | no |

The protocol identifiers are defined once, in `internal/preauth`:
`/sidera/cert-exchange/1.0.0`, `/sidera/pairing/1.0.0`, `/sidera/manage/1.0.0`,
`/sidera/reachability-query/1.0.0`.
The built-ins are listed by name only (`/ipfs/id/1.0.0`, `/ipfs/id/push/1.0.0`,
`/ipfs/ping/1.0.0`, `/libp2p/circuit/relay/0.2.0/stop`,
`/libp2p/circuit/relay/0.2.0/hop`, `/libp2p/dcutr`); the library registers them
itself.

A refusal reaches the peer as "the protocol was negotiated, then the stream was
reset". The protocol library has no per-connection hook at negotiation, so the
gate is a wrapper around the handler: it checks the state of the connection the
stream arrived on and resets the stream when `Allowed` refuses. Everything the
application registers must go through that wrapper; a handler registered on the
raw host bypasses the gate.

### The certificate exchange

A peer proves who it is with a certificate, sent in one message on the exchange
protocol. One protocol serves nodes and persons, and the message says which:

```
exchange = { 1: kind, 2: record }
kind     = 1 (node: the network-key certificate)
         | 2 (person: the device certificate)
record   = the encoded signed record, as a byte string
```

The message is deterministic CBOR and is not a signed record: the sender is
already authenticated by the secure handshake, and the certificate inside carries
the signature that matters. It is at most 1024 bytes (a certificate message is
about 230). A decoder applies the size bound first, then the strict rules of
[records](../records/README.md) to the message and again to the record inside:
duplicate keys, unknown fields, indefinite lengths, tags and any encoding but the
canonical one are refused, so a message that decodes encodes to the same bytes.

A person sends only the device certificate. The person identifier derives from
the root key that signed it, and verification checks that the key derives the
identifier the certificate names, so the receiver can look the person up on its
list from the certificate alone. Whether the certificate is revoked or the person
is on the list are the receiver's own facts.

The exchange has a fixed deadline of 5 seconds from the moment the connection
exists, after which an unauthenticated inbound connection is closed. The time
an outbound connection may take is the network layer's own: the node chose to
dial it, and its dial timeout already bounds it. Requirements on the network
layer:

- The exchange runs on the specific connection, opened with the connection's own
  stream function. The host's stream function picks the best connection to the
  peer, so a relayed connection is never asked and sits unauthenticated until the
  deadline.
- The side whose connection direction is outbound starts it. It opens the stream
  and writes its message; the other side reads it and writes its own. Each side
  writes one message and closes its write side; a reader reads at most the size
  bound plus one byte and refuses more.
- After a hole punch both ends may start an exchange, because the direction a
  hole-punched connection reports is outbound on both sides. An exchange on a
  connection that is already authenticated is ignored.

### From the exchange to a state

`Decide(facts, peerKey, exchange)` turns the message into a state. `peerKey` is
the public key the secure handshake authenticated; it is the only identity the
peer proved, and the certificate must name it. The decision verifies the
certificate itself, so no caller can skip that.

For a node:

1. The record must verify as a network-key certificate. Otherwise refused.
2. The serial is compared with the highest seen for the node (`DecideSerial`).
   A higher one advances the state, which is returned in the decision and kept
   even if the exchange is refused for another reason. The very certificate seen
   at the highest serial is the normal case of a peer that connects again and is
   accepted; a lower serial is an old network key and is refused; the same serial
   with another digest is a conflict and is refused.
3. The certificate must belong to the peer (`NetworkKeyBelongs` with the
   revocation lookup of the set): a different key, a revoked certificate, an
   expired one or one that has not begun is refused. A node whose identity key
   has a self-revocation is refused as retired.
4. `trust.Evaluate` decides, on the trust view with the retired nodes of the
   revocation set added to its own (a retired intermediate carries no trust):
   allowed directly or by inheritance is a member,
   **suspended is a member**, denied is a non-member node (pairing only).

For a person:

1. The record must verify as a device certificate, and the peer must hold the
   device key it names. Otherwise refused.
2. `admin.EffectiveLevel` with the local list decides. A revoked device key or
   root key, an expired certificate or one that has not begun is refused. A
   person with no standing on the list is a non-member person (reachability
   query only). A level makes an
   authorized person, at the lower of the list level and the certificate's cap.

A refused exchange leaves the state unauthenticated; the connection is closed.
Failure closed throughout: an unset time or peer key, an unset kind, and a
missing revocation set are refused, and a member or an authorized person exists
only through a positive answer from `trust` or `admin`.

### Connection accounting

Three limits keep strangers from crowding out the cluster. They rest on one
rule of the books: at most one inbound connection per key (the peer's network
key and the remote address) is tracked at a time, and a second one is refused.


- **The aggregate cap.** At most `UnauthenticatedCap` inbound connections may be
  unauthenticated at once. The check runs where the connection gater still can
  refuse, right after the secure handshake, when the peer's key is known.
- **The known-member exemption.** An inbound connection of a peer known as a
  member is let through and not counted. Known members are the peers whose
  exchange completed as a member or an authorized person, and the members whose
  valid network-key certificates the node holds (`HeldMembers` picks them with
  the same rules `Decide` uses: current serial, valid, unrevoked, and a member by
  `trust`). A peer stops being known when an exchange decides it a non-member
  or is refused, and when the caller says its standing ended (`Forget`: a
  member's trust ended, a person was removed from the list or revoked). A person's new
  device, and a node with a new network key, are strangers until their first
  exchange completes.
- **The non-member limits.** A connection whose exchange completed as a
  non-member, node or person alike, counts toward `NonMemberConcurrency`; when
  none is free the
  connection is closed. A non-member lives at most `NonMemberLifetime`, counted
  from the connection, so the limit covers the exchange and the online part of
  pairing and has to be at least `ExchangeDeadline` plus the pairing online
  timeout (checked by `Params.Validate`).

The model is a set of state transitions the network layer drives with the events
it sees (handshake done, connected, exchange decided, disconnected) and a clock
passed in. It has no timers: `Overdue` says which connections to close now, and
`Sweep` forgets entries never seen connected. All numbers are `Params`; none is
decided in this package.

Honest limits, from the measurements behind this design:

- **The cap bounds occupancy, not cost.** The secure handshake runs before the
  cap is consulted, so a refused stranger has already cost a handshake of CPU
  and bandwidth. Before the handshake only the resource manager's per-address
  limits apply, which do not tell strangers from members.
- **Per-address limits hurt members behind one address.** With the library's
  defaults a single IPv4 address may hold 8 concurrent connections, so several
  members behind one egress address contend. The network layer configures it.
- **Refusals look like resets.** A refused protocol is negotiated and then the
  stream is reset; a connection refused by the cap is closed after a successful
  handshake.
- **The hole-punch direction disagrees.** A hole-punched connection is inbound
  at the handshake and reports outbound afterwards on both sides, so entries are
  keyed without direction and both ends may start an exchange.
- **Entries can leak without a sweep.** After the handshake the connection can
  still be rejected (resource limits on the peer, multiplexer negotiation,
  upgrade checks) with no connected or disconnected callback.
- **A flood starves members without the exemption.** In the measurement with 1,000
  new identities arriving in a burst against a cap of 64 and a 5-second exchange
  deadline, only 9 of 20 members completed within 10 seconds; with the exemption
  all 20 completed in about 20 ms.

Per-peer limits belong to the network layer. The resource manager asks its
limiter once, when it creates a peer's scope, and keeps the returned object,
re-reading its values on every check. So the limiter must return one mutable
limit object per peer for as long as a scope may live (scopes with no use are
collected every minute and recreated on reconnect), move it between tiers when
the connection state changes, reset it to the unauthenticated tier at the peer's
last disconnect instead of deleting it (a stranger reconnecting under the same
identity would otherwise inherit a member's allowance from a live scope), and
reconcile its table with the manager's peer list, since the table grows with the
identities seen. The connection manager's protection is for members and is not a
cache of who is known: the accounting keeps its own known set.

## Why

- **Default closed.** The table lists the exceptions and every other protocol
  is for members. A forgotten entry for a new cluster protocol then fails
  closed; the reverse (a list of member protocols) would fail open.
- **Built-ins stay open in every state.** A node behind a NAT is reached
  through a relay, and the direct path comes from a hole punch that the library
  starts right after identify, before any certificate exists. Refusing identify,
  the circuit protocols or DCUtR to unknown peers would leave almost every NAT
  pair on the relay. Identify already tells any peer the listen addresses, so a
  punched path exposes nothing more; a stranger that obtains it can still only
  ask to establish trust.
- **Reservation is decided apart from the relay stream.** The relay protocol
  carries both "hold a slot for me" and "connect me to X". A relay serves the
  cluster, so only a member may reserve, and a reservation can only follow the
  exchange because the member state comes from it. A circuit request is open
  after the exchange, because a stranger who wants to pair with a node behind a
  relay has no other way to reach it; who gets through is the relay's allow list
  and rate limit.
- **A person without a standing is not a node without trust.** The two
  non-member states differ in what they may ask for. A node that is not a member
  may ask to establish trust, which is a request between nodes. A person has no
  trust to establish: a person with a valid, checked device certificate may ask
  any member for a node's reachability record by presenting an administrator's
  reachability proof, and that proof, not the person's standing on this node's
  list, is what entitles the person to the answer. The query handler checks the
  proof with `admin.CheckReachabilityProof`; the access table only opens the
  stream, to persons and to no node, so a node cannot probe reachability records
  and a person cannot start pairing. Both states are strangers for the
  accounting and share its limits.
- **The management protocol is its own class.** An authorized person is not a
  node and is not a member, and members are not people; neither may use the
  other's protocol. What a management request may do is the administration
  rules' decision.
- **A message, not a signed record.** The sender is authenticated by the secure
  handshake and the certificate carries its own signature; wrapping the exchange
  in a signed record would add a signature that proves nothing the handshake did
  not, and a signing domain to maintain. The protocol messages of pairing are
  typed canonical messages for the same reason.
- **One protocol, a kind field.** A node connecting to a node and a person's tool
  connecting to a node are told apart by what they present, so the receiver needs
  one handler and one deadline, and a new kind of peer needs a new value, not a
  new protocol.
- **Nothing extra for a person.** The person identifier is derived from the root
  public key and the certificate is signed by it, so the certificate already
  proves the identifier. An extra field would be a second statement of the same
  fact that could disagree with the first.
- **The size bound is a protocol rule.** It is the same on every node and small,
  because an unauthenticated peer must not be able to make a node buffer or
  decode anything of consequence. It covers Ed25519 certificates. Raising it, for
  example to carry the larger signatures of a post-quantum algorithm, is a
  protocol change: a node that keeps the old bound refuses the longer message, so
  the exchange protocol gets a new version identifier alongside.
- **Exchange on the specific connection.** Observed with the library: the host's
  stream function chooses a direct connection when there is one, so a relayed
  connection never receives an exchange and is closed at the deadline. Opening
  the stream on the connection avoids it.
- **A suspended node is a member.** `trust` suspends a node when a path to it
  exists and lacks only a fresh voucher. Authorization already treats that
  outcome on its own: it neither hands out nor rejects what it holds, and the
  sender sees nothing until trust returns or is denied. Refusing the connection
  would turn a pause into a denial, and the vouchers that end it travel on member
  protocols. The connection state follows `trust`'s outcome, the policy layer
  gets the outcome itself.
- **Retirement is read from the revocation set, once.** Whether a node is
  retired is a fact of the revocation set, and `trust` needs it for the
  intermediates of a path as well as for the target. If the caller had to copy the
  set into the trust view, forgetting to would silently trust through a retired
  intermediate, and the decision and the held-member selection could disagree
  about the same node. Both read it where the decision is made, as the union of
  the view's own set and the revocation set, and leave the caller's view alone.
- **The same certificate again is not stale.** `DecideSerial` answers stale for a
  certificate it has seen, because for storage "nothing new" is the right answer;
  for a connection the very certificate at the highest serial is exactly what a
  returning peer presents. Only a lower serial is an old key.
- **The stored serial only moves forward.** Exchanges on different connections
  are decided at the same time, each against the facts it loaded. The caller
  therefore stores `Decision.SerialSeen` only after re-deciding against the state
  stored now, or only when its serial is higher; storing a decision's value
  blindly would let a slower decision with a lower serial overwrite a higher one
  and bring an old network key back.
- **The serial moves forward even when the exchange is refused.** The node did
  issue that serial. An expired or revoked certificate with a higher serial still
  makes every lower serial an old key, which is the point of keeping the highest
  seen apart from the accepted certificate.
- **A person without a standing is a non-member person, not refused.** The
  certificate is valid; the person is merely not listed here. Refusing would
  force the connection down for a fact that is not wrong with the peer, and the
  reachability query is exactly a use a valid certificate alone opens. Such a
  connection is bounded by the non-member lifetime and concurrency limits like
  any other.
- **A missing revocation set is a refusal.** `revocation` panics on a `nil` set,
  so that a set nobody loaded cannot read as "nothing revoked". A
  pre-authentication decision runs on a connection a stranger controls, and a
  panic there would be a crash that peer can cause by timing alone, so this
  package refuses a `nil` set first, with a reason of its own: a missing set is a
  decision, not a crash. A node that has revoked nothing passes an explicit empty
  set.
- **Exempt known members from the cap.** The cap exists so that strangers cannot
  hold the node's connections. The same cap shuts out members that reconnect
  during a flood, and the flood costs the attacker nothing. A key authenticated
  by the handshake cannot be spoofed, so exempting it gives an attacker nothing;
  it also keeps its own deadline. The exemption is withdrawn when the standing
  behind it ends, not left to expire: a refused exchange (the certificate was
  revoked or replaced, so the peer is not who the exemption was for) and a
  non-member decision forget the peer at once, and the caller forgets a peer when
  a member's trust ends or a person leaves the list. The residual risk is a
  peer whose standing ended without the caller saying so and that never
  exchanges again: its connections stay outside the cap, though each is closed
  at its deadline, until its next exchange or the next refresh of the held
  members.
- **One inbound connection per key.** The connection gater is asked once per
  connection, before the library has given it an identifier, so (peer, address)
  is the finest key both the gater and the later connection events can obtain.
  A second call for a tracked key is therefore a second connection, not the same
  one told twice. Admitted, it would share the first connection's entry, and
  once either ended the other would live outside the books: past the cap, past
  the 5-second deadline, past the non-member lifetime. So the second connection
  is refused with a reason of its own, whether its peer is a known member or not
  (the exemption is from the cap, not from the books). A legitimate peer does
  not hold two simultaneous inbound connections from one address, and a refused
  retry succeeds as soon as the first connection completed its exchange or
  ended. The same rule keeps every entry tied to exactly one connection: the
  exempt path only creates entries for keys that have none, so nothing is counted
  twice, and an entry leaves with its connection (disconnect, sweep, or release
  as a member). A reference count of connections per key was rejected: a
  connection rejected after the handshake produces no event, so a count would
  never reach zero. Because events name a key and not a connection, the caller
  reports a disconnect only for the accounted connection (the one `Connected`
  answered true for and `Completed` has not released), so a released connection
  that ends cannot remove the entry of a newer one.
- **Key without direction.** The direction the handshake reports and the one the
  connection reports afterwards differ for a hole-punched connection. An entry
  keyed with direction was never found again by the later events and leaked.
- **Count inbound only.** The node chooses its outbound connections; inbound
  ones are what a stranger controls.
- **Lifetime counted from the connection.** The online part of pairing starts
  after the exchange, so a lifetime counted from the exchange would need only the
  pairing timeout. Counting from the connection keeps one clock per connection
  for the deadline and the lifetime, and the requirement is stated on the sum.
- **No timers in the model.** `Overdue` is a question about state and time, so
  a late or missed timer delays a close and cannot leak a slot, and tests step
  the clock in seconds instead of sleeping.
- **Sweep, not trust in callbacks.** Waiting for a disconnect that never comes
  would make the cap shrink over time until strangers and members alike are
  refused.

## Where

| Part | Code |
| --- | --- |
| States, verdicts | `internal/preauth/state.go` |
| Protocol identifiers and classes | `internal/preauth/protocol.go` |
| The access table | `internal/preauth/access.go`; tests `TestAllowedMatrix` (one row per rule, every cell written out), `TestBusinessProtocolBeforeTheExchangeIsRefused`, `TestNonMemberNodeMayOnlyPair`, `TestNonMemberPersonMayOnlyQueryReachability`, `TestRelayReservationBeforeTheExchangeIsRefused`, `TestAllowedRefusesAnInvalidState`, `TestProtocolIdentifiers` |
| The exchange message and the deadline | `internal/preauth/exchange.go`; tests `TestGoldenExchangeDecodes`, `TestExchangeEncodesTheGoldenBytes` (hand-written bytes, the signatures inside verified by an independent program), `TestDecodeExchangeRefuses` (one row per rule), `TestEncodeExchangeRefuses`, `FuzzDecodeExchange` |
| The decision | `internal/preauth/decide.go`; tests `TestDecideNode`, `TestDecidePerson` (one row per rule), `TestDecideAdvancesTheSerialSeen`, `TestDecideRecordsNothingForASerialConflict`, `TestHeldMembers`, `TestDecideLeavesTheCallersTrustViewAlone`, `TestKindDecidesWhichStatesArePossible`, `TestDecideOnTheGoldenMessages`, `FuzzDecide` |
| Parameters | `internal/preauth/params.go`; test `TestParamsValidate` |
| The accounting model | `internal/preauth/accounting.go`; tests `TestAggregateCapRefusesBeyondTheCap`, `TestOutboundConnectionsAreNotAccounted`, `TestAKnownMemberIsExemptFromTheCapUnderAFlood`, `TestAMemberLearnedFromACompletedExchangeIsExemptNextTime`, `TestASecondInboundConnectionOnATrackedKeyIsRefused`, `TestADuplicateKeyIsRefusedBeforeTheCapIsLookedAt`, `TestADuplicateKeyOfAKnownMemberIsRefusedWithoutLeakingAnEntry`, `TestADuplicateKeyOfANonMemberIsRefused`, `TestADuplicateKeyIsRefusedUntilTheSweepForgetsALostEntry`, `TestAKeyIsFreeAgainOnceItsConnectionBecameAMember`, `TestAnAuthorizedPersonIsKnownAfterItsFirstExchangeOnly`, `TestSetHeldReplacesTheHeldMembersAndKeepsTheLearnedOnes`, `TestExchangeCompletionReleasesTheAggregateSlot`, `TestDisconnectReleasesWhateverTheConnectionHeld`, `TestTheKeyHasNoDirection`, `TestNonMemberConcurrencyLimit`, `TestANonMemberThatBecomesAMemberFreesItsPlace`, `TestANonMemberDecisionTakesAwayWhatWasKnown`, `TestARefusedExchangeWithdrawsTheExemption`, `TestAnInvalidCompletedCallDoesNotWithdrawTheExemption`, `TestForgetEndsTheKnownStandingFromEitherSource`, `TestNonMemberPersonsAndNodesShareTheNonMemberLimits`, `TestSweepForgetsConnectionsNeverSeenConnected`, `TestOverdue*`; the property test `TestAccountingAgainstTheReference` follows every connection by an identifier of its own that the model never sees, compares every answer with a second implementation under 40,000 random events, asserts at every step that each live inbound connection is on the books and bound by a deadline or was released as a member, that every connected entry has an open connection behind it and that nothing remains once all connections ended, and asserts how often each path was taken |
