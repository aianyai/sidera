# preauth

What a freshly connected peer may do before and after the certificate exchange.
Read [../README.md](../README.md) first for the conventions every contract
package follows, and
[the topic document](../../docs/references/preauth/README.md) for the reasons
behind the rules and the honest limits of the mechanism. This package is pure
data and decisions: the network layer owns the libp2p runtime, the clock and the
timers, and applies the answers.

## Entry points

| Name                                                    | Purpose                                                                                                         |
| ------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `ProtocolCertExchange`, `ProtocolPairing`, `ProtocolManagement` | The Sidera protocol identifiers whose handling differs by state. Defined here and nowhere else.         |
| `BuiltinIdentify`, `BuiltinIdentifyPush`, `BuiltinPing`, `BuiltinCircuitStop`, `BuiltinCircuitHop`, `BuiltinDCUtR` | The libp2p protocols that stay open in every state, as names. |
| `ClassOf(protocol)`                                     | The class of a protocol: built-in, relay, exchange, pairing, management, or application (members only).         |
| `State`                                                 | `StateUnauthenticated`, `StateNonMember`, `StateMember`, `StateAuthorizedPerson`; the zero value is unset.     |
| `Exchange`, `NewNodeExchange`, `NewPersonExchange`, `Encode`, `DecodeExchange` | The certificate exchange message: kind plus the certificate record, canonical CBOR, at most `MaxMessageSize` bytes. Not a signed record. |
| `ExchangeDeadline`                                      | The 5 seconds a connection has to complete the exchange.                                                        |
| `Decide(Facts, peerKey, Exchange)`                      | From a received exchange and the local facts to a `Decision{State, Reason, ...}`: unauthenticated (refused), non-member, member or authorized person. |
| `Allowed(state, Use)`                                   | The one table: whether a peer in a state may open a stream of a protocol or make a relay request. `Access{Verdict, Reason}`. |

## Rules

- **Default closed.** Every protocol that is not listed is a cluster protocol
  and only members may use it. A new cluster protocol needs no entry here to be
  safe; a protocol that must be open to anyone else needs one, together with its
  rows in the test matrix.
- **Open in every state.** The libp2p built-ins (identify, identify push, ping,
  circuit stop, DCUtR), the relay stream itself, and the certificate exchange.
  Peers need them before any certificate exists, and refusing them would stop
  two nodes behind NATs from ever reaching each other.
- **A stranger that completed the exchange may only pair.** A non-member may use
  the pairing protocol and a relay circuit request, nothing else. A member may
  also pair.
- **Relay operations are decided one by one.** A reservation needs a member, so
  it can only follow a completed exchange; a circuit request needs a completed
  exchange. The relay's allow list and rate limit decide beyond that.
- **Management is for authorized persons.** Pairing is between nodes, so a
  person's device cannot use it.
- **Unjudgeable input is refused.** An unset state, an empty protocol identifier
  and a relay operation on a protocol that is not the relay protocol are refused
  with `RefusedInvalid`.
- **Exact match.** A protocol identifier with another version suffix is not the
  listed one and is a cluster protocol.

- **One message type, two kinds.** The message is `{1: kind, 2: record}` with
  kind 1 for a node's network-key certificate and 2 for a person's device
  certificate; the record is the encoded signed record as a byte string. A
  person sends nothing more than the device certificate: the root key that
  signed it derives the person identifier the certificate names, and
  verification checks that, so the receiver has what it needs to look the
  person up. Revocations and the authorized-persons list are the receiver's own.
- **Strict decoding, bounded first.** `DecodeExchange` applies the size bound
  (`MaxMessageSize`, 1 KiB) before anything else, then the strict rules of
  `record` (no duplicate keys, unknown fields, indefinite lengths, tags; the
  bytes must be the canonical encoding), then decodes the certificate record the
  same way. It checks no signature and not that the record's type fits the kind;
  verification does both. Any message that decodes encodes to the identical
  bytes.

- **The decision.** `Decide` verifies the certificate record against the
  descriptor of its kind itself, so a caller cannot skip verification, and then:
  - A node exchange is refused (state stays unauthenticated, the connection is
    closed) when the record does not verify, its serial is older than the highest
    seen or conflicts with it, the node is retired, the certificate is revoked,
    expired or not yet valid, or it names a network key other than the one the
    secure handshake authenticated. Otherwise `trust.Evaluate` decides: allowed
    directly or by inheritance and **suspended** are members, denied is a
    non-member. A suspended node stays a member because its trust is waiting for
    a voucher, not gone; the policy layer gets the suspended outcome in
    `Decision.Trust` and holds what it must.
  - A person exchange is refused when the device certificate does not verify,
    names a device key other than the authenticated one, is revoked (the device
    key, or the person's root key), expired or not yet valid. Otherwise
    `admin.EffectiveLevel` decides: a level is an authorized person, anything
    else (not listed, entry expired) a non-member.
  - **Failure closed.** An unset time, an unset peer key, an unset kind and a
    `nil` revocation set are refused. A `nil` set is not "nothing revoked" here,
    as it is in `revocation`: use `revocation.NewSet()` for a node that has
    revoked nothing. No code path makes a node a member or a person authorized
    without the positive answer of `trust` or `admin`.
  - **The serial seen** is advanced in `Decision.SerialSeen` whenever a verified
    certificate carries a higher serial, also when the exchange is refused for
    another reason. The same certificate again (the normal reconnect) is not
    stale; only a lower serial is.

## What the caller must guarantee

- **The exchange runs on the specific connection**, opened with the
  connection's own stream function and not the host's: the host picks the best
  connection to a peer, so a relayed connection would never be asked and would
  be closed at the deadline. The side whose connection direction is outbound
  starts it: it opens the stream and writes its message; the other side writes
  its own after reading the first. Each side writes exactly one message and then
  closes its write side; the reader reads up to `MaxMessageSize` plus one byte
  and refuses more. After a hole punch both ends may start one; the later
  exchange on a connection that is already authenticated is ignored.
- **The deadline is `ExchangeDeadline` from the moment the connection exists**;
  a connection still unauthenticated then is closed.

- The peer key passed to `Decide` is the public key the secure handshake
  authenticated on this connection, and nothing else. It is the only identity the
  peer proved; the message is what it claims.
- `Facts` is loaded fresh: the revocation set holds every revocation the node
  keeps, `Trust.Retired` is `Revocations.Retired()`, and `SerialSeen` is the
  stored state per node. The caller stores `Decision.SerialSeen` when it is not
  the zero value and keeps `Decision.NodeCertificate` as the node's accepted
  certificate when the exchange completed.
- Applications register their stream handlers only through the gated host that
  asks `Allowed` first. A handler registered on the raw host bypasses the gate;
  the library's own protocols are the ones registered that way on purpose.
- The state passed in is the state of the connection the stream arrived on, not
  of the peer: a peer can hold several connections in different states.
- A refusal is carried out by resetting the stream after negotiation. The peer
  sees "negotiation succeeded, then stream reset"; nothing earlier is possible.
