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
| `Declaration.DeclaredInheritance()`                                  | The entry of `trust.View.DeclaredAcceptsInheritance` this declaration stands for.                                   |
| `DeclarationSeen`, `DeclarationSeenOf(v)`, `DecideDeclaration(seen, v)` | The highest declaration counter seen for a node, and the accept / stale / conflict decision for a new one.        |
| `ReachabilityRecord`, `NewReachabilityRecord`, `ReachabilityDescriptor(accepted, now)` | The reachability record: addresses, reserved relays, the embedded network-key certificate, counter, expiry. |
| `EmbeddedNetworkKeyCertificate(r)`                                   | Reads the embedded certificate of a record whose own signature has not been checked yet.                            |
| `ReachabilityRecord.ExpiredAt(now)`, `ReachabilitySeen`, `ReachabilitySeenOf(v)`, `DecideReachability(seen, v, now)` | Expiry, and the decision for a reachability record: accept, stale, conflict or expired. |
| `ReachabilityProof`, `ReachabilityProofDescriptor`, `ReachabilityProof.Admin()` | The proof a node gives to a person on its list; the `admin.ReachabilityProof` it yields.                  |
| `Verdict`                                                            | The result of the two decisions.                                                                                    |

## Rules

- **Network-key records.** A declaration and a reachability record are signed by
  one key: the network key `record.NetworkKeyOf` accepts for the record's node,
  with the specific binding reason on refusal. The descriptors are built per
  verification from the certificate the verifier has accepted and the time. The
  node of a declaration is in its payload; the node of a reachability record is
  the node of its embedded certificate.
- **Declaration limits.** The longest queue time and the lease renewal time are
  whole seconds, above zero, and at most `delivery.MaxQueueCap` and
  `delivery.LeaseRenewalCap`. A declaration over the protocol limits does not
  decode and cannot be sealed. Senders compute their pull deadline from the
  limits, never from the declared values, so a larger value could only lie.
  Endpoints are listed in ascending order of their IDs, each once, at most
  `MaxEndpoints`; encoding sorts them. An endpoint's name is at most `MaxNameLen`
  bytes and is for people only. Reception preferences are four flags per
  endpoint; the list of accepted senders stays on the node. The inheritance and
  relay switches are always written, never implied by absence.
- **Highest counter.** `DecideDeclaration` and `DecideReachability` compare an
  incoming verified record with the highest counter seen for its node. A higher
  counter is `VerdictAccept`; a lower counter, or the very record already seen,
  is `VerdictStale`; the same counter with another digest is `VerdictConflict`
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
  does not move.
- **Addresses and relays are opaque.** An address is a byte string of 1 to
  `MaxAddressLen` bytes (at most `MaxAddresses` of them); this package never
  parses it. A relay is a node ID (at most `MaxRelays`).
- **Reachability proof.** Person, node and `NotAfter`, signed by the node's
  identity key. `NotAfter` is passed on to `admin.CheckReachabilityProof`, which
  judges it.

## What the caller must guarantee

- The caller moves the state forward with `DeclarationSeenOf` or
  `ReachabilitySeenOf` whenever a decision answers `VerdictAccept`, and keeps one
  state per node and per kind of record. It keeps the accepted record apart from
  the state.
- A caller that has not yet accepted the node's network-key certificate reads
  the one embedded in a reachability record with `EmbeddedNetworkKeyCertificate`,
  decides on it (`record.DecideSerial`, the revocation records) and builds the
  accepted key from the result before verifying the record itself.
- The caller puts `Declaration.DeclaredInheritance()` of the declaration the
  decision last accepted for a node into `trust.View.DeclaredAcceptsInheritance`.
  A node with no accepted declaration is absent from the map, which `trust` reads
  as "does not accept".
- Times have the precision of the wire: whole seconds.

## Dependencies

The standard library, `ident`, `record`, `delivery` (its protocol limits) and
`admin`. No libp2p.
