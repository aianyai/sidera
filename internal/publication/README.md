# publication

The signed records a node publishes about itself: its declaration, its
reachability record and the reachability proofs it gives to administrators. Each
is a record type of [`record`](../record/README.md); this package supplies the
payloads, the descriptors, the "keep only the highest counter" decisions and the
facts that [`trust`](../trust/README.md) and [`admin`](../admin/README.md) read.
Read [../README.md](../README.md) first for the conventions every contract
package follows, and
[../../docs/references/records/README.md](../../docs/references/records/README.md)
(section "Publication records") for the formats and the reasons behind them.

## Entry points

| Name                                                                 | Purpose                                                                                                            |
| -------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| `Declaration`, `EndpointDeclaration`, `Reception`, `DeclarationDescriptor(accepted, now)` | The declaration payload and its record type, signed by the node's network key.                        |
| `DeclarationNode(r) (ident.NodeID, error)`                           | The node a declaration claims to be about, read with `record.Peek` before verification to choose the context. Untrusted until `record.Verify` accepts the record. |
| `Declaration.DeclaredInheritance()`                                  | The entry of `trust.View.DeclaredAcceptsInheritance` this declaration stands for.                                   |
| `DeclarationSeen`, `DeclarationSeenOf(v, certSerial)`, `DecideDeclaration(seen, v, certSerial)` | The highest (certificate serial, counter) declaration seen for a node, and the accept / stale / conflict decision for a new one. |
| `ReachabilityRecord`, `NewReachabilityRecord`, `ReachabilityDescriptor(accepted, now)` | The reachability record: addresses, reserved relays, the embedded network-key certificate, counter, expiry. |
| `EmbeddedNetworkKeyCertificate(r)`, `ReachabilityRecord.Certificate()` | Reads the embedded certificate of a record whose own signature has not been checked yet (through `record.Peek`; untrusted); the accessor of the derived certificate. |
| `ReachabilityRecord.ExpiredAt(now)`, `ReachabilitySeen`, `ReachabilitySeenOf(v)`, `DecideReachability(seen, v, now)` | Expiry, and the decision for a reachability record: accept, stale, conflict or expired. |
| `ReachabilityProof`, `ReachabilityProofDescriptor`, `ReachabilityProof.Admin()` | The proof a node gives to a person on its list; the `admin.ReachabilityProof` it yields.                  |
| `Verdict`                                                            | The result of the two decisions.                                                                                    |

## Rules

- **Network-key records.** A declaration and a reachability record are signed by
  one key: the network key `record.NetworkKeyOf` accepts for the record's node,
  with the specific binding reason on refusal. The descriptors are built per
  verification from the certificate the verifier has accepted and the time. The
  node of a declaration is in its payload; the node of a reachability record is
  the node of its embedded certificate. The claimed node is read before
  verification with `DeclarationNode` and `EmbeddedNetworkKeyCertificate`, which
  go through `record.Peek`; what they return is a claim, untrusted until
  `record.Verify` accepts the record.
- **A reachability record is signed by the embedded certificate's key.** The
  signer slot requires the signing key to equal the network key of the
  certificate the record embeds, in addition to `NetworkKeyOf`. Without it a
  verifier that already holds a newer certificate would accept a record signed by
  the newer key while a verifier that reads the embedded certificate would refuse
  it; with it every verifier reaches the same verdict whatever certificate it
  holds. The embedded-key refusal comes first and is the same error for every
  verifier; the binding reason (expired, revoked, ...) is given for a key that
  is the embedded one.
- **Declaration limits.** The longest queue time and the lease renewal time are
  whole seconds, above zero, and at most `delivery.MaxQueueCap` and
  `delivery.LeaseRenewalCap`. A declaration over the protocol limits does not
  decode and cannot be sealed. Senders compute their pull deadline from the
  limits, never from the declared values, so a larger value could only lie.
  Endpoints are listed in ascending order of their IDs, each once, at most
  `MaxEndpointsPerNode`; encoding sorts them. `MaxEndpointsPerNode` (256) is the
  product-level cap on a node's endpoints and this constant is the one home of
  that number: endpoint registration on the node refuses the endpoint after the
  last one allowed, so a declaration of everything the node has can always be
  published. An endpoint's name is at most `MaxNameLen`
  bytes and is for people only. Reception preferences are four flags per
  endpoint; the list of accepted senders stays on the node. The inheritance and
  relay switches are always written, never implied by absence.
- **Highest counter, per certificate serial.** `DecideDeclaration` and
  `DecideReachability` compare an incoming verified record with the highest
  (certificate serial, counter) seen for its node, serial first: the serial is
  that of the network-key certificate the record was verified under, so a record
  under a higher serial supersedes whatever its counter (a key that leaked before
  a rotation cannot freeze the node's records by signing a huge counter). A
  declaration does not carry its serial: the caller passes the serial of the
  accepted certificate it verified the declaration under, to `DecideDeclaration`
  and `DeclarationSeenOf`; a reachability record's is the embedded certificate's.
  A higher (serial, counter) is `VerdictAccept`; a lower one, or the very record
  already seen, is `VerdictStale`; the same serial and counter with another digest
  is `VerdictConflict`
  (the network key signed two records under one counter: a copied key or a node
  restored from an old copy); a record of another node than the state is
  `VerdictWrongNode`; a record without a node, counter or digest is
  `VerdictInvalid`. The state is a record of its own (`DeclarationSeen`,
  `ReachabilitySeen`), kept apart from the record kept, so it survives the
  record's removal.
- **Reachability expiry is judged here**, because no other package owns that
  window: `ExpiredAt` is true at the instant `ExpiresAt` and after, with no
  tolerance, and for a record without an expiry time. Verification does not
  judge it. `DecideReachability` does, after the counter: a record that the
  counter would accept but that has expired is `VerdictExpired`, and the state
  does not move. The state stays put because the judgement uses the peer's own
  clock: if a fast clock made the peer record the counter of a record that is
  still valid by real time, the record would be stale for good once the clock
  was corrected. This differs from `record.DecideSerial`, which advances on an
  expired higher certificate: an expired certificate never becomes valid again,
  and advancing keeps an older network key from coming back.
- **Addresses and relays are opaque.** An address is a byte string of 1 to
  `MaxAddressLen` bytes (at most `MaxAddresses` of them); this package never
  parses it. A relay is a node ID (at most `MaxRelays`), listed once: a record
  that lists a relay twice does not decode.
- **Reachability proof.** Person, node and `NotAfter`, signed by the node's
  identity key. `NotAfter` is passed on to `admin.CheckReachabilityProof`, which
  judges it. `ReachabilityProof.Admin(retirement)` takes the caller's lookup of
  the issuing node in the retirement records (`revocation.Set.NodeRetirement`),
  because a retired node's proofs are void.

## What the caller must guarantee

- The caller moves the state forward with `DeclarationSeenOf` or
  `ReachabilitySeenOf` (with the certificate serial for a declaration) whenever a
  decision answers `VerdictAccept`, and keeps one state per node and per kind of
  record. It keeps the accepted record apart from
  the state.
- Every verifier of a reachability record first reads the embedded certificate
  with `EmbeddedNetworkKeyCertificate` (through `record.Peek`), runs it through
  `record.DecideSerial` and the revocation lookup (stating the status), and only
  then builds the accepted key for `ReachabilityDescriptor`. This holds even for
  a verifier that already holds a certificate for the node, since the record is
  verified against the embedded certificate's key. A declaration's verifier reads
  the node with `DeclarationNode` and looks up its accepted certificate and the
  result of its revocation lookup (`record.AcceptedNetworkKey`, status and the
  highest certificate serial seen for the node stated: a certificate below it is
  stale and its key signs nothing a verifier accepts).
- The caller puts `Declaration.DeclaredInheritance()` of the declaration the
  decision last accepted for a node into `trust.View.DeclaredAcceptsInheritance`.
  A node with no accepted declaration is absent from the map, which `trust` reads
  as "does not accept".
- Times have the precision of the wire: whole seconds.

## Dependencies

The standard library, `ident`, `record`, `delivery` (its protocol limits) and
`admin`. No libp2p.
