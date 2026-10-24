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
| `ProtocolCertExchange`, `ProtocolPairing`, `ProtocolManagement`, `ProtocolReachabilityQuery` | The Sidera protocol identifiers whose handling differs by state. Defined here and nowhere else. |
| `BuiltinIdentify`, `BuiltinIdentifyPush`, `BuiltinPing`, `BuiltinCircuitStop`, `BuiltinCircuitHop`, `BuiltinDCUtR` | The libp2p protocols that stay open in every state, as names. |
| `ClassOf(protocol)`                                     | The class of a protocol: built-in, relay, exchange, pairing, management, reachability query, or application (members only). |
| `State`                                                 | `StateUnauthenticated`, `StateNonMemberNode`, `StateNonMemberPerson`, `StateMember`, `StateAuthorizedPerson`; the zero value is unset. |
| `Exchange`, `NewNodeExchange`, `NewPersonExchange`, `Encode`, `DecodeExchange` | The certificate exchange message: kind plus the certificate record, canonical CBOR, at most `MaxMessageSize` bytes. Not a signed record. |
| `ExchangeDeadline`                                      | The 5 seconds a connection has to complete the exchange.                                                        |
| `Decide(Facts, peerKey, Exchange)`                      | From a received exchange and the local facts to a `Decision{State, Reason, ...}`: unauthenticated (refused), non-member node, non-member person, member or authorized person. |
| `Params`, `Params.Validate`, `ParamsError`                | The numbers of the accounting model, none decided here; the check that `NonMemberLifetime >= ExchangeDeadline + PairingOnlineTimeout`. |
| `Accounting`, `NewAccounting`                           | The connection-accounting model as pure state transitions: `Secured`, `Connected`, `Completed`, `Disconnected`, `Sweep`, `Overdue`, `SetHeld`, `Known`, `Counts`. |
| `HeldMembers(Facts, certificates)`                      | The network keys of held certificates that belong to members; what `SetHeld` is given. |
| `Allowed(state, Use)`                                   | The one table: whether a peer in a state may open a stream of a protocol or make a relay request. `Access{Verdict, Reason}`. |

## Rules

### Access

- **Default closed.** Every protocol that is not listed is a cluster protocol
  and only members may use it. A new cluster protocol needs no entry here to be
  safe; a protocol that must be open to anyone else needs one, together with its
  rows in the test matrix.
- **Open in every state.** The libp2p built-ins (identify, identify push, ping,
  circuit stop, DCUtR), the relay stream itself, and the certificate exchange.
  Peers need them before any certificate exists, and refusing them would stop
  two nodes behind NATs from ever reaching each other.
- **A node that is not a member may only pair.** A non-member node may use the
  pairing protocol and a relay circuit request, nothing else. A member may also
  pair.
- **A person with a valid certificate but no standing may only query
  reachability.** A non-member person (a valid, checked device certificate, not
  on this node's list) may use the reachability-query protocol and a relay
  circuit request, nothing else; an authorized person may also query. The query
  is how a person holding an administrator's reachability proof asks any member
  for a node's reachability record, so it cannot depend on the list. This
  package only opens the stream: the query handler checks the proof with
  `admin.CheckReachabilityProof`, and no node may use the protocol.
- **Relay operations are decided one by one.** A reservation needs a member, so
  it can only follow a completed exchange; a circuit request needs a completed
  exchange. The relay's allow list and rate limit decide beyond that.
- **Management is for authorized persons.** Pairing is between nodes, so a
  person's device cannot use it, listed or not.
- **Unjudgeable input is refused.** An unset state, an empty protocol identifier
  and a relay operation on a protocol that is not the relay protocol are refused
  with `RefusedInvalid`.
- **Exact match.** A protocol identifier with another version suffix is not the
  listed one and is a cluster protocol.

### The exchange message

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

### The decision

- **The decision.** `Decide` verifies the certificate record against the
  descriptor of its kind itself, so a caller cannot skip verification, and then:
  - A node exchange is refused (state stays unauthenticated, the connection is
    closed) when the record does not verify, its serial is older than the highest
    seen or conflicts with it, the node is retired, the certificate is revoked,
    expired or not yet valid, or it names a network key other than the one the
    secure handshake authenticated. Otherwise `trust.Evaluate` decides: allowed
    directly or by inheritance and **suspended** are members, denied is a
    non-member node. A suspended node stays a member because its trust is
    waiting for a voucher, not gone; the policy layer gets the suspended outcome in
    `Decision.Trust` and holds what it must.
  - A person exchange is refused when the device certificate does not verify,
    names a device key other than the authenticated one, is revoked (the device
    key, or the person's root key), expired or not yet valid. Otherwise
    `admin.EffectiveLevel` decides: a level is an authorized person, anything
    else (not listed, entry expired) a non-member person.
  - **Failure closed.** An unset time, an unset peer key, an unset kind and a
    `nil` revocation set are refused. A `nil` set is not "nothing revoked" here,
    as it is in `revocation`: use `revocation.NewSet()` for a node that has
    revoked nothing. No code path makes a node a member or a person authorized
    without the positive answer of `trust` or `admin`.
  - **The serial seen** is advanced in `Decision.SerialSeen` whenever a verified
    certificate carries a higher serial, also when the exchange is refused for
    another reason. The same certificate again (the normal reconnect) is not
    stale; only a lower serial is.

### Accounting

- **Accounting.** Only inbound connections are counted. A connection is named by
  the peer's network key and the remote address, with no direction, and at most
  one inbound connection per key is on the books at a time. Rules, in the order
  a connection lives them:
  - `Secured` (the handshake is done, the gater can still refuse): an inbound
    connection of a peer not known as a member is refused when
    `UnauthenticatedCap` connections are already unauthenticated; one of a known
    member is always let through and does not count. Outbound connections are
    let through and not accounted.
  - A second inbound connection on a key that is on the books is refused
    (`RefuseDuplicateKey`), known member or not, and checked before the cap. The
    gater is asked once per connection, so it means a second connection with
    the same peer and address; sharing the first one's entry would let it
    outlive the books once the first ended. The caller closes the refused
    connection and reports nothing more for its key.
  - Known members are the peers whose exchange completed as a member or an
    authorized person (`Completed` learns them), plus the members of the
    certificates the node holds: `HeldMembers` selects them (the certificate is
    the current one for its node, valid now, not revoked, and the node is a member
    as `Decide` would decide) and `SetHeld` replaces that set. A completed
    exchange that decides non-member removes the peer from both. The first
    connection of an authorized person's new device is a stranger's.
  - `Completed` (the exchange was decided): a member or authorized person is
    released from the accounting; a non-member (node or person alike) takes one
    of `NonMemberConcurrency` places or, when none is free, is refused and dropped
    (the caller closes it). The decision may change later on the same connection
    (pairing made a non-member node a member): call `Completed` again.
  - `Connected` answers whether the key is tracked: for the one connection
    the books hold for a key, whether it is the accounted one. The caller
    reports every established connection and remembers the answer; an inbound
    connection answered false has no deadline and is closed.
  - `Disconnected` releases whatever the connection held. The caller reports it
    for the accounted connection only (`Connected` answered true, and
    `Completed` has not released it since), because it names a key and not a
    connection.
  - `Sweep` forgets entries not seen connected within `SweepInterval`: a
    connection can be rejected after the handshake without any other callback.
  - `Overdue` lists, in a fixed order, the connections to close: unauthenticated
    ones `ExchangeDeadline` after they were connected, non-members
    `NonMemberLifetime` after they were connected (the lifetime therefore covers
    the exchange and pairing's online part). It changes nothing; the closing
    ends the connection and so releases it.
- **Parameters.** `Params` has no default and no field is decided here; zero is
  refused. `NonMemberLifetime` must be at least `ExchangeDeadline` plus the
  pairing online timeout, which pairing owns and the caller copies in.

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
- The accounting is driven from the connection gater's secured callback, the
  network notifee's connected and disconnected callbacks and the exchange's
  outcome. `Sweep` is called at least every `SweepInterval`, and `Overdue` often
  enough that a connection is closed soon after its deadline; a timer is only a
  wake-up and `Overdue` is the truth. `SetHeld` is refreshed from `HeldMembers`
  at start and whenever a held certificate, a revocation or the trust view
  changes. `SweepInterval` is longer than the longest time between the
  handshake and the connection being established. An `Accounting` is not safe
  for concurrent use.
- Per-peer resource limits (the network layer's part, not modelled here): the
  resource manager asks its limiter once when it creates a peer's scope and then
  keeps the object and re-reads it on every check, so the limiter must return the
  same mutable limit object for a peer for as long as the scope may live, move it
  between the unauthenticated tier and the member tier as the state changes, and
  on the peer's last disconnect reset it to the unauthenticated tier rather than
  delete it; reconcile the table with the manager's peer list from time to time.
- Applications register their stream handlers only through the gated host that
  asks `Allowed` first. A handler registered on the raw host bypasses the gate;
  the library's own protocols are the ones registered that way on purpose.
- The state passed in is the state of the connection the stream arrived on, not
  of the peer: a peer can hold several connections in different states.
- A refusal is carried out by resetting the stream after negotiation. The peer
  sees "negotiation succeeded, then stream reset"; nothing earlier is possible.

## Zero values

- `State`, `Verdict`, `Kind`, `Direction`, every reason enum and `Params` fields:
  zero is unset and is refused or reported as invalid, never a valid input.
- `Facts.Revocations == nil` is refused. In `revocation`, a `nil` set reads as
  "nothing revoked"; here an unloaded set must not let a revoked key in, so the
  caller passes `revocation.NewSet()` to say "looked, found nothing".
- `Facts.SerialSeen`: a node missing from the map has had no serial seen; an
  entry for the wrong node is an invalid input. `Decision.SerialSeen` zero means
  "keep what is stored".
- `Facts.Authorized` zero is the empty list: nobody is authorized.
- `Facts.Trust` zero is an empty view, in which no node is trusted.
- `admin.DeviceCert.MaxLevel` is never `LevelNone` for a certificate that
  decodes; the revocation status given to `admin` always comes from
  `revocation.Set.DeviceCertificate`, which sets a checked status.
