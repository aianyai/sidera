# record

The signed-record envelope shared by every signed statement, its strict
decoding, and the two records that bind keys to identifiers: the network-key
certificate and the device certificate. Read [../README.md](../README.md) first
for the conventions every contract package follows, and
[../../docs/references/records/README.md](../../docs/references/records/README.md)
for the format and the reasons behind it. Identifiers and keys come from
[`ident`](../ident/README.md).

## Entry points

| Name                                              | Purpose                                                                                                   |
| ------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `Record`, `Signature`                             | The envelope `{1: type, 2: payload, 3: signatures}`.                                                       |
| `Decode(data, maxLen)`, `Record.Encode()`         | Strict decoding with an input-size limit (`DefaultMaxInput` is 64 KiB) and canonical encoding.             |
| `Signer`, `NewEd25519Signer`                      | The signing interface callers inject, and an in-memory implementation.                                     |
| `Sign`, `CoSign(d, r, signer)`                    | Sign a raw payload; verify a partial record against its descriptor and append a second party's signature to an empty slot. |
| `Descriptor[P]`, `NewDescriptor`                  | One record type: type string, payload codec, signer rule. Exported by the package that defines the type.  |
| `Seal`, `Verify`, `VerifyPartial`                 | Encode and sign a typed payload; check a complete record (`Verified[P]`); check a record that may still lack signers (`Partial[P]`). |
| `SignedBytes`, `DigestOf`, `Record.Digest`        | The bytes that are signed and the signature-free digest.                                                  |
| `SignerSlot`, `NodeIdentityKey`, `PersonRootKey`, `NetworkKeyOf`, `BindingError` | Signer slots: "the key that derives this node / person identifier", "the network key `NetworkKeyBelongs` accepts for this node". A refusal by `NetworkKeyOf` is a `*BindingError` with the specific reason. |
| `TimeToWire`, `TimeFromWire`, `OptionalTime*`, `RequireCounter`, `LevelToWire`, `LevelFromWire` | The one place wire times, counters and levels are converted. |
| `MarshalCanonical`, `UnmarshalStrict`             | The canonical codec for payload wire structs defined elsewhere.                                            |
| `NetworkKeyCertificate`, `NetworkKeyCertificateDescriptor`, `IssueNetworkKeyCertificate` | Node ID, network public key, serial, validity; signed by the identity key. The issue constructor sets `NotBefore` back by the clock tolerance. |
| `SerialSeen`, `SerialSeenOf(v)`, `DecideSerial(seen, incoming)` | The highest serial seen for a node with the digest of the certificate seen at it; accept, stale, conflict, wrong node or invalid for a new verified certificate. |
| `AcceptedNetworkKey`, `NetworkKeyBelongs(accepted, node, key, now)` | The accepted certificate and the result of its revocation lookup; the one answer to "is this network key this node's key": every network-key record relies on it. |
| `DeviceCertificate`, `DeviceCertificateDescriptor`, `IssueDeviceCertificate`, `DeviceCertificate.AdminCert()` | Person ID, device key, level cap, validity; signed by the root key; conversion to `admin.DeviceCert`. The issue constructor sets `NotBefore` back by the clock tolerance. |

Errors wrap the sentinels in `errors.go`; match them with `errors.Is`.

## Rules

- **Envelope.** A deterministic CBOR map with integer keys 1 to 3. The payload
  is a byte string whose exact bytes are signed and hashed. Signatures are
  `{1: public key, 2: signature}`, strictly ascending by the encoded public key,
  one to eight of them, no signer twice. A public key is `{1: algorithm, 2:
  bytes}`: Ed25519 only, canonical, not small order.
- **Signed bytes and digest.** `frame.Frame(domain, type, payload)`, that is
  `uvarint(len(domain)) || domain || uvarint(len(type)) || type ||
  uvarint(len(payload)) || payload`, with the signing domain for signatures and
  the digest domain for the digest (the digest is the SHA-256 of the framed
  bytes). The digest does not cover signatures, so partial and complete forms
  of a record share it. The domains are declared with `frame.Domain`; the
  records reference lists every domain of the project.
- **No registry.** A record type is a `Descriptor`, built with `NewDescriptor`
  and exported by the package that defines the type. A record verifies only
  against the descriptor whose type string it carries. The type string includes
  the version; an incompatible change is a new type string.
- **One home for payload validation.** A descriptor's `fromWire` is the only
  place that refuses an invalid payload; `toWire` only converts. Encoding (`Seal`)
  runs the decoding path (`UnmarshalStrict`, then `fromWire`) on the bytes it
  produced and refuses when that fails, so what this package writes is what every
  receiver accepts.
- **Strict decoding.** The size limit comes first. Then duplicate map keys,
  indefinite lengths, tags, unknown fields, NaN and Inf are refused, and the
  decoded value is encoded again: any difference (non-shortest integers,
  unsorted keys, a missing field, `null`) refuses the input. Decoding the
  envelope checks its shape only; signatures and payload are checked by
  `Verify`.
- **Signer slots.** A descriptor's signer rule is a list of slots. A slot is a
  function that returns nil for a key it accepts and a reason for one it does
  not. `Verify` needs every slot filled by exactly one signature and no
  signature outside a slot. `VerifyPartial` accepts empty slots and reports
  which are filled. Slots of one rule must not overlap.
- **Network-key slots and binding reasons.** `NetworkKeyOf(node, accepted, now)`
  accepts exactly the keys `NetworkKeyBelongs` accepts, so the binding rules live
  in one function. When it refuses, the verification error wraps
  `ErrSignerNotAllowed` and a `*BindingError` whose `Binding` is the specific
  reason (expired, not yet valid, key mismatch, node mismatch, revoked, revocation not
  checked, no certificate); read it with `errors.As`.
- **Partial is not verified.** `Verify` is the only function that returns a
  `Verified[P]`. `VerifyPartial` returns a `Partial[P]`: the payload, the digest
  and, per slot in the rule's order, whether it is signed and by whom. The two
  types do not convert into each other, so a record that still lacks a signer can
  never be handed on as verified by mistake; a complete `Partial` is still
  checked again by `Verify`.
- **Partial signatures.** The first party calls `Seal` (or `Sign`) with its
  signer and sends the encoded record. The second party decodes it, calls
  `CoSign(descriptor, record, signer)`, which verifies the record as
  `VerifyPartial` does and requires the new signer to fill an empty slot; a zero
  record, a signer the rule does not allow, a signer that already signed and a
  record with every slot filled are refused. The signature of an injected
  `Signer` is checked when it is made, so a broken signer fails at its own side,
  and the error wraps both `ErrSigner` and the signer's own error.
- **Times, counters, levels.** Required times are positive Unix seconds up to
  year 9999; optional times are left out, never written as 0; counters and serials
  start at 1; levels are 1 view, 2 manage, 3 full control and are required on a
  device certificate ("no cap" is written 3).
- **Network-key certificate.** Signed by the identity key whose derived node
  identifier is the certificate's node. The window is `NotBefore <= now <
  NotAfter` with no tolerance; this package judges it
  (`NetworkKeyCertificate.ValidityAt`, `NetworkKeyBelongs`). A binding answer is
  one of ok, no certificate, node mismatch, key mismatch, not yet valid, expired
  or revoked.
- **Issuing.** Certificates are issued with `IssueNetworkKeyCertificate` and
  `IssueDeviceCertificate`, which set `NotBefore` to the issue time minus
  `timing.ClockTolerance` (whole seconds). A higher serial replaces the old
  certificate immediately and validity is compared without tolerance, so a peer
  whose clock is up to the tolerance behind would otherwise find the node
  unverifiable after every renewal; a device certificate issued moments ago
  would likewise be refused as not yet valid. The comparison rules themselves are
  unchanged. Hand-built certificates are legal too; they just lack this margin.
- **Role separation.** A network-key certificate whose network key derives to
  its own node ID (the network key is the identity key) and a device certificate
  whose device key derives to its own person ID (the device key is the root key)
  are refused, on sealing and on receiving, with `ErrRoleSeparation`.
- **Highest serial seen.** `SerialSeen` is its own state, apart from the
  accepted certificate: the serial and the digest of the certificate seen at it.
  A higher serial is accepted, a lower one or the very same certificate is
  stale, and the same serial with another digest is a conflict (another network
  key or the same key with another window: the identity key signed two
  certificates under one serial). An expired certificate with a higher serial
  advances the serial; a revocation never lowers it.
- **Device certificate.** Signed by the root key whose derived person identifier
  is the certificate's person. Its validity window is carried as written and is
  not judged here.

## What the caller must guarantee

- `Verify` shows that a record is well formed, correctly signed and signed by
  the keys its type allows. It does not show that anything in the payload is
  true or current. The network-key certificate's validity, whether a serial is
  the highest seen, and whether a device certificate is expired or revoked are
  separate decisions, made by the functions above, by `admin` and by the
  revocation records.
- `AcceptedNetworkKey.Revocation` is filled by the caller from the revocation
  records; this package never reads them. Its zero value `KeyRevocationUnchecked`
  means nobody looked, and `NetworkKeyBelongs` refuses it with
  `BindingRevocationUnchecked`, so a caller without a revocation source gets
  refusals, never certificates that look unrevoked. The caller keeps
  `SerialSeen` per node and moves it forward with `SerialSeenOf` whenever
  `DecideSerial` answers accept, whether or not the certificate is also taken as
  the accepted one.
- `AdminCert()` leaves `Revocation` at `admin.RevocationUnchecked`, which `admin`
  refuses. The caller sets it from the revocation lookup of the certificate and
  of the person's root key before the value is handed to `admin`.
  `MaxLevel` is never `LevelNone`: a certificate without a level does not decode.
- A time or optional field converted by a record type goes through the helpers
  in `timeconv.go`; a zero `time.Time` in an optional position means "not
  provided", and the consumer's own documentation says what that means (for
  example a credential without `NotAfter` never expires).
- A payload wire struct passed to `NewDescriptor` has integer-keyed fields and
  tags optional fields `omitempty`; it must not use pointers to hold values that
  may be zero without also rejecting an explicit zero in its conversion.
- A descriptor with a fixed signer rule (the certificates) is created once, at
  package initialisation. A descriptor whose rule depends on the caller's context
  (a record signed by a network key needs the accepted certificate, its
  revocation and the time) is built per verification by a function that takes
  that context, for example `DeclarationDescriptor(accepted, now)`; the package
  that defines such a record type exports that function, not a variable.
  `NewDescriptor` panics on a malformed definition so that a test run finds it.

## Dependencies

The standard library, `frame`, `ident`, `admin` (for the level and the device-certificate
facts) and `github.com/fxamacker/cbor/v2` v2.9.6 (v2.9.5 is retracted). Nothing
else; in particular no libp2p.
