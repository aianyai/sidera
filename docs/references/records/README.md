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

### Signed bytes and digest

```
signed = uvarint(len(D)) || D || uvarint(len(type)) || type || uvarint(len(payload)) || payload
D      = "sidera/record-sig"
digest = SHA-256 of the same layout with D = "sidera/record-digest"
```

Both domains start with `sidera/` and are shorter than 64 bytes; the build
fails if one grows past that. The digest covers type and payload and no
signature, so a record has one digest whatever signatures are attached and
however a signature might be re-encoded.

### Record types

A record type is a `Descriptor`: the type string, how to convert its payload
to and from a wire struct (refusing invalid values), and the signer rule. The
package that defines the type exports the descriptor and callers pass it to
`Verify` explicitly. There is no registry, so a package can define record types
without `record` knowing about them.

The signer rule is a list of **slots**. A slot is a predicate on a public key
("the key whose derived node identifier is X"). `Verify` accepts a record only if
every slot is filled by exactly one signature and every signature fills a slot.
`VerifyPartial` accepts empty slots and says the record is incomplete.

### Decoding

1. The input length is checked against a limit passed by the caller (64 KiB to
   start). A limit that is not positive refuses everything.
2. The CBOR library decodes with duplicate map keys, indefinite lengths, tags,
   unknown fields, NaN and Inf refused. Nesting stays at the library's bound of
   32 levels, and records decode into typed structs.
3. The value is encoded again with the deterministic options (nil slices as
   empty byte strings, tags refused). If the bytes differ from the input, the
   input is refused.
4. For the envelope: signature count, key validity, strictly ascending order.
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
record with its own signer and sends it; the second party decodes it, checks it
with `VerifyPartial` (every present signature is valid and allowed), and appends
its signature with `CoSign`. The digest is the same at every step, so the second
party signs exactly what the first one signed and both can compute the digest
of the final credential before it is complete. `Verify` passes only when the
signer set is complete.

### Network-key certificate

Type `sidera/network-key-cert/1`. Payload `{1: node ID, 2: network public key,
3: serial, 4: not_before, 5: not_after}`. Signed by one key: the identity key
whose derived node ID equals field 1. The certificate holds the network public
key and not a peer identifier, so the contract packages do not depend on libp2p.

- **Validity** is judged by `record`, with no tolerance: valid from `not_before`,
  expired at `not_after`. `not_after` must be after `not_before`.
- **Serial**: peers accept only the highest serial seen (`DecideSerial`).
  Higher is accepted; lower, or equal with the same key, is stale; equal with a
  different network key is a conflict (the identity key signed two certificates
  for one serial, the mark of a copied or compromised key); a certificate of
  another node is refused. Expiry does not enter this decision: an expired
  certificate with a higher serial still raises the serial seen, so an older key
  cannot come back.
- **`NetworkKeyBelongs`** is the only function that decides whether a network
  public key is a node's key, given the certificate accepted for the node.
  Declarations, vouchers, reachability records and delivery reports that a
  network key signs all rely on it.

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
- `DeviceCertificate.AdminCert()` yields the signature-derived part of
  `admin.DeviceCert`: the cap and the window as written. The two revocation flags
  are false; the revocation records fill them in before the value is used.

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
  anything libp2p signs with that key:
  - libp2p's signed-envelope format uses the same length-prefixed layout, but
    with its own domain strings (for example `libp2p-peer-record`). The layout
    is prefix-free, so two messages are equal only if their domains are equal,
    and ours is `sidera/record-sig`.
  - The Noise handshake signs a payload that begins with the bytes
    `noise-libp2p-static-key:` and the TLS handshake one that begins with
    `libp2p-tls-handshake:`; they start with `n` (110) and `l` (108). Our signed
    bytes start with the length of the domain, a value below 64, so the first
    bytes differ.
  Ed25519 context strings were not used because libp2p's key interface has a
  single-argument `Sign`.
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
- **Descriptors, not a registry.** Another package can define a record type
  without a change here, tests can define their own, and no global state is
  needed; the descriptor passed to `Verify` also stops a record of one type
  from being accepted as another.
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

- `internal/frame/frame.go`: `Frame`, `Domain`; `domains_test.go` is the check that every domain in the module is valid and distinct.
- `internal/record/record.go`: envelope, signed bytes, digest, `Sign`, `CoSign`,
  `Decode`, `Encode`, `Signer`.
- `internal/record/descriptor.go`: `Descriptor`, signer slots, `Seal`, `Verify`,
  `VerifyPartial`.
- `internal/record/codec.go`: hardened decoding options and `UnmarshalStrict`.
- `internal/record/timeconv.go`, `level.go`: the conversions.
- `internal/record/networkcert.go`, `devicecert.go`: the two certificates,
  `DecideSerial`, `NetworkKeyBelongs`, `AdminCert`.
- Tests: `golden_test.go` (hand-written vectors with CBOR diagnostic notation and
  signatures from RFC 8032 keys), `record_test.go` (`TestGoldenPairRecord`,
  `TestPartialSignatures`, `TestDecodeRejects`, `TestVerifyRejects`,
  `TestEveryBitOfARecordIsProtected`, `TestEnvelopeRules`),
  `codec_test.go` (`TestStrictUnmarshal`, `TestTimeConversion`, `TestLevelTable`,
  `TestOptionalFieldsInAnExternalRecordType`), `certs_test.go`
  (`TestNetworkKeyCertificateRules`, `TestDecideSerial`, `TestNetworkKeyBelongs`,
  `TestRotatingTheNetworkKeyKeepsTheNodeID`, `TestDeviceCertificateRules`,
  `TestDeviceCertificateToAdminFacts`), `fuzz_test.go` (decoders, payloads, the
  strict codec, time conversion).
- `internal/record/README.md`: entry points and what callers must guarantee.
