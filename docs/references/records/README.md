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
- **`NetworkKeyBelongs`** is the only function that decides whether a network
  public key is a node's key, given what is accepted for the node
  (`AcceptedNetworkKey`: the certificate and the revocation status the caller fills
  from the revocation records). Declarations, vouchers, reachability records and
  delivery reports that a network key signs all rely on it. A key the certificate
  does not name is a mismatch whatever the certificate's state; a certificate
  that names it is then judged for revocation, then for its window. The
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
  `trust.NodeSet` that `trust.View.Retired` takes; `Set.NetworkKey` sets
  `AcceptedNetworkKey.Revocation` to `KeyRevocationRevoked` (the certificate's
  own revocation, or any self-revocation of its node) or `KeyRevocationClear`.
  The two certificate queries never return the unchecked status, which `admin`
  and `record.NetworkKeyBelongs` refuse; this package is the one place that turns
  the revocation set into those statuses. None of them reads a clock or touches
  `SerialSeen`, so a revocation never lowers the highest serial seen. A `Set` is
  not safe for concurrent use.
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
  itself, empty or nil, because it did look. `RemotePerson` exists so the person
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
  `PersonRootKey`, `NetworkKeyOf`, `BindingError`), `Seal`, `Verify`,
  `VerifyPartial`, `CoSign`, `Peek`, `Verified`, `Partial`, `Unverified`.
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
  `TestSealRunsTheDecodingPath`, `TestSignerFailuresAreCaughtAtTheSigner`),
  `peek_test.go` (`TestPeek`, `TestPeekChoosesTheVerificationContext`,
  `TestUnverifiedIsADistinctType`),
  `codec_test.go` (`TestStrictUnmarshal`, `TestNestingLimit`,
  `TestTimeConversion`, `TestLevelTable`, `TestOptionalFieldsInAnExternalRecordType`),
  `certs_test.go` (`TestNetworkKeyCertificateRules`, `TestDecideSerial`,
  `TestNetworkKeyBelongs`, `TestNetworkKeySignedRecords`,
  `TestRotatingTheNetworkKeyKeepsTheNodeID`, `TestRolesAreSeparated`,
  `TestIssuedCertificatesAreBackdatedByTheClockTolerance`,
  `TestDeviceCertificateRules`, `TestDeviceCertificateToAdminFacts`),
  `fuzz_test.go` (decoders, payloads, the strict codec, time conversion).
- `internal/record/README.md`: entry points and what callers must guarantee.
