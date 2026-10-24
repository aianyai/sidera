# revocation

The identity-level revocation statements, the queries that turn verified
revocations into the facts the judging packages read, and the rule that says how
long a node keeps each revocation. Read [../README.md](../README.md) first for
the conventions every contract package follows, and the "Identity revocations"
section of
[../../docs/references/records/README.md](../../docs/references/records/README.md)
for the reasons behind the rules. The envelope, signing and verification are
[`record`](../record/README.md); this package defines four record types on top
of it.

## Entry points

| Name                                                                  | Purpose                                                                                                   |
| --------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `NodeSelfRevocation`, `NodeSelfRevocationDescriptor`, `IssueNodeSelfRevocation(identity)` | A node's identity key declares itself void. Made with the key and kept offline.            |
| `PersonSelfRevocation`, `PersonSelfRevocationDescriptor`, `IssuePersonSelfRevocation(root)` | A person's root key declares itself void. Made with the key and kept offline.            |
| `NetworkKeyCertificateRevocation`, `...Descriptor`, `IssueNetworkKeyCertificateRevocation(cert, identity)` | Retires one network-key certificate: node, serial and digest. Signed by the identity key. |
| `DeviceKeyRevocation`, `...Descriptor`, `IssueDeviceKeyRevocation(root, deviceKey)` | Retires a device key of the person whose root key signs. Applies to that person only.      |
| `Verify(record)`, `Of(verified)`, `Revocation`, `Kind`                | Verify a record of any of the four types and reduce it to a `Revocation`, the one value the rest takes.   |
| `Set`, `NewSet`, `Set.Add`                                            | The revocations a node chose to keep. Permanent, order independent, idempotent.                           |
| `Set.DeviceCertificate(cert)`                                         | `admin.DeviceCert` with `Revocation` set to a checked status: clear, cert revoked or root key revoked.    |
| `Set.RemotePerson(cert)`                                              | The `admin.Actor` for a remote person: the person and the facts both come from the one certificate. The way to build a remote actor. |
| `Set.Retired()`, `Set.NodeRetired(n)`, `Set.PersonRevoked(p)`         | The nodes with a verified self-revocation, as `trust.NodeSet`; single lookups.                           |
| `Set.NodeRetirement(n)`                                               | `admin.RetirementStatus` (clear or retired, never unchecked) for the issuer of a reachability proof.      |
| `Set.TrustView(view)`                                                 | The view with its `Retired` set replaced by this set's: the one way a `trust.View` gets its retired nodes. Nothing is merged in. |
| `Set.NetworkKey(cert, highestSerial)`                                 | `record.AcceptedNetworkKey` with `Revocation` set to a checked status (clear or revoked) and `HighestSerial` as given, for `record.NetworkKeyBelongs`. |
| `Classify(rev, ctx, held, params)`, `Context`, `Params`, `Retention`  | Long-term, forward-once or dropped, with a reason.                                                        |

## Rules

- **Four statements, each signed by the key it concerns.** A node
  self-revocation `{1: node}` and a certificate revocation `{1: node, 2: serial,
  3: certificate digest}` are signed by the identity key that derives the node
  ID. A person self-revocation `{1: person}` and a device key revocation `{1:
  person, 2: device key}` are signed by the root key that derives the person ID.
  A statement signed by any other key, including a network key, a device key or
  a third party, is refused, so whoever forwards a statement cannot forge one.
  Type strings are `sidera/node-self-revocation/1`,
  `sidera/person-self-revocation/1`, `sidera/network-key-cert-revocation/1` and
  `sidera/device-key-revocation/1`.
- **A person revokes only their own devices.** The person in a device
  revocation must be the one the signing root key derives, and the lookup is by
  the certificate's person and the device key together. A revocation signed by P
  can neither name Q nor touch a certificate Q issued, even for a device key both
  use.
- **A revocation of a key is of every certificate for it.** A device key revoked
  by its person stays revoked under any later certificate of that person; the
  person issues a new device key. A device revocation that names the person's own
  root key is refused, since no certificate can have it and it would revoke
  nothing while looking as if it revoked the root key.
- **A certificate revocation names one certificate.** Node, serial and the
  certificate's record digest must all match, so a second certificate signed
  under the same serial is not revoked by it.
- **No time in any statement.** A revocation applies whenever it is learned,
  however old, and `admin` already judges revocations without regard to time. A
  signing time would be a field no rule may read, and it would give one fact
  several digests, one for each moment it was signed.
- **No optional field.** Every field is required; an unknown field, including
  one written as 0, is refused by the strict decoding of `record`. Serial starts
  at 1 and the digest and identifiers are never zero.
- **Queries read the set and no clock, and always report a checked status.**
  `DeviceCertificate` sets `admin.RevocationRootKeyRevoked` from the person's
  self-revocation (the wider fact, reported even when the device key is revoked
  too), else `admin.RevocationCertRevoked` from a revocation of the certificate's
  device key by the certificate's person, else `admin.RevocationClear`; the cap
  and window stay as `AdminCert()` gives them, so a revoked certificate is
  refused by `admin` at every instant. `NetworkKey` sets
  `record.KeyRevocationRevoked` when the certificate's node, serial and digest are
  revoked, **or** when the node has a self-revocation, and
  `record.KeyRevocationClear` otherwise: the identity key that signed the certificate is declared void,
  and so is everything it signed, including the certificates an attacker with the
  leaked key signs afterwards. `Retired` is the nodes with a self-revocation.
  The unchecked status is never returned: this package is the one place that
  turns the revocation set into these statuses, and a lookup that found nothing
  says so with "clear", which is how `admin` and `record.NetworkKeyBelongs` tell
  it from a lookup nobody made.
- **A remote actor is built with `Set.RemotePerson`.** It takes the person ID
  from the certificate itself, so a caller cannot pair a person with another
  person's certificate facts, nor with a status nobody looked up.
- **Revocation never lowers the highest serial seen.** Nothing here takes or
  changes `record.SerialSeen`: an older certificate stays stale after the newer
  one is revoked.
- **Retention.** `Classify` keeps for good (`LongTerm`): every person
  self-revocation and device key revocation (few, and a node cannot tell which
  concern its peers); and every node-level revocation (self-revocation or
  certificate revocation) of the local node, of a node in the local trust graph,
  of the signer of a declaration the local node has cached, or of a node for
  which the local node holds a pairing expectation or a pending pairing request
  (`Context.PairingNodes`). The last rule exists because a node whose identity
  was retired can later be paired by an attacker holding the stolen key, and an
  expectation the owner entered confirms that pairing by itself; a retirement
  that was passed on once and dropped could not be seen by the check at
  confirmation. Any other
  node-level revocation is passed on once (`ForwardOnce`) while the node holds
  fewer than `Params.MaxForwardOnce` of them and is `Dropped` after that; a
  related revocation is never refused for the cap. The reason says which rule
  decided. An invalid input (zero `Revocation`, negative count, unset cap for an
  unrelated revocation) is `Dropped` with `ReasonInvalidInput`; nothing is kept
  on an input the rule cannot judge.

## What the caller must guarantee

- Pass `Of` only values that `record.Verify` returned for the matching
  descriptor, or use `Verify`, which does that. A zero
  `record.Verified` (the only one code outside package `record` can write)
  produces a `Revocation` that `Set.Add` and `Classify` refuse.
- Add a revocation to the `Set` only when `Classify` says `LongTerm` (a node
  keeps no unrelated node-level revocation long term). The caller counts the
  forward-once revocations it holds, passes the count to `Classify` and lowers
  it when one has been passed on.
- A `Set` is not safe for concurrent use: `Add` writes the maps the queries
  read, so a caller that shares one between goroutines serializes the calls.
- A `nil` `*Set` is a set that was never loaded, and every query and `Add` panics
  on it with "revocation set not loaded": answering "nothing is revoked" for a
  set nobody loaded would be the fail-open path the three-valued status exists to
  close, and a forgotten field would never be noticed. A set that has really been
  checked and holds no revocation is `NewSet()`, whose queries report everything
  as clear (a checked status, not "unchecked"), so every caller loads the set
  before it judges. A caller that holds a `*Set` field initialises it with
  `NewSet()` or loads it before the first query.
- Other cross-package zero values: `Retired()` is a non-nil empty
  `trust.NodeSet` for no retired node, which `trust` reads as "nobody retired";
  `DeviceCertificate` of a zero `Verified` gives `LevelNone` and a zero
  `NotAfter`, which `admin` reads as "grants nothing" and "expired";
  `NetworkKey` always returns a non-nil certificate and
  `record.KeyRevocationClear` when nothing is revoked.
- `Context.PairingNodes` lists every node the local node holds a pairing
  expectation or a pending pairing request for, kept current by the caller; a
  node missing from it has its retirement treated as any unrelated one.
- Statements for the keys of this node are made when the keys are: the issue
  functions only build and sign, and storing the self-revocation offline is the
  caller's. Verification of an incoming statement does not depend on what the
  node holds, so a node verifies a self-revocation of a node it has never heard
  of.
- The synchronisation protocol decides the set digest two peers compare;
  `Revocation.Digest` is its input. The protocol must also bound the volume of
  person-level revocations, which this package retains long term without a count
  limit: anyone can mint root keys and self-revocations cheaply, so the bound
  (for example per connection) belongs to the protocol.

## Dependencies

The standard library, `record`, `ident`, `admin` (the device-certificate facts)
and `trust` (the retired set). Nothing else; in particular no libp2p.
