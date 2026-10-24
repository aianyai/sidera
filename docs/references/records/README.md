# Signed records

The envelope every signed statement in Sidera uses, how it is encoded, signed,
hashed and decoded, and the two records that bind keys to identifiers: the
network-key certificate and the device certificate. Identifiers and public keys
are described in [identity](../identity/README.md).

## What

### Envelope

A record is a deterministic CBOR map with integer keys:

```
{ 1: type (text), 2: payload (bytes), 3: signatures (array) }
signature      = { 1: public key, 2: signature (64 bytes) }
public key     = { 1: algorithm tag, 2: key bytes }
```

- The **type** is a string that carries its version, for example
  `sidera/device-cert/1`. A change that is not backward compatible is a new
  type string; nothing is negotiated.
- The **payload** is itself a deterministic CBOR map with integer keys starting
  at 1, defined by the record type. It travels as a byte string and is never
  re-encoded: what was signed is what is verified.
- **Signatures** are sorted ascending by the encoded public key, hold one to
  eight entries, and a public key appears at most once. The algorithm tag is 1
  for Ed25519; 2 is reserved for P-256 and refused today. An Ed25519 key must be
  canonical and not of small order (see identity).

### Framing

Everything that is hashed or signed is built by one function, `frame.Frame`
(package `internal/frame`):

```
Frame(domain, f1, f2, ...) = uvarint(len(domain)) || domain
                             || uvarint(len(f1)) || f1 || uvarint(len(f2)) || f2 ...
```

A **domain** names the purpose of a hash or a signature. It starts with `sidera/`,
is at most 63 bytes, ends in a version, and is declared with `frame.Domain`,
which panics on a malformed one. All domains are different: a test parses the
module for every `frame.Domain` call and fails on a repeat or on a domain that
is not a string literal.

Every part carries its own length, so two different inputs never give the same
bytes and the framed bytes of one domain are never a prefix of another's. That
is why one rule serves identifier derivation, record signatures, record digests
and any later signed construction: a new domain cannot collide with an old one,
and no construction has to argue separately that its own joining is unambiguous.

### Domains in use

This table is the one list of the domains of the project. Each is declared
with `frame.Domain` in the package named, and a test of package `frame` fails
if two are equal. The type strings of record types (for example
`sidera/device-cert/1`) are not domains: they are a field inside the signed
bytes of the record domain.

| Domain                 | Package  | Purpose                                              | Framed fields                 |
| ---------------------- | -------- | ---------------------------------------------------- | ----------------------------- |
| `sidera/id/node/1`     | `ident`  | Derivation of a node identifier from its identity key | algorithm tag, key bytes      |
| `sidera/id/person/1`   | `ident`  | Derivation of a person identifier from its root key   | algorithm tag, key bytes      |
| `sidera/record-sig`    | `record` | The bytes every signer of a record signs              | type, payload                 |
| `sidera/record-digest` | `record` | The signature-free digest of a record                 | type, payload                 |
| `sidera/request-digest/1` | `envelope` | The digest a target stores for deduplication of a request | kind, preset, content type, body, accept_before, report_by |
| `sidera/pairing/commit/1`            | `pairing` | The initiator's commitment to its nonce and the terms | terms, nA                |
| `sidera/pairing/transcript/1`        | `pairing` | The pairing identifier                              | terms, C, nB, nA             |
| `sidera/pairing/sas/1`               | `pairing` | The input of the verification-code derivation         | terms, C, nB, nA             |
| `sidera/pairing/credential-nonce/1`  | `pairing` | The input of the credential-nonce derivation          | terms, C, nB, nA             |
| `sidera/pairing/request/1`           | `pairing` | The fingerprint of a management request about a pairing | request kind, arguments   |

### Signed bytes and digest

```
signed = Frame("sidera/record-sig",    type, payload)
digest = SHA-256(Frame("sidera/record-digest", type, payload))
```

The digest covers type and payload and no signature, so a record has one
digest whatever signatures are attached and however a signature might be
re-encoded.

### Record types

A record type is a `Descriptor`: the type string, how to convert its payload
to and from a wire struct, and the signer rule. Validation of a payload lives
only in the conversion from the wire struct; the conversion to it only converts.
Encoding runs the decoding path on the bytes it produced and refuses on failure,
so a payload the package writes is always one a receiver accepts. The
package that defines the type exports the descriptor and callers pass it to
`Verify` explicitly. There is no registry, so a package can define record types
without `record` knowing about them.

The signer rule is a list of **slots**. A slot is a function of a public key
that returns nil when the key may fill it and a reason when it may not ("the key
whose derived node identifier is X"). `Verify` accepts a record only if every
slot is filled by exactly one signature and every signature fills a slot.
`VerifyPartial` accepts empty slots and says which are filled. It returns a
`Partial` (payload, digest, one signed/unsigned entry per slot in the rule's
order), a type of its own; only `Verify` returns a `Verified`, so a record that
still lacks a signer cannot be passed on as verified.

`Peek` reads the payload without checking any signature or signer rule: it checks
the type string and runs the descriptor's own decoding path, and returns an
`Unverified`, a type of its own that does not convert to `Verified` or `Partial`.
It exists for a record whose signer rule depends on a node or key the payload
names: the caller needs that claim to choose which certificate to verify
against, before verification can run. Nothing it returns is trustworthy.

A rule that depends on the caller is stated with the slot built for that
context. `NetworkKeyOf(node, accepted, now)` accepts the keys `NetworkKeyBelongs`
accepts, so a record signed by a network key is checked by the one binding
function. The descriptor of such a record type is built per verification, by a
function of the accepted certificate, its revocation and the time. A refused key
returns an error that wraps `ErrSignerNotAllowed` and a `*BindingError`; its
`Binding` field names the cause.

### Decoding

1. The input length is checked against a limit passed by the caller (64 KiB to
   start). A limit that is not positive refuses everything.
2. The CBOR library decodes with duplicate map keys, indefinite lengths, tags,
   unknown fields, NaN and Inf refused. Nesting stays at the library's bound of
   32 levels, and records decode into typed structs.
3. The value is encoded again with the deterministic options (nil slices as
   empty byte strings, tags refused). If the bytes differ from the input, the
   input is refused.
4. For the envelope: signature count first, then key validity, then strictly
   ascending order. The count is checked before any key is converted because
   converting a key decodes a curve point, and a hostile record within the size
   limit holds hundreds of signature items.
   For a payload: the record type's own conversion, which refuses values that
   are well formed but invalid.

### Times, counters and levels

One set of functions converts them for every record type.

- A time on the wire is `int64` Unix seconds. A required time must be in
  `1..253402300799` (the year 9999). Zero, negative and larger values are refused.
- An optional time is left out of the map to mean "not provided". An explicit
  `0` is refused. A zero `time.Time` converts to "left out" only for optional
  fields; what absence means is up to the consuming type (a credential without
  an end time never expires).
- Counters and serials start at 1.
- Levels: 1 view, 2 manage, 3 full control. The wire values are their own table,
  independent of the Go constants of `admin.Level`. A device certificate must
  state its level; "no cap" is written as 3. `LevelNone` and every other value
  are refused.

### Partial signatures

Records that two parties sign are built in two steps. The first party seals the
record with its own signer and sends it; the second party decodes it and calls
`CoSign(descriptor, record, signer)`. `CoSign` verifies the record against the
descriptor first (every present signature is valid and allowed, the payload is
valid), then requires the new signer to fill an empty slot, and appends the
signature. The digest is the same at every step, so the second party signs
exactly what the first one signed and both can compute the digest of the final
credential before it is complete. `Verify` passes only when the signer set is
complete.

### Network-key certificate

Type `sidera/network-key-cert/1`. Payload `{1: node ID, 2: network public key,
3: serial, 4: not_before, 5: not_after}`. Signed by one key: the identity key
whose derived node ID equals field 1. The certificate holds the network public
key and not a peer identifier, so the contract packages do not depend on libp2p.

- **Validity** is judged by `record`, with no tolerance: valid from `not_before`,
  expired at `not_after`. `not_after` must be after `not_before`.
- **Issuing.** `IssueNetworkKeyCertificate` sets `not_before` to the issue time
  minus `timing.ClockTolerance`. The comparison stays strict, so without the
  margin a peer whose clock is behind by up to the tolerance would hold a higher
  serial that replaced the old certificate immediately and yet is not valid yet:
  the node would be unverifiable for that peer after every renewal.
- **Roles.** The network key must not derive to the certificate's own node ID. A
  connection key that was also the identity key would let anyone who compromised
  a connection sign certificates for the node.
- **Serial**: peers accept only the highest serial seen (`DecideSerial`). The
  state is `SerialSeen`: the highest serial and the digest of the certificate
  seen at it, kept apart from the certificate currently accepted. Higher is
  accepted; lower, or the very same certificate, is stale; the same serial with
  another digest is a conflict (the identity key signed two certificates for one
  serial, the mark of a copied or compromised key; this covers the same key with
  another window as well); a certificate of another node is refused. Neither
  expiry nor revocation enters this decision: an expired certificate with a
  higher serial still raises the serial seen, and revoking the accepted
  certificate never lowers it, so an older key cannot come back.
- **The accepted certificate only moves forward.** A certificate is valid for
  binding only while no higher serial has been seen for its node. Without that,
  a network key that leaked before a rotation would keep binding after it for
  every record a network key signs, as long as the old certificate was neither
  expired nor revoked, and the new key's owner could do nothing about it but
  revoke each old certificate by hand. So the serial seen enters the binding
  judgment itself (below), and not each caller's own check.
- **`NetworkKeyBelongs`** is the only function that decides whether a network
  public key is a node's key, given what is accepted for the node
  (`AcceptedNetworkKey`: the certificate, the highest serial seen for its node
  and the revocation status the caller fills from its own state). Declarations,
  vouchers, reachability records, status reports, control replies and pairing
  parties that a network key signs all rely on it. A key the certificate does
  not name is a mismatch whatever the certificate's state; a certificate that
  names it is then judged against the serial seen (`BindingStale` when a higher
  serial was seen, `BindingSerialUnchecked` when the caller gave none or one
  below the certificate's own, which a seen serial cannot be), then for
  revocation, then for its window. The
  revocation status is three-valued: its zero value means nobody looked the
  certificate up and is refused with its own binding
  (`BindingRevocationUnchecked`), surfaced by `NetworkKeyOf` through
  `BindingError` like the others. A boolean whose zero value meant "not revoked"
  would let a caller that forgot the lookup accept a revoked network key.

Rotating the network key therefore leaves the node ID unchanged: a certificate
with serial 2 names the new key for the same node ID, the old key stops
belonging to the node, and a second certificate at serial 2 is detected.

### Device certificate

Type `sidera/device-cert/1`. Payload `{1: person ID, 2: device public key,
3: maximum level, 4: not_before, 5: not_after}`. Signed by one key: the root key
whose derived person ID equals field 1 (the chain check). A temporary sub-key is
the same record with a short window.

- `record` does not judge the window: administration judges it, sometimes at the
  signing time of an offline directive rather than at the time of the check.
- **Issuing.** `IssueDeviceCertificate` sets `not_before` to the issue time minus
  `timing.ClockTolerance`, for the reason given for the network-key certificate:
  the window is compared without tolerance, and a certificate that starts at the
  issue time is refused as not yet valid by a verifier whose clock is behind.
- **Roles.** The device key must not derive to the certificate's own person ID:
  a device that held the root key could certify further devices, and its loss
  would be the loss of the person.
- `DeviceCertificate.AdminCert()` yields the signature-derived part of
  `admin.DeviceCert`: the cap and the window as written. The revocation status is
  left unchecked, which administration refuses; the revocation lookup sets it
  before the value is used.

### Identity revocations

Package `internal/revocation`. Four record types built with `NewDescriptor` on
the envelope above, the queries that turn verified revocations into the facts
`admin`, `trust` and `record` read, and the rule that decides how long a node
keeps a revocation.

| Type | Payload | Signed by |
| ---- | ------- | --------- |
| `sidera/node-self-revocation/1` | `{1: node ID}` | the identity key that derives the node ID |
| `sidera/person-self-revocation/1` | `{1: person ID}` | the root key that derives the person ID |
| `sidera/network-key-cert-revocation/1` | `{1: node ID, 2: serial, 3: certificate digest}` | the identity key that derives the node ID |
| `sidera/device-key-revocation/1` | `{1: person ID, 2: device public key}` | the root key that derives the person ID |

- **Self-revocations** are made when the key is made and kept offline; they are
  published when the key leaks. Only the key itself can sign one, so a forwarder
  cannot forge it.
- **A certificate revocation** names the node, the serial and the record digest
  of the certificate. All three must match the certificate. The network key that
  the certificate names cannot sign it.
- **A device key revocation** applies only to certificates of the person it
  names, and that person is the one the signing root key derives, so a person's
  revocation can reach nobody else's device. It revokes the key under every
  certificate of that person, past and future. It cannot name the person's own
  root key (nothing would be revoked).
- **No statement carries a time.** Revocations apply whenever they are learned.
- **Queries.** `Set.DeviceCertificate` sets `admin.DeviceCert.Revocation` to
  `RevocationRootKeyRevoked` (the person's self-revocation, the wider fact),
  `RevocationCertRevoked` (the person's revocation of this device key) or
  `RevocationClear`; `Set.RemotePerson` builds the remote `admin.Actor` from one
  certificate, taking the person from it, and is the way to build one;
  `Set.Retired` returns the node IDs with a verified self-revocation as the
  `trust.NodeSet` that `trust.View.Retired` takes; `Set.NetworkKey(cert,
  highestSerial)` sets `AcceptedNetworkKey.Revocation` to `KeyRevocationRevoked`
  (the certificate's own revocation, or any self-revocation of its node) or
  `KeyRevocationClear`, and passes the highest serial seen on.
  The two certificate queries never return the unchecked status, which `admin`
  and `record.NetworkKeyBelongs` refuse; this package is the one place that turns
  the revocation set into those statuses. None of them reads a clock or touches
  `SerialSeen`, so a revocation never lowers the highest serial seen. A nil
  `*Set` is a set that was never loaded, and every query and `Add` panics on it;
  an empty set that has really been checked is `NewSet()` and answers "clear". A
  `Set` is not safe for concurrent use.
- **Retention.** `Classify` returns long-term for person self-revocations and
  device key revocations, and for node-level revocations (self-revocations and
  certificate revocations) of the local node, of a node in the local trust graph,
  of the signer of a cached declaration and of a node the local node holds a
  pairing expectation or a pending pairing request for. Other node-level
  revocations are forward-once, within a total cap (`Params.MaxForwardOnce`, no
  value decided, zero is unset), and dropped over it.
- **Synchronisation.** The synchronisation protocol decides the set digest two
  peers compare; `Revocation.Digest` is its input. Person-level revocations are
  retained long term without a count limit, and anyone can mint root keys and
  self-revocations cheaply, so the protocol must bound their volume (for example
  per connection).

Why these shapes:

- A statement is signed by the key it concerns, with the identifier inside the
  payload, so the signer rule is "the key that derives it", the same check the
  certificates use; without it anybody could cut anybody's edges or devices. The
  person in a device revocation is not a separate claim: a statement signed by P
  naming Q fails the signer rule, and the lookup by (person, key) makes P's
  statement about a key Q also uses concern P's own certificates only.
- Certificate revocations are signed by the identity key, not the network key:
  the network key is the one that leaks in day-to-day use, and if it could revoke
  certificates the holder of a leaked old key could retire the certificate that
  limits it, or cut the node's current certificate off. The digest is in the
  statement because the identity key could sign two certificates under one serial
  (the highest-serial rule reports a conflict); a serial alone would revoke both
  or the wrong one.
- A self-revocation voids the certificates of its node: an attacker with a leaked
  identity key signs valid certificates for his own network key, and the owner
  cannot name certificates not yet made. `trust` separately refuses the node.
- No time field and no optional field. A time no rule may read is a trap, and the
  same statement signed at two moments would have two digests that a digest-based
  synchronisation cannot tell from two different facts; every field decides what
  is revoked, so an absent one would be a second way to write a weaker statement.
- A status of three values with a zero "unchecked" keeps a caller that forgot the
  lookup from presenting a revoked key as clear; the set answers "clear" for
  itself when it is empty, because it did look. A nil set did not look: it is an
  uninitialised field, and answering "clear" for it would be the same
  fail-open path through the set itself, so every method on it panics, the way
  `AdminDirective` panics on a nil source. `RemotePerson` exists so the person
  and the certificate facts cannot come from different certificates.
- Retention is asymmetric. Person and device revocations are few and a node
  cannot tell which concern its peers (the authorized list is local), so all are
  kept. Node-level revocations are many and mostly irrelevant to a given node:
  storing every one would let an attacker with many throwaway identities fill
  every node's store, so only those of the local node, its trust graph, the
  signers of declarations it holds and the nodes it is pairing with are kept for
  good; the rest pass through once under a total cap that bounds memory whoever
  floods and never refuses a related revocation. The pairing nodes are kept
  because a node whose identity was retired can later be paired by an attacker
  holding the stolen key, and an expectation the owner entered confirms that
  pairing by itself; a retirement passed on once and dropped could not be seen at
  confirmation. Classification is separate from the set: the set only answers
  questions, and what to put in it is `Classify`'s decision, so the rule has one
  home.

Where: `internal/revocation` (`statements.go`: payloads, descriptors and `Issue*`
functions; `revocation.go`: `Revocation`, `Kind`, `Of`, `Verify`; `set.go`: `Set`
and its queries; `retention.go`: `Classify`, `Params`, `Context`). Tests:
`golden_test.go` (vectors computed with Python's `hashlib` and `cryptography`,
from the RFC 8032 keys), `statements_test.go` (`TestGoldenStatements`, `TestSignerRules`,
`TestDeviceRevocationAppliesOnlyToTheSignersPerson`, `TestPayloadRules`,
`TestTypeStrings`, `TestEveryBitIsProtected`), `set_test.go`
(`TestDeviceCertificateStanding`, `TestRemotePerson`,
`TestRevokedDeviceCertificateIsRefusedAtAnyTime`, `TestRetired`,
`TestNetworkKeyRevoked`), `scenarios_test.go`
(`TestOldCertificateRevocationRefusesTheOldKey`, `TestIdentityKeyCompromise`,
`TestFlood`), `retention_test.go` (`TestClassify`), `property_test.go`
(`TestClassifyAgainstOracle`, `TestFloodNeverExceedsTheCap`), and the fuzz targets
in `fuzz_test.go` (`FuzzDecodeStatement`, `FuzzPayloads`, `FuzzStatementRoundTrip`,
`FuzzClassifyCounts`). The package `README.md` lists entry points and what callers
must guarantee.

### Trust records

Package `internal/credential`. Four record types carry trust between nodes. Every
one is a deterministic CBOR map with integer keys; times are Unix seconds,
counters start at 1, and an optional field is left out and never written as 0.
A node ID is its 32 bytes; the two ends of an edge are always in ascending byte
order, so the same facts give the same bytes.

| Type | Payload | Signed by |
| ---- | ------- | --------- |
| `sidera/trust-credential/1` | `{1: end a, 2: end b, 3: created_at, 4?: not_after, 5: nonce (16 bytes)}` | the identity keys of both ends |
| `sidera/trust-credential-revocation/1` | `{1: the revoked credential's payload (bytes)}` | the identity key of either end of that credential, one signature |
| `sidera/online-voucher/1` | `{1: issuer, 2: edge (credential digest), 3: issued_at, 4: counter}` | the issuer's network key |
| `sidera/offline-directive/1` | `{1: addressed node, 2: credential digest, 3: end a, 4: end b, 5: signed_at, 6: device certificate (a record)}` | the device key of the attached certificate |

- **Credential.** The two ends differ and ascend; `not_after`, when present, is
  after `created_at`; the nonce is 16 bytes and not all zero, so two
  credentials between the same nodes in the same second differ. The credential
  digest is the record digest, which the signatures do not cover, so it is known
  to both sides before either signs. The pairing protocol builds the payload
  independently on each side from facts both hold: the initiator signs first with
  `Seal`, the target checks the partial record with `VerifyPartial`, compares the
  payload with its own and completes it with `CoSign`. A credential without
  `not_after` never expires (the key is left out; `trust` reads the zero time as
  "never"). An expired credential is still a valid record: `trust` judges its end.
- **Revocation.** It embeds the credential's payload, not only its digest. The
  verifier decodes the payload strictly, recomputes the digest and requires the
  signer to be one of the two ends the payload shows. A stored revocation is
  therefore safe: a record naming only a digest could not be checked against
  anyone, and a third party that had seen a digest could cut that edge for good.
  Exactly one signature is accepted. The type string includes the credential
  type it embeds. The digest is derived from the embedded payload and is
  readable only through an accessor, so it cannot disagree with it;
  `IssueRevocation(credential, signer)` builds and signs the revocation and
  refuses a signer that is neither end, as the verifier would.
- **Voucher.** It carries the issuer's node ID because the signer rule needs it:
  the network key is bound to a node through the accepted certificate
  (`NetworkKeyOf`); `VoucherIssuer` reads that claim through `Peek` before the
  signature is checked, only to find the certificate to verify against, and what
  it returns is untrusted until `Verify` accepts the record. It carries no validity period; the verifier computes validity
  from `issued_at`, its clock and its grace period, so a future-dated voucher is
  `trust.Vouchers.Accept`'s to refuse. That the issuer is an end of the credential
  the digest names is checked by `CheckVoucher`, which needs the credential.
- **Directive.** The type fixes the kind: revoke one trust edge. The attached
  certificate is checked when the directive is decoded (signed by the root key of
  the person it names), and the directive must be signed by that certificate's
  device key; whether the person is on the node's list, the level, and the
  certificate's window at the signing time are `admin`'s decisions. The claimed
  ends are compared with the credential by `trust.ApplyDirective`, not here. The
  directive yields the `admin.Directive` (kind, node, signing time, person, and
  the certificate facts) and the `trust.EdgeRevocationDirective`. The
  certificate facts come from a `DeviceCertificateFacts` source passed to
  `AdminDirective`, a revocation set in production: the revocation status of the
  certificate and of the person's root key is therefore part of the call and
  cannot be left out, a source that has not looked returns the unchecked status
  that `admin` refuses, and a nil source panics at once. The certificate decoded
  from the directive is derived from the embedded record and exposed through an
  accessor for the same reason as the revocation digest.

Why these shapes: a credential revocation that embeds the payload makes the
signer check possible and the digest unforgeable by the sender; a fixed order of
the ends gives one encoding per edge, so a digest names one credential; the
directive embeds the certificate so that a node that has been offline can check
the whole chain from the directive alone, and it carries the claimed ends so that
a directive for an edge the node is not part of is recognised without the
credential.

The payload of a credential is produced by the descriptor's own conversion and
read back through `Peek`, so there is one validation path for a credential
whether it is a record of its own or embedded in a revocation.

Where: `internal/credential` (`credential.go`, `revocation.go`, `voucher.go`,
`directive.go`). Tests: `golden_test.go` (vectors computed with an independent
implementation), `credential_test.go`, `revocation_test.go`, `voucher_test.go`,
`directive_test.go` (rules, signers, every bit protected, the revocation source
with every status), `e2e_test.go` (the records through `admin` and `trust`, with a
fake revocation source, and `TestOfflineDirectiveThroughTheRevocationSet`, which
runs a directive through the real revocation set), `property_test.go` (payload bytes against
an oracle), `fuzz_test.go`.

### Publication records

Package `internal/publication`. Three record types that a node publishes about
itself. A declaration and a reachability record are signed by the node's network
key, bound to the node through `NetworkKeyOf`; the proof is signed by the node's
identity key.

| Type | Payload | Signed by |
| ---- | ------- | --------- |
| `sidera/node-declaration/1` | `{1: node, 2: counter, 3: boot nonce (16 bytes), 4: accepts inheritance, 5: offers relay, 6: longest queue time (s), 7: lease renewal time (s), 8: endpoints}`; an endpoint is `{1: ID, 2: kind (1 session, 2 runtime), 3?: name, 4?: reception flags}` | the node's network key |
| `sidera/reachability-record/1` | `{1: counter, 2: expires_at, 3: network-key certificate (a record), 4: addresses, 5: relays}` | the node's network key |
| `sidera/reachability-proof/1` | `{1: person, 2: node, 3: not_after}` | the node's identity key |

- **Declaration.** The two switches are always written. Reception flags are bits:
  1 paused, 2 refuses several recipients, 4 restricts senders, 8 hidden from the
  directory; an unknown bit is refused. The list of senders an endpoint accepts
  is not published. Endpoints ascend by ID and are listed once, so one
  declaration has one encoding; at most 256 endpoints (`MaxEndpointsPerNode`, the product-level cap on a node's endpoints: endpoint registration on the node refuses the 257th so a declaration can always be published) and 64-byte names. `DeclarationNode` reads the claimed node through `Peek`, only to find the certificate to verify against. The two
  durations are whole seconds above zero and at most the protocol limits of
  `delivery` (`MaxQueueCap`, `LeaseRenewalCap`): senders compute their pull
  deadline from the limits, so a declared value above them would only mislead,
  and it is refused when sealed and when received. The boot nonce is chosen anew
  at every start, so a declaration after a restart differs even when nothing
  else did. The inheritance switch yields the entry of
  `trust.View.DeclaredAcceptsInheritance`.
- **Reachability record.** Addresses (at most 16, 1 to 256 bytes each) are
  opaque byte strings, in order of preference; relays (at most 8) are node IDs,
  each listed once. The record embeds the node's network-key certificate and its
  node is that certificate's node. Every verifier reads the certificate first
  (`EmbeddedNetworkKeyCertificate`, through `Peek`; untrusted), decides on it with
  the serial rule and the revocation status, and verifies the record against what
  it accepted. The signing key must also be the embedded certificate's network
  key: a verifier that already holds a newer certificate would otherwise accept a
  record signed by the newer key that a verifier reading the embedded certificate
  refuses, and every verifier must reach the same verdict. The expiry is the one
  time window judged by this package: expired at `expires_at` and after, without
  tolerance.
- **Highest counter.** Both records carry a monotonic counter from 1 and a peer
  keeps only the highest. The decision is accept for a higher counter, stale for
  a lower one or the very record seen, conflict for the same counter with another
  digest (two records signed under one counter: a copied key or a restored copy),
  and wrong node for a state of another node. The state (counter and digest) is
  kept apart from the record kept, so an expired or dropped record's counter
  still stands. For the reachability record the counter decides first and expiry
  second: a record the counter would accept but that has expired is refused as
  expired and does not move the state. The serial rule of the certificate
  (`DecideSerial`) does advance on an expired higher certificate; the two differ
  on purpose, for two reasons that each rest on what the expiry means:
  - A reachability record that the local clock calls expired does not advance
    the counter state, because the local clock may be wrong. If a fast clock
    made the node record the counter of a record that is still valid, the
    record would turn stale for good once the clock was corrected, and the
    node would refuse a record it should have kept.
  - `DecideSerial` advances on an expired certificate that has a higher serial.
    A certificate that is expired never becomes valid again, so no correction of
    the clock can undo the advance, and the higher serial is the identity key's
    statement that every lower certificate is void. Advancing is what keeps an
    old network key from coming back when the newest certificate has lapsed.
- **Proof.** It lets a person ask any cluster member for the node's reachability
  record and grants no management right. Its `NotAfter` is passed on to
  `admin.CheckReachabilityProof`.

Why these shapes: the declaration carries its node and the reachability record
embeds the certificate because the signer rule needs the node before the
signature is checked; the senders' list stays local because publishing it would
publish the node's contact graph for endpoints; the counter state outlives the
record because the bar that stops an old record returning must not fall when the
record is removed.

Where: `internal/publication` (`declaration.go`, `reachability.go`, `proof.go`,
`decide.go`). Tests: `golden_test.go` (vectors computed with an independent
implementation), `declaration_test.go` (rules, protocol limits, signer binding),
`reachability_test.go`, `proof_test.go` (through `admin`), `decide_test.go`
(decisions against an oracle under random arrival orders; a declaration through
`trust`), `fuzz_test.go` (every bit protected, every decoder).

### Delivery records

Package `internal/envelope`. Two record types that a target node issues about a
delivery, both signed by the node's network key bound through `NetworkKeyOf`. The
signing node is not in the payload: the verifier names it from its own record of
the message. The sending node is, so a record cannot be applied to another
sender's message of the same id. The formats and the reasons are in [envelope](../envelope/README.md).

| Type | Payload | Signed by |
| ---- | ------- | --------- |
| `sidera/status-report/1` | `{1: message, 2: recipient endpoint, 3: state, 4?: reject reason, 5?: block cause, 6: reported_at, 7?: delivered_at, 8: sending node}` | the target node's network key |
| `sidera/control-reply/1` | `{1: message, 2: recipient endpoint, 3: issued_at, 4: sending node, 5?: create result, 6?: close result, 7?: interrupt result, 8?: status result}`, exactly one result | the target node's network key |

## Why

- **Deterministic CBOR with a re-encode comparison.** Revocations are kept by
  digest and credentials are signed by two nodes that must compute the same
  bytes, so the bytes must be canonical. The decoding library has no option
  that refuses non-deterministic input, and its defaults keep one of two
  duplicate keys and accept indefinite lengths and tags, so each is switched off
  and the decoded value is encoded again and compared. Unknown fields are
  refused, not skipped: an old node that skipped a new restriction would fail
  open. The version in the signed type string is how fields are added.
- **Payload as bytes.** Signatures and digests cover the bytes that travelled,
  so no encoder difference can change what was signed.
- **Digest without signatures, with the type.** An Ed25519 signature may have
  several encodings in other implementations; a digest that included it would give
  one record several digests. Including the type makes two records with equal
  payloads and different types differ.
- **Signature domain and the network key's other signatures.** The same network
  key also signs libp2p's own material. A signature made for one purpose must
  never be valid for another, so the signed bytes of this format cannot equal
  anything libp2p signs with that key. libp2p uses two kinds of signing input,
  and each is separated from ours on its own:
  - *Text-prefixed inputs.* The Noise handshake signs a payload that begins with
    `noise-libp2p-static-key:`, the TLS handshake one that begins with
    `libp2p-tls-handshake:`, and the HTTP peer-ID authentication handshake one
    that begins with `libp2p-PeerID`. Every such prefix is text that begins with a
    letter (`n` 110, `l` 108), so the first byte is at least 65. Our signed bytes start with the
    length of the domain, a value below 64, so the first bytes differ. This holds
    for any signing input that starts with a letter, which includes any text
    prefix libp2p adds later.
  - *Framed inputs.* libp2p's signed-envelope format uses the same
    length-prefixed layout, but with its own domain strings (for example
    `libp2p-peer-record`). The layout is prefix-free, so two messages are equal
    only if their domains are equal, and ours is `sidera/record-sig`.
  The argument depends on both kinds being the only ones: a libp2p feature that
  signs with the host key and starts with a byte below 64 and is not framed with
  a domain would need a new look. Ed25519 context strings were not used because
  libp2p's key interface has a single-argument `Sign`.
- **Why not the alternatives.** *Protobuf*: its own documentation says
  serialization is not and cannot be canonical, and the Go library does not
  promise stability across versions. *COSE*: it could carry several signatures,
  but it brings a large surface, an MPL-licensed library and many options we
  would then have to forbid. *JWS*: no domain separation, a text container for
  binary data, and payload re-encoding is a source of mismatches. *libp2p signed
  envelope*: a single signature, no validity, serial or counter fields, a
  process-wide type registry, and the signing key must be a libp2p key. The format
  here is a small custom envelope on the same layout as the envelope for domain
  separation.
- **One home for payload validation.** If the encoding conversion repeated the
  rules of the decoding one, the two copies would drift and a node could seal a
  payload its peers refuse, a failure that shows only on the far side. Checking
  by decoding what was encoded makes the receiver's rule the only rule.
  `EncodePayload` exports that same step, so a caller that needs the payload
  bytes before a record exists (to know a digest before the second party has
  signed) asks the descriptor and cannot compose its own conversion, canonical
  encoding and read-back, which would be a second copy of the rule.
- **Descriptors, not a registry.** Another package can define a record type
  without a change here, tests can define their own, and no global state is
  needed; the descriptor passed to `Verify` also stops a record of one type
  from being accepted as another.
- **The highest serial seen is separate from the accepted certificate.** A
  certificate can be seen and never accepted (expired, revoked). If the only
  record kept were the accepted certificate, discarding it would lower the bar
  and let an older certificate, and its key, return. A conflict is judged by
  digest and not by key, so a second certificate with the same key and another
  window under one serial is caught too.
- **Co-signing goes through the descriptor.** A co-signer who appended a
  signature to whatever record it was handed would put its key next to a payload
  it never read and next to signatures nobody checked, and could fill a slot the
  type does not give it. Verifying first, and assigning the signer to an empty
  slot, makes co-signing the same check as verification plus one more signature.
- **Partial is a type of its own.** The same result type with a "complete" flag
  would let a caller who forgets the flag treat a half-signed two-party
  credential as verified, a failure that compiles and passes every test with
  complete records. Two types make the mistake a compile error. Slot states are
  reported in the rule's order, not the record's, because the record sorts
  signatures by key bytes and that order says nothing about who is missing.
- **Peek is one entry, and its result is a type of its own.** A descriptor
  whose signer rule depends on the claimed node needs that node before it can
  verify. Without one shared entry every record type would re-implement the
  decoding, and two decoders for one payload drift apart: the one used to
  choose the context could accept a payload the verifier refuses, or read a
  field differently. `Peek` goes through the decoding path that `Verify` and
  `Seal` use. Its result is distinct from `Verified` and `Partial` for the same
  reason `Partial` is distinct from `Verified`: a payload no signature backs
  must not be passable where a checked one is expected. It carries no digest
  and no signer list, so there is nothing to mistake for the result of a check.
- **The results are sealed.** With exported fields, `record.Verified[P]{Payload: x}`
  compiles in any package, and together with `Peek` it turns a payload nobody
  checked into an apparently verified one in a single line. Every decision
  function downstream trusts a `Verified` without looking at a signature, so the
  type is the proof only if no other code can write one. The fields are
  unexported and read through accessors; `Partial` and `Unverified` are sealed
  the same way, because each is a statement about what was checked. The zero
  value remains writable, and carries no digest, which every consumer refuses.
  Tests that need a verified value seal and verify a real record. The cost is
  that a test cannot fabricate a payload no descriptor would accept (a zero
  counter, a missing node); those states cannot reach a decision function any
  more, so they are no longer tested there.
- **Network-key slots are built per verification, with reasons.** Whether a
  key is a node's network key depends on the certificate accepted now, on its
  revocation and on the time. A descriptor created once could not hold that,
  and a global holding it would make verification depend on hidden state. A
  slot that returns a reason, and a typed error that carries it, lets the caller
  act on the cause: an expired certificate asks for a refreshed one, a key
  mismatch is hostile or stale, a revoked certificate is a security event. A bare
  "signer not allowed" would collapse these.
- **Slots and partial verification.** Two-party credentials need to be signed
  one after the other. A signer rule as slots lets `VerifyPartial` check what the
  first party signed before the second signs, and lets `Verify` insist that the
  set is complete. The digest does not depend on how many signatures exist.
- **Validity and revocation are separate.** `Verify` answers a cryptographic
  question. Whether a window is current is a rule that already lives in `admin`
  and `trust` (they compare at different instants depending on the use); doing it
  here too would make two places that could disagree.
- **Level table of its own.** The signed value must keep its meaning if the Go
  constants are reordered or extended. Requiring the level, with 3 for "no cap",
  means a certificate always states what it grants.
- **Time range.** The upper bound keeps every conversion far from the point
  where `time.Time` would overflow.

## Where

- `internal/frame/frame.go`: `Frame`, `Domain`; `domains_test.go` is the check
  that every domain in the module is valid and distinct.
- `internal/record/record.go`: envelope, signed bytes, digest, `Sign`, `Decode`,
  `Encode`, `Signer`.
- `internal/record/descriptor.go`: `Descriptor`, signer slots (`NodeIdentityKey`,
  `PersonRootKey`, `NetworkKeyOf`, `BindingError`), `EncodePayload`, `Seal`,
  `Verify`, `VerifyPartial`, `CoSign`, `Peek`, `Verified`, `Partial`, `Unverified`.
- `internal/record/codec.go`: hardened decoding options and `UnmarshalStrict`.
- `internal/record/timeconv.go`, `level.go`: the conversions.
- `internal/record/networkcert.go`, `devicecert.go`: the two certificates,
  `IssueNetworkKeyCertificate`, `IssueDeviceCertificate`, `SerialSeen`,
  `DecideSerial`, `AcceptedNetworkKey`, `NetworkKeyBelongs`, `AdminCert`.
- Tests: `golden_test.go` (hand-written vectors with CBOR diagnostic notation,
  identifiers, digests and signatures computed with an independent
  implementation from RFC 8032 keys), `record_test.go` (`TestGoldenPairRecord`,
  `TestPartialSignatures`, `TestCoSign`, `TestDecodeRejects`, `TestVerifyRejects`,
  `TestEveryBitOfARecordIsProtected`, `TestEnvelopeRules`,
  `TestSealRunsTheDecodingPath`, `TestEncodePayload`, `TestSignerFailuresAreCaughtAtTheSigner`),
  `peek_test.go` (`TestPeek`, `TestPeekChoosesTheVerificationContext`,
  `TestResultTypesAreMutuallyDistinct`,
  `TestResultTypesHaveNoExportedField`, `TestResultAccessorsReturnCopies`),
  `codec_test.go` (`TestStrictUnmarshal`, `TestNestingLimit`,
  `TestTimeConversion`, `TestLevelTable`, `TestOptionalFieldsInAnExternalRecordType`),
  `certs_test.go` (`TestNetworkKeyCertificateRules`, `TestDecideSerial`,
  `TestNetworkKeyBelongs`, `TestNetworkKeySignedRecords`,
  `TestRotationVoidsTheOldNetworkKey`,
  `TestRotatingTheNetworkKeyKeepsTheNodeID`, `TestRolesAreSeparated`,
  `TestIssuedCertificatesAreBackdatedByTheClockTolerance`,
  `TestDeviceCertificateRules`, `TestDeviceCertificateToAdminFacts`),
  `fuzz_test.go` (decoders, payloads, the strict codec, time conversion).
- `internal/record/README.md`: entry points and what callers must guarantee.
