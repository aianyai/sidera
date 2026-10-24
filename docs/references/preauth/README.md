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

## Where

| Part | Code |
| --- | --- |
| States, verdicts | `internal/preauth/state.go` |
| Protocol identifiers and classes | `internal/preauth/protocol.go` |
| The access table | `internal/preauth/access.go`; tests `TestAllowedMatrix` (one row per rule, every cell written out), `TestBusinessProtocolBeforeTheExchangeIsRefused`, `TestStrangerAfterTheExchangeMayOnlyPair`, `TestRelayReservationBeforeTheExchangeIsRefused`, `TestAllowedRefusesAnInvalidState`, `TestProtocolIdentifiers` |
