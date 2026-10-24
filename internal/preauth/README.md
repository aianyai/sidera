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

## What the caller must guarantee

- Applications register their stream handlers only through the gated host that
  asks `Allowed` first. A handler registered on the raw host bypasses the gate;
  the library's own protocols are the ones registered that way on purpose.
- The state passed in is the state of the connection the stream arrived on, not
  of the peer: a peer can hold several connections in different states.
- A refusal is carried out by resetting the stream after negotiation. The peer
  sees "negotiation succeeded, then stream reset"; nothing earlier is possible.
