# Pre-authentication

What a peer may do from the moment the secure handshake ends until its
certificate exchange has been judged, how that judgement turns into a connection
state, and how many connections each state may hold. The topic covers decisions
and data only; opening streams, timers and the libp2p resource manager belong to
the network layer, which applies these answers.

## What

### Connection states

Each connection is in one of four states, and the state belongs to the
connection, not to the peer:

| State | Meaning |
| --- | --- |
| unauthenticated | The secure handshake is done, so the peer's network key is known. No certificate exchange has been accepted on this connection. |
| non-member | The exchange completed with a valid certificate, but the peer is not a member and not an authorized person. It may only ask to establish trust. |
| member | The peer is a node the local node trusts directly or by inheritance, or whose trust is suspended. |
| authorized person | The peer is a person's device with a valid device certificate and a standing on the local authorized-persons list. |

### What each state may use

One function, `Allowed(state, use)`, answers it. `use` is a protocol or, on the
relay protocol, one relay operation.

| Use | unauthenticated | non-member | member | authorized person |
| --- | :---: | :---: | :---: | :---: |
| libp2p built-ins: identify, identify push, ping, circuit stop, DCUtR | yes | yes | yes | yes |
| relay stream (hop) | yes | yes | yes | yes |
| relay reservation | no | no | yes | no |
| relay circuit request | no | yes | yes | yes |
| certificate exchange | yes | yes | yes | yes |
| pairing | no | yes | yes | no |
| management | no | no | no | yes |
| every other protocol | no | no | yes | no |

The protocol identifiers are defined once, in `internal/preauth`:
`/sidera/cert-exchange/1.0.0`, `/sidera/pairing/1.0.0`, `/sidera/manage/1.0.0`.
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
exists, after which an unauthenticated connection is closed. Requirements on the
network layer:

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
4. `trust.Evaluate` decides: allowed directly or by inheritance is a member,
   **suspended is a member**, denied is a non-member (pairing only).

For a person:

1. The record must verify as a device certificate, and the peer must hold the
   device key it names. Otherwise refused.
2. `admin.EffectiveLevel` with the local list decides. A revoked device key or
   root key, an expired certificate or one that has not begun is refused. A
   person with no standing on the list is a non-member. A level makes an
   authorized person, at the lower of the list level and the certificate's cap.

A refused exchange leaves the state unauthenticated; the connection is closed.
Failure closed throughout: an unset time or peer key, an unset kind, and a
missing revocation set are refused, and a member or an authorized person exists
only through a positive answer from `trust` or `admin`.

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
  decode anything of consequence.
- **Exchange on the specific connection.** Measured with the library: the host's
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
- **The same certificate again is not stale.** `DecideSerial` answers stale for a
  certificate it has seen, because for storage "nothing new" is the right answer;
  for a connection the very certificate at the highest serial is exactly what a
  returning peer presents. Only a lower serial is an old key.
- **The serial moves forward even when the exchange is refused.** The node did
  issue that serial. An expired or revoked certificate with a higher serial still
  makes every lower serial an old key, which is the point of keeping the highest
  seen apart from the accepted certificate.
- **A person without a standing is a non-member, not refused.** The certificate
  is valid; the person is merely not listed here. Refusing would force the
  connection down for a fact that is not wrong with the peer, and the state
  leaves room for protocols a valid certificate alone may use. Such a connection
  is bounded by the non-member lifetime and concurrency limits like any other.
- **A missing revocation set is a refusal.** In `revocation` a `nil` set revokes
  nothing, which suits callers that load it first. A pre-authentication decision
  made without having looked at revocations would let a revoked key in, so it
  asks for an explicit empty set instead.

## Where

| Part | Code |
| --- | --- |
| States, verdicts | `internal/preauth/state.go` |
| Protocol identifiers and classes | `internal/preauth/protocol.go` |
| The access table | `internal/preauth/access.go`; tests `TestAllowedMatrix` (one row per rule, every cell written out), `TestBusinessProtocolBeforeTheExchangeIsRefused`, `TestStrangerAfterTheExchangeMayOnlyPair`, `TestRelayReservationBeforeTheExchangeIsRefused`, `TestAllowedRefusesAnInvalidState`, `TestProtocolIdentifiers` |
| The exchange message and the deadline | `internal/preauth/exchange.go`; tests `TestGoldenExchangeDecodes`, `TestExchangeEncodesTheGoldenBytes` (hand-written bytes, the signatures inside verified by an independent program), `TestDecodeExchangeRefuses` (one row per rule), `TestEncodeExchangeRefuses`, `FuzzDecodeExchange` |
| The decision | `internal/preauth/decide.go`; tests `TestDecideNode`, `TestDecidePerson` (one row per rule), `TestDecideAdvancesTheSerialSeen`, `TestKindDecidesWhichStatesArePossible`, `TestDecideOnTheGoldenMessages`, `FuzzDecide` |
